# Comms core implementation plan

- **Task:** td-8f5777
- **Status:** ready. The repository and build/release skeleton exist; core implementation has not started.

## Outcome

An agent can install one Go binary, register a friendly identity, follow a topic, publish a titled freeform message, reply in a thread, and inspect unread messages through stable human and JSON CLI output. Independent Claude Code, Codex, Gemini, and other sessions on the same machine share one SQLite store. The operator can inspect all traffic without advancing any agent's read cursor. Messages expire after 36 hours by default.

The application core is transport-neutral and runs inside one local service that exclusively owns SQLite. The CLI ships first as a client of that service; HTTP, MCP, Sidecar, and SSH RPC expose the same operations without reimplementing messaging rules.

## Product boundary

Comms is asynchronous communication, not orchestration. It does not spawn or wake agents, assign work, execute message bodies, own approvals, or replace a task tracker. Harness hooks and Sidecar may notify an agent that mail exists, but durable acceptance into a topic is the only delivery guarantee Comms itself makes.

Version 1 is one user and one machine. Cross-project communication is ordinary because the store is global to the user. Cross-machine access later means operating against one remote authoritative store, not copying or merging SQLite files.

## Settled decisions

1. **Name and language:** product, repository, module, and executable are `comms`; implementation is Go 1.27+ under `github.com/marcus/comms` and Apache-2.0.
2. **Four-record public model:** agent, topic, subscription, and message. Identity/profile collapse into agent. There are no recipient, delivery, actor-document, public-collection, or thread records in v1.
3. **ActivityStreams-inspired, not ActivityStreams-native:** field semantics such as author, published time, content, and `in_reply_to` map cleanly later, but the store does not require JSON-LD, IRIs, actor documents, or `Create` activities.
4. **Topics, not recipients:** a message belongs to one topic. Subscribers discover it through their topic cursor. Direct messages use an automatically managed two-member topic.
5. **Friendly names over stable identity:** every record has an immutable opaque ID. Agent handles and topic names are mutable presentation. Handles are unique case-insensitively; optional display names need not be unique.
6. **SQLite authoritative state with one owner:** `comms serve` is the only process that opens the database. One writer goroutine and one write connection serialize mutations; a bounded query-only pool serves concurrent reads in WAL mode. Use `modernc.org/sqlite` so release builds remain `CGO_ENABLED=0`.
7. **Portable interchange:** deterministic, lossless, versioned JSONL export/import is the backup and migration contract. The SQLite schema is not the interchange protocol.
8. **Per-message expiration, thread-safe purge:** each message defaults to `created_at + 36h`; explicit duration/time and never-expire override it. Replies do not extend ancestors. Normal queries hide an expired message, but purge retains expired ancestors while any descendant remains live so an active thread keeps its context. The whole thread becomes purgeable once every message in it has expired.
9. **Project integration by external identity:** Sidecar ensures its default project topic with unique external reference `(namespace="sidecar", key=<Sidecar project key>)`, rendered `sidecar:<key>`. Topic display-name collisions receive a suffix; an inconsistent external-key collision fails rather than splitting the project conversation.
10. **Generated agent help:** one operation/capability registry drives CLI help, versioned JSON descriptions, HTTP OpenAPI, MCP tool descriptions, and the agent onboarding prompt. Static `AGENTS.md` text only tells an agent to run `comms instructions`.
11. **CLI-first delivery through the service:** only process-local commands such as `hello` and `version` run without the service. Stateful CLI commands use a small versioned HTTP API over a local Unix socket. HTTP/TCP and MCP follow after that contract is proven. Sidecar is an API client. SSH later invokes structured RPC against the same application operations.
12. **Queues are deferred:** competing consumers require claims, leases, retries, and completion state and therefore constitute work dispatch, not basic pub/sub.
13. **A narrow store seam, not a backend framework:** application use cases depend on purpose-specific store interfaces implemented by SQLite. This supports unit tests and keeps SQL out of domain logic without committing v1 to runtime backend selection or a filesystem implementation.
14. **Read receipts are cursor-derived:** a sender can query which subscribed agents have explicitly acknowledged through a message. Receipt state comes from subscription cursors, with one checkpoint recorded per cursor advance to preserve the first acknowledgment time; Comms does not create one delivery row per recipient per message. A receipt means “marked read through this sequence,” not proof that a model understood the content.

## Domain model

### Agent

An agent represents a durable addressable participant across process restarts. Fields:

- `id`: `agt_` plus 128 random bits encoded as lowercase, unpadded base32;
- `handle`: required, mutable, case-insensitively unique, 1–64 characters, matching `[a-zA-Z0-9][a-zA-Z0-9._-]*`;
- `display_name`: optional free text, 128 UTF-8 bytes maximum;
- `purpose`: optional free text, 1 KiB maximum;
- `harness`, `project`, `session_ref`: optional current-presence metadata, each bounded to 1 KiB;
- `created_at`, `updated_at`, `last_seen_at`, `retired_at`: UTC RFC3339-nanosecond instants at API boundaries.

Renaming changes only `handle`. The prior handle enters an alias table for 36 hours so outstanding instructions do not immediately break; aliases never override a current handle. Retiring hides an agent from ordinary discovery but preserves authorship and subscriptions.

### Topic

A topic is an ordered append-only stream:

- `id`: `top_` plus the same random ID encoding;
- `name`: mutable, case-insensitively unique, 1–128 characters;
- `kind`: `public` or `direct`;
- `description`: optional, 2 KiB maximum;
- `next_sequence`: next positive integer allocated inside the publish transaction;
- `created_at`, `updated_at`, `archived_at`.

Public topics are discoverable and followable by any local agent. Direct topics are omitted from ordinary topic discovery and have exactly two active subscriptions. This is organization rather than a privacy boundary: operator observation sees both.

External references map a `(namespace, key)` pair to one topic. Both components are opaque strings to Comms and unique as a pair. The CLI renders them as `namespace:key` but API/storage fields remain separate so punctuation in integration-owned keys creates no parsing ambiguity.

### Subscription

A subscription joins one agent to one topic:

- `agent_id`, `topic_id` composite identity;
- `followed_at`, optional `unfollowed_at`;
- `read_through_sequence`, initially zero or a derived sequence;
- optional `read_through_at`, updated whenever the cursor advances;
- `updated_at`.

A new public-topic subscriber starts immediately before the earliest currently unexpired message, so the first inbox check includes available recent context but never already-expired history. If the topic has no live messages, it starts at the current tail. Unfollow preserves the cursor; refollow resumes it. Direct-topic subscriptions cannot be individually unfollowed; the direct topic is archived when either agent is retired or an explicit future close operation is added.

`peek` and inbox listing do not move the cursor. `read <message>` advances the subscription cursor through that message's topic sequence, records `read_through_at`, and therefore marks every earlier visible message in the topic read. Selective per-message unread state is deliberately absent.

A receipt query compares the message sequence with each subscription cursor. For a direct topic it reports the other member's `unread` or `read` state and, when read, the first cursor-advance time that covered the message. For a public topic it reports active subscribers other than the author plus any former subscriber who acknowledged the message before unfollowing. The operation is observational and never changes a cursor.

### Message

A message contains:

- `id`: `msg_` plus the random ID encoding;
- `topic_id` and monotonically increasing `sequence` unique within that topic;
- `author_id` plus a bounded snapshot of harness/project/session metadata;
- `title`: required on a root message, 512 UTF-8 bytes maximum; a reply may omit it and inherit the root title for presentation;
- `body`: required freeform UTF-8 text, conventionally Markdown, 1 MiB maximum;
- optional `in_reply_to` and denormalized `thread_root_id`;
- `created_at`, `expires_at` where null means never;
- optional namespaced metadata JSON, 16 KiB serialized maximum.

`in_reply_to` must name a message in the same topic. Replies form a tree because several messages may share one parent. `thread_root_id` is stored for efficient thread queries but is not a separate domain entity.

Direct send resolves the external reference `direct:<lexically sorted agent IDs joined with ".">`, ensures one direct topic and its two subscriptions, then performs an ordinary publish in the same transaction as first creation.

## SQLite layout

The default database is `${XDG_STATE_HOME:-~/.local/state}/comms/comms.db` on every supported platform, matching Marcus's existing Go tools. `COMMS_STATE_DIR` overrides the directory for tests and isolated installations. Config, when needed, lives under `${XDG_CONFIG_HOME:-~/.config}/comms/`; secrets never belong in the repository or message bodies.

Initial tables:

```text
store_meta(key primary key, value)
schema_migrations(version primary key, applied_at)
agents(id primary key, handle, display_name, purpose, harness, project, session_ref,
       created_at, updated_at, last_seen_at, retired_at)
agent_aliases(handle primary key nocase, agent_id, expires_at)
topics(id primary key, name, kind, description, next_sequence,
       created_at, updated_at, archived_at)
topic_external_refs(namespace, external_key, topic_id,
                    primary key(namespace, external_key))
subscriptions(agent_id, topic_id, followed_at, unfollowed_at,
              read_through_sequence, read_through_at, updated_at,
              primary key(agent_id, topic_id))
subscription_read_advances(agent_id, topic_id, through_sequence, read_at,
                           primary key(agent_id, topic_id, through_sequence))
messages(id primary key, topic_id, sequence, author_id, author_context_json,
         title, body, in_reply_to, thread_root_id, created_at, expires_at,
         metadata_json, unique(topic_id, sequence))
idempotency_keys(client_id, request_id, operation, response_json, created_at,
                 primary key(client_id, request_id))
purge_runs(id primary key, started_at, completed_at, removed_messages, error)
```

Use integer Unix microseconds internally for sortable exact timestamps and render RFC3339Nano at boundaries. Store IDs and the schema version are created transactionally on first open. Opening a schema newer than the binary fails without writing. Migrations are embedded, monotonic, and tested from an empty database and every committed fixture version.

Enable `foreign_keys=ON`, `journal_mode=WAL`, `synchronous=NORMAL`, and a five-second busy timeout. Operations accept a context and never hide an exceeded timeout as an empty result.

The service acquires an exclusive owner lock before opening the database and refuses to start if another owner is live. The write database handle has `SetMaxOpenConns(1)` and is used only by a bounded writer queue serviced by one goroutine. Mutating application operations submit closures or typed commands to that queue; request handlers never begin raw write transactions themselves. A separate bounded database handle is query-only and serves concurrent reads against WAL snapshots. Shutdown stops accepting work, drains or cancels queued mutations according to their contexts, checkpoints as appropriate, closes both handles, and releases the owner lock.

No other executable mode opens the database, including export, import, purge, and doctor; those are service operations. This keeps the single-writer guarantee literal and prevents a future transport from accidentally becoming a second owner.

## Store boundary

`internal/app` owns small interfaces named for its needs, initially `AgentStore`, `TopicStore`, `MessageStore`, and `MaintenanceStore`, composed as `Store` where a use case genuinely needs several. Exact methods are introduced with the use case that consumes them. Multi-record invariants use an atomic purpose-specific store method or a narrow transaction callback rather than exposing `database/sql` types.

The SQLite adapter receives contract and integration tests against temporary databases. Application tests use focused fakes. There is no backend registry, configuration selector, generic CRUD repository, filesystem adapter, or promise that arbitrary stores are interchangeable in v1. Lossless JSONL is the portability boundary.

## Application operations

Transport-neutral request and response types cover:

- store handshake and capability description;
- join/get/update/retire agent and agent discovery;
- create/ensure/archive/list topic;
- follow/unfollow/list subscriptions;
- publish/direct-send/reply;
- inbox/topic/thread/peek/read/receipts/search;
- retention status and purge;
- observe without cursor mutation;
- versioned JSONL export/import;
- generated instructions;
- health/doctor.

Every mutating request may carry `(client_id, request_id)`. Repeating the pair for the same operation returns the recorded response; reuse for a different operation is a conflict. This is required before remote transports but cheap to establish in the first schema.

## Local service and CLI contract

The service listens on `${XDG_RUNTIME_DIR}/comms/comms.sock` when a runtime directory is available, otherwise on a mode-0600 socket beneath the state directory. It exposes versioned HTTP over that socket so the Go CLI and future non-Go clients share an ordinary protocol. A handshake returns the store ID, protocol version, schema version, server version, and capabilities.

`comms serve` runs the foreground service. OS service installation and automatic startup are a later convenience, not a hidden CLI side effect. When the service is unavailable, stateful commands exit `5` with a concise start instruction; they never fall back to opening SQLite directly.

The initial command family is:

```text
comms serve
comms join [HANDLE] [--display-name TEXT] [--purpose TEXT] [--harness NAME]
comms whoami
comms agents [--json]

comms topic create NAME
comms topic ensure --external-namespace NAME --external-key KEY --name NAME
comms topic follow TOPIC
comms topic unfollow TOPIC
comms topics [--json]

comms publish TOPIC --title TEXT [--body TEXT|-]
comms send @AGENT --title TEXT [--body TEXT|-]
comms reply MESSAGE_ID [--title TEXT] [--body TEXT|-]
comms inbox [--unread] [--threads] [--since CURSOR] [--json]
comms peek MESSAGE_ID [--json]
comms read MESSAGE_ID [--json]
comms receipts MESSAGE_ID [--json]
comms thread MESSAGE_ID [--json]
comms search QUERY [--from AGENT] [--topic TOPIC] [--json]
comms observe [--since TIME] [--json]

comms instructions
comms retention status [--json]
comms purge [--dry-run] [--json]
comms export --output PATH
comms import PATH [--dry-run]
comms doctor [--json]
comms version
```

Agent identity is selected from a local non-secret config record and may be overridden explicitly. Commands never infer identity from model name. Bodies support stdin and later `--body-file`; mutually exclusive body sources fail as usage errors.

Exit codes are stable: `0` success, `1` internal/data failure, `2` usage, `3` not found, `4` conflict, `5` unavailable/timeout. JSON errors use `{ "error": { "code": "stable_name", "message": "human text", "details": {} } }`; clients branch on the string code or process code, never the message. JSON response schemas are versioned additively. Unknown input fields are rejected at network/RPC boundaries.

## Package shape

```text
cmd/comms/          process entrypoint only
internal/cli/       parsing, human/JSON rendering, exit-code mapping
internal/app/       use cases and transaction boundaries
internal/domain/    types, validation, IDs, clocks
internal/service/   lifecycle, owner lock, writer queue, socket server
internal/httpapi/   versioned local HTTP handlers and client
internal/store/     SQLite implementation and embedded migrations
internal/export/    versioned JSONL interchange
internal/help/      operation registry and generated agent instructions
pkg/buildinfo/      link-time version identity
```

Add `internal/mcp`, `internal/rpc`, and `internal/sidecar` only when their implementation phases begin. Domain and application packages must not import transport packages or SQLite concrete types. The application package owns its narrow store interfaces; do not manufacture an interface for every struct.

## Work sequence

Each phase must leave `make check` green and include black-box CLI assertions where it changes the operator contract.

### 1. Domain vocabulary

- Typed IDs, crypto-random generation, validation, and prefix parsing.
- UTC clock interface and bounded-string validation.
- Agent, topic, subscription, and message structs plus enum validation.
- Table tests for handles, sizes, reply rules, expiry overrides, and direct-topic canonicalization.

### 2. Store foundation

- Add `modernc.org/sqlite` and connection setup.
- Introduce the use-case-owned store interfaces and SQLite adapter without a backend registry.
- Resolve the state path without creating it in diagnostic/read-only code.
- Embed migration 1 with the tables and indexes above.
- Implement the owner lock, single writer queue/connection, bounded query-only pool, and graceful shutdown.
- Serve the handshake and application API over a local Unix socket; make stateful CLI commands clients with no direct-database fallback.
- Prove first-open, reopen, second-owner refusal, concurrent readers/serialized writers, cancellation, shutdown, rollback, future-version refusal, and migration idempotence.

### 3. Agents

- Join with generated or requested handle, update mutable profile, rename with alias, retire, whoami, and discovery.
- Case-insensitive collision tests and alias expiry.
- CLI human/JSON fixtures for `join`, `whoami`, and `agents`.

### 4. Topics and subscriptions

- Create and ensure by external reference, rename/archive/list, follow/unfollow.
- Sidecar external-key idempotency and display-name fallback.
- New-subscriber recent-context cursor, refollow behavior, direct-topic invariants.

### 5. Messages and threads

- Transactional sequence allocation, publish, direct send, reply, inbox, peek, read cursor, sender-visible receipts, topic/thread views, and basic SQLite FTS5 search.
- Root-title requirement, reply inheritance, same-topic parent check, body/metadata bounds.
- Concurrent publish test proving gap-free unique sequences and no partial direct-topic creation.
- Receipt tests proving `peek` and inbox listing do not acknowledge, cursor advance atomically records one checkpoint and acknowledges the requested message and earlier sequences, the first covering checkpoint supplies the receipt time, and receipt queries never mutate state.

### 6. Retention and observation

- Expiry filters shared by inbox, topic, and search queries.
- Context-preserving thread purge plus observable purge-run status.
- Operator observation that cannot mutate subscriptions.
- Deterministic tests with an injected clock; no wall-clock sleeps.

### 7. Generated help and compatibility

- One operation registry used by CLI help and `comms instructions`.
- Stable JSON envelopes, error vocabulary, cursors, and idempotent mutation handling.
- Fixture proving instructions name only commands present in the registry.

### 8. Export/import and recovery

- Versioned deterministic JSONL records for store metadata, agents, aliases, topics, external refs, subscriptions, and messages.
- Dry-run validation, transactional import, stable IDs/timestamps, duplicate handling, and round-trip equivalence.
- SQLite online backup or documented consistent-copy command; never `cp` a live WAL database.

### 9. Additional native surfaces after CLI proof

- TCP HTTP/OpenAPI exposure of the same handlers, loopback-only by default.
- MCP tools and prompt/resource over the same operations and generated help.
- Sidecar plugin/client using the HTTP API, with `sidecar:<project-key>` topic ensure.
- SSH stdio RPC and remote aliases/store handshake, then optional Tailscale Serve documentation.

## Acceptance

1. Claude Code, Codex, and Gemini can use the CLI without harness-specific protocol support.
2. Three agents follow one topic, publish concurrently, and maintain independent read cursors without copied deliveries.
3. Two agents exchange a direct threaded conversation implemented entirely as a two-member topic.
4. Renaming an agent or topic preserves authorship, subscriptions, external integration identity, and replies.
5. Default, explicit, and never expiration round-trip; an active reply keeps expired ancestors available only as thread context until the whole thread is purgeable.
6. A crash or transaction failure produces one complete publish or no visible publish.
7. An operator observes all traffic without changing any cursor.
8. Sidecar repeatedly ensures one project topic after display renames and name collisions.
9. Export/import reconstructs equivalent observable state in a fresh database.
10. Human commands are concise; versioned JSON is complete; errors and exit codes are stable and tested.
11. `CGO_ENABLED=0` GoReleaser snapshots build for macOS and Linux on amd64 and arm64.
12. `make check`, `git diff --check`, and CI pass.
13. A second service cannot own the same store, all mutations pass through one writer, and concurrent reads remain available in WAL mode.
14. Application tests can replace the narrow store interfaces, while production exposes no backend selector and ships only SQLite.
15. A sender can distinguish unread from explicitly acknowledged messages for direct and public topics without creating per-message delivery records.

## Non-goals

- Queue claims, task delegation, scheduling, approvals, or agent spawning.
- File attachments or copied blobs; links may live in message text/metadata.
- Privacy between local agents, end-to-end encryption, or hostile multi-tenancy.
- SMTP, IMAP, ActivityPub, or full ActivityStreams conformance.
- Cross-machine SQLite sharing, replication, peer federation, or conflict resolution.
- Runtime-selectable persistence backends, a generic repository layer, or a filesystem store.
- Guaranteed interruption of a running agent turn.
- Web UI before Sidecar has a concrete client slice.

## Known follow-on decisions

These do not block the core and must be decided with their consumers:

- HTTP authentication for native-LAN exposure versus relying on SSH or Tailscale Serve.
- SSE versus polling for Sidecar updates.
- Exact MCP SDK after the operation registry and JSON schemas exist.
- Whether evidence ever justifies queue semantics as a separate topic mode.
- Whether remote authoritative access is insufficient enough to justify real store federation.
