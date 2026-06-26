-- 0001_records — the immutable, content-addressed knowledge log.
--
-- A "record" is one durable knowledge unit: an episodic event, a curated
-- note, a web scrape, or an attachment. Identity is by CONTENT (sha) per
-- tenant — dedup is UNIQUE(profile, vault, sha). Records are appended and
-- never updated in place except by the synthesis pipeline filling in the
-- derived fields (body, gate verdict, embedding, synthesised).
--
-- Attachment bytes live in MinIO (records.minio_key); this table holds the
-- metadata + machine-extracted text. "One record per attachment" — no
-- stub-pair (the old pb_summaries + pb_attachments split is collapsed).

-- Shared trigger: bump updated_at on any row update.
CREATE OR REPLACE FUNCTION set_updated_at() RETURNS trigger AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TABLE records (
    id            bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    profile       text NOT NULL,
    vault         text NOT NULL,
    sha           text NOT NULL,                     -- content hash (dedup key)

    kind          text NOT NULL,                     -- shape of the record
    memory_type   text,                              -- Tulving: semantic|episodic|procedural

    title         text NOT NULL DEFAULT '',
    raw_body      text,                              -- original text
    body          text,                              -- distilled (NULL until synthesised)

    -- provenance
    source_url    text,
    source        text[] NOT NULL DEFAULT '{}',       -- URLs, task:/agent: ids, file paths
    tags          text[] NOT NULL DEFAULT '{}',
    captured_at   timestamptz,                        -- when the content was authored
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),

    -- gate verdict (NULL until synthesised)
    reliability   text,                               -- high|medium|low|contested
    topic         text,
    gate_reason   text,
    synthesised   boolean NOT NULL DEFAULT false,

    -- attachment metadata (NULL for non-attachment records)
    minio_key         text,                           -- pointer to the blob in MinIO
    mime_type         text,
    size_bytes        bigint,
    original_filename text,
    extracted_text    text,                           -- OCR / pdftotext / office output

    -- semantic vector (NULL until embedded). 768 = nomic-embed-text.
    embedding     vector(768),

    CONSTRAINT records_kind_chk CHECK (kind IN
        ('note','web_scrape','task_summary','attachment','email_import','manual_curate')),
    CONSTRAINT records_memtype_chk CHECK (memory_type IS NULL OR memory_type IN
        ('semantic','episodic','procedural')),
    CONSTRAINT records_uniq UNIQUE (profile, vault, sha)
);

CREATE TRIGGER records_set_updated_at
    BEFORE UPDATE ON records
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE INDEX records_tenant_idx     ON records (profile, vault);
-- partial index: the "what still needs synthesis" scan (the resynth path).
CREATE INDEX records_unsynth_idx    ON records (profile, vault) WHERE NOT synthesised;
CREATE INDEX records_kind_idx       ON records (profile, vault, kind);
-- provenance: same-source updates key on source_url.
CREATE INDEX records_source_url_idx ON records (profile, vault, source_url)
    WHERE source_url IS NOT NULL;
CREATE INDEX records_tags_gin       ON records USING gin (tags);
CREATE INDEX records_source_gin     ON records USING gin (source);
-- vector similarity (HNSW, cosine). NULL embeddings are skipped.
CREATE INDEX records_embedding_hnsw ON records USING hnsw (embedding vector_cosine_ops);
