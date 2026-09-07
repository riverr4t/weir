CREATE TABLE strikes (
  download_id TEXT NOT NULL,
  app         TEXT NOT NULL,
  title       TEXT NOT NULL,
  condition   TEXT NOT NULL,
  count       INTEGER NOT NULL DEFAULT 0,
  first_seen  TEXT NOT NULL,
  last_seen   TEXT NOT NULL,
  cleared_at  TEXT,
  PRIMARY KEY (download_id, condition)
);

CREATE TABLE actions (
  id      INTEGER PRIMARY KEY AUTOINCREMENT,
  at      TEXT NOT NULL,
  kind    TEXT NOT NULL,
  app     TEXT NOT NULL,
  subject TEXT NOT NULL,
  detail  TEXT NOT NULL DEFAULT '{}',
  actor   TEXT NOT NULL DEFAULT 'weir',
  outcome TEXT NOT NULL,
  error   TEXT NOT NULL DEFAULT ''
);
CREATE INDEX actions_at ON actions(at);
CREATE INDEX actions_kind_at ON actions(kind, at);
