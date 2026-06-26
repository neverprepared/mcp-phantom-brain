-- 0003_facts — mutable state: the referent-keyed projection.
--
-- A fact is (entity, attribute, value): "Project Phoenix / status /
-- shipped", "Bob / employer / Acme". UNIQUE(entity, attribute) makes the
-- CURRENT value a single row you UPSERT — not a blob you re-append. The
-- prior value moves to fact_history (valid_from/valid_to), so updates are
-- versioned, never destructive.
--
-- This is the layer the reconciliation job maintains (derived from the
-- immutable records); it is eventually-consistent with the record log.

CREATE TABLE facts (
    id               bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    profile          text NOT NULL,
    vault            text NOT NULL,
    entity_id        bigint NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
    attribute        text NOT NULL,
    value            text NOT NULL,
    -- which record asserted the current value (provenance into the log).
    source_record_id bigint REFERENCES records(id) ON DELETE SET NULL,
    confidence       real,                            -- reconciliation confidence 0..1
    valid_from       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT facts_uniq UNIQUE (entity_id, attribute)
);

CREATE TRIGGER facts_set_updated_at
    BEFORE UPDATE ON facts
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE INDEX facts_tenant_idx ON facts (profile, vault);
CREATE INDEX facts_entity_idx ON facts (entity_id);

-- Immutable history of superseded fact values. No FK on entity_id so the
-- history survives even if an entity is later removed/merged — it is a
-- record of what was believed true, and when.
CREATE TABLE fact_history (
    id                      bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    profile                 text NOT NULL,
    vault                   text NOT NULL,
    entity_id               bigint NOT NULL,
    attribute               text NOT NULL,
    value                   text NOT NULL,
    source_record_id        bigint,
    valid_from              timestamptz NOT NULL,
    valid_to                timestamptz NOT NULL DEFAULT now(),
    superseded_by_record_id bigint
);
CREATE INDEX fact_history_entity_idx ON fact_history (entity_id, attribute);
CREATE INDEX fact_history_tenant_idx ON fact_history (profile, vault);
