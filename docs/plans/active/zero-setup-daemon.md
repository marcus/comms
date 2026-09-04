# Comms zero-setup daemon and upgrade handoff plan

- **Task:** td-c77398
- **Status:** Shipped on main (td-d90621, td-d67f5a, td-b3afad, td-20ff20)
- **Depends on:** published v1.0.0 CLI and Unix-socket HTTP service

## Outcome

An agent or developer can install `comms` and run an ordinary service-backed CLI command such as `comms join`, `comms publish`, or `comms inbox` without first starting `comms serve` in another terminal. The CLI starts one detached per-user service, waits until the real handshake succeeds, and then performs the requested operation.

When a later stable CLI is installed, its next ordinary command replaces an older **CLI-managed** daemon before sending the operation. Shutdown drains accepted HTTP requests and the writer queue, the old process releases SQLite ownership, and the new executable becomes ready before the original operation is sent. The command is not speculatively sent to one server and replayed against another.

`comms serve` remains the only process that opens SQLite. CLI, HTTP, generated MCP contracts, and future consumers continue to reach the same application operations through the service.

## Product and rollout boundary

This work removes local startup ceremony; it does not add another service architecture or persistence path. Auto-start applies only to commands using the local Unix socket. Process-local commands (`help`, `version`, `openapi`, `capabilities`, and `instructions`) remain process-local. Diagnostic lifecycle commands (`status`, `health`, `hello`, `doctor`, and `stop`) do not start a stopped service merely by inspecting it; `restart` starts one deliberately.

The implementation must distinguish three launch modes:

- `auto`: a detached child started by the CLI and eligible for transparent client-driven replacement;
- `foreground`: an operator's explicit `comms serve`, which a routine CLI command must never terminate;
- `supervised`: a process started by Homebrew services or another supervisor, whose restart policy belongs to that supervisor.

The already-published v1.0.0 server exposes no process incarnation or shutdown operation. A newer CLI therefore cannot prove which PID owns its socket and must not guess or signal a PID discovered from an untrusted stale file. Moving a live v1.0.0 process onto the lifecycle-aware release requires a one-time explicit termination of that foreground process. Once it is stopped, the next ordinary command or `comms restart` starts the lifecycle-aware daemon. Transparent replacement is guaranteed between lifecycle-aware releases after that bootstrap.

Auto-start never falls back to direct SQLite access. It also does not turn Comms into a login service: after logout or reboot, the next ordinary command starts it again. Users who want login startup opt into an OS supervisor.

## Settled decisions

1. **Preflight before operations.** An auto-start-eligible command first calls `GET /v1/hello`. A successful handshake establishes protocol compatibility, server version, process incarnation, and launch mode before the real request is constructed or sent. Version mismatch is never discovered from the response to a possibly mutating operation.
2. **One lifecycle lock per socket.** Start and replacement are serialized with an advisory lock adjacent to the resolved Unix socket, for example `${XDG_RUNTIME_DIR}/comms/comms.sock.lifecycle.lock`. After acquiring it, a client always repeats the handshake because another process may have completed the transition. The database owner lock remains the final authority if two different socket paths target one store.
3. **Typed connection failures only.** Auto-start is attempted only when the Unix dial fails with `ENOENT` or `ECONNREFUSED`. Permission failures, deadlines, malformed responses, HTTP errors, and protocol/schema incompatibility are returned unchanged. The HTTP client must preserve the underlying dial cause instead of flattening every transport error into generic `unavailable`.
4. **A real daemon child mode.** The parent starts its own resolved executable as `comms serve --daemon-child --socket <resolved-path>`, with a new session/process group, closed stdin, inherited Comms configuration, and stdout/stderr appended to a mode-0600 log. It waits for a matching handshake, not merely for the socket file or lightweight health response. Unix-specific process code lives behind Darwin/Linux build tags.
5. **Lifecycle identity comes from the live server.** Each service process creates a random `server_instance_id` and reports it, PID, start time, launch mode, version, commit, socket path, and database path through the handshake/status representation. A PID file is not control authority and is unnecessary for normal operation.
6. **Conditional graceful shutdown.** The Unix-socket-only `POST /v1/admin/shutdown` request includes the expected `server_instance_id`. The service rejects a stale expectation, responds `202 Accepted`, stops accepting new work after the response is committed, drains in-flight requests and queued writes within its shutdown deadline, closes SQLite, removes its socket, and releases the owner lock. The route is not exposed by a TCP listener.
7. **Only CLI-managed daemons restart transparently.** For stable semantic releases, a newer client may replace an older `auto` daemon. An older client never downgrades a newer server. Development/dirty versions never trigger an automatic replacement. A routine command encountering a mismatch with `foreground` or `supervised` returns a stable `server_restart_required` result with the appropriate operator action instead of killing the process.
8. **No unsafe signal fallback.** `stop`, `restart`, and upgrade handoff use the conditional admin operation. They do not send a signal based only on a PID file, and they do not unlink a socket owned by a live service. If an older server lacks lifecycle control, the CLI reports the one-time manual transition.
9. **Stale artifacts are recovered by ownership checks.** Advisory `flock` locks release when a process exits and their files may remain. The service may remove a stale socket only after a failed connect and successful acquisition of the database owner lock. A client never deletes database locks, WAL files, sockets, or another process's logs.
10. **Explicit opt-out.** Global `--no-auto-start` or false-like `COMMS_AUTO_START` values (`0`, `false`, `no`, `off`) retain the v1 unavailable behavior. The flag wins over the environment. Tests, containers, and callers that manage lifecycle explicitly use this path.
11. **Supervisor coexistence is explicit.** The generated Homebrew formula adds a native service block that launches `comms serve --supervised`, uses Homebrew's stable `opt_bin` path, and owns its logs through Homebrew. The CLI detects the advertised `supervised` mode and directs version handoff to `brew services restart comms`; it does not race the supervisor by spawning a second process.
12. **Lifecycle is an owned, versioned capability.** `status`, `stop`, and `restart` have human and versioned JSON output and are present in the operation registry, generated help, and OpenAPI where an HTTP operation exists. Process lifecycle stays in `internal/service`; mailbox/domain rules remain in `internal/app`.

## User-visible contract

```text
comms [--no-auto-start] status
comms [--no-auto-start] stop
comms restart
comms serve [--socket PATH] [--db PATH] [--listen ADDRESS]
```

`--daemon-child` and `--supervised` are service launch-mode markers, not alternate database owners. They may be documented under `comms serve --help` but must not create different application behavior.

`status` never auto-starts. Its JSON distinguishes at least:

```json
{
  "schema": "comms.cli.v1",
  "data": {
    "running": true,
    "server_instance_id": "srv_...",
    "pid": 12345,
    "started_at": "2026-09-04T12:34:56Z",
    "launch_mode": "auto",
    "version": "v1.1.0",
    "commit": "abc1234",
    "socket_path": "/.../comms.sock",
    "database_path": "/.../comms.db"
  }
}
```

A stopped service returns success with `{"schema":"comms.cli.v1","data":{"running":false,"socket_path":"..."}}` so scripts can inspect normal absence without treating it as an internal failure. Permission, malformed-state, or incompatible-server failures remain nonzero. `stop` is idempotent for an already-stopped service. Explicit `stop` may stop a lifecycle-aware `auto` or `foreground` service; explicit `restart` may replace either with an `auto` daemon. Both refuse to race a `supervised` service and instead name its supervisor command.

Human startup is quiet on success. If startup fails, the original command exits `5`, names the startup phase and log path, and preserves the stable JSON error envelope under `--json`.

## Request and lifecycle sequence

For an ordinary service-backed command:

```text
resolve Unix socket and auto-start policy
  -> GET /v1/hello
     -> compatible/current: send the requested operation once
     -> ENOENT or ECONNREFUSED:
          acquire <socket>.lifecycle.lock within the command deadline
          -> repeat handshake
          -> if still absent, spawn daemon child
          -> wait for matching lifecycle-aware handshake
          -> release lock and send the requested operation once
     -> older CLI-managed stable server:
          acquire lifecycle lock and repeat handshake
          -> POST shutdown with expected server_instance_id
          -> wait for that incarnation to disappear and owner lock to release
          -> spawn current executable
          -> wait for matching version/commit handshake
          -> release lock and send the requested operation once
     -> incompatible, foreground, supervised, or legacy server:
          return a specific error and recovery instruction without sending the operation
```

The command's existing overall `--timeout` bounds lock acquisition, shutdown, startup, readiness, and the application request. Internal phase caps may reserve time for cleanup, but they do not extend the caller's deadline. Waiting uses bounded backoff rather than fixed 20 ms polling.

Mutations retain their existing `(client_id, request_id)` idempotency pair. If the connection fails after a request may have been transmitted, the CLI may retry a read or an idempotent mutation once after re-establishing a compatible server, reusing the exact same request IDs and body. It must not transparently retry a non-idempotent or partially streamed response. Startup-before-first-request is the normal path and avoids this ambiguity.

## Runtime files

Socket-scoped runtime artifacts follow the existing resolver:

```text
${XDG_RUNTIME_DIR}/comms/                 # when XDG_RUNTIME_DIR is set
├── comms.sock
└── comms.sock.lifecycle.lock
```

When no runtime directory exists, those files live beside the database under the state directory. Persistent state remains:

```text
${COMMS_STATE_DIR:-${XDG_STATE_HOME:-~/.local/state}/comms}/
├── comms.db
├── comms.db-wal
├── comms.db-shm
├── comms.db.lock
└── server.log                            # CLI-managed daemon only
```

Explicit `COMMS_SOCKET` or `--socket` values derive their lifecycle lock from the exact socket path. Runtime directories are mode 0700; the socket and CLI-managed log are mode 0600. The lifecycle lock file is persistent but contains no authority. Startup diagnostics may include it, but cleanup does not require deleting it.

## Internal design

### Service lifecycle controller

Add a small `internal/service` controller that owns process metadata and cancellation. `internal/httpapi` receives a narrow lifecycle interface when constructing a Unix handler; it does not import process globals or place shutdown logic in `internal/app`. The HTTP handshake response composes the existing application/store handshake with process metadata; lifecycle fields do not become store concerns merely because they share one wire response.

The controller exposes transport-neutral status and a conditional shutdown request. Shutdown scheduling must let the HTTP response flush before `http.Server.Shutdown` begins. The current signal path and the admin path converge on the same bounded drain/close sequence. The store remains responsible for draining its writer queue and releasing SQLite ownership.

The handshake extends additively with lifecycle fields. Protocol and schema checks happen before version replacement: a client must not assume restarting can make an incompatible request safe. Stable machine-readable errors include `server_restart_required`, `legacy_server_restart_required`, `server_instance_changed`, and the existing unavailable/timeout categories.

Restart-required and legacy-transition failures use process exit code `5` because the requested service operation is unavailable until lifecycle is reconciled. An incarnation change discovered under the lifecycle lock causes a recheck rather than a user-visible failure; if reconciliation cannot finish before the caller's deadline, it also exits `5` as a timeout.

### CLI lifecycle manager

Add `internal/cli/lifecycle.go` around the existing HTTP client and socket resolver. Command dispatch supplies an explicit policy (`process-local`, `inspect-only`, `auto-start`, or `restart`) rather than inferring behavior from HTTP method. This keeps `status` and `doctor` observational while allowing a read such as `inbox` to start the service.

The manager owns:

- typed dial-error inspection;
- lifecycle-lock acquisition and mandatory recheck;
- child process construction and release;
- readiness handshake and early-child-exit diagnostics;
- stable-version comparison;
- conditional stop and restart sequencing;
- safe retry classification using the existing idempotency metadata.

Keep process creation behind a small adapter so unit tests do not fork real daemons. Black-box tests must still execute real binaries and independent OS processes.

### Homebrew service

Extend `scripts/release-render-formula.sh` with a formula `service do` block using `opt_bin/"comms"`, `run_at_load true`, `keep_alive true`, a supervised launch marker, and Homebrew-owned log/error paths. Add renderer assertions so release tests verify both Ruby syntax and the exact service command. Document `brew services start comms`, `brew services restart comms`, and the distinction between login-managed and lazy CLI-managed operation.

## Package and file changes

```text
cmd/comms/main.go                    signal context remains the process entrypoint
internal/cli/cli.go                  lifecycle commands, flag, and per-command policy
internal/cli/lifecycle.go            preflight, lock, spawn, restart, and retry rules
internal/cli/process_unix.go         Darwin/Linux detach implementation
internal/cli/lifecycle_test.go       policy, version, errors, and contention tests
internal/cli/integration_test.go     real-process zero-start and upgrade journeys
internal/httpapi/httpapi.go          additive handshake fields and conditional shutdown route
internal/httpapi/httpapi_test.go     Unix-only control and incarnation fencing
internal/service/service.go          lifecycle controller and unified graceful shutdown
internal/service/lifecycle.go        instance metadata and control interface
internal/help/registry.go            lifecycle operations and generated contracts
scripts/release-render-formula.sh    Homebrew service block
scripts/release-test.sh              rendered service contract checks
README.md                            zero-setup quick start and explicit service operation
docs/releasing.md                    first lifecycle-aware release transition
docs/plans/active/core.md            replace the published v1 startup limitation when shipped
```

Names may move to match the code as it evolves, but the lifecycle/app/store boundaries and user-visible contracts are controlling.

## Work sequence

Each phase leaves `make check` and `git diff --check` green. Tests use temporary state/runtime roots and never touch a developer's live Comms service.

### 1. Establish lifecycle identity and safe control

- Extend the handshake with server incarnation, PID, start time, launch mode, commit, socket path, and database path.
- Add the lifecycle controller and Unix-only conditional shutdown endpoint; route signals and admin shutdown through the same drain path.
- Add registry/OpenAPI entries and stable errors for status and control.
- Prove stale-incarnation refusal, response-before-shutdown ordering, in-flight mutation drain, writer-queue drain, socket cleanup, and owner-lock release.

This phase intentionally requires explicit foreground startup and provides the safe base future clients need.

### 2. Deliver the zero-setup steel thread

- Add typed Unix dial errors, the per-socket lifecycle lock, launch-mode flags, detached process creation, log handling, and readiness handshake.
- Route one real journey through preflight: clean roots -> `join` -> `topic create/follow` -> `publish` -> `inbox`, with no prior `serve` command.
- Add `--no-auto-start` and `COMMS_AUTO_START` parsing, quiet success, actionable startup failures, and command-policy coverage.
- Prove 20 independent CLI processes contend on one cold socket, exactly one server incarnation wins, and every command reaches that server.

### 3. Add safe upgrade handoff

- Compare compatible stable build identities during preflight and replace only an older `auto` daemon.
- Fence shutdown by the rechecked incarnation, wait for complete ownership release, start `os.Executable()`, and verify the new version/commit before the operation.
- Preserve one mutation request ID/body across any allowed retry.
- Build two test binaries with different link-time versions and prove one mutating command is applied exactly once across handoff.
- Prove newer-server non-downgrade, development-build no-op, foreground/supervised refusal, concurrent upgrade contention, timeout behavior, and the legacy-v1 manual-transition error.

### 4. Complete operator and supervisor ergonomics

- Add `status`, idempotent `stop`, and explicit `restart` with human and versioned JSON output.
- Render and test the Homebrew service block, then update the README, release documentation, and controlling core contract so none still prescribe mandatory foreground startup.
- Prove `status`, `health`, `hello`, `doctor`, and `stop` do not auto-start; `restart` does; and supervisor-owned processes are never replaced by the CLI.
- Run a release snapshot for macOS/Linux amd64/arm64 with `CGO_ENABLED=0`.

## Acceptance

1. With isolated empty state/runtime roots and no server, `comms join test-agent` succeeds and leaves one reachable `auto` daemon.
2. The first real conversation works without an explicit `serve`: two isolated identities join, follow, publish, inspect inbox, read through, and reply.
3. Twenty simultaneous OS processes targeting one cold socket produce one server incarnation; all operations complete without SQLite ownership or socket races.
4. A stable newer CLI replaces an older CLI-managed daemon before a mutation, and the mutation is committed exactly once with the same request identity.
5. A newer server is not downgraded. Development, foreground, supervised, incompatible, and legacy servers are not killed automatically and return precise recovery guidance.
6. Conditional shutdown refuses a stale incarnation and cannot terminate a replacement that won a race.
7. Graceful shutdown stops acceptance, completes or explicitly cancels in-flight work by deadline, drains the writer queue, closes/checkpoints SQLite as appropriate, removes the socket, and releases the owner lock.
8. Stale socket and lifecycle-lock files recover without deleting a live process's files. PID reuse cannot cause Comms to signal an unrelated process.
9. `status` reports live handshake facts and returns `running:false` for normal absence. `stop` is idempotent. Diagnostic commands do not start a service as a side effect.
10. `COMMS_AUTO_START=0` and `--no-auto-start` preserve exit code `5` and the structured `unavailable` error for an ordinary command against a stopped service.
11. The socket continues to honor `XDG_RUNTIME_DIR`, state honors `COMMS_STATE_DIR`/`XDG_STATE_HOME`, and explicit isolated paths never touch the default installation.
12. Generated help, JSON schemas, OpenAPI, README, and Homebrew service instructions agree on lifecycle behavior and intentional surface differences.
13. `make check`, `git diff --check`, release renderer tests, and `CGO_ENABLED=0` release snapshots pass on the supported Darwin/Linux targets.

## Non-goals

- Direct or fallback SQLite access from any client.
- Automatically terminating an operator's foreground service or racing an OS supervisor.
- Pretending the already-published v1.0.0 server supports a safe transparent handoff.
- System-wide/root daemons, login startup by default, or a generic daemon-management framework.
- Windows process or named-pipe support before there is demand.
- `SIGHUP` reload; a version change replaces a lifecycle-aware CLI daemon through the fenced shutdown path.
- Unbounded retries, retrying partially streamed output, or claiming that a successful publish wakes an agent.
