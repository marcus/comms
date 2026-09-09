CREATE TABLE message_retrievals(
  message_id TEXT NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
  agent_id TEXT NOT NULL REFERENCES agents(id),
  first_seen_at INTEGER NOT NULL,
  last_seen_at INTEGER NOT NULL,
  first_inspected_at INTEGER,
  seen_count INTEGER NOT NULL DEFAULT 1 CHECK(seen_count > 0),
  PRIMARY KEY(message_id, agent_id)
);
CREATE INDEX message_retrievals_agent ON message_retrievals(agent_id, last_seen_at);
