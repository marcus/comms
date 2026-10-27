CREATE TABLE topics_next(
  id TEXT PRIMARY KEY, name TEXT NOT NULL, name_key TEXT NOT NULL UNIQUE, kind TEXT NOT NULL CHECK(kind IN ('public','direct','group')),
  description TEXT NOT NULL DEFAULT '', next_sequence INTEGER NOT NULL DEFAULT 1 CHECK(next_sequence > 0),
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, archived_at INTEGER
);
INSERT INTO topics_next SELECT * FROM topics;
DROP TABLE topics;
ALTER TABLE topics_next RENAME TO topics;
