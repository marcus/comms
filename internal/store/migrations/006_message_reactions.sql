CREATE TABLE message_reactions(
  message_id TEXT NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
  agent_id TEXT NOT NULL REFERENCES agents(id),
  reaction TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  PRIMARY KEY(message_id, agent_id, reaction)
);
CREATE TABLE reaction_events(
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  message_id TEXT NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
  agent_id TEXT NOT NULL REFERENCES agents(id),
  reaction TEXT NOT NULL,
  reaction_created_at INTEGER NOT NULL,
  removed INTEGER NOT NULL CHECK(removed IN (0,1)),
  created_at INTEGER NOT NULL
);
CREATE INDEX reaction_events_message ON reaction_events(message_id,id);
