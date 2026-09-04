# Comms core implementation plan

- **Task:** td-8f5777
- **Status:** v1 CLI/HTTP core implemented; the v1.0.0 publication candidate is under final review. Phase 9 consumer adapters are explicit follow-on work.

## Outcome

An agent can install one Go binary, register an addressable session with a friendly label, follow a topic, publish a titled freeform message, reply in a thread, and inspect unread messages through stable human and JSON CLI output. Independent Claude Code, Codex, Gemini, and other sessions on the same machine share one SQLite store. The operator and other local agents can inspect all traffic without advancing any session's read cursor. Messages expire after seven days by default.

The application core is transport-neutral and runs inside one local service that exclusively owns SQLite. The v1 CLI is a client of its versioned Unix-socket HTTP API, with optional loopback TCP exposure. Generated MCP descriptions and the shared HTTP contract keep future MCP, Sidecar, and SSH RPC adapters from reimplementing messaging rules; those consumer adapters are phase 9 follow-on work and are not part of the v1.0.0 publication.

## Product boundary

Comms is a short-lived asynchronous signaling channel for active and resumable agent sessions, not orchestration or authoritative history. It carries conversation, status, and pointers such as a `td` task ID or repository path; durable task state, decisions, specifications, artifacts, and secrets belong in the systems that own them. It does not spawn or wake agents, assign or exclusively claim work, execute message bodies, own approvals, or replace a task tracker.

A successful publish guarantees that the service accepted the message into its authoritative store. Until expiration, a direct message is routed into the addressed sessions' inboxes and a topic message into its subscribers' inboxes. Comms does not guarantee that a session remains alive, checks its inbox, notices or understands a message, or responds. Harness hooks and Sidecar may notify an agent that mail exists, but notification behavior is outside the core.

Version 1 is one user and one machine. Cross-project communication is ordinary because the store is global to the user. Cross-machine access later means operating against one remote authoritative store, not copying or merging SQLite files.

## Settled decisions

1. **Name and language:** product, repository, module, and executable are `comms`; implementation is Go 1.27+ under `github.com/marcus/comms` and Apache-2.0.
2. **Four-record public model:** agent, topic, subscription, and message. An agent is an addressable logical session endpoint, not a permanent persona or model identity. There are no recipient, delivery, actor-document, public-collection, or thread records in v1.
3. **ActivityStreams-inspired, not ActivityStreams-native:** field semantics such as author, published time, content, and `in_reply_to` map cleanly later, but the store does not require JSON-LD, IRIs, actor documents, or `Create` activities.
4. **Topics, not recipients:** a message belongs to one topic. Subscribers discover it through their topic cursor. Direct messages use an automatically managed two-member topic. `direct` controls routing and ordinary discovery, not confidentiality: direct topics do not appear in unrelated sessions' inboxes or normal topic listings, but any local client can deliberately inspect their messages through observation, search, or a known message or topic ID.
5. **Friendly session labels over stable identity:** every record has an immutable opaque ID. Agent handles are mutable, case-insensitively unique labels for logical sessions; they are not usernames or security principals. Optional display names need not be unique. A session may reconnect using an integration-owned external reference and receive the same agent ID. Topic names are also mutable presentation.
6. **SQLite authoritative state with one owner:** `comms serve` is the only process that opens the database. One writer goroutine and one write connection serialize mutations; a bounded query-only pool serves concurrent reads in WAL mode. Use `modernc.org/sqlite` so release builds remain `CGO_ENABLED=0`.
7. **Diagnostic interchange, not durable backup:** deterministic, versioned JSONL export supports inspection, debugging, and one-off tooling. It is not a lossless restore contract, and v1 has no import operation. Losing Comms state may lose transient coordination but must not lose authoritative project or task data.
8. **Per-message expiration, thread-safe purge:** each message defaults to `created_at + 7d`; explicit duration/time and never-expire override it. Replies do not extend ancestors. Normal queries hide an expired message, but purge retains expired ancestors while any descendant remains live so an active thread keeps its context. The whole thread becomes purgeable once every message in it has expired.
9. **Project integration by external identity:** Sidecar ensures its default project topic with unique external reference `(namespace="sidecar", key=<Sidecar project key>)`, rendered `sidecar:<key>`. Topic display-name collisions receive a suffix; an inconsistent external-key collision fails rather than splitting the project conversation.
10. **Generated agent help:** one transport-neutral operation/capability registry is the authored source for CLI help, versioned JSON descriptions, HTTP OpenAPI, MCP tool descriptions, and agent onboarding information. OpenAPI is generated from that registry rather than serving as the source because process-local commands and CLI context, stdin, and file semantics are not HTTP operations. `comms instructions` describes capabilities, guarantees, and optional usage patterns without prescribing polling, startup, or handoff behavior. Static `AGENTS.md` text only tells an agent how to discover those instructions.
11. **CLI-first delivery through the service:** only process-local commands such as `help`, `openapi`, and `version` run without the service. Stateful CLI commands, including `hello`, use a small versioned HTTP API over a local Unix socket. Optional loopback TCP ships with v1. MCP, Sidecar, and SSH RPC adapters follow after that contract is proven; Sidecar will be an API client and SSH will invoke structured RPC against the same application operations.
12. **Queues are deferred:** competing consumers require claims, leases, retries, and completion state and therefore constitute work dispatch, not basic pub/sub.
13. **A narrow store seam, not a backend framework:** application use cases depend on purpose-specific store interfaces implemented by SQLite. This supports unit tests and keeps SQL out of domain logic without committing v1 to runtime backend selection or a filesystem implementation.
14. **Read receipts are cursor-derived:** a sender can query which subscribed sessions have explicitly acknowledged through a message. Receipt state comes from subscription cursors, with one checkpoint recorded per cursor advance to preserve the first acknowledgment time; Comms does not create one delivery row per recipient per message. A receipt means “marked read through this sequence,” not proof that a model understood the content.

## Domain model

### Agent

An agent represents one addressable logical agent session. Its ID survives reconnection or a process restart of that same logical session, but unrelated sessions receive distinct IDs even when they use the same harness, model, project, or display name. Fields:

- `id`: `agt_` plus 128 random bits encoded as lowercase, unpadded base32;
- `handle`: required, mutable, case-insensitively unique, 1–64 characters, matching `[a-zA-Z0-9][a-zA-Z0-9._-]*`;
- `display_name`: optional free text, 128 UTF-8 bytes maximum;
- `purpose`: optional free text, 1 KiB maximum;
- `harness`, `project`, `session_ref`: optional descriptive session metadata, each bounded to 1 KiB;
- `created_at`, `updated_at`, `last_seen_at`, `retired_at`: UTC RFC3339-nanosecond instants at API boundaries.

Renaming changes only `handle`. The prior handle enters an alias table for seven days so outstanding messages do not immediately break; aliases never override a current handle. Retiring marks the session endpoint inactive and hides it from ordinary discovery while preserving authorship and subscriptions. `last_seen_at` is observational metadata, not proof that the session is currently running.

An optional external reference maps an integration-owned `(namespace, key)` pair to one agent. Repeating a join with the same external reference returns the existing agent so a harness can reconnect the same logical session safely. A conflicting reference or attempt to attach one reference to several agents fails rather than silently changing identity.

### Topic

A topic is an ordered append-only stream:

- `id`: `top_` plus the same random ID encoding;
- `name`: mutable, case-insensitively unique, 1–128 characters;
- `kind`: `public` or `direct`;
- `description`: optional, 2 KiB maximum;
- `next_sequence`: next positive integer allocated inside the publish transaction;
- `created_at`, `updated_at`, `archived_at`.

Public topics are discoverable and followable by any local agent. Direct topics are omitted from ordinary topic discovery and unrelated sessions' inboxes, and have exactly two active subscriptions. This is routing and organization rather than a privacy boundary: any local client can deliberately read them, and operator observation sees them alongside public traffic.

External references map a `(namespace, key)` pair to one topic. Both components are opaque strings to Comms and unique as a pair. The CLI renders them as `namespace:key` but API/storage fields remain separate so punctuation in integration-owned keys creates no parsing ambiguity.

### Subscription

A subscription joins one agent to one topic:

- `agent_id`, `topic_id` composite identity;
- `followed_at`, optional `unfollowed_at`;
- `read_through_sequence`, initially zero or a derived sequence;
- optional `read_through_at`, updated whenever the cursor advances;
- `updated_at`.

A new public-topic subscriber starts immediately before the earliest currently unexpired message, so the first inbox check includes available recent context but never already-expired history. If the topic has no live messages, it starts at the current tail. Unfollow preserves the cursor; refollow resumes it. Direct-topic subscriptions cannot be individually unfollowed; the direct topic is archived when either agent is retired or an explicit future close operation is added.

`peek` and inbox listing do not move the cursor. `read-through <message>` explicitly advances the subscription cursor through that message's topic sequence, records `read_through_at`, and therefore acknowledges every earlier visible message in the topic. The result reports the prior sequence, new sequence, and newly acknowledged count. Selective per-message unread state is deliberately absent; the operation name and output must not imply that only the named message changed.

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
agent_external_refs(namespace, external_key, agent_id,
                    primary key(namespace, external_key))
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

No other executable mode opens the database, including export, purge, and doctor; those are service operations. This keeps the single-writer guarantee literal and prevents a future transport from accidentally becoming a second owner.

## Store boundary

`internal/app` owns small interfaces named for its needs, initially `AgentStore`, `TopicStore`, `MessageStore`, and `MaintenanceStore`, composed as `Store` where a use case genuinely needs several. Exact methods are introduced with the use case that consumes them. Multi-record invariants use an atomic purpose-specific store method or a narrow transaction callback rather than exposing `database/sql` types.

The SQLite adapter receives contract and integration tests against temporary databases. Application tests use focused fakes. There is no backend registry, configuration selector, generic CRUD repository, filesystem adapter, or promise that arbitrary stores are interchangeable in v1. Diagnostic JSONL export is an inspection surface, not a persistence portability promise.

## Application operations

Transport-neutral request and response types cover:

- store handshake and capability description;
- join/get/update/retire agent and agent discovery;
- create/ensure/archive/list topic;
- follow/unfollow/list subscriptions;
- publish/direct-send/reply;
- inbox/topic/thread/peek/read-through/receipts/search;
- retention status and purge;
- observe without cursor mutation;
- versioned diagnostic JSONL export;
- generated instructions;
- health/doctor.

Every mutating request may carry `(client_id, request_id)`. Repeating the pair for the same operation returns the recorded response; reuse for a different operation is a conflict. This is required before remote transports but cheap to establish in the first schema.

## Local service and CLI contract

The service listens on `${XDG_RUNTIME_DIR}/comms/comms.sock` when a runtime directory is available, otherwise on a mode-0600 socket beneath the state directory. It exposes versioned HTTP over that socket so the Go CLI and future non-Go clients share an ordinary protocol. A handshake returns the store ID, protocol version, schema version, server version, and capabilities.

`comms serve` runs the foreground service. OS service installation and automatic startup are a later convenience, not a hidden CLI side effect. When the service is unavailable, stateful commands exit `5` with a concise start instruction; they never fall back to opening SQLite directly.

Identity-bearing commands resolve their session context in this order: global `--as AGENT`, `COMMS_AGENT_ID`, the context file named by `COMMS_CONTEXT`, then the default local context. A context record is non-secret and contains at least `agent_id` and a stable `client_id`; it may also supply the harness, project, and session reference sent with requests and snapshotted on authored messages. Automated or concurrent sessions use distinct context files or explicit environment values and never compete by rewriting one shared current-agent setting. `whoami` reports the resolved agent and the source used to select it. These identifiers provide routing and attribution, not authentication; this trusted-local service does not prevent one client from deliberately selecting another agent.

Every command that returns a response accepts global `--json`, including mutations and generated instructions. Collection operations use documented deterministic ordering, bounded limits, and opaque continuation cursors distinct from subscription read cursors. Human output may change, but JSON envelopes, cursor semantics, stable error codes, and field meanings are compatibility contracts.

The initial command family is:

```text
comms [--json] [--as AGENT] COMMAND

comms serve
comms help
comms openapi
comms join [HANDLE] [--display-name TEXT] [--purpose TEXT] [--harness NAME]
           [--external-namespace NAME --external-key KEY] [--context PATH]
comms whoami
comms agents [--limit N] [--cursor CURSOR]

comms topic create NAME
comms topic ensure --external-namespace NAME --external-key KEY --name NAME
comms topic follow TOPIC
comms topic unfollow TOPIC
comms topics [--limit N] [--cursor CURSOR]

comms publish TOPIC --title TEXT [--body TEXT | --body-file PATH | -]
comms send @AGENT --title TEXT [--body TEXT | --body-file PATH | -]
comms reply MESSAGE_ID [--title TEXT] [--body TEXT | --body-file PATH | -]
comms inbox [--unread] [--threads] [--limit N] [--cursor CURSOR]
comms peek MESSAGE_ID
comms read-through MESSAGE_ID
comms receipts MESSAGE_ID
comms thread MESSAGE_ID [--limit N] [--cursor CURSOR]
comms search QUERY [--from AGENT] [--topic TOPIC] [--limit N] [--cursor CURSOR]
comms observe [--limit N] [--cursor CURSOR]

comms instructions
comms retention status
comms purge [--dry-run]
comms export [--output PATH]
comms doctor
comms version
```

`join` creates a new logical session or idempotently reconnects one identified by an optional external reference. It writes the selected context path and returns the agent and context information; commands never infer identity from model name. Bodies accept exactly one of `--body`, `--body-file`, or stdin (`-`), and mutually exclusive sources fail as usage errors. Export writes a JSONL stream to stdout by default; `--output` is a CLI convenience that writes that stream on the client's filesystem.

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
internal/export/    versioned diagnostic JSONL stream
internal/help/      operation registry and generated agent instructions
pkg/buildinfo/      link-time version identity
```

Add `internal/mcp`, `internal/rpc`, and `internal/sidecar` only when their implementation phases begin. Domain and application packages must not import transport packages or SQLite concrete types. The application package owns its narrow store interfaces; do not manufacture an interface for every struct.

## Work sequence

Each phase must leave `make check` green and include black-box CLI assertions where it changes the operator contract.

### 1. Working local conversation

- Establish the minimum domain vocabulary: typed IDs, crypto-random generation, UTC clock, bounded strings, and agent, topic, subscription, and message validation needed by the slice.
- Add `modernc.org/sqlite`, migration 1, the use-case-owned store interfaces, and the SQLite adapter without a backend registry.
- Implement the owner lock, one writer queue/connection, bounded query-only pool, Unix-socket handshake, and enough graceful shutdown behavior to run the slice safely.
- Connect real CLI, HTTP adapter, application, and store paths for session-context join/rejoin, whoami, topic create/follow, publish, inbox, read-through, and reply.
- Support human and global JSON output plus flag, file, and stdin message bodies from the first publish.
- Prove through black-box CLI runs that two isolated session contexts can join, exchange and reply to a message, acknowledge it, and reconnect without one context changing the other.

### 2. Foundation hardening

- Complete typed ID parsing, enum and size validation, expiry overrides, direct-topic canonicalization, and table-driven domain coverage.
- Resolve the state path without creating it in diagnostic/read-only code.
- Prove first-open, reopen, second-owner refusal, concurrent readers/serialized writers, cancellation, shutdown, rollback, future-version refusal, and migration idempotence.
- Complete bounded writer-queue overload behavior, context propagation, and clean service-unavailable errors.

### 3. Session identity and discovery

- Complete generated or requested handles, external-reference rejoin, update mutable session metadata, rename with alias, retire, whoami, and discovery.
- Implement explicit context resolution, isolated context files, `--as`, and identity-selection reporting.
- Prove case-insensitive collisions, alias expiry, external-reference conflicts, observational `last_seen_at`, and simultaneous clients with distinct identities.
- Add CLI human/JSON fixtures for `join`, `whoami`, and `agents`.

### 4. Topics, routing, and subscriptions

- Complete create and ensure by external reference, rename/archive/list, follow/unfollow, and direct send.
- Prove Sidecar external-key idempotency, display-name fallback, new-subscriber recent-context cursors, refollow behavior, and direct-topic invariants.
- Prove that direct traffic appears only in its participants' inboxes and ordinary discovery while remaining intentionally readable through observe, search, and known IDs by unrelated local clients.

### 5. Messages, threads, and receipts

- Complete transactional sequence allocation, thread and topic views, peek, sender-visible receipts, and basic SQLite FTS5 search.
- Add deterministic bounded collection ordering and opaque continuation cursors distinct from subscription read state.
- Prove root-title requirements, reply inheritance, same-topic parent checks, body/metadata bounds, and gap-free concurrent publishing with no partial direct-topic creation.
- Prove that `peek` and inbox listing do not acknowledge; read-through reports its full cursor effect; cursor advance atomically records one checkpoint; the first covering checkpoint supplies the receipt time; and receipt queries never mutate state.

### 6. Retention and observation

- Expiry filters shared by inbox, topic, and search queries.
- Context-preserving thread purge plus observable purge-run status.
- Operator observation that cannot mutate subscriptions.
- Deterministic tests with an injected clock; no wall-clock sleeps.

### 7. Generated help and compatibility

- One operation registry used by CLI help, versioned capability descriptions, and `comms instructions`.
- Capability and guarantee descriptions plus optional usage examples that do not prescribe agent polling or lifecycle behavior.
- Stable JSON envelopes for every command, error vocabulary, cursors, and idempotent mutation handling.
- Fixture proving instructions name only commands present in the registry.

### 8. Diagnostic export

- Versioned deterministic JSONL records for store metadata, agents, aliases, topics, external refs, subscriptions, and messages.
- Stream export through the service and let the CLI write an optional client-local output file.
- Document that export is for inspection and one-off tooling, not a guaranteed restore, replication, or historical-archive format.

### 9. Additional native surfaces after CLI proof (TCP complete; remaining adapters follow-on)

- TCP HTTP/OpenAPI exposure of the same handlers, loopback-only by default.
- After v1.0.0, add MCP tools and prompt/resource over the same operations and generated help.
- After v1.0.0, add a Sidecar plugin/client using the HTTP API, with `sidecar:<project-key>` topic ensure.
- After v1.0.0, add SSH stdio RPC and remote aliases/store handshake, then optional Tailscale Serve documentation.

## Acceptance

1. Claude Code, Codex, and Gemini can use the CLI without harness-specific protocol support.
2. Three session endpoints follow one topic, publish concurrently, and maintain independent read cursors without copied deliveries or shared current-agent state.
3. Two sessions exchange a direct threaded conversation implemented entirely as a two-member topic; an unrelated local session does not receive or ordinarily discover it but can deliberately inspect it through ordinary read operations without requesting permission.
4. Reconnecting by agent external reference preserves the same session identity; unrelated sessions receive distinct identities. Renaming an agent or topic preserves authorship, subscriptions, external integration identity, and replies.
5. Seven-day default, explicit, and never expiration round-trip; an active reply keeps expired ancestors available only as thread context until the whole thread is purgeable.
6. A crash or transaction failure produces one complete publish or no visible publish.
7. An operator or local agent observes all traffic, including direct traffic, without changing any cursor.
8. Sidecar repeatedly ensures one project topic after display renames and name collisions.
9. Diagnostic export emits deterministic, versioned JSONL without implying that Comms is the source of truth or promising import compatibility.
10. Human commands are concise; every operation has complete versioned JSON; collection ordering, cursors, errors, and exit codes are stable and tested.
11. `CGO_ENABLED=0` GoReleaser snapshots build for macOS and Linux on amd64 and arm64.
12. `make check`, `git diff --check`, and CI pass.
13. A second service cannot own the same store, all mutations pass through one writer, and concurrent reads remain available in WAL mode.
14. Application tests can replace the narrow store interfaces, while production exposes no backend selector and ships only SQLite.
15. A sender can distinguish unread from explicitly acknowledged messages for direct and public topics without creating per-message delivery records, and read-through reports that it acknowledges all earlier topic sequences.
16. Message bodies work through flags, files, and stdin without requiring agents to shell-quote multiline content.

## Non-goals

- Queue claims, task delegation, scheduling, approvals, or agent spawning.
- Authoritative task state, durable decision records, artifact storage, or historical archiving.
- File attachments or copied blobs; links may live in message text/metadata.
- Private or access-controlled direct messages, privacy between local agents, end-to-end encryption, or hostile multi-tenancy. Direct messages are inbox routing, not confidential channels.
- SMTP, IMAP, ActivityPub, or full ActivityStreams conformance.
- Cross-machine SQLite sharing, replication, peer federation, or conflict resolution.
- Runtime-selectable persistence backends, a generic repository layer, or a filesystem store.
- Lossless restore/import, mailbox replication, or backup guarantees for transient Comms state.
- Guaranteed interruption of a running agent turn.
- Web UI before Sidecar has a concrete client slice.

## Known follow-on decisions

These do not block the core and must be decided with their consumers:

- HTTP authentication for native-LAN exposure versus relying on SSH or Tailscale Serve.
- SSE versus polling for Sidecar updates.
- Exact MCP SDK after the operation registry and JSON schemas exist.
- Whether evidence ever justifies queue semantics as a separate topic mode.
- Whether remote authoritative access is insufficient enough to justify real store federation.
