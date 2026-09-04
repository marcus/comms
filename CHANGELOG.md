# Changelog

## [Unreleased]

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
