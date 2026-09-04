# Working with Comms

Comms is a local-first, durable topic system through which independent coding agents can communicate across harnesses, projects, sessions, and eventually machines. The product is deliberately smaller than an orchestrator: it stores agent identities, topics, subscriptions, messages, and read cursors; it does not spawn agents, assign work, or execute messages.

The buildable repository skeleton exists, but the core is not implemented. Read `docs/plans/active/core.md` before changing the domain model or starting implementation. The plan records the settled product decisions, schema, package boundaries, work sequence, tests, and non-goals.

## Engineering rules

- Keep `cmd/comms` thin. Argument parsing and rendering belong in `internal/cli`; domain behavior belongs behind application interfaces and must be testable without executing the binary.
- The same application operations must serve the CLI, HTTP API, MCP server, SSH RPC transport, and Sidecar. Do not put mailbox semantics in a transport.
- SQLite is authoritative and `comms serve` is its sole process owner. Serialize mutations through one writer goroutine and connection; use a small read-only pool in WAL mode. Clients never open the database directly.
- Keep a narrow application-facing store interface so use cases are testable and persistence details stay contained. SQLite is the only production implementation; do not build a runtime-selectable backend or filesystem adapter without evidence that one is needed.
- Use a pure-Go SQLite driver so release builds remain `CGO_ENABLED=0`; the planned driver is `modernc.org/sqlite`.
- Stable IDs are immutable. Friendly handles and topic names are mutable presentation.
- Human output may evolve. Versioned JSON and RPC shapes are compatibility contracts.
- Bodies come from stdin or files as well as flags; do not require agents to shell-quote multiline content.
- Comms is operator-visible and trusted-local, not a privacy or adversarial security boundary. Network exposure still defaults to loopback.
- Never copy or share the live SQLite database between machines. Cross-machine v1 means remote access to one authoritative store.
- Run `make check` and `git diff --check` before review.

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
