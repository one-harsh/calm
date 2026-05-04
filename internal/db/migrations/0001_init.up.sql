-- Initial schema. Matches HLD §7 logical data model.
--
-- Preflight (NOT in this migration):
--   * pg_textsearch must be in shared_preload_libraries (postgresql.conf,
--     server restart). The service-level preflight check verifies the
--     extension is loadable before serving traffic.
--   * pg_trgm is a standard contrib extension that ships with Postgres.
--
-- Tokenizer mapping (HLD DL06; HLD §7 uses SQLite-tokenizer terminology):
--   HLD "porter + unicode61" → pg_textsearch text_config = 'english'
--   HLD "unicode61 only"     → pg_textsearch text_config = 'simple'

CREATE EXTENSION IF NOT EXISTS pg_trgm;
CREATE EXTENSION IF NOT EXISTS pg_textsearch;

CREATE TABLE clients (
  namespace        TEXT NOT NULL,
  name             TEXT NOT NULL,
  created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_activity_at TIMESTAMPTZ,
  PRIMARY KEY (namespace, name)
);

CREATE TABLE sessions (
  session_id    TEXT PRIMARY KEY CHECK (length(session_id) BETWEEN 1 AND 256),
  namespace     TEXT NOT NULL,
  client        TEXT NOT NULL DEFAULT 'default',
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_activity TIMESTAMPTZ NOT NULL DEFAULT now(),
  ttl_minutes   INTEGER NOT NULL,
  FOREIGN KEY (namespace, client) REFERENCES clients(namespace, name) ON DELETE CASCADE
);
CREATE INDEX sessions_namespace_client_idx ON sessions(namespace, client);
CREATE INDEX sessions_last_activity_idx    ON sessions(last_activity);

CREATE TABLE session_labels (
  session_id TEXT NOT NULL REFERENCES sessions(session_id) ON DELETE CASCADE,
  key        TEXT NOT NULL,
  value      TEXT NOT NULL,
  PRIMARY KEY (session_id, key)
);
CREATE INDEX session_labels_key_value_idx ON session_labels(key, value);

CREATE TABLE sources (
  id         BIGSERIAL PRIMARY KEY,
  session_id TEXT NOT NULL REFERENCES sessions(session_id) ON DELETE CASCADE,
  label      TEXT NOT NULL,
  indexed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (session_id, label)
);
CREATE INDEX sources_session_idx ON sources(session_id);

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
-- time runs against both indexes and combines via RRF (HLD §7).
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
  session_id TEXT NOT NULL REFERENCES sessions(session_id) ON DELETE CASCADE,
  word       TEXT NOT NULL,
  doc_freq   INTEGER NOT NULL,
  PRIMARY KEY (session_id, word)
);

CREATE TABLE session_events (
  id         BIGSERIAL PRIMARY KEY,
  session_id TEXT NOT NULL REFERENCES sessions(session_id) ON DELETE CASCADE,
  type       TEXT NOT NULL,
  priority   INTEGER NOT NULL CHECK (priority BETWEEN 1 AND 4),
  data       JSONB NOT NULL,
  data_hash  TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX session_events_priority_idx ON session_events(session_id, priority, created_at DESC);
CREATE INDEX session_events_dedup_idx    ON session_events(session_id, data_hash);
