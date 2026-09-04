package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/marcus/comms/internal/app"
	"github.com/marcus/comms/internal/help"
	"github.com/marcus/comms/internal/httpapi"
	"github.com/marcus/comms/internal/service"
)

type commandPolicy int

const (
	policyProcessLocal commandPolicy = iota
	policyInspectOnly
	policyAutoStart
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
	case "hello", "health", "doctor":
		return policyInspectOnly
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

func (r *runner) ensureReady(client *httpapi.Client) error {
	ctx := r.commandContext()
	_, err := handshake(ctx, client)
	if err == nil {
		return nil
	}
	if !autoStartEnabled(r.g.noAutoStart, r.env.Getenv) || !httpapi.IsAutoStartableDial(err) {
		return err
	}
	logPath := r.serverLogPath()
	lockPath := r.socket + ".lifecycle.lock"
	lock, err := acquireLifecycleLock(ctx, lockPath)
	if err != nil {
		return startupFailure("lock", logPath, err)
	}
	defer func() { _ = lock.Close() }()
	_, err = handshake(ctx, client)
	if err == nil {
		return nil
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

func handshake(ctx context.Context, client *httpapi.Client) (help.Handshake, error) {
	var hs help.Handshake
	err := client.Do(ctx, "GET", "/v1/hello", nil, nil, &hs)
	return hs, err
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
		if err == nil && hs.ServerInstanceID != "" && hs.LaunchMode == string(service.LaunchModeAuto) {
			return nil
		}
		if err := waitBackoff(ctx, attempt); err != nil {
			return startupFailure("readiness", logPath, err)
		}
		attempt++
	}
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
