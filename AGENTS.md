# Working with Comms

Comms is a local-first, short-lived messaging and pub/sub system enabling independent agent sessions (Claude Code, Codex, Gemini, etc.) to communicate across harnesses, projects, and workspaces. The core application is implemented in Go 1.27+, backed by SQLite in WAL mode, and exposed via a versioned HTTP REST API over a local Unix domain socket.

## Codebase Architecture

The codebase separates presentation, domain use cases, and storage:

- `cmd/comms/`: Minimal process entrypoint only (`main.go`). Handles OS signals (`SIGINT`, `SIGTERM`) and forwards to `internal/cli`.
- `internal/cli/`: Command-line flag parsing, human/JSON output rendering, and exit code classification. Acts as an HTTP client connecting to `comms.sock`. Never opens the database directly.
- `internal/app/`: Core application service, transaction boundaries, and store interfaces (`AgentStore`, `TopicStore`, `MessageStore`, `MaintenanceStore`).
- `internal/domain/`: Pure domain types (`Agent`, `Topic`, `Subscription`, `Message`), validation rules, stable ID generation (`agt_`, `top_`, `msg_`), and clocks.
- `internal/service/`: Daemon lifecycle, exclusive advisory lock management (`comms.db.lock`), and socket/listener binding.
- `internal/httpapi/`: Versioned HTTP API handlers (`/v1/...`) and the Unix domain socket client (`httpapi.Client`).
- `internal/store/`: Authoritative SQLite implementation using `modernc.org/sqlite`, embedded monotonic migrations (`migrations/*.sql`), read pool, and single-writer queue (`a.runWriter`).
- `internal/help/`: Central operation registry generating CLI usage text, agent instructions, and the OpenAPI 3.1 schema.
- `pkg/buildinfo/`: Link-time version metadata stamped during builds.

## Engineering Rules & Invariants

1. **Sole SQLite Process Owner:**
   `comms serve` is the only process that opens SQLite (`comms.db`). It enforces this via an exclusive flock on `${COMMS_STATE_DIR}/comms.db.lock`. All other commands (CLI, future MCP/Sidecar) interact with Comms as clients of the Unix domain socket HTTP API. Never open SQLite directly in CLI commands.
2. **Serialized Single-Writer Goroutine:**
   Database mutations are submitted as closures to a bounded channel (`requests chan writeRequest`) serviced by a single writer goroutine (`runWriter`). Request handlers never open raw write transactions or run write queries directly.
3. **Pure-Go SQLite with WAL Mode:**
   Release builds must remain `CGO_ENABLED=0` using `modernc.org/sqlite`. Mutations run in WAL mode (`journal_mode=WAL`), while concurrent reads are served by a dedicated read-only connection pool (`mode=ro&_pragma=query_only(1)`).
4. **Thin CLI & Reusable Operations:**
   Domain logic belongs behind `app.Service` and application store interfaces. Keep `internal/cli` focused on flag parsing and output formatting. Domain operations must be testable without executing the CLI binary.
5. **Stable IDs vs. Presentation:**
   Stable IDs (`agt_...`, `top_...`, `msg_...`) are immutable. Handles (`@alice`) and topic names are mutable presentation.
6. **Strict Error & Output Contracts:**
   Human-readable output may evolve; JSON envelopes (`{"schema": "...", "data": ...}`), error shapes (`{"error": {"code": "...", "message": "..."}}`), and exit codes (`0` ok, `1` internal, `2` invalid argument, `3` not found, `4` conflict, `5` unavailable/timeout) are stable compatibility contracts.
7. **Multiline Body Input:**
   Message bodies must accept `--body`, `--body-file`, or stdin (`-`). Agents must never be required to shell-quote multiline content.
8. **Trusted-Local Security Model:**
   Comms is local-first, trusted-local, and operator-visible. Direct topics route traffic to inboxes but do not create a cryptographic privacy boundary. Sockets default to `0600` permissions on loopback.
9. **Repository Hygiene:**
   Run `make check` (build, race-enabled tests, vet, linter) and `git diff --check` before submitting changes or requesting reviews.

<!-- td-agent-instructions:start -->
<!-- td-agent-instructions:version=3 -->

## Working with td

td keeps task context durable across sessions. In a new context, run `td usage --new-session -q` to see current work.

Use your judgment about how much tracking a task needs. For substantive work: `td start <id>`, record progress with `td log`, hand off with `td handoff <id>`, then `td review <id>`.

Closing needs a review. Say who did it (default trusted mode; delegated/strict allow only the first):

- independent session: `td approve <id> --reason "..."`
- a sub-agent: `td approve <id> --reviewed-by "<who>"`
- you: `td approve <id> --self-review --reason "..."`

Prefer a reviewer with its own `TD_CONTEXT_ID`; never name one who did not review.

Run `td usage` or `td <command> --help`.

<!-- td-agent-instructions:end -->
