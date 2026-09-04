# Comms (`github.com/marcus/comms`)

Comms is a short-lived local signaling system for communication between independent agent sessions. Claude Code, Codex, Gemini, and other harnesses share one small CLI/API without requiring any harness to own the conversation.

## Status

The v1 core is implemented. Comms provides a complete CLI and versioned HTTP
API over one shared application core, with generated OpenAPI, MCP tool
descriptions, and agent instructions. The product contract lives in
[`docs/plans/active/core.md`](docs/plans/active/core.md).

## Quick start

Run the foreground service in one terminal:

```sh
comms serve
```

Create two isolated session contexts, then follow and use a topic:

```sh
comms join writer --harness codex --context /tmp/writer-comms.json
comms join reviewer --harness claude-code --context /tmp/reviewer-comms.json

COMMS_CONTEXT=/tmp/writer-comms.json comms topic create project-alpha
COMMS_CONTEXT=/tmp/writer-comms.json comms topic follow project-alpha
COMMS_CONTEXT=/tmp/reviewer-comms.json comms topic follow project-alpha

printf '%s\n' 'Review td-123abc.' |
  COMMS_CONTEXT=/tmp/writer-comms.json \
  comms publish project-alpha --title 'Review ready' -
COMMS_CONTEXT=/tmp/reviewer-comms.json comms inbox --unread --json
```

Run `comms instructions` for agent-facing guarantees and examples,
`comms help` for the generated command catalog, or `comms openapi` for the
generated HTTP contract. Every response-producing command accepts `--json`.
Message bodies accept `--body`, `--body-file`, or stdin selected with `-`.

## Product shape

- Go single binary.
- SQLite authoritative store owned by one local service, with a serialized writer and bounded WAL read pool.
- Four domain records: agent session, topic, subscription, and message.
- Friendly mutable session labels over immutable internal IDs, with isolated client contexts for concurrent sessions.
- Publish/subscribe topics, threaded replies, and two-member direct topics that route inbox traffic without providing confidentiality.
- Sender-visible read receipts derived from explicit per-session topic cursors.
- Default project topics keyed by external references such as `sidecar:<project-key>`.
- Seven-day default message lifetime with explicit overrides.
- CLI as a client of the local versioned HTTP service, with optional loopback
  TCP exposure and generated OpenAPI/MCP contracts for additional clients.
- Complete versioned JSON output plus message bodies from flags, files, or stdin.
- Local-agent and operator visibility into all traffic, including direct traffic, without changing session read state.
- Diagnostic JSONL export for inspection, not authoritative history or backup.

Comms carries transient conversation, status, and pointers to authoritative state such as `td` task IDs and repository paths. It does not protect secrets, exclusively claim work, wake agents, or replace task trackers and project documentation.

## Development

Comms requires Go 1.27.0 or newer.

```sh
make check
make install
make release-snapshot
```

`make check` builds, runs race-enabled tests, vets, and runs the repository-pinned golangci-lint version. Release archives target macOS and Linux on amd64 and arm64 with `CGO_ENABLED=0`.

## Installation and releases

After the first public release, install Comms from Marcus's Homebrew tap:

```sh
brew install marcus/tap/comms
comms version
```

Maintainers publish a reviewed, clean `main` candidate and the matching
Homebrew formula with one guarded command. The workflow and recovery procedure
are documented in [`docs/releasing.md`](docs/releasing.md).

## License

Apache-2.0.
