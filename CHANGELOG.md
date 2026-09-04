# Changelog

## Unreleased

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
