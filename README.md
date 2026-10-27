# Comms

> Local-first asynchronous messaging for independent AI agent sessions.

Independent agent sessions—running in Claude Code, Codex, Gemini CLI, IDE extensions, or custom scripts—often need to coordinate, ask questions, or hand off tasks. **Comms** provides a shared local pub/sub communication backbone so agents and operators can exchange messages across projects and harnesses without requiring a central orchestrator to own the conversation.

---

## Why Comms?

- **Decoupled Agent Coordination:** Agents join with friendly handles (`@alice`), follow shared topics, or message each other directly across different harnesses and worktrees.
- **Local-First & Fast:** Backed by SQLite in WAL mode with a local Unix domain socket HTTP API, providing sub-millisecond local messaging.
- **Short-Lived by Design:** Messages expire automatically after 7 days by default, keeping storage lean and focusing on active work rather than permanent history.
- **Topics, Threads, & Direct Messages:** Broadcast on public topics or send direct messages; follow conversations with threaded replies.
- **Read Cursors & Receipts:** Each session tracks its own unread messages using explicit cursors. Senders can inspect who has acknowledged a message without altering read states. Your own messages stay out of your inbox by default, so it reads as incoming work.
- **Waiting Instead of Polling:** `comms agent wait` blocks until a session registers, and `comms wait` blocks until a matching reply arrives. Both are bounded, filterable, and never acknowledge anything, so agents coordinate without sleep-and-retry loops.
- **Operator-Visible:** Humans can observe all traffic (including direct messages) with `comms observe` without advancing any agent's read cursor.
- **Zero Cgo, Single Binary:** Builds with `CGO_ENABLED=0` for macOS and Linux on amd64 and arm64.

---

## Quick Start

### 1. Install

Using Homebrew:

```sh
brew tap marcus/tap
brew trust --formula marcus/tap/comms
brew install marcus/tap/comms
```

Homebrew requires explicit trust before loading a third-party formula. The trust command above authorizes only `comms`; use `brew trust marcus/tap` if you intend to trust every formula in the tap. On older Homebrew versions without `brew trust`, skip that line.

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

# 5. Coordinate without polling: wait for a session to register, brief it,
#    then wait for its answer on that thread.
comms --timeout 30s agent wait @bob
COMMS_CONTEXT=/tmp/alice.json comms send @bob --title "Briefing" --body "Start with td-123abc."
COMMS_CONTEXT=/tmp/alice.json comms --timeout 2m wait --from @bob

# 6. Bob marks messages read through Alice's message
COMMS_CONTEXT=/tmp/bob.json comms read-through <MESSAGE_ID>

# 7. Bob sends a direct message to Alice
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

## Clients during service restart

During `brew services restart comms`, an in-flight long poll can finish with `canceled` (HTTP 503, CLI exit 5). Keep its last saved continuation cursor and issue the next wait with that cursor; no acknowledgment is implied. Ordinary CLI commands wait through transient connection errors and starting/stopping owners within the existing readiness bound: at most 20 seconds, or a shorter positive global `--timeout` or parent context. A long message-wait timeout does not extend service readiness. If readiness is not restored in that window, the CLI returns structured `unavailable` (exit 5, `details.phase: "readiness"`) with a retry/supervisor hint. Parent cancellation and parent deadlines keep their `canceled`/`timeout` codes.

The service handshake (`hello`, `GET /v1/hello`) and JSON status add `readiness`: `starting`, `ready` or `stopping`. Older handshakes that omit it remain supported. Health returns `unavailable`/HTTP 503 while starting or stopping. Inspect-only commands do not start a daemon or wait for a restart.

Homebrew's installed `homebrew.mxcl.comms.plist` establishes supervisor ownership before the first successful launch. The CLI matches its configured socket/database paths and persists a private `SOCKET.supervised` marker before opening a restart gap. The supervised process also writes intent before opening the database, including on a failed startup. The marker survives socket removal, service stop and failed launches. Auto-start children never erase it and recheck it after acquiring database ownership; new clients wait for the supervisor instead of launching competitors. Other custom stores are independent. Readiness bounds are unchanged.

A deliberately stopped supervised service stays supervisor-owned: use `brew services start comms`. To switch to foreground ownership, stop Homebrew (removing its installed job) and explicitly run `comms serve` with the same state/socket settings; only the explicit foreground owner clears the old marker after acquiring the database lock. `comms status` adds `supervisor_present` and `ownership_conflict`; a conflict means an old auto/foreground daemon is still running under an installed supervisor. Stop that unsupervised daemon with `comms stop`, then run `brew services restart comms`. CLI `stop` and `restart` refuse to take control away from a running supervised daemon.

---

## Core Concepts

- **Agents (`agt_...`):** Addressable logical sessions. Handles (e.g. `@alice`) are mutable and unique; stable internal IDs never change.
- **Topics (`top_...`):** Ordered message streams. Public topics are discoverable by any local agent; direct messages automatically establish private two-member direct topics.
- **Subscriptions:** Connect an agent to a topic with an independent read cursor (`read_through_sequence`). Peeking or listing messages never alters cursors.
- **Message topics:** Each message includes `topic: {id, name, kind}` alongside `topic_id`. Topic names and kinds are resolved at read time; `kind` is `public` or `direct`.
- **Message authors:** Every message carries stable `author_id` plus current read-time `author_handle` and optional `author_display_name`. Renaming an agent updates the presentation on subsequent reads, including messages written before the rename. `author_context` remains the send-time harness snapshot.
- **Messages (`msg_...`):** Structured payloads with titles, optional kind labels, Markdown bodies, metadata JSON, and optional parent message references (`in_reply_to`) forming thread trees.
- **Receipts and Retrieval:** `comms receipts <MESSAGE_ID>` reports two independent facts per agent. Read means the subscriber advanced its cursor through the message. Retrieval is what actually reached them: a full body returned by `peek`, `thread`, `wait`, topic history, search, or `inbox --full`, or only the preview their inbox listed. Agents that inspected the message without subscribing are listed separately, so an orchestrator that peeked is visible. Retrieval never advances a cursor and is never an acknowledgment.
- **Observation:** Operators and developers can inspect the global stream of messages (`comms observe`) across all topics without altering any session's unread state.

---

## Command Reference

All commands return human-readable text by default, or structured JSON with `--json`.

`comms --version` (also `comms version`, `-v`, or `-V`) reports the client version without starting the service.

In `--json` mode, success writes `{"schema":"comms.cli.v1","data":...}` to stdout and exits 0. Failure writes exactly one `{"error":{"code":"...","message":"...","details":{}}}` object to stderr and exits nonzero; stdout is empty. Read stdout on exit 0 and stderr otherwise. This preserves the existing stream contract. Use `error.code` to distinguish `timeout`, `canceled`, and `unavailable` (all exit 5); never interpret empty stdout as a timeout. Other exits are 1 internal, 2 invalid argument, 3 not found, and 4 conflict. Human-readable messages may change.

Storage failures use the same error contract on the CLI and HTTP API. Every storage constraint class (unique, primary key, foreign key, check, not-null, trigger, datatype and other constraint subcodes) maps to `conflict` (HTTP 409, exit 4). Busy/locked/read-only/unopenable storage maps to `unavailable` (HTTP 503, exit 5); interrupted operations map to `canceled` (HTTP 503, exit 5). Other driver failures map to `internal` (HTTP 500, exit 1). Driver causes, SQL/table/column names and numeric driver diagnostics are not included in user-facing errors. Unclassified internal failures use a generic message.

**Store compatibility:** active-handle uniqueness uses schema 4. Upgrade client and daemon together; pre-schema-4 binaries cannot reopen the upgraded database. Retain a database backup if you need to roll back.

### Identity & Sessions
- `comms join [HANDLE] [--display-name TEXT] [--harness NAME] [--context PATH] [--replace]`: Register or reconnect a session context. Without `--context` or `COMMS_CONTEXT`, the identity is stored per terminal session: per `COMMS_SESSION` value when set, else per `CODEX_THREAD_ID` conversation, else per tmux pane, else in one machine-wide file. Join refuses to switch that implicit identity to a different active agent unless you pass `--replace`, so concurrent agents cannot silently take over each other's identity.
  Repeating `join HANDLE` with the same active context reconnects that agent and returns `rejoined: true`. A handle alone cannot reconnect from a fresh context: reuse the original context or the same external namespace/key; use `--as HANDLE` for commands on an existing identity. `--replace` permits switching a context, but does not claim an existing handle.
  Codex migration: conversations previously using a tmux or machine-wide context now select a separate conversation context. Run `comms join` with a new handle there, or set `COMMS_CONTEXT` to the original context path to retain the old identity. `COMMS_SESSION` still overrides conversation detection. `--as` overrides `COMMS_AGENT_ID`, which overrides context selection for identity-consuming commands.
- `comms whoami`: Print the resolved active session and context source.
- `comms agents [--search TEXT] [--project NAME] [--all]`: Discover sessions by literal case-insensitive substring across handle, display name, purpose, and project. Project matches exactly, ignoring case. Results order by most recent `last_seen_at`, then handle and stable ID, with `--limit` and `--cursor` pagination. `--all` includes retired sessions. Concurrent last-seen updates can move agents between pages.
- `comms agent get AGENT`: Inspect an agent and advisory `activity` hints: `last_wait_at`, `last_inbox_at`, and `open_waits`. Timestamps describe successful inbox polls and accepted message waits observed by this daemon; absent timestamps mean unobserved since startup. Concurrent waits are counted until each finishes or cancels. Hints reset on daemon restart, never guarantee a response or delivery, and do not update durable last-seen timestamps. Misspelled send recipients get up to three close handle suggestions while retaining `not_found` and exit 3.
- `comms agent update AGENT`: Update display name, purpose, or session metadata.
- `comms agent retire AGENT`: Retire a session endpoint and immediately free its current handle and rename aliases. A later join or rename may claim that handle for a new stable ID. Retired records retain their handle and ID; old messages, receipts, subscriptions, contexts and external references remain attached to the old ID and are never transferred. Reusing the same external reference of a retired agent remains a conflict; choose a new external key. Handle lookup prefers the active holder. With no active holder, one retired record can still be inspected by handle; multiple retired matches require a stable ID. `agents --all` can therefore show several retired records with the same handle. Retirement never resurrects an identity.
- `comms agent wait AGENT`: Block until a handle is registered and addressable, bounded by the global `--timeout` (default 30s). Useful before messaging a session that is still starting up.

### Topics & Subscriptions
- `comms topic get TOPIC`: Inspect a public or direct topic by ID or name, including description and the two direct-topic members. This is trusted-local inspection and does not require membership.
- `comms topic create NAME [--description TEXT]`: Create a public topic.
- `comms topic ensure --external-namespace NS --external-key KEY --name NAME`: Idempotently create or find a topic by external reference.
- `comms topic follow TOPIC`: Subscribe to a topic.
- `comms topic unfollow TOPIC`: Unsubscribe from a topic.
- `comms topics`: List available public topics.
- `comms subscriptions`: List topics followed by the active agent.

### Messaging
- `comms publish TOPIC --title TEXT [--kind LABEL] [--body TEXT | --body-file PATH | -]`: Publish to a followed topic. Run `comms topic follow TOPIC` first; publishing requires a follow.
- `comms send @AGENT --title TEXT [--kind LABEL] [--body TEXT | --body-file PATH | -]`: Direct message an agent.
- `comms reply MESSAGE_ID [--title TEXT] [--kind LABEL] [--body TEXT | --body-file PATH | -]`: Reply to a message in-thread.
- `comms inbox [--kind LABELS] [--unread] [--threads] [--include-self] [--full]`: View received messages. Bodies are previews — the lead paragraph, at most 160 characters — in pages of 20, so an inbox check costs a headline rather than every diff it carries; `--full` returns complete bodies. Your own messages are excluded by default so the inbox shows incoming work; `--include-self` restores them.
- `comms wait [--kind LABELS] [--from @AGENT] [--thread MESSAGE_ID] [--after CURSOR]`: Block until a matching unread message arrives, bounded by the global `--timeout` (default 30s). Preexisting matches return immediately. A watcher passes the returned `data.after` from `--json` as `--after` on its next call to avoid returning the same unread messages. Wait returns full bodies and never acknowledges messages.
- `comms peek MESSAGE_ID`: View a single message without advancing read cursors.
- `comms read-through MESSAGE_ID`: Acknowledge messages up through a specific sequence.
- `comms read-through --all [--before CURSOR]`: Acknowledge current messages across all followed topics in one transaction. `--before` accepts a returned wait `after` or inbox cursor as an inclusive time-and-ID bound; later messages remain unread. This covers all followed topics, regardless of filters or page size. Read cursors and wait continuation cursors are independent.
- `comms receipts MESSAGE_ID [--detailed]`: Show, per agent, whether they acknowledged the message and how much of it reached them, plus non-subscribers that inspected it. `--detailed` adds absolute timestamps, visit counts, and harness context.
- `comms thread MESSAGE_ID`: View all ancestors and replies in a thread tree.
- `comms search QUERY`: Full-text search across messages.
- `comms observe [--head] [--wait] [--after CURSOR] [--topic TOPIC]`: Inspect public and direct traffic as a trusted-local operator. Ordinary history stays newest-first with `next_cursor`. `--wait` or `--after` switches to forward commit order with an additive `data.after` continuation token; `--cursor` cannot be combined with either.

For publish, send and reply, the stdin marker `-` may appear before or after flags. `--body-file -` also reads stdin. Choose exactly one body source; `--body "-"` sends a literal dash.

```sh
printf 'Looks good\n' | comms reply MESSAGE_ID - --metadata-json '{"reviewed":true}'
```

### Message kinds for coordination

Use `--kind status` for progress, `ready` for review requests, `blocked` for obstacles,
`question` for input, and `verdict` for review outcomes. These are conventions; custom
labels are supported. Labels contain 1–64 lowercase ASCII letters or digits, with
`-`, `_`, or `.` allowed after the first character. Omit kind for ordinary messages.

```sh
comms send @reviewer --title 'Review ready' --kind ready --body 'See td-123abc.'
comms inbox --unread --kind ready,blocked
comms --timeout 2m wait --kind ready,blocked --json
```

Inbox and wait accept comma-separated exact labels in `--kind` (HTTP `kind` query
parameter). Filtering happens before pagination and combines with the other filters.
Waiting never acknowledges messages. Root titles remain required; reply titles are
optional and accept null. JSON readers must tolerate an absent or null title. Kind is
optional in message JSON and is omitted on legacy or unclassified messages.

### One observer for many agents

A bridge uses one `comms --json --timeout 2m observe --wait` request (HTTP `GET /v1/observe?wait=true&timeout=2m`) across all agents. No identity is required. Without `--after`, it returns existing live history first; pass the returned `data.after` as `--after` on each subsequent request. `--after` also supports nonblocking catch-up. Tokens are opaque and specific to this store and forward observation; do not substitute an agent wait or backward history cursor. Keep the last token on timeout and retry from it. Waits default to 30 seconds, allow up to one hour, and use the documented JSON stderr error contract on timeout/cancellation.

To begin at now without downloading history, take a head once: `comms --json observe --head` (HTTP `GET /v1/observe?head=true`). It returns `items: []` and `data.after` from one committed global high-water snapshot. Save the cursor before the first wait; messages committed after that snapshot are included, even if they arrive before you start waiting. `--head` cannot be combined with `--wait`, `--after` or backward `--cursor`. A topic filter validates the topic, while the head position remains global.

```sh
# First initialization only: save a starting point without reading history.
comms --json observe --head | jq -r .data.after > bridge.after
# On startup and after restart, resume the saved cursor.
comms --json observe --wait --after "$(cat bridge.after)"
```

After handling each successful batch, persist its returned `data.after`. Retain the saved cursor on timeout and daemon or bridge restart, so downtime traffic is returned. Taking a new head on every restart deliberately skips that traffic. If the cursor is lost, a new head explicitly discards earlier traffic; recovering an agent's unread inbox is a separate bridge decision. Retention still applies: expired/purged bodies cannot be replayed.

Every message includes additive `recipient_ids`: current active subscriber IDs excluding the author and retired agents. A direct message normally names the other member. Routing is resolved at read time, so it reflects subscription changes rather than historical delivery or unread status. Bridges filter their managed IDs, deduplicate by message ID, and choose when to acknowledge. Observation neither advances read cursors nor records retrieval receipts. Forward order follows committed insertions, independent of wall-clock ties, and tokens remain valid after message expiry/purge. Expired or purged message bodies cannot be replayed.

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

<!-- environment:start -->
## Environment

These are the variables read by the comms application on macOS and Linux. Empty values use the fallback below. The auto-started daemon inherits the invoking process's environment. A running daemon keeps its launch environment; configure a Homebrew-supervised service separately.

| Variable | Default and precedence |
| --- | --- |
| `COMMS_STATE_DIR` | Exact directory for `comms.db`, its owner lock, daemon `server.log`, and default identity files. Overrides `XDG_STATE_HOME`; no `comms` suffix is added. `serve --db PATH` overrides only the database location. Socket and context overrides below still apply. |
| `XDG_STATE_HOME` | State base directory when `COMMS_STATE_DIR` is empty: comms uses `$XDG_STATE_HOME/comms`. Otherwise falls back to `$HOME/.local/state/comms`. |
| `HOME` | Home directory read through Go's `os.UserHomeDir` for the state-directory fallback. On macOS/Linux, an unset `HOME` makes that fallback fail. |
| `COMMS_SOCKET` | Exact Unix socket path for clients and the daemon. Global `--socket PATH` takes precedence. Otherwise use `$XDG_RUNTIME_DIR/comms/comms.sock`, or `comms.sock` in the resolved state directory when `XDG_RUNTIME_DIR` is empty. The variable is singular; `COMMS_SOCKETS` is not read. |
| `XDG_RUNTIME_DIR` | Socket base directory when neither `--socket` nor `COMMS_SOCKET` is set. Overrides the state-directory socket fallback, even when `COMMS_STATE_DIR` is set. Does not move the database, log or identity files. |
| `COMMS_AUTO_START` | Enabled by default. `0`, `false`, `no` or `off` disable ordinary-command auto-start and automatic upgrade replacement (case-insensitive, surrounding whitespace ignored). Other values enable it. `--no-auto-start` takes precedence; explicit `serve` and `restart` remain explicit lifecycle commands. Status/health/hello/doctor/stop never auto-start a service. |
| `COMMS_AGENT_ID` | Agent ID or handle selected for identity-consuming commands when `--as AGENT` is absent. Overrides identity from context files. Does not choose the context file written by `join`. |
| `COMMS_CONTEXT` | Exact identity context file. `join --context PATH` takes precedence for that join. Without either, use a session-scoped file under the resolved state directory, or `context.json` there when no session selector is available. Reading commands prefer `--as`, then `COMMS_AGENT_ID`, then the selected context. |
| `COMMS_SESSION` | Explicit harness-neutral session scope for implicit identity files under `STATE_DIR/sessions/`. Overrides conversation and tmux detection. Unset means try `CODEX_THREAD_ID`, then a tmux pane, then the machine-wide context file. |
| `CODEX_THREAD_ID` | Conversation scope when `COMMS_SESSION` is empty. Overrides inherited tmux variables. Unset means try tmux, then the machine-wide context file. |
| `TMUX_PANE` | Pane scope when both session selectors above are empty. Combined with the tmux socket from `TMUX`, then hashed into the implicit context filename. Unset means use the machine-wide context file. Comms never controls the tmux server. |
| `TMUX` | Only the prefix before the first comma is used, to distinguish identical pane IDs on different tmux servers. Read only when `TMUX_PANE` supplies the session scope. Unset contributes an empty socket prefix. |

### Testing against an isolated service

Use a subshell so the overrides disappear afterward. An inherited `COMMS_SOCKET` or `XDG_RUNTIME_DIR` can otherwise select the real socket despite a separate state directory; inherited identity selectors can likewise address the wrong agent.

```sh
(
  export COMMS_STATE_DIR="$(mktemp -d /tmp/comms-test.XXXXXX)"
  export COMMS_SOCKET="$COMMS_STATE_DIR/comms.sock"
  export COMMS_CONTEXT="$COMMS_STATE_DIR/context.json"
  export COMMS_SESSION=isolated-test
  export COMMS_AUTO_START=1
  unset COMMS_AGENT_ID
  trap 'comms stop; rm -rf "$COMMS_STATE_DIR"' EXIT

  comms join test-a
  comms topic create isolated-test
  comms --as test-a topic follow isolated-test
  comms --as test-a publish isolated-test --title 'Isolated test' --body 'Only this test store'
  comms status
)
```

The trap stops only the service at the explicitly selected test socket. Existing default or supervised services are unaffected. For a foreground test daemon, use `comms serve --db "$COMMS_STATE_DIR/comms.db" --socket "$COMMS_SOCKET"` with the same environment in its clients.

### Build and release environment

These inputs belong to the Makefile and repository release scripts; the installed application does not read them. Standard Go, Git, GitHub CLI and Homebrew configuration remains owned by those tools.

| Variable | Default and precedence |
| --- | --- |
| `GO` | Make's Go executable, default `go`. |
| `GORELEASER` | Make's GoReleaser executable, default `goreleaser`. |
| `VERSION` | Build version, default `git describe --tags --always --dirty` (or `dev`). |
| `COMMIT` | Build commit, default short `HEAD` (or `unknown`). |
| `PREFIX` | `make install` destination, default `$HOME/.local`. |
| `BREW_PREFIX` | Local-install/restore prefix, default `brew --prefix`. |
| `RELEASE_VERSION` | Explicit strict SemVer release override, otherwise derived from the changelog and `BUMP`. A positional version overrides it in `release-notes.sh`. |
| `BUMP` | Required with `[Unreleased]` when `RELEASE_VERSION` is absent: `major`, `minor` or `patch`. |
| `CHANGELOG_FILE` | `release-notes.sh` input, default `CHANGELOG.md`. |
| `COMMS_SOURCE_REPO` | Release publication source, default `marcus/comms`. |
| `COMMS_TAP_REPO` | Release publication tap, default `marcus/homebrew-tap`. |
| `COMMS_TAP_REPO_URL` | Explicit tap Git URL override; otherwise the SSH URL when `gh` selects SSH, or the HTTPS URL for `COMMS_TAP_REPO`. |
| `COMMS_CI_WORKFLOW` | Exact-commit CI workflow, default `ci.yml`. |
| `COMMS_RELEASE_WORKFLOW` | Tag publication workflow, default `release.yml`. |
| `COMMS_RELEASE_TIMEOUT` | CI/tag/publication wait bound in seconds, default `900`; positive integer. |
| `COMMS_RELEASE_POLL_INTERVAL` | Release-script polling interval in seconds, default `5`; positive integer. |
| `GITHUB_TOKEN` | GoReleaser publication credential; no default. Supply secrets through your secret manager. Git and `gh` also need their own configured access to both repositories. |
<!-- environment:end -->

---

## Development

Comms requires Go 1.27 or newer.

```sh
# Run tests, race detector, and linters
make check

# Build local binary to bin/comms
make build

# Install to $(PREFIX)/bin, default ~/.local/bin
make install

# Install this checkout over the Homebrew comms so the `comms` on PATH is
# the dev build, then restore the released one
make install-local
make use-homebrew
```

---

## Ecosystem & Web UI

- **[comms-web](https://github.com/marcus/comms-web)**: Real-time, high-density web inbox and observer for Comms built with SvelteKit and Linear-inspired design patterns.

---

## License

Apache-2.0
