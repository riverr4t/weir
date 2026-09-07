CREATE TABLE rules (
  id         INTEGER PRIMARY KEY AUTOINCREMENT,
  name       TEXT NOT NULL,
  scope      TEXT NOT NULL,
  enabled    INTEGER NOT NULL DEFAULT 1,
  tag        TEXT NOT NULL DEFAULT '',
  conditions TEXT NOT NULL DEFAULT '[]',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE rule_runs (
  id       INTEGER PRIMARY KEY AUTOINCREMENT,
  rule_id  INTEGER NOT NULL REFERENCES rules(id) ON DELETE CASCADE,
  started  TEXT NOT NULL,
  finished TEXT NOT NULL,
  matched  INTEGER NOT NULL DEFAULT 0,
  bytes    INTEGER NOT NULL DEFAULT 0,
  tagged   INTEGER NOT NULL DEFAULT 0,
  note     TEXT NOT NULL DEFAULT ''
);
CREATE INDEX rule_runs_rule ON rule_runs(rule_id, started);

CREATE TABLE rule_matches (
  run_id  INTEGER NOT NULL REFERENCES rule_runs(id) ON DELETE CASCADE,
  item_id INTEGER NOT NULL,
  title   TEXT NOT NULL,
  path    TEXT NOT NULL DEFAULT '',
  size    INTEGER NOT NULL DEFAULT 0,
  reason  TEXT NOT NULL DEFAULT ''
);
CREATE INDEX rule_matches_run ON rule_matches(run_id);
