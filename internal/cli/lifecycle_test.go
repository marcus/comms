package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/marcus/comms/internal/httpapi"
	"github.com/marcus/comms/internal/service"
)

type isolatedRoots struct {
	root    string
	state   string
	runtime string
	socket  string
}

func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "comms-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func newIsolatedRoots(t *testing.T) isolatedRoots {
	t.Helper()
	root := shortTempDir(t)
	iso := isolatedRoots{
		root:    root,
		state:   filepath.Join(root, "state"),
		runtime: filepath.Join(root, "runtime"),
	}
	iso.socket = filepath.Join(iso.runtime, "comms.sock")
	for _, dir := range []string{iso.state, iso.runtime} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return iso
}

func (iso isolatedRoots) getenv(extra map[string]string) func(string) string {
	return func(key string) string {
		if extra != nil {
			if value, ok := extra[key]; ok {
				return value
			}
		}
		switch key {
		case "COMMS_STATE_DIR":
			return iso.state
		case "XDG_RUNTIME_DIR":
			return iso.runtime
		case "HOME":
			return iso.root
		case "XDG_STATE_HOME":
			return filepath.Join(iso.root, "xdg-state")
		default:
			return ""
		}
	}
}

type spawnRecorder struct {
	mu    sync.Mutex
	specs []DaemonSpec
	start func(context.Context, DaemonSpec) (DaemonHandle, error)
}

func (s *spawnRecorder) Start(ctx context.Context, spec DaemonSpec) (DaemonHandle, error) {
	s.mu.Lock()
	s.specs = append(s.specs, spec)
	start := s.start
	s.mu.Unlock()
	if start == nil {
		return nil, errors.New("spawn disabled")
	}
	return start(ctx, spec)
}

func (s *spawnRecorder) calls() []DaemonSpec {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]DaemonSpec, len(s.specs))
	copy(out, s.specs)
	return out
}

type stubDaemon struct {
	pid  int
	done chan struct{}
	err  error
}

func (s *stubDaemon) PID() int              { return s.pid }
func (s *stubDaemon) Done() <-chan struct{} { return s.done }
func (s *stubDaemon) Err() error            { return s.err }

func TestCommandPolicyMatrix(t *testing.T) {
	tests := []struct {
		command string
		want    commandPolicy
	}{
		{command: "help", want: policyProcessLocal},
		{command: "version", want: policyProcessLocal},
		{command: "openapi", want: policyProcessLocal},
		{command: "capabilities", want: policyProcessLocal},
		{command: "instructions", want: policyProcessLocal},
		{command: "serve", want: policyProcessLocal},
		{command: "hello", want: policyInspectOnly},
		{command: "health", want: policyInspectOnly},
		{command: "doctor", want: policyInspectOnly},
		{command: "join", want: policyAutoStart},
		{command: "whoami", want: policyAutoStart},
		{command: "agents", want: policyAutoStart},
		{command: "agent", want: policyAutoStart},
		{command: "topic", want: policyAutoStart},
		{command: "topics", want: policyAutoStart},
		{command: "subscriptions", want: policyAutoStart},
		{command: "publish", want: policyAutoStart},
		{command: "send", want: policyAutoStart},
		{command: "reply", want: policyAutoStart},
		{command: "inbox", want: policyAutoStart},
		{command: "peek", want: policyAutoStart},
		{command: "read-through", want: policyAutoStart},
		{command: "receipts", want: policyAutoStart},
		{command: "thread", want: policyAutoStart},
		{command: "search", want: policyAutoStart},
		{command: "observe", want: policyAutoStart},
		{command: "retention", want: policyAutoStart},
		{command: "purge", want: policyAutoStart},
		{command: "export", want: policyAutoStart},
		{command: "nope", want: policyProcessLocal},
	}
	for _, tt := range tests {
		if got := commandPolicyOf(tt.command); got != tt.want {
			t.Errorf("commandPolicyOf(%q)=%v want %v", tt.command, got, tt.want)
		}
	}
}

func TestPolicyDispatchDoesNotSpawnForProcessLocalOrInspect(t *testing.T) {
	iso := newIsolatedRoots(t)
	spawn := &spawnRecorder{}
	tests := []struct {
		name string
		args []string
		code int
	}{
		{name: "version", args: []string{"version"}, code: 0},
		{name: "help", args: []string{"help"}, code: 0},
		{name: "openapi", args: []string{"openapi"}, code: 0},
		{name: "capabilities", args: []string{"capabilities"}, code: 0},
		{name: "instructions", args: []string{"instructions"}, code: 0},
		{name: "hello", args: []string{"--socket", iso.socket, "hello"}, code: 5},
		{name: "health", args: []string{"--socket", iso.socket, "health"}, code: 5},
		{name: "doctor", args: []string{"--socket", iso.socket, "doctor"}, code: 5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stderr bytes.Buffer
			code := Run(Env{Args: tt.args, Stderr: &stderr, Getenv: iso.getenv(nil), StartDaemon: spawn.Start})
			if code != tt.code {
				t.Fatalf("code=%d want %d stderr=%s", code, tt.code, stderr.String())
			}
		})
	}
	if calls := spawn.calls(); len(calls) != 0 {
		t.Fatalf("unexpected spawn: %#v", calls)
	}
}

func TestAutoStartEligibleCommandsSpawnOnMissingSocket(t *testing.T) {
	iso := newIsolatedRoots(t)
	spawn := &spawnRecorder{}
	for _, args := range [][]string{
		{"--socket", iso.socket, "join", "alice"},
		{"--socket", iso.socket, "agents"},
		{"--socket", iso.socket, "export"},
	} {
		t.Run(args[len(args)-1], func(t *testing.T) {
			before := len(spawn.calls())
			var stderr bytes.Buffer
			code := Run(Env{Args: args, Stderr: &stderr, Getenv: iso.getenv(nil), StartDaemon: spawn.Start})
			if code != 5 {
				t.Fatalf("code=%d stderr=%s", code, stderr.String())
			}
			calls := spawn.calls()
			if len(calls) != before+1 {
				t.Fatalf("spawn calls=%d want %d stderr=%s", len(calls), before+1, stderr.String())
			}
			spec := calls[len(calls)-1]
			joined := strings.Join(spec.Args, " ")
			if !strings.Contains(joined, "serve --daemon-child --socket "+iso.socket) || !strings.Contains(joined, "--db "+filepath.Join(iso.state, "comms.db")) {
				t.Fatalf("args=%q", joined)
			}
			if spec.LogPath != filepath.Join(iso.state, "server.log") {
				t.Fatalf("log=%q", spec.LogPath)
			}
		})
	}
}

func TestNoAutoStartFlagWinsOverEnv(t *testing.T) {
	iso := newIsolatedRoots(t)
	spawn := &spawnRecorder{}
	var stderr bytes.Buffer
	code := Run(Env{
		Args:        []string{"--no-auto-start", "--socket", iso.socket, "join", "alice"},
		Stderr:      &stderr,
		Getenv:      iso.getenv(map[string]string{"COMMS_AUTO_START": "1"}),
		StartDaemon: spawn.Start,
	})
	if code != 5 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "Start the service with 'comms serve'.") {
		t.Fatalf("stderr=%s", stderr.String())
	}
	if calls := spawn.calls(); len(calls) != 0 {
		t.Fatalf("spawned: %#v", calls)
	}
}

func TestCOMMSAutoStartFalseLikeValues(t *testing.T) {
	for _, value := range []string{"0", "false", "no", "off", "FALSE", "Off"} {
		t.Run(value, func(t *testing.T) {
			iso := newIsolatedRoots(t)
			spawn := &spawnRecorder{}
			var stderr bytes.Buffer
			code := Run(Env{
				Args:        []string{"--socket", iso.socket, "join", "alice"},
				Stderr:      &stderr,
				Getenv:      iso.getenv(map[string]string{"COMMS_AUTO_START": value}),
				StartDaemon: spawn.Start,
			})
			if code != 5 {
				t.Fatalf("code=%d stderr=%s", code, stderr.String())
			}
			if calls := spawn.calls(); len(calls) != 0 {
				t.Fatalf("spawned for %q: %#v", value, calls)
			}
		})
	}
}

func TestPermissionErrorDoesNotSpawn(t *testing.T) {
	iso := newIsolatedRoots(t)
	blocked := filepath.Join(iso.root, "blocked")
	if err := os.Mkdir(blocked, 0o700); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(blocked, "comms.sock")
	if err := os.Chmod(blocked, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(blocked, 0o700) })
	spawn := &spawnRecorder{}
	var stderr bytes.Buffer
	code := Run(Env{
		Args:        []string{"--socket", socket, "join", "alice"},
		Stderr:      &stderr,
		Getenv:      iso.getenv(nil),
		StartDaemon: spawn.Start,
	})
	if code != 5 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if calls := spawn.calls(); len(calls) != 0 {
		t.Fatalf("spawned: %#v", calls)
	}
}

func TestCanceledContextDoesNotSpawn(t *testing.T) {
	iso := newIsolatedRoots(t)
	spawn := &spawnRecorder{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var stderr bytes.Buffer
	code := Run(Env{
		Args:        []string{"--socket", iso.socket, "join", "alice"},
		Context:     ctx,
		Stderr:      &stderr,
		Getenv:      iso.getenv(nil),
		StartDaemon: spawn.Start,
	})
	if code != 5 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if calls := spawn.calls(); len(calls) != 0 {
		t.Fatalf("spawned: %#v", calls)
	}
}

func TestLockRecheckDoesNotSpawn(t *testing.T) {
	iso := newIsolatedRoots(t)
	lock, err := acquireLifecycleLock(context.Background(), iso.socket+".lifecycle.lock")
	if err != nil {
		t.Fatal(err)
	}
	released := false
	defer func() {
		if !released {
			_ = lock.Close()
		}
	}()

	var spawned atomic.Int32
	var stdout, stderr bytes.Buffer
	done := make(chan int, 1)
	go func() {
		done <- Run(Env{
			Args:   []string{"--socket", iso.socket, "--json", "join", "alice"},
			Stdout: &stdout,
			Stderr: &stderr,
			Getenv: iso.getenv(nil),
			StartDaemon: func(context.Context, DaemonSpec) (DaemonHandle, error) {
				spawned.Add(1)
				return nil, errors.New("should not spawn")
			},
		})
	}()
	select {
	case code := <-done:
		t.Fatalf("join finished while lock held, code=%d stderr=%s", code, stderr.String())
	case <-time.After(150 * time.Millisecond):
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- service.Run(ctx, service.Config{
			SocketPath:   iso.socket,
			DatabasePath: filepath.Join(iso.state, "comms.db"),
			LaunchMode:   service.LaunchModeForeground,
		})
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-serverDone:
		case <-time.After(5 * time.Second):
			t.Error("server did not stop")
		}
	})
	waitForHello(t, iso.socket)
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	released = true

	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("code=%d stderr=%s stdout=%s", code, stderr.String(), stdout.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("join did not complete after lock release")
	}
	if spawned.Load() != 0 {
		t.Fatal("spawned after handshake succeeded under lock")
	}
}

func TestStartupFailureNamesPhaseAndLogPath(t *testing.T) {
	iso := newIsolatedRoots(t)
	var stderr bytes.Buffer
	code := Run(Env{
		Args:   []string{"--json", "--socket", iso.socket, "join", "alice"},
		Stderr: &stderr,
		Getenv: iso.getenv(nil),
		StartDaemon: func(context.Context, DaemonSpec) (DaemonHandle, error) {
			return nil, errors.New("exec failed")
		},
	})
	if code != 5 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	var envelope httpapi.ErrorEnvelope
	if err := json.Unmarshal(stderr.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Error.Code != "unavailable" {
		t.Fatalf("code=%s body=%s", envelope.Error.Code, stderr.String())
	}
	if envelope.Error.Details["phase"] != "spawn" {
		t.Fatalf("details=%v", envelope.Error.Details)
	}
	logPath, _ := envelope.Error.Details["log_path"].(string)
	if logPath != filepath.Join(iso.state, "server.log") {
		t.Fatalf("log_path=%q", logPath)
	}
	if !strings.Contains(envelope.Error.Message, "spawn") || !strings.Contains(envelope.Error.Message, logPath) {
		t.Fatalf("message=%q", envelope.Error.Message)
	}
}

func TestChildExitNamesReadinessPhase(t *testing.T) {
	iso := newIsolatedRoots(t)
	var stderr bytes.Buffer
	done := make(chan struct{})
	close(done)
	code := Run(Env{
		Args:   []string{"--json", "--socket", iso.socket, "join", "alice"},
		Stderr: &stderr,
		Getenv: iso.getenv(nil),
		StartDaemon: func(context.Context, DaemonSpec) (DaemonHandle, error) {
			return &stubDaemon{done: done, err: errors.New("exit 1")}, nil
		},
	})
	if code != 5 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	var envelope httpapi.ErrorEnvelope
	if err := json.Unmarshal(stderr.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Error.Details["phase"] != "readiness" {
		t.Fatalf("details=%v body=%s", envelope.Error.Details, stderr.String())
	}
	if !strings.Contains(envelope.Error.Message, filepath.Join(iso.state, "server.log")) {
		t.Fatalf("message=%q", envelope.Error.Message)
	}
}

func TestAutoStartJoinWithFakeDaemonIsQuiet(t *testing.T) {
	iso := newIsolatedRoots(t)
	var stdout, stderr bytes.Buffer
	code := Run(Env{
		Args:        []string{"--socket", iso.socket, "join", "alice"},
		Stdout:      &stdout,
		Stderr:      &stderr,
		Getenv:      iso.getenv(nil),
		StartDaemon: inProcessAutoStart(t),
	})
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr=%q", stderr.String())
	}
	if strings.Contains(stdout.String(), "serving on") || strings.Contains(stdout.String(), "started daemon") {
		t.Fatalf("stdout=%q", stdout.String())
	}
	if !strings.Contains(stdout.String(), "alice") {
		t.Fatalf("stdout=%q", stdout.String())
	}
}

func TestStaleSocketAutoStarts(t *testing.T) {
	iso := newIsolatedRoots(t)
	addr, err := net.ResolveUnixAddr("unix", iso.socket)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.ListenUnix("unix", addr)
	if err != nil {
		t.Fatal(err)
	}
	listener.SetUnlinkOnClose(false)
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := Run(Env{
		Args:        []string{"--socket", iso.socket, "--json", "join", "alice"},
		Stdout:      &stdout,
		Stderr:      &stderr,
		Getenv:      iso.getenv(nil),
		StartDaemon: inProcessAutoStart(t),
	})
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
}

func inProcessAutoStart(t *testing.T) func(context.Context, DaemonSpec) (DaemonHandle, error) {
	t.Helper()
	return func(_ context.Context, spec DaemonSpec) (DaemonHandle, error) {
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		done := make(chan struct{})
		handle := &stubDaemon{done: done}
		go func() {
			handle.err = service.Run(ctx, service.Config{
				SocketPath:   spec.SocketPath,
				DatabasePath: spec.DatabasePath,
				LaunchMode:   service.LaunchModeAuto,
			})
			close(done)
		}()
		return handle, nil
	}
}

func waitForHello(t *testing.T, socket string) {
	t.Helper()
	client := httpapi.NewUnixClient(socket, "")
	deadline := time.Now().Add(5 * time.Second)
	var last error
	for time.Now().Before(deadline) {
		var payload map[string]any
		last = client.Do(context.Background(), "GET", "/v1/hello", nil, nil, &payload)
		if last == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("hello not ready: %v", last)
}
