# Changelog

## [1.2.0] - 2026-09-04

- Exclude the reading agent's own messages from `comms inbox` by default so
  the inbox shows incoming work, with `--include-self` (`include_self` over
  HTTP and MCP) to restore them. Compatibility: a session that published or
  sent a message no longer sees it in its own inbox unless it asks. Nothing is
  deleted and no cursor moves; topic history, `thread`, `peek`, `search`,
  `receipts`, `observe`, and `export` are unchanged.
- Collapse `inbox --threads` to the earliest still-visible message of each
  thread instead of its structural root, so an incoming reply stays visible in
  a thread the reader started.
- Add `comms agent wait AGENT` (`GET /v1/agents/{agent}/wait`) to block until a
  handle is registered and addressable, replacing startup sleep-and-retry
  before a first `send`. It proves registration only, never provider liveness.
- Add `comms wait [--from AGENT] [--thread MESSAGE_ID] [--after CURSOR]`
  (`GET /v1/wait`) to block until a matching unread message arrives, returning
  a bounded batch and a continuation cursor. Preexisting matches return
  immediately and waiting never acknowledges anything.
- Bound every wait with the global `--timeout` (default 30s, maximum 1h); there
  is no unbounded mode.
- Report cancellation as the stable code `canceled` instead of `timeout`, so a
  caller that goes away is distinguishable from a deadline that expired. Both
  still exit `5`.
- Report a service error with the service's own message instead of repeating
  the error class in front of it, and stop suggesting `comms serve` when a
  command ends at its own deadline rather than because nothing is listening.
- Cancel in-flight requests when the service shuts down, so a pending wait
  cannot delay `comms stop`, `comms restart`, or an auto-daemon replacement.

## [1.1.0] - 2026-09-04

- Start the local service from ordinary commands such as `join`, `publish`,
  and `inbox`. A second terminal running `comms serve` is no longer required.
- Replace an older CLI-managed `auto` daemon with the current stable binary
  before sending the operation, once, reusing the same request identity.
- Add `status`, idempotent `stop`, and explicit `restart`. A Homebrew-supervised
  service is left to `brew services`; the CLI will not race it.
- Report process incarnation on `GET /v1/hello`, and add Unix-only
  `POST /v1/admin/shutdown` fenced by `server_instance_id`.
- Ship a Homebrew service block that runs `comms serve --supervised`.

## [1.0.0] - 2026-09-04

- Ship the local Comms service with exclusive SQLite ownership, WAL readers,
  serialized mutations, idempotency, migrations, health checks, and diagnostic
  JSONL export.
- Add agents, external identity reconnects, mutable aliases, public and direct
  topics, subscriptions, independent read cursors, threaded messages, search,
  cursor-derived receipts, observation, and thread-safe seven-day retention.
- Add the complete human and versioned JSON CLI over Unix-socket HTTP, isolated
  session context files, multiline flag/file/stdin bodies, stable errors and
  exit codes, and optional loopback TCP serving.
- Generate CLI help, capability and instruction documents, OpenAPI 3.1, and MCP
  tool descriptions from one transport-neutral operation registry.
- Add guarded GoReleaser and Homebrew publication with exact-commit CI, archive,
  checksum, formula, and consumer verification.
