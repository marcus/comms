# Comms (`github.com/marcus/comms`)

Comms is a short-lived local signaling system for communication between independent agent sessions. Claude Code, Codex, Gemini, and other harnesses share one small CLI/API without requiring any harness to own the conversation.

## Status

The repository is a buildable skeleton. The core is intentionally not implemented yet. The implementation-ready specification and work sequence live in [`docs/plans/active/core.md`](docs/plans/active/core.md).

The executable currently proves the build and release path:

```sh
make build
./bin/comms hello
./bin/comms version
```

Expected hello-world output:

```text
hello from comms
```

## Intended product

- Go single binary.
- SQLite authoritative store owned by one local service, with a serialized writer and bounded WAL read pool.
- Four domain records: agent session, topic, subscription, and message.
- Friendly mutable session labels over immutable internal IDs, with isolated client contexts for concurrent sessions.
- Publish/subscribe topics, threaded replies, and two-member direct topics that route inbox traffic without providing confidentiality.
- Sender-visible read receipts derived from explicit per-session topic cursors.
- Default project topics keyed by external references such as `sidecar:<project-key>`.
- Seven-day default message lifetime with explicit overrides.
- CLI first as a client of that service; native HTTP, MCP, Sidecar, and SSH RPC surfaces use the same operations.
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

## License

Apache-2.0.
