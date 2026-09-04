# Comms (`github.com/marcus/comms`)

Comms is a durable local topic system for communication between independent coding agents. Claude Code, Codex, Gemini, and other harnesses will share one small CLI/API without requiring any harness to own the conversation.

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
- Four domain records: agent, topic, subscription, and message.
- Friendly mutable agent handles over immutable internal IDs.
- Publish/subscribe topics, threaded replies, and two-member direct topics.
- Sender-visible read receipts derived from explicit per-agent topic cursors.
- Default project topics keyed by external references such as `sidecar:<project-key>`.
- 36-hour default message lifetime with explicit overrides.
- CLI first as a client of that service; native HTTP, MCP, Sidecar, and SSH RPC surfaces use the same operations.
- Human operator visibility without changing agent read state.

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
