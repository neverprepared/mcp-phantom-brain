package mcp

import "testing"

func TestFtsPhrase(t *testing.T) {
	cases := map[string]string{
		// Motivating bug: multi-word query needs OR fan-out so BM25
		// matches docs that have all the terms but not consecutively.
		"loop engineering AI coding agents": `"loop" OR "engineering" OR "AI" OR "coding" OR "agents"`,
		// Single token passes through quoted.
		"kubernetes": `"kubernetes"`,
		// Adjacent tokens still produce OR (not phrase) — relying on
		// BM25's term-frequency ranking to surface docs where they
		// also appear adjacently.
		"ReAct pattern": `"ReAct" OR "pattern"`,
		// Embedded quote in token gets escaped.
		`he said "hi"`: `"he" OR "said" OR """hi"""`,
		// FTS5 operator tokens dropped — bare AND/OR/NOT would error.
		"AI and ML":  `"AI" OR "ML"`,
		"foo or bar": `"foo" OR "bar"`,
		// Whitespace collapses; empty string returns empty.
		"":    "",
		"   ": "",
		// Punctuation in tokens is preserved inside quotes — FTS5
		// won't reinterpret them as operators because they're literal.
		"foo:bar baz": `"foo:bar" OR "baz"`,
	}
	for in, want := range cases {
		if got := ftsPhrase(in); got != want {
			t.Errorf("ftsPhrase(%q) = %q, want %q", in, got, want)
		}
	}
}
