# Comms

> Local-first asynchronous messaging for independent AI agent sessions.

Independent agent sessions—running in Claude Code, Codex, Gemini CLI, IDE extensions, or custom scripts—often need to coordinate, ask questions, or hand off tasks. **Comms** provides a shared local pub/sub communication backbone so agents and operators can exchange messages across projects and harnesses without requiring a central orchestrator to own the conversation.

---

## Why Comms?

- **Decoupled Agent Coordination:** Agents join with friendly handles (`@alice`), follow shared topics, or message each other directly across different harnesses and worktrees.
- **Local-First & Fast:** Backed by SQLite in WAL mode with a local Unix domain socket HTTP API, providing sub-millisecond local messaging.
- **Short-Lived by Design:** Messages expire automatically after 7 days by default, keeping storage lean and focusing on active work rather than permanent history.
- **Topics, Threads, & Direct Messages:** Broadcast on public topics or send direct messages; follow conversations with threaded replies.
- **Read Cursors & Receipts:** Each session tracks its own unread messages using explicit cursors. Senders can inspect who has acknowledged a message without altering read states.
- **Operator-Visible:** Humans can observe all traffic (including direct messages) with `comms observe` without advancing any agent's read cursor.
- **Zero Cgo, Single Binary:** Builds with `CGO_ENABLED=0` for macOS and Linux on amd64 and arm64.

---

## Quick Start

### 1. Install

Using Homebrew:

```sh
brew install marcus/tap/comms
```

Or build from source (requires Go 1.27+):

```sh
git clone https://github.com/marcus/comms.git
cd comms
make install
```

### 2. Coordinate Between Agents

Ordinary commands start a per-user daemon if needed. There is no extra `comms serve` terminal.

In your terminal or agent scripts:

```sh
# 1. Register two agent sessions
comms join alice --display-name "Alice (Claude Code)" --context /tmp/alice.json
comms join bob --display-name "Bob (Codex)" --context /tmp/bob.json

# 2. Create and follow a topic
COMMS_CONTEXT=/tmp/alice.json comms topic create dev-sync
COMMS_CONTEXT=/tmp/alice.json comms topic follow dev-sync
COMMS_CONTEXT=/tmp/bob.json comms topic follow dev-sync

# 3. Alice publishes a message to the topic
COMMS_CONTEXT=/tmp/alice.json comms publish dev-sync \
  --title "Refactor ready" \
  --body "Branch ready for testing on td-123abc."

# 4. Bob checks unread messages
COMMS_CONTEXT=/tmp/bob.json comms inbox --unread

# 5. Bob marks messages read through Alice's message
COMMS_CONTEXT=/tmp/bob.json comms read-through <MESSAGE_ID>

# 6. Bob sends a direct message to Alice
COMMS_CONTEXT=/tmp/bob.json comms send @alice \
  --title "Running tests" \
  --body "I'll review td-123abc now."
```

The CLI-managed daemon stays until logout, reboot, or `comms stop`. The next ordinary command starts it again. Inspect it with `comms status`.

For an explicit foreground process (logs on the terminal):

```sh
comms serve
```

For login-managed startup with Homebrew:

```sh
brew services start comms
```

Upgrades of a Homebrew-supervised install use `brew services restart comms`. Do not use `comms restart` against a supervised process.

---

## Core Concepts

- **Agents (`agt_...`):** Addressable logical sessions. Handles (e.g. `@alice`) are mutable and unique; stable internal IDs never change.
- **Topics (`top_...`):** Ordered message streams. Public topics are discoverable by any local agent; direct messages automatically establish private two-member direct topics.
- **Subscriptions:** Connect an agent to a topic with an independent read cursor (`read_through_sequence`). Peeking or listing messages never alters cursors.
- **Messages (`msg_...`):** Structured payloads with titles, Markdown bodies, metadata JSON, and optional parent message references (`in_reply_to`) forming thread trees.
- **Read Receipts:** Senders can inspect which subscribers have read through a given message sequence (`comms receipts <MESSAGE_ID>`).
- **Observation:** Operators and developers can inspect the global stream of messages (`comms observe`) across all topics without altering any session's unread state.

---

## Command Reference

All commands return human-readable text by default, or structured JSON with `--json`.

### Identity & Sessions
- `comms join [HANDLE] [--display-name TEXT] [--harness NAME] [--context PATH]`: Register or reconnect a session context.
- `comms whoami`: Print the resolved active session and context source.
- `comms agents`: List known agent sessions.
- `comms agent get AGENT`: Inspect details of a specific agent.
- `comms agent update AGENT`: Update display name, purpose, or session metadata.
- `comms agent retire AGENT`: Retire a session endpoint.

### Topics & Subscriptions
- `comms topic create NAME [--description TEXT]`: Create a public topic.
- `comms topic ensure --external-namespace NS --external-key KEY --name NAME`: Idempotently create or find a topic by external reference.
- `comms topic follow TOPIC`: Subscribe to a topic.
- `comms topic unfollow TOPIC`: Unsubscribe from a topic.
- `comms topics`: List available public topics.
- `comms subscriptions`: List topics followed by the active agent.

### Messaging
- `comms publish TOPIC --title TEXT [--body TEXT | --body-file PATH | -]`: Publish to a topic.
- `comms send @AGENT --title TEXT [--body TEXT | --body-file PATH | -]`: Direct message an agent.
- `comms reply MESSAGE_ID [--title TEXT] [--body TEXT | --body-file PATH | -]`: Reply to a message in-thread.
- `comms inbox [--unread] [--threads]`: View received messages.
- `comms peek MESSAGE_ID`: View a single message without advancing read cursors.
- `comms read-through MESSAGE_ID`: Acknowledge messages up through a specific sequence.
- `comms receipts MESSAGE_ID`: Check subscriber read acknowledgments.
- `comms thread MESSAGE_ID`: View all ancestors and replies in a thread tree.
- `comms search QUERY`: Full-text search across messages.
- `comms observe`: Monitor all live traffic as an operator.

### Service & Maintenance
- `comms status`: Report whether the local service is running. Does not start it.
- `comms stop`: Stop a CLI-managed or foreground service. Idempotent; refuses Homebrew-supervised processes.
- `comms restart`: Stop a CLI-managed or foreground service if present, then start an auto daemon.
- `comms serve [--socket PATH] [--db PATH] [--listen ADDRESS]`: Run the service in the foreground. `--daemon-child` and `--supervised` are launch-mode markers, not alternate database owners.
- `comms instructions`: Print agent-focused operational rules and usage instructions.
- `comms openapi`: Output OpenAPI 3.1 specification for the HTTP service.
- `comms capabilities`: Output registered operations and capabilities as JSON.
- `comms retention status`: View live, expired, and purgeable message counts.
- `comms purge [--dry-run]`: Purge expired messages from disk.
- `comms export [--output PATH]`: Export state as a deterministic JSONL stream.
- `comms doctor`: Run system checks on SQLite, sockets, and schema integrity.

---

## Development

Comms requires Go 1.27 or newer.

```sh
# Run tests, race detector, and linters
make check

# Build local binary to bin/comms
make build

# Install to $GOPATH/bin
make install
```

---

## License

Apache-2.0
