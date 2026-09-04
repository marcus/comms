CREATE TABLE store_meta(key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE schema_migrations(version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL);
CREATE TABLE agents(
  id TEXT PRIMARY KEY, handle TEXT NOT NULL COLLATE NOCASE UNIQUE, display_name TEXT NOT NULL DEFAULT '',
  purpose TEXT NOT NULL DEFAULT '', harness TEXT NOT NULL DEFAULT '', project TEXT NOT NULL DEFAULT '', session_ref TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, last_seen_at INTEGER NOT NULL, retired_at INTEGER
);
CREATE TABLE agent_external_refs(namespace TEXT NOT NULL, external_key TEXT NOT NULL, agent_id TEXT NOT NULL REFERENCES agents(id), PRIMARY KEY(namespace, external_key));
CREATE TABLE agent_aliases(handle TEXT PRIMARY KEY COLLATE NOCASE, agent_id TEXT NOT NULL REFERENCES agents(id), expires_at INTEGER NOT NULL);
CREATE TABLE topics(
  id TEXT PRIMARY KEY, name TEXT NOT NULL, name_key TEXT NOT NULL UNIQUE, kind TEXT NOT NULL CHECK(kind IN ('public','direct')),
  description TEXT NOT NULL DEFAULT '', next_sequence INTEGER NOT NULL DEFAULT 1 CHECK(next_sequence > 0),
  created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, archived_at INTEGER
);
CREATE TABLE topic_external_refs(namespace TEXT NOT NULL, external_key TEXT NOT NULL, topic_id TEXT NOT NULL REFERENCES topics(id), PRIMARY KEY(namespace, external_key));
CREATE TABLE subscriptions(
  agent_id TEXT NOT NULL REFERENCES agents(id), topic_id TEXT NOT NULL REFERENCES topics(id), followed_at INTEGER NOT NULL,
  unfollowed_at INTEGER, read_through_sequence INTEGER NOT NULL DEFAULT 0, read_through_at INTEGER, updated_at INTEGER NOT NULL,
  PRIMARY KEY(agent_id, topic_id)
);
CREATE TABLE subscription_read_advances(
  agent_id TEXT NOT NULL, topic_id TEXT NOT NULL, through_sequence INTEGER NOT NULL, read_at INTEGER NOT NULL,
  PRIMARY KEY(agent_id, topic_id, through_sequence), FOREIGN KEY(agent_id,topic_id) REFERENCES subscriptions(agent_id,topic_id)
);
CREATE TABLE messages(
  id TEXT PRIMARY KEY, topic_id TEXT NOT NULL REFERENCES topics(id), sequence INTEGER NOT NULL,
  author_id TEXT NOT NULL REFERENCES agents(id), author_context_json TEXT NOT NULL, title TEXT NOT NULL, body TEXT NOT NULL,
  in_reply_to TEXT REFERENCES messages(id), thread_root_id TEXT NOT NULL, created_at INTEGER NOT NULL, expires_at INTEGER,
  metadata_json TEXT, UNIQUE(topic_id, sequence)
);
CREATE INDEX messages_topic_sequence ON messages(topic_id, sequence);
CREATE INDEX messages_thread_sequence ON messages(thread_root_id, sequence);
CREATE INDEX messages_expiry ON messages(expires_at);
CREATE INDEX subscriptions_topic ON subscriptions(topic_id, unfollowed_at);
CREATE VIRTUAL TABLE messages_fts USING fts5(message_id UNINDEXED, title, body);
CREATE TABLE idempotency_keys(
  client_id TEXT NOT NULL, request_id TEXT NOT NULL, operation TEXT NOT NULL, response_json TEXT NOT NULL, created_at INTEGER NOT NULL,
  PRIMARY KEY(client_id, request_id)
);
CREATE TABLE purge_runs(id TEXT PRIMARY KEY, started_at INTEGER NOT NULL, completed_at INTEGER, removed_messages INTEGER NOT NULL DEFAULT 0, error TEXT NOT NULL DEFAULT '');
