# Postgres migrations — System of Record schema

Schema for the OpenSearch-native redesign (epic #92, design doc
`docs/design/opensearch-native-memory-architecture.md`). Postgres is the
**System of Record**; OpenSearch is a derived index projected from these
tables; MinIO holds attachment bytes (referenced by `records.minio_key`).

## Convention

Plain numbered SQL with `golang-migrate`-style up/down pairs
(`NNNN_name.up.sql` / `NNNN_name.down.sql`). Tool-agnostic — run with
[`golang-migrate`](https://github.com/golang-migrate/migrate), `goose`, or
raw `psql` (in order). Each file is idempotent-safe where practical
(`IF NOT EXISTS`), but they're meant to run once, in sequence.

```sh
# golang-migrate
migrate -path migrations -database "$DATABASE_URL" up

# or raw psql (the pgvector image already has the extensions from
# docker/postgres-init/01-extensions.sql)
for f in migrations/*.up.sql; do psql "$DATABASE_URL" -f "$f"; done
```

Requires the `vector` and `pg_trgm` extensions (the compose pgvector
image enables them on init).

## The model (two identities)

| Layer | Tables | Identity | Mutability |
|---|---|---|---|
| **Records** | `records` | content (SHA, dedup) | immutable — append + synth-fill |
| **Entities** | `entities`, `entity_aliases`, `record_entities` | canonical id + aliases | identity survives rename |
| **State** | `facts`, `fact_history` | referent (entity, attribute) | upsert + versioned history |

- **Records** = the durable log (episodic events, raw + synthesized
  content, attachment metadata + embedding). "What mentions entity X" is a
  join over `record_entities`, **not** a denormalized backlink.
- **Facts** = the referent-keyed mutable projection. `UNIQUE(entity,
  attribute)` → upsert; the prior value moves to `fact_history`
  (`valid_from`/`valid_to`). This is the layer reconciliation maintains.

## Migrations

| # | Adds |
|---|---|
| 0001 | `records` (+ `set_updated_at()` trigger helper) |
| 0002 | `entities`, `entity_aliases`, `record_entities` |
| 0003 | `facts`, `fact_history` |

## Open decisions (intentionally deferred)

- Multi-tenant strategy beyond `(profile, vault)` columns (e.g. row-level
  security) — columns + indexes for now.
- `references[]` between records — modeled as a join later if needed.
- Embedding dim is fixed at **768** (`nomic-embed-text`); a model change
  means a re-embed + an `ALTER`.
- `id` is `bigint IDENTITY` (single-cluster). Switch to UUID only if/when
  cross-system id generation matters.
