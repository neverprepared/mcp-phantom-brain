# mcp-phantom-brain

A [Model Context Protocol](https://modelcontextprotocol.io) server that gives Claude a structured, validated long-term memory backed by an Obsidian vault on disk.

Unlike a simple note-taking tool, phantom-brain doesn't just store what you tell it — it validates claims before committing them. The host LLM evaluates each incoming claim for logical fallacies, source quality, and contradictions with existing knowledge before anything is written to memory.

## How it works

Memory storage is a two-phase protocol:

1. **`brain_remember`** — runs Layer 1 server-side checks (coherence, source tier, near-duplicate detection) and returns a structured evaluation package to the host LLM, including relevant existing atoms and a structured prompt covering 12 fallacy checks, 6 philosophical razors, and a contradiction scan.

2. **`brain_commit`** — the host LLM calls this with its verdict (`store`, `reject`, or `ask`). Accepted claims become atoms in `Memory/`. Rejected claims are logged with full reasoning to `_log/rejections.jsonl`.

The server never reasons — all epistemic judgment happens in the host LLM's context. The server enforces structure and provides the reference material.

## Tools

| Tool | Purpose |
|---|---|
| `brain_recall` | Hybrid FTS5 + vector search over Memory atoms and Wiki pages |
| `brain_remember` | Layer 1 validation; returns evaluation package for host LLM |
| `brain_commit` | Commits the host LLM's verdict: store, reject, or ask |
| `brain_reflect` | Maintenance pass: prunes stale atoms |
| `brain_why_rejected` | Query the rejection log by topic, fallacy, or claim content |

## Vault structure

The vault is a directory of Markdown files with YAML frontmatter.

```
<vault>/
  Memory/       ← atoms: short factual claims, one per file
  Wiki/
    HowTos/
    Runbooks/
    References/ ← seed pages: logical fallacies, razors, philosophical logic
    Scratch/
  Input/        ← raw source material (immutable after ingest)
  Output/       ← deliverables
  _index/       ← SQLite FTS5 + vector index
  _log/         ← rejection log (rejections.jsonl)
```

On first startup, three reference Wiki pages are seeded:
- **Logical Fallacies** — 12-fallacy taxonomy used in Layer 2 evaluation
- **Philosophical Razors** — Occam, Hitchens, Sagan, Hanlon, Hume, Popper
- **Philosophical Logic** — fallback frameworks for ambiguous claims (modal logic, fuzzy logic, etc.)

## Search

`brain_recall` uses **hybrid RRF** (Reciprocal Rank Fusion) combining BM25 full-text search and cosine vector similarity when Ollama is available. Falls back to FTS5-only otherwise. Memory atoms and Wiki pages are ranked together in the same result set.

## Setup

**Prerequisites:** Node.js ≥ 18. Optionally [Ollama](https://ollama.ai) with `nomic-embed-text` for vector search.

```bash
git clone https://github.com/mindmorass/mcp-phantom-brain
cd mcp-phantom-brain
npm install
cp .env.example .env  # edit BRAIN_VAULT_PATH
npm run build
```

**Claude Code / Claude Desktop** — add to your MCP config:

```json
{
  "phantom-brain": {
    "command": "node",
    "args": ["/path/to/mcp-phantom-brain/dist/index.js"],
    "env": {
      "BRAIN_VAULT_PATH": "/path/to/your/vault"
    }
  }
}
```

> **Note:** Do not use nested shell fallback syntax (`${VAR:-${OTHER}}`) in the MCP env block — Claude Code partially expands it, leaving a trailing `}`. Use plain `${VAR}` references only.

## Configuration

| Var | Default | Purpose |
|---|---|---|
| `BRAIN_VAULT_PATH` | `~/...memory` | Vault root directory |
| `OLLAMA_BASE_URL` | `http://localhost:11434` | Embeddings endpoint |
| `EMBEDDING_MODEL` | `nomic-embed-text` | Ollama model name |
| `EMBEDDING_DIMS` | `768` | Vector dimensions |
| `MCP_BRAIN_LOG_LEVEL` | `info` | Log verbosity (`debug\|info\|warn\|error`) |

## Development

```bash
npm run dev       # run with tsx (no build required)
npm run typecheck # type-check without emitting
npm run build     # compile to dist/
```

There are no tests. `npm run typecheck` is the primary verification step.

## License

MIT
