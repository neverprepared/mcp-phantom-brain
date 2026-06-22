package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// ExtractEntitiesLLM asks the claude CLI to identify the named
// entities in a document. Replaces the regex extractor's tendency to
// pull every ## heading and **bold** term regardless of whether they
// constitute real entities.
//
// The prompt narrows the model to proper nouns + coined technical
// concepts and away from section labels / descriptive phrases /
// list-item enumerations. Output is parsed as a JSON array of strings.
// On any failure (CLI missing, timeout, parse error) returns the
// error — callers fall back to ExtractEntities (the regex path) for
// resilience.
func ExtractEntitiesLLM(ctx context.Context, title, body, model string, timeout time.Duration) ([]string, error) {
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	prompt := buildEntityPrompt(title, body)
	raw, err := CallClaudeCLI(ctx, prompt, model, timeout)
	if err != nil {
		return nil, fmt.Errorf("entities LLM: %w", err)
	}
	names, err := parseEntityResponse(raw)
	if err != nil {
		return nil, fmt.Errorf("entities LLM parse: %w (raw: %s)", err, truncate(raw, 200))
	}
	return names, nil
}

// buildEntityPrompt produces the prompt sent to claude. Kept as its
// own function so tests can compare exact strings.
//
// The body is truncated to ~8000 chars in case the doc is unusually
// long — entity extraction doesn't need the full text, and longer
// prompts inflate both latency and cost without proportional gain.
func buildEntityPrompt(title, body string) string {
	const bodyLimit = 8000
	if len(body) > bodyLimit {
		body = body[:bodyLimit] + "\n[...truncated]"
	}
	return `You are a named-entity extractor for an agent's long-term memory store.

Read the document and return the REAL named entities it mentions.

INCLUDE
- Proper nouns: people, organizations, places, products, projects
- Named technologies, libraries, frameworks (e.g. "LangGraph", "Kubernetes", "OpenSearch")
- Coined technical concepts with established names (e.g. "ReAct", "Plan-Execute-Verify", "Human-in-the-Loop") — only when treated as named patterns, not descriptive phrases

EXCLUDE
- Section headings ("Overview", "Core Concept", "Conclusion", "Anatomy of X")
- Descriptive phrases ("Clear Goal Definition", "Useful Tool Access", "Error Handling and Recovery")
- Generic common nouns ("agents", "loops", "patterns")
- Numbered or bulleted list items
- Question phrases

OUTPUT FORMAT: a single JSON array of strings, nothing else. No prose, no markdown, no explanation.

Example output: ["LangGraph", "ReAct", "Anthropic", "Plan-Execute-Verify"]

If no real entities are present, output: []

DOCUMENT TITLE: ` + title + `

DOCUMENT BODY:
` + body
}

// parseEntityResponse decodes the LLM's response into a clean
// []string. Tolerant of leading/trailing whitespace and a leading
// "```json" code fence (some models like to wrap output). Empty
// strings + duplicates are filtered.
func parseEntityResponse(raw string) ([]string, error) {
	s := strings.TrimSpace(raw)
	// Strip markdown code fences if the model wrapped the JSON.
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	s = strings.TrimSpace(s)

	// Find the JSON array start — some models prefix a one-liner like
	// "Here are the entities:" before the array.
	if idx := strings.Index(s, "["); idx > 0 {
		s = s[idx:]
	}

	var list []string
	if err := json.Unmarshal([]byte(s), &list); err != nil {
		return nil, err
	}

	seen := make(map[string]bool, len(list))
	out := make([]string, 0, len(list))
	for _, name := range list {
		n := strings.TrimSpace(name)
		if n == "" {
			continue
		}
		// Defensive: drop anything that's still a numbered list prefix
		// or starts with markdown noise the LLM might have leaked in.
		if strings.HasPrefix(n, "#") || strings.HasPrefix(n, "*") {
			continue
		}
		if seen[strings.ToLower(n)] {
			continue
		}
		seen[strings.ToLower(n)] = true
		out = append(out, n)
	}
	return out, nil
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

// extractEntitiesBest tries the LLM extractor first; falls back to
// the regex ExtractEntities on any error (CLI absent, timeout, parse
// error). Returns the chosen path so the caller can log which fired.
//
// This is what the SynthWorker should call — it gets the cleaner
// LLM-driven output when the CLI is available, and the regex
// behaviour when it isn't. Same return shape either way.
func extractEntitiesBest(ctx context.Context, title, body string, cliAvailable bool, logger *slog.Logger) []string {
	if cliAvailable {
		names, err := ExtractEntitiesLLM(ctx, title, body, "", 60*time.Second)
		if err == nil {
			return names
		}
		if logger != nil {
			logger.Warn("phantom-brain: LLM entity extraction failed; falling back to regex",
				slog.String("err", err.Error()))
		}
	}
	return ExtractEntities(body)
}

