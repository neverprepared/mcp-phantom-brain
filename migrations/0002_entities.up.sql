-- 0002_entities — canonical entities, aliases, and the record↔entity join.
--
-- Replaces the denormalized `mentioned_by[]` from the OpenSearch model.
-- The FORWARD reference (which entities a record mentions) lives in
-- record_entities; the REVERSE ("what mentions X") is just a query over it.
-- No read-modify-write on an array, so synthesis is no longer forced to be
-- single-worker.
--
-- Identity is the entity row, NOT the name — so a rename doesn't fork the
-- entity. The old/new names both resolve via entity_aliases.

CREATE TABLE entities (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    profile     text NOT NULL,
    vault       text NOT NULL,
    slug        text NOT NULL,                       -- canonical key (current)
    name        text NOT NULL,                       -- display name (current)
    description text,                                -- accumulated blurb (optional)
    embedding   vector(768),                         -- for similarity-based resolution
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT entities_uniq UNIQUE (profile, vault, slug)
);

CREATE TRIGGER entities_set_updated_at
    BEFORE UPDATE ON entities
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE INDEX entities_tenant_idx ON entities (profile, vault);
-- fuzzy name match for entity resolution (alongside vector similarity).
CREATE INDEX entities_name_trgm  ON entities USING gin (name gin_trgm_ops);
CREATE INDEX entities_embed_hnsw ON entities USING hnsw (embedding vector_cosine_ops);

-- Alternate names that resolve to the same entity (renames, "Bob"↔"Robert").
CREATE TABLE entity_aliases (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    entity_id  bigint NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
    alias      text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CONSTRAINT entity_aliases_uniq UNIQUE (entity_id, alias)
);
CREATE INDEX entity_aliases_alias_trgm ON entity_aliases USING gin (alias gin_trgm_ops);

-- Forward edge: record → entities it mentions. Reverse query gives backlinks.
CREATE TABLE record_entities (
    record_id bigint NOT NULL REFERENCES records(id)  ON DELETE CASCADE,
    entity_id bigint NOT NULL REFERENCES entities(id) ON DELETE CASCADE,
    PRIMARY KEY (record_id, entity_id)
);
-- the "what mentions entity X" lookup.
CREATE INDEX record_entities_entity_idx ON record_entities (entity_id);
