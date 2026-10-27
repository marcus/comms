## Environment

These are the variables read by the comms application on macOS and Linux. Empty values use the fallback below. The auto-started daemon inherits the invoking process's environment. A running daemon keeps its launch environment; configure a Homebrew-supervised service separately.

| Variable | Default and precedence |
| --- | --- |
| `COMMS_STATE_DIR` | Exact directory for `comms.db`, its owner lock, daemon `server.log`, and default identity files. Overrides `XDG_STATE_HOME`; no `comms` suffix is added. `serve --db PATH` overrides only the database location. Socket and context overrides below still apply. |
| `XDG_STATE_HOME` | State base directory when `COMMS_STATE_DIR` is empty: comms uses `$XDG_STATE_HOME/comms`. Otherwise falls back to `$HOME/.local/state/comms`. |
| `HOME` | Home directory read through Go's `os.UserHomeDir` for the state-directory fallback. On macOS/Linux, an unset `HOME` makes that fallback fail. |
| `COMMS_SOCKET` | Exact Unix socket path for clients and the daemon. Global `--socket PATH` takes precedence. Otherwise use `$XDG_RUNTIME_DIR/comms/comms.sock`, or `comms.sock` in the resolved state directory when `XDG_RUNTIME_DIR` is empty. The variable is singular; `COMMS_SOCKETS` is not read. |
| `XDG_RUNTIME_DIR` | Socket base directory when neither `--socket` nor `COMMS_SOCKET` is set. Overrides the state-directory socket fallback, even when `COMMS_STATE_DIR` is set. Does not move the database, log or identity files. |
| `COMMS_AUTO_START` | Enabled by default. `0`, `false`, `no` or `off` disable ordinary-command auto-start and automatic upgrade replacement (case-insensitive, surrounding whitespace ignored). Other values enable it. `--no-auto-start` takes precedence; explicit `serve` and `restart` remain explicit lifecycle commands. Status/health/hello/doctor/stop never auto-start a service. |
| `COMMS_AGENT_ID` | Agent ID or handle selected for identity-consuming commands when `--as AGENT` is absent. Overrides identity from context files. Does not choose the context file written by `join`. |
| `COMMS_CONTEXT` | Exact identity context file. `join --context PATH` takes precedence for that join. Without either, use a session-scoped file under the resolved state directory, or `context.json` there when no session selector is available. Reading commands prefer `--as`, then `COMMS_AGENT_ID`, then the selected context. |
| `COMMS_SESSION` | Explicit harness-neutral session scope for implicit identity files under `STATE_DIR/sessions/`. Overrides conversation and tmux detection. Unset means try `CODEX_THREAD_ID`, then a tmux pane, then the machine-wide context file. |
| `CODEX_THREAD_ID` | Conversation scope when `COMMS_SESSION` is empty. Overrides inherited tmux variables. Unset means try tmux, then the machine-wide context file. |
| `TMUX_PANE` | Pane scope when both session selectors above are empty. Combined with the tmux socket from `TMUX`, then hashed into the implicit context filename. Unset means use the machine-wide context file. Comms never controls the tmux server. |
| `TMUX` | Only the prefix before the first comma is used, to distinguish identical pane IDs on different tmux servers. Read only when `TMUX_PANE` supplies the session scope. Unset contributes an empty socket prefix. |

### Testing against an isolated service

Use a subshell so the overrides disappear afterward. An inherited `COMMS_SOCKET` or `XDG_RUNTIME_DIR` can otherwise select the real socket despite a separate state directory; inherited identity selectors can likewise address the wrong agent.

```sh
(
  export COMMS_STATE_DIR="$(mktemp -d /tmp/comms-test.XXXXXX)"
  export COMMS_SOCKET="$COMMS_STATE_DIR/comms.sock"
  export COMMS_CONTEXT="$COMMS_STATE_DIR/context.json"
  export COMMS_SESSION=isolated-test
  export COMMS_AUTO_START=1
  unset COMMS_AGENT_ID
  trap 'comms stop; rm -rf "$COMMS_STATE_DIR"' EXIT

  comms join test-a
  comms topic create isolated-test
  comms --as test-a topic follow isolated-test
  comms --as test-a publish isolated-test --title 'Isolated test' --body 'Only this test store'
  comms status
)
```

The trap stops only the service at the explicitly selected test socket. Existing default or supervised services are unaffected. For a foreground test daemon, use `comms serve --db "$COMMS_STATE_DIR/comms.db" --socket "$COMMS_SOCKET"` with the same environment in its clients.

### Build and release environment

These inputs belong to the Makefile and repository release scripts; the installed application does not read them. Standard Go, Git, GitHub CLI and Homebrew configuration remains owned by those tools.

| Variable | Default and precedence |
| --- | --- |
| `GO` | Make's Go executable, default `go`. |
| `GORELEASER` | Make's GoReleaser executable, default `goreleaser`. |
| `VERSION` | Build version, default `git describe --tags --always --dirty` (or `dev`). |
| `COMMIT` | Build commit, default short `HEAD` (or `unknown`). |
| `PREFIX` | `make install` destination, default `$HOME/.local`. |
| `BREW_PREFIX` | Local-install/restore prefix, default `brew --prefix`. |
| `RELEASE_VERSION` | Explicit strict SemVer release override, otherwise derived from the changelog and `BUMP`. A positional version overrides it in `release-notes.sh`. |
| `BUMP` | Required with `[Unreleased]` when `RELEASE_VERSION` is absent: `major`, `minor` or `patch`. |
| `CHANGELOG_FILE` | `release-notes.sh` input, default `CHANGELOG.md`. |
| `COMMS_SOURCE_REPO` | Release publication source, default `marcus/comms`. |
| `COMMS_TAP_REPO` | Release publication tap, default `marcus/homebrew-tap`. |
| `COMMS_TAP_REPO_URL` | Explicit tap Git URL override; otherwise the SSH URL when `gh` selects SSH, or the HTTPS URL for `COMMS_TAP_REPO`. |
| `COMMS_CI_WORKFLOW` | Exact-commit CI workflow, default `ci.yml`. |
| `COMMS_RELEASE_WORKFLOW` | Tag publication workflow, default `release.yml`. |
| `COMMS_RELEASE_TIMEOUT` | CI/tag/publication wait bound in seconds, default `900`; positive integer. |
| `COMMS_RELEASE_POLL_INTERVAL` | Release-script polling interval in seconds, default `5`; positive integer. |
| `GITHUB_TOKEN` | GoReleaser publication credential; no default. Supply secrets through your secret manager. Git and `gh` also need their own configured access to both repositories. |
