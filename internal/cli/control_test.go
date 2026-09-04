package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/marcus/comms/internal/httpapi"
	"github.com/marcus/comms/internal/service"
)

func decodeStatus(t *testing.T, stdout []byte) statusReport {
	t.Helper()
	var envelope struct {
		Schema string       `json:"schema"`
		Data   statusReport `json:"data"`
	}
	if err := json.Unmarshal(stdout, &envelope); err != nil {
		t.Fatalf("decode status %q: %v", stdout, err)
	}
	if envelope.Schema != cliSchema {
		t.Fatalf("schema=%q", envelope.Schema)
	}
	return envelope.Data
}

func TestStatusStoppedIsSuccess(t *testing.T) {
	iso := newIsolatedRoots(t)
	spawn := &spawnRecorder{}
	var stdout, stderr bytes.Buffer
	code := Run(Env{
		Args:        []string{"--socket", iso.socket, "--json", "status"},
		Stdout:      &stdout,
		Stderr:      &stderr,
		Getenv:      iso.getenv(nil),
		StartDaemon: spawn.Start,
	})
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	report := decodeStatus(t, stdout.Bytes())
	if report.Running {
		t.Fatalf("running=%v", report)
	}
	if report.SocketPath != iso.socket {
		t.Fatalf("socket=%q", report.SocketPath)
	}
	if report.PID != 0 || report.ServerInstanceID != "" {
		t.Fatalf("unexpected live fields: %#v", report)
	}
	if calls := spawn.calls(); len(calls) != 0 {
		t.Fatalf("spawned: %#v", calls)
	}
}

func TestStatusRunningReportsHandshake(t *testing.T) {
	iso := newIsolatedRoots(t)
	hello := testHello("v1.1.0", "abc1234", "auto", "srv_liveinstance0000000000001")
	hello.SocketPath = iso.socket
	hello.DatabasePath = filepath.Join(iso.state, "comms.db")
	_ = startScriptedServer(t, iso.socket, hello)
	spawn := &spawnRecorder{}
	var stdout, stderr bytes.Buffer
	code := Run(Env{
		Args:        []string{"--socket", iso.socket, "--json", "status"},
		Stdout:      &stdout,
		Stderr:      &stderr,
		Getenv:      iso.getenv(nil),
		StartDaemon: spawn.Start,
	})
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	report := decodeStatus(t, stdout.Bytes())
	if !report.Running || report.ServerInstanceID != hello.ServerInstanceID || report.PID != 42 {
		t.Fatalf("report=%#v", report)
	}
	if report.LaunchMode != "auto" || report.Version != "v1.1.0" || report.Commit != "abc1234" {
		t.Fatalf("identity=%#v", report)
	}
	if report.SocketPath != iso.socket || report.DatabasePath != hello.DatabasePath {
		t.Fatalf("paths=%#v", report)
	}
	if report.StartedAt.IsZero() {
		t.Fatal("missing started_at")
	}
	if calls := spawn.calls(); len(calls) != 0 {
		t.Fatalf("spawned: %#v", calls)
	}
}

func TestStatusIncompatibleIsNonzero(t *testing.T) {
	iso := newIsolatedRoots(t)
	hello := testHello("v1.1.0", "oldold1", "auto", "srv_bad")
	hello.ProtocolVersion = 99
	_ = startScriptedServer(t, iso.socket, hello)
	var stderr bytes.Buffer
	code := Run(Env{
		Args:   []string{"--socket", iso.socket, "--json", "status"},
		Stderr: &stderr,
		Getenv: iso.getenv(nil),
	})
	if code != 4 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
}

func TestStopIdempotentWhenAlreadyStopped(t *testing.T) {
	iso := newIsolatedRoots(t)
	spawn := &spawnRecorder{}
	var stdout, stderr bytes.Buffer
	code := Run(Env{
		Args:        []string{"--socket", iso.socket, "--json", "stop"},
		Stdout:      &stdout,
		Stderr:      &stderr,
		Getenv:      iso.getenv(nil),
		StartDaemon: spawn.Start,
	})
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	report := decodeStatus(t, stdout.Bytes())
	if report.Running || report.SocketPath != iso.socket {
		t.Fatalf("report=%#v", report)
	}
	if calls := spawn.calls(); len(calls) != 0 {
		t.Fatalf("spawned: %#v", calls)
	}
}

func TestStopAutoDaemonThenStatusStopped(t *testing.T) {
	iso := newIsolatedRoots(t)
	var stdout, stderr bytes.Buffer
	code := Run(Env{
		Args:        []string{"--socket", iso.socket, "--json", "join", "alice"},
		Stdout:      &stdout,
		Stderr:      &stderr,
		Getenv:      iso.getenv(nil),
		StartDaemon: inProcessAutoStart(t),
	})
	if code != 0 {
		t.Fatalf("join code=%d stderr=%s", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	code = Run(Env{
		Args:   []string{"--socket", iso.socket, "--json", "stop"},
		Stdout: &stdout,
		Stderr: &stderr,
		Getenv: iso.getenv(nil),
		StartDaemon: func(context.Context, DaemonSpec) (DaemonHandle, error) {
			return nil, errors.New("should not spawn")
		},
	})
	if code != 0 {
		t.Fatalf("stop code=%d stderr=%s", code, stderr.String())
	}
	if decodeStatus(t, stdout.Bytes()).Running {
		t.Fatal("stop reported running")
	}
	stdout.Reset()
	stderr.Reset()
	code = Run(Env{
		Args:   []string{"--socket", iso.socket, "--json", "status"},
		Stdout: &stdout,
		Stderr: &stderr,
		Getenv: iso.getenv(nil),
		StartDaemon: func(context.Context, DaemonSpec) (DaemonHandle, error) {
			return nil, errors.New("should not spawn")
		},
	})
	if code != 0 {
		t.Fatalf("status code=%d stderr=%s", code, stderr.String())
	}
	if decodeStatus(t, stdout.Bytes()).Running {
		t.Fatal("status still running")
	}
}

func TestStopSupervisedRefusedLeavesProcessUp(t *testing.T) {
	iso := newIsolatedRoots(t)
	hello := testHello("v1.2.0", "newnew1", "supervised", "srv_sup")
	srv := startScriptedServer(t, iso.socket, hello)
	var stdout, stderr bytes.Buffer
	code := Run(Env{
		Args:    []string{"--socket", iso.socket, "--json", "stop"},
		Stdout:  &stdout,
		Stderr:  &stderr,
		Getenv:  iso.getenv(nil),
		Version: "v1.2.0",
		Commit:  "newnew1",
		StartDaemon: func(context.Context, DaemonSpec) (DaemonHandle, error) {
			return nil, errors.New("should not spawn")
		},
	})
	if code != 5 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	envelope := decodeError(t, stderr.Bytes())
	if envelope.Error.Code != "server_restart_required" {
		t.Fatalf("error=%#v", envelope.Error)
	}
	if envelope.Error.Details["launch_mode"] != "supervised" {
		t.Fatalf("details=%v", envelope.Error.Details)
	}
	if !strings.Contains(envelope.Error.Message, "brew services stop comms") {
		t.Fatalf("message=%q", envelope.Error.Message)
	}
	if srv.count("POST", "/v1/admin/shutdown") != 0 {
		t.Fatalf("shutdown called: %v", srv.requests())
	}
	var helloOut, helloErr bytes.Buffer
	helloCode := Run(Env{
		Args:   []string{"--socket", iso.socket, "--json", "hello"},
		Stdout: &helloOut,
		Stderr: &helloErr,
		Getenv: iso.getenv(nil),
	})
	if helloCode != 0 {
		t.Fatalf("hello after refused stop code=%d stderr=%s", helloCode, helloErr.String())
	}
}

func TestStopLegacyRefusedLeavesSocket(t *testing.T) {
	iso := newIsolatedRoots(t)
	hello := testHello("v1.0.0", "unknown", "", "")
	hello.ServerInstanceID = ""
	hello.LaunchMode = ""
	srv := startScriptedServer(t, iso.socket, hello)
	var stderr bytes.Buffer
	code := Run(Env{
		Args:   []string{"--socket", iso.socket, "--json", "stop"},
		Stderr: &stderr,
		Getenv: iso.getenv(nil),
		StartDaemon: func(context.Context, DaemonSpec) (DaemonHandle, error) {
			return nil, errors.New("should not spawn")
		},
	})
	if code != 5 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	envelope := decodeError(t, stderr.Bytes())
	if envelope.Error.Code != "legacy_server_restart_required" {
		t.Fatalf("error=%#v", envelope.Error)
	}
	if srv.count("POST", "/v1/admin/shutdown") != 0 {
		t.Fatalf("shutdown called: %v", srv.requests())
	}
	if _, err := os.Stat(iso.socket); err != nil {
		t.Fatalf("socket unlinked: %v", err)
	}
}

func TestRestartStartsStoppedService(t *testing.T) {
	iso := newIsolatedRoots(t)
	var stdout, stderr bytes.Buffer
	code := Run(Env{
		Args:        []string{"--socket", iso.socket, "--json", "restart"},
		Stdout:      &stdout,
		Stderr:      &stderr,
		Getenv:      iso.getenv(nil),
		StartDaemon: inProcessAutoStart(t),
	})
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	report := decodeStatus(t, stdout.Bytes())
	if !report.Running || report.LaunchMode != "auto" || report.PID == 0 {
		t.Fatalf("report=%#v", report)
	}
}

func TestRestartReplacesForegroundWithAuto(t *testing.T) {
	iso := newIsolatedRoots(t)
	old := startScriptedServer(t, iso.socket, testHello("v1.2.0", "newnew1", "foreground", "srv_fg"))
	newHello := testHello("v1.2.0", "newnew1", "auto", "srv_new")
	newHello.SocketPath = iso.socket
	var spawned atomic.Int32
	var newSrv *scriptedServer
	var stdout, stderr bytes.Buffer
	code := Run(Env{
		Args:    []string{"--socket", iso.socket, "--json", "restart"},
		Stdout:  &stdout,
		Stderr:  &stderr,
		Getenv:  iso.getenv(nil),
		Version: "v1.2.0",
		Commit:  "newnew1",
		StartDaemon: func(_ context.Context, spec DaemonSpec) (DaemonHandle, error) {
			spawned.Add(1)
			joined := strings.Join(spec.Args, " ")
			if !strings.Contains(joined, "serve --daemon-child --socket "+iso.socket) {
				t.Errorf("spawn args=%q", joined)
			}
			newSrv = startScriptedServer(t, spec.SocketPath, newHello)
			return &stubDaemon{done: make(chan struct{})}, nil
		},
	})
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if spawned.Load() != 1 {
		t.Fatalf("spawned=%d", spawned.Load())
	}
	if old.count("POST", "/v1/admin/shutdown") != 1 {
		t.Fatalf("old shutdowns=%v", old.shutdownIDs())
	}
	report := decodeStatus(t, stdout.Bytes())
	if !report.Running || report.LaunchMode != "auto" || report.ServerInstanceID != "srv_new" {
		t.Fatalf("report=%#v newSrv=%#v", report, newSrv)
	}
}

func TestRestartSupervisedRefused(t *testing.T) {
	iso := newIsolatedRoots(t)
	srv := startScriptedServer(t, iso.socket, testHello("v1.2.0", "newnew1", "supervised", "srv_sup"))
	var stderr bytes.Buffer
	code := Run(Env{
		Args:    []string{"--socket", iso.socket, "--json", "restart"},
		Stderr:  &stderr,
		Getenv:  iso.getenv(nil),
		Version: "v1.2.0",
		Commit:  "newnew1",
		StartDaemon: func(context.Context, DaemonSpec) (DaemonHandle, error) {
			return nil, errors.New("should not spawn")
		},
	})
	if code != 5 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	envelope := decodeError(t, stderr.Bytes())
	if envelope.Error.Code != "server_restart_required" {
		t.Fatalf("error=%#v", envelope.Error)
	}
	if !strings.Contains(envelope.Error.Message, "brew services restart comms") {
		t.Fatalf("message=%q", envelope.Error.Message)
	}
	if srv.count("POST", "/v1/admin/shutdown") != 0 {
		t.Fatalf("shutdown called: %v", srv.requests())
	}
}

func TestRestartLegacyRefused(t *testing.T) {
	iso := newIsolatedRoots(t)
	hello := testHello("v1.0.0", "unknown", "", "")
	hello.ServerInstanceID = ""
	hello.LaunchMode = ""
	srv := startScriptedServer(t, iso.socket, hello)
	var stderr bytes.Buffer
	code := Run(Env{
		Args:   []string{"--socket", iso.socket, "--json", "restart"},
		Stderr: &stderr,
		Getenv: iso.getenv(nil),
		StartDaemon: func(context.Context, DaemonSpec) (DaemonHandle, error) {
			return nil, errors.New("should not spawn")
		},
	})
	if code != 5 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	envelope := decodeError(t, stderr.Bytes())
	if envelope.Error.Code != "legacy_server_restart_required" {
		t.Fatalf("error=%#v", envelope.Error)
	}
	if srv.count("POST", "/v1/admin/shutdown") != 0 {
		t.Fatalf("shutdown called: %v", srv.requests())
	}
}

func TestInspectOnlyCommandsDoNotAutoStart(t *testing.T) {
	iso := newIsolatedRoots(t)
	spawn := &spawnRecorder{}
	for _, command := range []string{"hello", "health", "doctor", "status", "stop"} {
		t.Run(command, func(t *testing.T) {
			before := len(spawn.calls())
			var stderr bytes.Buffer
			code := Run(Env{
				Args:        []string{"--socket", iso.socket, "--json", command},
				Stderr:      &stderr,
				Getenv:      iso.getenv(nil),
				StartDaemon: spawn.Start,
			})
			want := 5
			if command == "status" || command == "stop" {
				want = 0
			}
			if code != want {
				t.Fatalf("code=%d want %d stderr=%s", code, want, stderr.String())
			}
			if len(spawn.calls()) != before {
				t.Fatalf("spawned on %s: %#v", command, spawn.calls())
			}
		})
	}
}

func TestNoAutoStartJoinStillExit5(t *testing.T) {
	iso := newIsolatedRoots(t)
	spawn := &spawnRecorder{}
	var stderr bytes.Buffer
	code := Run(Env{
		Args:        []string{"--no-auto-start", "--socket", iso.socket, "--json", "join", "alice"},
		Stderr:      &stderr,
		Getenv:      iso.getenv(nil),
		StartDaemon: spawn.Start,
	})
	if code != 5 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	envelope := decodeError(t, stderr.Bytes())
	if envelope.Error.Code != "unavailable" {
		t.Fatalf("error=%#v", envelope.Error)
	}
	if calls := spawn.calls(); len(calls) != 0 {
		t.Fatalf("spawned: %#v", calls)
	}
}

func TestNoAutoStartDoesNotPreventRestart(t *testing.T) {
	iso := newIsolatedRoots(t)
	var stdout, stderr bytes.Buffer
	code := Run(Env{
		Args:        []string{"--no-auto-start", "--socket", iso.socket, "--json", "restart"},
		Stdout:      &stdout,
		Stderr:      &stderr,
		Getenv:      iso.getenv(nil),
		StartDaemon: inProcessAutoStart(t),
	})
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	report := decodeStatus(t, stdout.Bytes())
	if !report.Running || report.LaunchMode != "auto" {
		t.Fatalf("report=%#v", report)
	}
}

func TestRestartReplacesInProcessForeground(t *testing.T) {
	iso := newIsolatedRoots(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- service.Run(ctx, service.Config{
			SocketPath:   iso.socket,
			DatabasePath: filepath.Join(iso.state, "comms.db"),
			LaunchMode:   service.LaunchModeForeground,
		})
	}()
	waitForHello(t, iso.socket)
	var stdout, stderr bytes.Buffer
	code := Run(Env{
		Args:        []string{"--socket", iso.socket, "--json", "restart"},
		Stdout:      &stdout,
		Stderr:      &stderr,
		Getenv:      iso.getenv(nil),
		StartDaemon: inProcessAutoStart(t),
	})
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	report := decodeStatus(t, stdout.Bytes())
	if !report.Running || report.LaunchMode != "auto" {
		t.Fatalf("report=%#v", report)
	}
	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("foreground serve: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("foreground serve did not exit after restart")
	}
}

func TestBlackBoxStatusStopRestart(t *testing.T) {
	bin := commsBinary(t)
	env, _, socket := isolatedProcEnv(t)
	t.Cleanup(func() { shutdownIsolated(t, socket) })

	stdout, stderr, code, err := execBin(bin, env, "--json", "status")
	if err != nil {
		t.Fatal(err)
	}
	if code != 0 {
		t.Fatalf("status stopped code=%d stderr=%s stdout=%s", code, stderr, stdout)
	}
	stopped := decodeStatus(t, []byte(stdout))
	if stopped.Running {
		t.Fatalf("expected stopped: %#v", stopped)
	}

	stdout, stderr, code, err = execBin(bin, env, "--json", "stop")
	if err != nil {
		t.Fatal(err)
	}
	if code != 0 {
		t.Fatalf("stop idempotent code=%d stderr=%s stdout=%s", code, stderr, stdout)
	}

	for _, command := range []string{"hello", "health", "doctor"} {
		_, stderr, code, err = execBin(bin, env, "--json", command)
		if err != nil {
			t.Fatal(err)
		}
		if code != 5 {
			t.Fatalf("%s code=%d stderr=%s", command, code, stderr)
		}
	}
	if _, err := os.Stat(socket); !os.IsNotExist(err) {
		t.Fatalf("inspect-only created socket: %v", err)
	}

	stdout, stderr, code, err = execBin(bin, env, "--json", "restart")
	if err != nil {
		t.Fatal(err)
	}
	if code != 0 {
		t.Fatalf("restart code=%d stderr=%s stdout=%s", code, stderr, stdout)
	}
	started := decodeStatus(t, []byte(stdout))
	if !started.Running || started.LaunchMode != "auto" || started.PID == 0 {
		t.Fatalf("restart=%#v", started)
	}

	stdout, stderr, code, err = execBin(bin, env, "--json", "status")
	if err != nil {
		t.Fatal(err)
	}
	if code != 0 {
		t.Fatalf("status running code=%d stderr=%s stdout=%s", code, stderr, stdout)
	}
	running := decodeStatus(t, []byte(stdout))
	if !running.Running || running.ServerInstanceID != started.ServerInstanceID {
		t.Fatalf("status=%#v want instance %s", running, started.ServerInstanceID)
	}

	stdout, stderr, code, err = execBin(bin, env, "--json", "stop")
	if err != nil {
		t.Fatal(err)
	}
	if code != 0 {
		t.Fatalf("stop running code=%d stderr=%s stdout=%s", code, stderr, stdout)
	}
	if decodeStatus(t, []byte(stdout)).Running {
		t.Fatal("stop reported running")
	}
	stdout, stderr, code, err = execBin(bin, env, "--json", "status")
	if err != nil {
		t.Fatal(err)
	}
	if code != 0 {
		t.Fatalf("status after stop code=%d stderr=%s stdout=%s", code, stderr, stdout)
	}
	if decodeStatus(t, []byte(stdout)).Running {
		t.Fatal("status still running after stop")
	}

	stdout, stderr, code, err = execBin(bin, env, "--no-auto-start", "--json", "join", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if code != 5 {
		t.Fatalf("no-auto-start join code=%d stderr=%s stdout=%s", code, stderr, stdout)
	}
	var envelope httpapi.ErrorEnvelope
	if err := json.Unmarshal([]byte(stderr), &envelope); err != nil {
		t.Fatalf("stderr=%s err=%v", stderr, err)
	}
	if envelope.Error.Code != "unavailable" {
		t.Fatalf("error=%#v", envelope.Error)
	}
}

func TestBlackBoxRestartReplacesForegroundWithAuto(t *testing.T) {
	bin := commsBinary(t)
	env, _, socket := isolatedProcEnv(t)
	t.Cleanup(func() { shutdownIsolated(t, socket) })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, "serve")
	cmd.Env = env
	var serveOut, serveErr bytes.Buffer
	cmd.Stdout = &serveOut
	cmd.Stderr = &serveErr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()

	deadline := time.Now().Add(5 * time.Second)
	var before statusReport
	for {
		stdout, stderr, code, err := execBin(bin, env, "--json", "status")
		if err == nil && code == 0 {
			report := decodeStatus(t, []byte(stdout))
			if report.Running && report.LaunchMode == "foreground" {
				before = report
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("foreground serve not ready stderr=%s stdout wait last=%s/%s", serveErr.String(), stdout, stderr)
		}
		time.Sleep(20 * time.Millisecond)
	}

	stdout, stderr, code, err := execBin(bin, env, "--json", "restart")
	if err != nil {
		t.Fatal(err)
	}
	if code != 0 {
		t.Fatalf("restart code=%d stderr=%s stdout=%s serveErr=%s", code, stderr, stdout, serveErr.String())
	}
	after := decodeStatus(t, []byte(stdout))
	if !after.Running || after.LaunchMode != "auto" {
		t.Fatalf("after=%#v", after)
	}
	if after.PID == 0 || after.PID == before.PID {
		t.Fatalf("pid did not change: before=%#v after=%#v", before, after)
	}
	if after.ServerInstanceID == "" || after.ServerInstanceID == before.ServerInstanceID {
		t.Fatalf("instance did not change: before=%#v after=%#v", before, after)
	}

	select {
	case waitErr := <-exited:
		if waitErr != nil && !errors.Is(waitErr, context.Canceled) {
			t.Fatalf("foreground serve: %v stderr=%s", waitErr, serveErr.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("foreground serve did not exit after restart")
	}
}
