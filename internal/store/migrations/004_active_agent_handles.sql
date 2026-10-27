CREATE TABLE agents_next(
  id TEXT PRIMARY KEY, handle TEXT NOT NULL COLLATE NOCASE, display_name TEXT NOT NULL DEFAULT '',
  purpose TEXT NOT NULL DEFAULT '', harness TEXT NOT NULL DEFAULT '', project TEXT NOT NULL DEFAULT '', session_ref TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, last_seen_at INTEGER NOT NULL, retired_at INTEGER
);
INSERT INTO agents_next SELECT * FROM agents;
DROP TABLE agents;
ALTER TABLE agents_next RENAME TO agents;
CREATE UNIQUE INDEX agents_active_handle ON agents(handle COLLATE NOCASE) WHERE retired_at IS NULL;
CREATE INDEX agents_handle_history ON agents(handle COLLATE NOCASE);
DELETE FROM agent_aliases WHERE agent_id IN (SELECT id FROM agents WHERE retired_at IS NOT NULL);
