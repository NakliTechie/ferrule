package store

const schema = `
PRAGMA journal_mode = WAL;
PRAGMA foreign_keys = ON;

CREATE TABLE IF NOT EXISTS sources (
  id             TEXT PRIMARY KEY,
  name           TEXT NOT NULL UNIQUE,
  provider       TEXT NOT NULL,
  kind           TEXT NOT NULL,          -- local | cloud
  lane           TEXT NOT NULL,          -- tokens | passthrough
  base_url       TEXT NOT NULL,
  key_ref        TEXT NOT NULL DEFAULT '',
  status         TEXT NOT NULL,          -- probing | live | failed
  status_code    TEXT NOT NULL DEFAULT '',   -- closed vocabulary; agents branch on this
  status_reason  TEXT NOT NULL DEFAULT '',   -- the message, for a person
  status_remedy  TEXT NOT NULL DEFAULT '',   -- the exact next move
  detected       INTEGER NOT NULL DEFAULT 0,
  insecure       INTEGER NOT NULL DEFAULT 0,   -- key travels over http, acknowledged
  created_at     INTEGER NOT NULL,
  updated_at     INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS models (
  source_id      TEXT NOT NULL REFERENCES sources(id) ON DELETE CASCADE,
  model_id       TEXT NOT NULL,
  display_name   TEXT NOT NULL DEFAULT '',
  capabilities   TEXT NOT NULL DEFAULT '[]',   -- json array
  modalities     TEXT NOT NULL DEFAULT '[]',   -- json array
  context_length INTEGER NOT NULL DEFAULT 0,
  is_async       INTEGER NOT NULL DEFAULT 0,
  in_cost        REAL NOT NULL DEFAULT 0,      -- USD per 1M input tokens
  out_cost       REAL NOT NULL DEFAULT 0,      -- USD per 1M output tokens
  classified_by  TEXT NOT NULL DEFAULT '',
  updated_at     INTEGER NOT NULL,
  PRIMARY KEY (source_id, model_id)
);

CREATE TABLE IF NOT EXISTS aliases (
  name       TEXT PRIMARY KEY,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS alias_rungs (
  alias     TEXT NOT NULL REFERENCES aliases(name) ON DELETE CASCADE,
  position  INTEGER NOT NULL,
  source_id TEXT NOT NULL,
  model_id  TEXT NOT NULL,
  PRIMARY KEY (alias, position)
);

CREATE TABLE IF NOT EXISTS remaps (
  from_model TEXT PRIMARY KEY,
  target     TEXT NOT NULL,   -- alias name, or "source_id/model_id"
  created_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS grants (
  id         TEXT PRIMARY KEY,
  app        TEXT NOT NULL,
  token_hash TEXT NOT NULL UNIQUE,
  created_at INTEGER NOT NULL,
  revoked_at INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS ledger (
  id                INTEGER PRIMARY KEY AUTOINCREMENT,
  ts                INTEGER NOT NULL,
  grant_id          TEXT NOT NULL DEFAULT '',
  app               TEXT NOT NULL DEFAULT '',
  source_id         TEXT NOT NULL DEFAULT '',
  provider          TEXT NOT NULL DEFAULT '',
  model_id          TEXT NOT NULL DEFAULT '',
  requested_model   TEXT NOT NULL DEFAULT '',
  lane              TEXT NOT NULL DEFAULT 'tokens',
  egress            TEXT NOT NULL DEFAULT 'local',   -- local | cloud
  prompt_tokens     INTEGER NOT NULL DEFAULT 0,
  completion_tokens INTEGER NOT NULL DEFAULT 0,
  cost              REAL NOT NULL DEFAULT 0,
  latency_ms        INTEGER NOT NULL DEFAULT 0,
  status            INTEGER NOT NULL DEFAULT 0,
  err               TEXT NOT NULL DEFAULT '',
  req_bytes         INTEGER NOT NULL DEFAULT 0,
  resp_bytes        INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS ledger_ts   ON ledger(ts);
CREATE INDEX IF NOT EXISTS ledger_app  ON ledger(app);
CREATE INDEX IF NOT EXISTS ledger_mdl  ON ledger(source_id, model_id);

-- What a live probe cost money to learn is kept, so it is never learned twice. The
-- catalog is the authority; this is only for ids the catalog is silent on.
-- Keyed by provider as well as id: two providers can serve different things under the
-- same name, and inheriting one's answer for the other is worse than probing again.
CREATE TABLE IF NOT EXISTS learned (
  provider     TEXT NOT NULL,
  model_id     TEXT NOT NULL,
  capabilities TEXT NOT NULL DEFAULT '[]',
  learned_at   INTEGER NOT NULL,
  PRIMARY KEY (provider, model_id)
);

CREATE TABLE IF NOT EXISTS settings (
  k TEXT PRIMARY KEY,
  v TEXT NOT NULL
);

-- Content lives apart from the metadata ledger on purpose: the ledger can then be
-- described, honestly and without qualification, as holding no prompt or response text.
CREATE TABLE IF NOT EXISTS content_log (
  id        INTEGER PRIMARY KEY AUTOINCREMENT,
  ledger_id INTEGER NOT NULL,
  ts        INTEGER NOT NULL,
  app       TEXT NOT NULL DEFAULT '',
  model     TEXT NOT NULL DEFAULT '',
  request   TEXT NOT NULL DEFAULT '',
  response  TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS content_ledger ON content_log(ledger_id);

CREATE TABLE IF NOT EXISTS staged_ops (
  id         TEXT PRIMARY KEY,
  op         TEXT NOT NULL,
  payload    TEXT NOT NULL,
  door       TEXT NOT NULL DEFAULT '',
  caller     TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL,
  applied_at INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS control_log (
  id        INTEGER PRIMARY KEY AUTOINCREMENT,
  ts        INTEGER NOT NULL,
  op        TEXT NOT NULL,
  door      TEXT NOT NULL DEFAULT '',
  caller    TEXT NOT NULL DEFAULT '',
  outcome   TEXT NOT NULL DEFAULT ''
);
`
