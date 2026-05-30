-- Initial schema.
--
-- Preflight (NOT in this migration):
--   * pg_textsearch must be in shared_preload_libraries (postgresql.conf,
--     server restart). The service-level preflight check verifies the
--     extension is loadable before serving traffic.
--   * pg_trgm is a standard contrib extension that ships with Postgres.

CREATE EXTENSION IF NOT EXISTS pg_trgm;
CREATE EXTENSION IF NOT EXISTS pg_textsearch;

CREATE TABLE clients (
  namespace         TEXT NOT NULL,
  name              TEXT NOT NULL,
  created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_activity_at  TIMESTAMPTZ,
  client_token_hash BYTEA,
  token_issued_at   TIMESTAMPTZ,
  token_rotated_at  TIMESTAMPTZ,
  PRIMARY KEY (namespace, name)
);

-- Partial index keeps it tight — only credentialed clients participate.
CREATE INDEX clients_token_hash_idx ON clients(namespace, client_token_hash)
  WHERE client_token_hash IS NOT NULL;

-- session_id is namespace-scoped (composite PK with namespace): the schema
-- treats sessions as subordinate to namespace, matching the conceptual model.
CREATE TABLE sessions (
  session_id    TEXT NOT NULL CHECK (length(session_id) BETWEEN 1 AND 256),
  namespace     TEXT NOT NULL,
  client        TEXT NOT NULL DEFAULT 'default',
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_activity TIMESTAMPTZ NOT NULL DEFAULT now(),
  ttl_minutes   INTEGER NOT NULL,
  -- Maintained by the sessions_set_expires_at trigger below; never written
  -- by app code. The TTL scanner's WHERE expires_at < $1 hits sessions_expires_at_idx
  -- instead of recomputing the predicate per-row. A GENERATED ALWAYS column would
  -- be cleaner, but TIMESTAMPTZ + INTERVAL is only STABLE (DST math depends on
  -- session timezone), and generated columns require IMMUTABLE expressions.
  -- Trigger functions run in execution context where STABLE is fine.
  expires_at    TIMESTAMPTZ NOT NULL,
  PRIMARY KEY (namespace, session_id),
  FOREIGN KEY (namespace, client) REFERENCES clients(namespace, name) ON DELETE CASCADE
);
CREATE INDEX sessions_namespace_client_idx ON sessions(namespace, client);
CREATE INDEX sessions_last_activity_idx    ON sessions(last_activity);
CREATE INDEX sessions_expires_at_idx       ON sessions(expires_at);

CREATE FUNCTION sessions_set_expires_at() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
  NEW.expires_at := NEW.last_activity + (NEW.ttl_minutes * INTERVAL '1 minute');
  RETURN NEW;
END;
$$;

-- Fires on every INSERT and on UPDATEs that touch last_activity or ttl_minutes
-- (Touch hits this; pure no-op UPDATEs do not).
CREATE TRIGGER sessions_set_expires_at_trg
BEFORE INSERT OR UPDATE OF last_activity, ttl_minutes ON sessions
FOR EACH ROW EXECUTE FUNCTION sessions_set_expires_at();

CREATE TABLE session_labels (
  namespace  TEXT NOT NULL,
  session_id TEXT NOT NULL,
  key        TEXT NOT NULL,
  value      TEXT NOT NULL,
  PRIMARY KEY (namespace, session_id, key),
  FOREIGN KEY (namespace, session_id) REFERENCES sessions(namespace, session_id) ON DELETE CASCADE
);
CREATE INDEX session_labels_key_value_idx ON session_labels(namespace, key, value);

CREATE TABLE sources (
  id         BIGSERIAL PRIMARY KEY,
  namespace  TEXT NOT NULL,
  session_id TEXT NOT NULL,
  label      TEXT NOT NULL,
  indexed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (namespace, session_id, label),
  FOREIGN KEY (namespace, session_id) REFERENCES sessions(namespace, session_id) ON DELETE CASCADE
);
CREATE INDEX sources_session_idx ON sources(namespace, session_id);

CREATE TABLE chunks (
  id           BIGSERIAL PRIMARY KEY,
  source_id    BIGINT NOT NULL REFERENCES sources(id) ON DELETE CASCADE,
  title        TEXT NOT NULL,
  content      TEXT NOT NULL,
  content_type TEXT NOT NULL DEFAULT 'prose'
);
CREATE INDEX chunks_source_idx ON chunks(source_id);

-- Layer-1 BM25 indexes, partitioned by tokenization class. Chunks with
-- content_type = 'code' get tokenized by 'simple' (preserves identifiers);
-- everything else by 'english' (porter stemming). Layer-1 fusion at query
-- time runs against both indexes and combines via RRF.
-- pg_textsearch's bm25 access method does not support multi-column indexes,
-- so each (tokenizer-class × field) gets its own index — 4 total.
CREATE INDEX chunks_bm25_prose_title_idx   ON chunks USING bm25 (title)
  WITH (text_config = 'english') WHERE content_type <> 'code';
CREATE INDEX chunks_bm25_prose_content_idx ON chunks USING bm25 (content)
  WITH (text_config = 'english') WHERE content_type <> 'code';
CREATE INDEX chunks_bm25_code_title_idx    ON chunks USING bm25 (title)
  WITH (text_config = 'simple')  WHERE content_type =  'code';
CREATE INDEX chunks_bm25_code_content_idx  ON chunks USING bm25 (content)
  WITH (text_config = 'simple')  WHERE content_type =  'code';

-- Layer-2 trigram substring indexes. Single index per field, all chunks.
CREATE INDEX chunks_title_trgm_idx   ON chunks USING gin (title   gin_trgm_ops);
CREATE INDEX chunks_content_trgm_idx ON chunks USING gin (content gin_trgm_ops);

CREATE TABLE vocabulary (
  namespace  TEXT NOT NULL,
  session_id TEXT NOT NULL,
  word       TEXT NOT NULL,
  doc_freq   INTEGER NOT NULL,
  PRIMARY KEY (namespace, session_id, word),
  FOREIGN KEY (namespace, session_id) REFERENCES sessions(namespace, session_id) ON DELETE CASCADE
);

-- data_hash is BYTEA (raw SHA-256, 32 bytes) rather than hex TEXT (64 chars):
-- value is system-only (never returned to clients), and BYTEA halves storage
-- and index size on a hot table.
CREATE TABLE session_events (
  id         BIGSERIAL PRIMARY KEY,
  namespace  TEXT NOT NULL,
  session_id TEXT NOT NULL,
  type       TEXT NOT NULL,
  priority   INTEGER NOT NULL CHECK (priority BETWEEN 1 AND 4),
  data       JSONB NOT NULL,
  data_hash  BYTEA NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  FOREIGN KEY (namespace, session_id) REFERENCES sessions(namespace, session_id) ON DELETE CASCADE
);
CREATE INDEX session_events_priority_idx ON session_events(namespace, session_id, priority, created_at DESC);
CREATE INDEX session_events_dedup_idx    ON session_events(namespace, session_id, data_hash);
