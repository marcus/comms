package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/marcus/comms/internal/app"
	"github.com/marcus/comms/internal/help"
	"github.com/marcus/comms/internal/httpapi"
	"github.com/marcus/comms/internal/service"
	"github.com/marcus/comms/pkg/buildinfo"
)

type commandPolicy int

const (
	policyProcessLocal commandPolicy = iota
	policyInspectOnly
	policyAutoStart
	policyRestart
)

// DaemonSpec is the process-creation request the spawn adapter executes.
type DaemonSpec struct {
	Executable   string
	Args         []string
	SocketPath   string
	DatabasePath string
	LogPath      string
}

// DaemonHandle is a started daemon child. Wait is observed via Done/Err;
// the adapter must not kill the child when the parent command times out.
type DaemonHandle interface {
	PID() int
	Done() <-chan struct{}
	Err() error
}

type startupError struct {
	phase   string
	logPath string
	err     error
}

func (e *startupError) Error() string {
	if e.logPath != "" {
		return fmt.Sprintf("service startup failed during %s: %v (log: %s)", e.phase, e.err, e.logPath)
	}
	return fmt.Sprintf("service startup failed during %s: %v", e.phase, e.err)
}

func (e *startupError) Unwrap() error { return e.err }

type restartRequiredError struct {
	code       string
	launchMode string
	action     string
	message    string
}

func (e *restartRequiredError) Error() string { return e.message }
func (e *restartRequiredError) Unwrap() error { return app.ErrUnavailable }

func serverRestartRequired(launchMode string) error {
	action := "stop the foreground Comms process, then rerun this command"
	if launchMode == string(service.LaunchModeSupervised) {
		action = "run 'brew services restart comms'"
	}
	return &restartRequiredError{
		code:       "server_restart_required",
		launchMode: launchMode,
		action:     action,
		message:    fmt.Sprintf("running %s Comms service is older than this CLI and cannot be replaced automatically; %s", launchMode, action),
	}
}

func legacyServerRestartRequired() error {
	action := "stop that foreground process once, then rerun this command"
	return &restartRequiredError{
		code:    "legacy_server_restart_required",
		action:  action,
		message: "the running Comms server predates lifecycle control and cannot be replaced automatically; " + action,
	}
}

func supervisedControlRequired(verb string) error {
	action := "run 'brew services " + verb + " comms'"
	return &restartRequiredError{
		code:       "server_restart_required",
		launchMode: string(service.LaunchModeSupervised),
		action:     action,
		message:    "running supervised Comms service is owned by Homebrew; " + action,
	}
}

func startupFailure(phase, logPath string, err error) error {
	if err == nil {
		err = app.ErrUnavailable
	}
	if !errors.Is(err, app.ErrUnavailable) && !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		err = fmt.Errorf("%w: %w", app.ErrUnavailable, err)
	}
	return &startupError{phase: phase, logPath: logPath, err: err}
}

func commandPolicyOf(command string) commandPolicy {
	switch command {
	case "help", "-h", "--help", "version", "openapi", "capabilities", "instructions", "serve":
		return policyProcessLocal
	case "hello", "health", "doctor", "status", "stop":
		return policyInspectOnly
	case "restart":
		return policyRestart
	case "join", "whoami", "agents", "agent", "topic", "topics", "subscriptions", "publish", "send", "reply", "inbox", "peek", "read-through", "receipts", "thread", "search", "observe", "retention", "purge", "export":
		return policyAutoStart
	default:
		return policyProcessLocal
	}
}

func autoStartEnabled(flagDisabled bool, getenv func(string) string) bool {
	if flagDisabled {
		return false
	}
	if getenv == nil {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(getenv("COMMS_AUTO_START"))) {
	case "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

func (r *runner) commandContext() context.Context {
	if r.cmdCtx != nil {
		return r.cmdCtx
	}
	if r.g.timeout <= 0 {
		r.cmdCtx = r.env.Context
		r.cmdCancel = func() {}
		return r.cmdCtx
	}
	r.cmdCtx, r.cmdCancel = context.WithTimeout(r.env.Context, r.g.timeout)
	return r.cmdCtx
}

func (r *runner) resolveSocket() (string, error) {
	if r.socket != "" {
		return r.socket, nil
	}
	socket := r.g.socket
	if socket == "" {
		socket = r.env.Getenv("COMMS_SOCKET")
	}
	if socket == "" {
		var err error
		socket, err = service.ResolveSocketPath(false)
		if err != nil {
			return "", err
		}
	}
	r.socket = socket
	return socket, nil
}

type replaceAction int

const (
	replaceNone replaceAction = iota
	replaceAuto
	replaceForeground
	replaceSupervised
	replaceLegacy
)

func (a replaceAction) String() string {
	switch a {
	case replaceNone:
		return "none"
	case replaceAuto:
		return "auto"
	case replaceForeground:
		return "foreground"
	case replaceSupervised:
		return "supervised"
	case replaceLegacy:
		return "legacy"
	default:
		return strconv.Itoa(int(a))
	}
}

func (r *runner) ensureReady(client *httpapi.Client) error {
	ctx := r.commandContext()
	hs, err := handshake(ctx, client)
	if err == nil {
		if err := checkHandshakeCompatibility(hs); err != nil {
			return err
		}
		switch r.lifecycleDecision(hs) {
		case replaceNone:
			return nil
		case replaceLegacy:
			return legacyServerRestartRequired()
		case replaceForeground, replaceSupervised:
			return r.refuseLaunchMode(hs.LaunchMode)
		case replaceAuto:
			return r.reconcile(ctx, client)
		default:
			return nil
		}
	}
	if !httpapi.IsAutoStartableDial(err) {
		return err
	}
	if r.policy != policyRestart && !autoStartEnabled(r.g.noAutoStart, r.env.Getenv) {
		return err
	}
	return r.reconcile(ctx, client)
}

func (r *runner) reconcile(ctx context.Context, client *httpapi.Client) error {
	logPath := r.serverLogPath()
	lock, err := acquireLifecycleLock(ctx, r.socket+".lifecycle.lock")
	if err != nil {
		return startupFailure("lock", logPath, err)
	}
	defer func() { _ = lock.Close() }()
	return r.reconcileLocked(ctx, client, logPath)
}

func (r *runner) reconcileLocked(ctx context.Context, client *httpapi.Client, logPath string) error {
	for {
		if err := ctx.Err(); err != nil {
			return startupFailure("readiness", logPath, err)
		}
		hs, err := handshake(ctx, client)
		if err == nil {
			if err := checkHandshakeCompatibility(hs); err != nil {
				return err
			}
			switch r.lifecycleDecision(hs) {
			case replaceNone:
				return nil
			case replaceLegacy:
				return legacyServerRestartRequired()
			case replaceForeground, replaceSupervised:
				return r.refuseLaunchMode(hs.LaunchMode)
			case replaceAuto:
				if err := shutdownInstance(ctx, client, hs.ServerInstanceID); err != nil {
					if errors.Is(err, httpapi.ErrServerInstanceChanged) {
						continue
					}
					if !httpapi.IsAutoStartableDial(err) && !retryableTransportError(err) {
						return err
					}
				}
				if err := r.waitUntilReleased(ctx, client, hs.ServerInstanceID, logPath); err != nil {
					return err
				}
				continue
			default:
				return nil
			}
		}
		if !httpapi.IsAutoStartableDial(err) {
			return err
		}
		handle, err := r.spawnDaemon(ctx)
		if err != nil {
			return startupFailure("spawn", logPath, err)
		}
		return r.waitUntilReady(ctx, client, handle, logPath)
	}
}

func handshake(ctx context.Context, client *httpapi.Client) (help.Handshake, error) {
	var hs help.Handshake
	err := client.Do(ctx, "GET", "/v1/hello", nil, nil, &hs)
	return hs, err
}

func shutdownInstance(ctx context.Context, client *httpapi.Client, instanceID string) error {
	var accepted help.ShutdownAccepted
	return client.Do(ctx, "POST", "/v1/admin/shutdown", nil, map[string]string{"server_instance_id": instanceID}, &accepted)
}

func checkHandshakeCompatibility(hs help.Handshake) error {
	if hs.ProtocolVersion != app.ProtocolVersion {
		return fmt.Errorf("%w: incompatible protocol version %d (client supports %d)", app.ErrConflict, hs.ProtocolVersion, app.ProtocolVersion)
	}
	if hs.SchemaVersion != app.SchemaVersion {
		return fmt.Errorf("%w: incompatible schema version %d (client supports %d)", app.ErrConflict, hs.SchemaVersion, app.SchemaVersion)
	}
	return nil
}

func (r *runner) lifecycleDecision(hs help.Handshake) replaceAction {
	if r.policy == policyRestart {
		return restartDecision(hs)
	}
	return r.replacementDecision(hs)
}

func restartDecision(hs help.Handshake) replaceAction {
	if hs.ServerInstanceID == "" || hs.LaunchMode == "" {
		return replaceLegacy
	}
	switch hs.LaunchMode {
	case string(service.LaunchModeAuto), string(service.LaunchModeForeground):
		return replaceAuto
	case string(service.LaunchModeSupervised):
		return replaceSupervised
	default:
		return replaceForeground
	}
}

func (r *runner) replacementDecision(hs help.Handshake) replaceAction {
	if !isNewerStable(r.buildVersion(), hs.ServerVersion) {
		return replaceNone
	}
	if !autoStartEnabled(r.g.noAutoStart, r.env.Getenv) {
		return replaceNone
	}
	if hs.ServerInstanceID == "" || hs.LaunchMode == "" {
		return replaceLegacy
	}
	switch hs.LaunchMode {
	case string(service.LaunchModeAuto):
		return replaceAuto
	case string(service.LaunchModeForeground):
		return replaceForeground
	case string(service.LaunchModeSupervised):
		return replaceSupervised
	default:
		return replaceForeground
	}
}

func (r *runner) refuseLaunchMode(launchMode string) error {
	if launchMode == string(service.LaunchModeSupervised) {
		switch r.policy {
		case policyRestart:
			return supervisedControlRequired("restart")
		case policyInspectOnly:
			return supervisedControlRequired("stop")
		default:
			return supervisedControlRequired("restart")
		}
	}
	return serverRestartRequired(launchMode)
}

var stableSemver = regexp.MustCompile(`^v([0-9]+)\.([0-9]+)\.([0-9]+)$`)

type semverTriple struct{ major, minor, patch int }

func parseStableSemver(version string) (semverTriple, bool) {
	match := stableSemver.FindStringSubmatch(strings.TrimSpace(version))
	if match == nil {
		return semverTriple{}, false
	}
	major, err1 := strconv.Atoi(match[1])
	minor, err2 := strconv.Atoi(match[2])
	patch, err3 := strconv.Atoi(match[3])
	if err1 != nil || err2 != nil || err3 != nil {
		return semverTriple{}, false
	}
	return semverTriple{major: major, minor: minor, patch: patch}, true
}

func (v semverTriple) cmp(other semverTriple) int {
	switch {
	case v.major != other.major:
		return v.major - other.major
	case v.minor != other.minor:
		return v.minor - other.minor
	default:
		return v.patch - other.patch
	}
}

func isNewerStable(clientVersion, serverVersion string) bool {
	client, clientOK := parseStableSemver(clientVersion)
	server, serverOK := parseStableSemver(serverVersion)
	if !clientOK || !serverOK {
		return false
	}
	return client.cmp(server) > 0
}

func (r *runner) buildVersion() string {
	if r.env.Version != "" {
		return r.env.Version
	}
	return buildinfo.Version
}

func (r *runner) buildCommit() string {
	if r.env.Commit != "" {
		return r.env.Commit
	}
	return buildinfo.Commit
}

func (r *runner) spawnDaemon(ctx context.Context) (DaemonHandle, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	socket, err := r.resolveSocket()
	if err != nil {
		return nil, err
	}
	executable, err := r.resolvedExecutable()
	if err != nil {
		return nil, err
	}
	stateDir, err := r.stateDir()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, err
	}
	dbPath := filepath.Join(stateDir, "comms.db")
	logPath := filepath.Join(stateDir, "server.log")
	spec := DaemonSpec{
		Executable:   executable,
		Args:         []string{"serve", "--daemon-child", "--socket", socket, "--db", dbPath},
		SocketPath:   socket,
		DatabasePath: dbPath,
		LogPath:      logPath,
	}
	return r.startDaemon(ctx, spec)
}

func (r *runner) startDaemon(ctx context.Context, spec DaemonSpec) (DaemonHandle, error) {
	if r.env.StartDaemon != nil {
		return r.env.StartDaemon(ctx, spec)
	}
	return startDetachedDaemon(spec)
}

func (r *runner) resolvedExecutable() (string, error) {
	if r.env.Executable != nil {
		return r.env.Executable()
	}
	return os.Executable()
}

func (r *runner) waitUntilReady(ctx context.Context, client *httpapi.Client, handle DaemonHandle, logPath string) error {
	wantVersion := r.buildVersion()
	wantCommit := r.buildCommit()
	attempt := 0
	for {
		select {
		case <-ctx.Done():
			return startupFailure("readiness", logPath, ctx.Err())
		case <-handle.Done():
			err := handle.Err()
			if err == nil {
				err = errors.New("daemon exited before becoming ready")
			}
			return startupFailure("readiness", logPath, err)
		default:
		}
		hs, err := handshake(ctx, client)
		if err == nil && hs.ServerInstanceID != "" && hs.LaunchMode == string(service.LaunchModeAuto) && hs.ServerVersion == wantVersion && hs.Commit == wantCommit {
			if err := checkHandshakeCompatibility(hs); err != nil {
				return err
			}
			return nil
		}
		if err := waitBackoff(ctx, attempt); err != nil {
			return startupFailure("readiness", logPath, err)
		}
		attempt++
	}
}

func (r *runner) waitUntilReleased(ctx context.Context, client *httpapi.Client, instanceID, logPath string) error {
	attempt := 0
	for {
		if err := ctx.Err(); err != nil {
			return startupFailure("shutdown", logPath, err)
		}
		hs, err := handshake(ctx, client)
		switch {
		case err == nil:
			if hs.ServerInstanceID != instanceID {
				return nil
			}
		case httpapi.IsAutoStartableDial(err):
			released, lockErr := ownerLockReleased(r.dbLockPath())
			if lockErr != nil {
				return startupFailure("shutdown", logPath, lockErr)
			}
			if released {
				return nil
			}
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			return startupFailure("shutdown", logPath, err)
		case !retryableTransportError(err):
			return err
		}
		if err := waitBackoff(ctx, attempt); err != nil {
			return startupFailure("shutdown", logPath, err)
		}
		attempt++
	}
}

func (r *runner) dbLockPath() string {
	dir, err := r.stateDir()
	if err != nil {
		return "comms.db.lock"
	}
	return filepath.Join(dir, "comms.db.lock")
}

func retryableTransportError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var op *net.OpError
	if errors.As(err, &op) {
		return true
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return errno == syscall.EPIPE || errno == syscall.ECONNRESET || errno == syscall.ENOENT || errno == syscall.ECONNREFUSED
	}
	return false
}

func waitBackoff(ctx context.Context, attempt int) error {
	delay := 5 * time.Millisecond
	if attempt > 0 {
		if attempt > 6 {
			attempt = 6
		}
		delay = 5 * time.Millisecond * time.Duration(1<<attempt)
	}
	if delay > 250*time.Millisecond {
		delay = 250 * time.Millisecond
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (r *runner) stateDir() (string, error) {
	if dir := r.env.Getenv("COMMS_STATE_DIR"); dir != "" {
		return dir, nil
	}
	if dir := r.env.Getenv("XDG_STATE_HOME"); dir != "" {
		return filepath.Join(dir, "comms"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "state", "comms"), nil
}

func (r *runner) serverLogPath() string {
	dir, err := r.stateDir()
	if err != nil {
		return "server.log"
	}
	return filepath.Join(dir, "server.log")
}

type statusReport struct {
	Running          bool      `json:"running"`
	ServerInstanceID string    `json:"server_instance_id,omitempty"`
	PID              int       `json:"pid,omitempty"`
	StartedAt        time.Time `json:"started_at,omitempty"`
	LaunchMode       string    `json:"launch_mode,omitempty"`
	Version          string    `json:"version,omitempty"`
	Commit           string    `json:"commit,omitempty"`
	SocketPath       string    `json:"socket_path,omitempty"`
	DatabasePath     string    `json:"database_path,omitempty"`
}

func statusFromHandshake(hs help.Handshake, fallbackSocket string) statusReport {
	socket := hs.SocketPath
	if socket == "" {
		socket = fallbackSocket
	}
	report := statusReport{
		Running:          true,
		ServerInstanceID: hs.ServerInstanceID,
		PID:              hs.PID,
		LaunchMode:       hs.LaunchMode,
		Version:          hs.ServerVersion,
		Commit:           hs.Commit,
		SocketPath:       socket,
		DatabasePath:     hs.DatabasePath,
	}
	if !hs.StartedAt.IsZero() {
		report.StartedAt = hs.StartedAt.UTC()
	}
	return report
}

func (r *runner) inspectStatus() (statusReport, error) {
	socket, err := r.resolveSocket()
	if err != nil {
		return statusReport{}, err
	}
	client := httpapi.NewUnixClient(socket, "")
	hs, err := handshake(r.commandContext(), client)
	if err != nil {
		if httpapi.IsAutoStartableDial(err) {
			return statusReport{Running: false, SocketPath: socket}, nil
		}
		return statusReport{}, err
	}
	if err := checkHandshakeCompatibility(hs); err != nil {
		return statusReport{}, err
	}
	return statusFromHandshake(hs, socket), nil
}

func (r *runner) liveStatus(client *httpapi.Client) (statusReport, error) {
	socket, err := r.resolveSocket()
	if err != nil {
		return statusReport{}, err
	}
	hs, err := handshake(r.commandContext(), client)
	if err != nil {
		return statusReport{}, err
	}
	if err := checkHandshakeCompatibility(hs); err != nil {
		return statusReport{}, err
	}
	return statusFromHandshake(hs, socket), nil
}

func (r *runner) printStatus(report statusReport) error {
	if r.g.json {
		return r.output(report)
	}
	if !report.Running {
		_, err := fmt.Fprintln(r.env.Stdout, "comms is not running")
		return err
	}
	_, err := fmt.Fprintf(r.env.Stdout, "comms is running (pid %d, %s, %s)\n", report.PID, report.LaunchMode, report.Version)
	return err
}

func (r *runner) printStopped(socket string, didStop bool) error {
	if r.g.json {
		return r.output(statusReport{Running: false, SocketPath: socket})
	}
	msg := "comms is not running"
	if didStop {
		msg = "stopped"
	}
	_, err := fmt.Fprintln(r.env.Stdout, msg)
	return err
}

func (r *runner) stopService(client *httpapi.Client) (didStop bool, err error) {
	ctx := r.commandContext()
	logPath := r.serverLogPath()
	hs, err := handshake(ctx, client)
	if err != nil {
		if httpapi.IsAutoStartableDial(err) {
			return false, nil
		}
		return false, err
	}
	if err := checkHandshakeCompatibility(hs); err != nil {
		return false, err
	}
	if err := r.stoppableHandshake(hs); err != nil {
		return false, err
	}

	lock, err := acquireLifecycleLock(ctx, r.socket+".lifecycle.lock")
	if err != nil {
		return false, startupFailure("lock", logPath, err)
	}
	defer func() { _ = lock.Close() }()

	for {
		if err := ctx.Err(); err != nil {
			return false, startupFailure("shutdown", logPath, err)
		}
		hs, err := handshake(ctx, client)
		if err != nil {
			if httpapi.IsAutoStartableDial(err) {
				return false, nil
			}
			return false, err
		}
		if err := checkHandshakeCompatibility(hs); err != nil {
			return false, err
		}
		if err := r.stoppableHandshake(hs); err != nil {
			return false, err
		}
		if err := shutdownInstance(ctx, client, hs.ServerInstanceID); err != nil {
			if errors.Is(err, httpapi.ErrServerInstanceChanged) {
				continue
			}
			if !httpapi.IsAutoStartableDial(err) && !retryableTransportError(err) {
				return false, err
			}
		}
		if err := r.waitUntilReleased(ctx, client, hs.ServerInstanceID, logPath); err != nil {
			return false, err
		}
		return true, nil
	}
}

func (r *runner) stoppableHandshake(hs help.Handshake) error {
	if hs.ServerInstanceID == "" || hs.LaunchMode == "" {
		return legacyServerRestartRequired()
	}
	switch hs.LaunchMode {
	case string(service.LaunchModeAuto), string(service.LaunchModeForeground):
		return nil
	case string(service.LaunchModeSupervised):
		return supervisedControlRequired("stop")
	default:
		return r.refuseLaunchMode(hs.LaunchMode)
	}
}
