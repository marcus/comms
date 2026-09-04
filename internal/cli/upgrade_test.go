package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/marcus/comms/internal/app"
	"github.com/marcus/comms/internal/help"
	"github.com/marcus/comms/internal/httpapi"
)

func TestParseStableSemver(t *testing.T) {
	tests := []struct {
		in   string
		ok   bool
		want semverTriple
	}{
		{in: "v1.2.3", ok: true, want: semverTriple{1, 2, 3}},
		{in: "v0.0.1", ok: true, want: semverTriple{0, 0, 1}},
		{in: "v1.10.0", ok: true, want: semverTriple{1, 10, 0}},
		{in: "dev", ok: false},
		{in: "v1.2.3-dirty", ok: false},
		{in: "v1.0.0-6-gd0371ba", ok: false},
		{in: "v1.0.0-rc.1", ok: false},
		{in: "1.0.0", ok: false},
		{in: "v1.0", ok: false},
		{in: "", ok: false},
	}
	for _, tt := range tests {
		got, ok := parseStableSemver(tt.in)
		if ok != tt.ok {
			t.Errorf("parseStableSemver(%q) ok=%v want %v", tt.in, ok, tt.ok)
		}
		if ok && got != tt.want {
			t.Errorf("parseStableSemver(%q)=%v want %v", tt.in, got, tt.want)
		}
	}
	if !isNewerStable("v1.2.0", "v1.1.0") {
		t.Fatal("v1.2.0 should be newer than v1.1.0")
	}
	if isNewerStable("v1.1.0", "v1.2.0") {
		t.Fatal("older client must not replace")
	}
	if isNewerStable("v1.2.0", "v1.2.0") {
		t.Fatal("equal versions are not newer")
	}
	if isNewerStable("v1.2.0", "v1.10.0") {
		t.Fatal("minor 2 is not newer than 10")
	}
	if isNewerStable("dev", "v1.0.0") || isNewerStable("v1.2.0", "v1.0.0-dirty") {
		t.Fatal("non-stable versions must not compare as newer")
	}
}

func TestReplacementDecision(t *testing.T) {
	decision := func(client, server, mode, instance string, noAuto bool) replaceAction {
		r := &runner{
			env: Env{Version: client, Getenv: func(string) string { return "" }},
			g:   globals{noAutoStart: noAuto},
		}
		return r.replacementDecision(help.Handshake{
			Handshake:        app.Handshake{ServerVersion: server},
			ServerInstanceID: instance,
			LaunchMode:       mode,
		})
	}
	if got := decision("v1.2.0", "v1.1.0", "auto", "srv_old", false); got != replaceAuto {
		t.Fatalf("older auto=%s", got)
	}
	if got := decision("v1.1.0", "v1.2.0", "auto", "srv_new", false); got != replaceNone {
		t.Fatalf("newer server=%s", got)
	}
	if got := decision("v1.2.0", "v1.2.0", "auto", "srv_same", false); got != replaceNone {
		t.Fatalf("equal=%s", got)
	}
	if got := decision("dev", "v1.1.0", "auto", "srv_old", false); got != replaceNone {
		t.Fatalf("dev client=%s", got)
	}
	if got := decision("v1.2.0-dirty", "v1.1.0", "auto", "srv_old", false); got != replaceNone {
		t.Fatalf("dirty client=%s", got)
	}
	if got := decision("v1.2.0", "v1.0.0-6-gd0371ba", "auto", "srv_old", false); got != replaceNone {
		t.Fatalf("git-describe server=%s", got)
	}
	if got := decision("v1.2.0", "v1.1.0", "foreground", "srv_fg", false); got != replaceForeground {
		t.Fatalf("foreground=%s", got)
	}
	if got := decision("v1.2.0", "v1.1.0", "supervised", "srv_sup", false); got != replaceSupervised {
		t.Fatalf("supervised=%s", got)
	}
	if got := decision("v1.2.0", "v1.0.0", "", "", false); got != replaceLegacy {
		t.Fatalf("legacy=%s", got)
	}
	if got := decision("v1.2.0", "v1.1.0", "auto", "srv_old", true); got != replaceNone {
		t.Fatalf("no-auto-start=%s", got)
	}
}

func TestRestartDecisionReplacesAutoAndForeground(t *testing.T) {
	if got := restartDecision(help.Handshake{ServerInstanceID: "srv_auto", LaunchMode: "auto"}); got != replaceAuto {
		t.Fatalf("auto=%s", got)
	}
	if got := restartDecision(help.Handshake{ServerInstanceID: "srv_fg", LaunchMode: "foreground"}); got != replaceAuto {
		t.Fatalf("foreground=%s", got)
	}
	if got := restartDecision(help.Handshake{ServerInstanceID: "srv_sup", LaunchMode: "supervised"}); got != replaceSupervised {
		t.Fatalf("supervised=%s", got)
	}
	if got := restartDecision(help.Handshake{}); got != replaceLegacy {
		t.Fatalf("legacy=%s", got)
	}
}

func TestOwnerLockReleased(t *testing.T) {
	dir := shortTempDir(t)
	path := filepath.Join(dir, "comms.db.lock")
	released, err := ownerLockReleased(path)
	if err != nil || !released {
		t.Fatalf("missing lock: released=%v err=%v", released, err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	released, err = ownerLockReleased(path)
	if err != nil || released {
		t.Fatalf("held lock: released=%v err=%v", released, err)
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	released, err = ownerLockReleased(path)
	if err != nil || !released {
		t.Fatalf("unlocked: released=%v err=%v", released, err)
	}
}

func TestReplaceOlderAutoDoesNotSendOperationToOldInstance(t *testing.T) {
	iso := newIsolatedRoots(t)
	oldHello := testHello("v1.1.0", "oldold1", "auto", "srv_oldinstance0000000000001")
	old := startScriptedServer(t, iso.socket, oldHello)
	newHello := testHello("v1.2.0", "newnew1", "auto", "srv_newinstance0000000000001")
	var spawned atomic.Int32
	var newSrv *scriptedServer
	var stdout, stderr bytes.Buffer
	code := Run(Env{
		Args:    []string{"--socket", iso.socket, "--json", "join", "alice"},
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
	if old.count("POST", "/v1/agents/join") != 0 {
		t.Fatalf("old instance received join: %v", old.requests())
	}
	if old.count("POST", "/v1/admin/shutdown") != 1 {
		t.Fatalf("old shutdowns=%v", old.shutdownIDs())
	}
	if newSrv == nil || newSrv.count("POST", "/v1/agents/join") != 1 {
		t.Fatalf("new join missing: %#v", newSrv)
	}
}

func TestNewerOrEqualOrUnstableServerIsNotReplaced(t *testing.T) {
	tests := []struct {
		name           string
		client, server string
		clientCommit   string
		serverCommit   string
		mode, instance string
	}{
		{name: "newer-server", client: "v1.1.0", server: "v1.2.0", clientCommit: "oldold1", serverCommit: "newnew1", mode: "auto", instance: "srv_new"},
		{name: "equal", client: "v1.2.0", server: "v1.2.0", clientCommit: "newnew1", serverCommit: "newnew1", mode: "auto", instance: "srv_same"},
		{name: "dev-client", client: "dev", server: "v1.1.0", clientCommit: "unknown", serverCommit: "oldold1", mode: "auto", instance: "srv_old"},
		{name: "dirty-client", client: "v1.2.0-dirty", server: "v1.1.0", clientCommit: "newnew1", serverCommit: "oldold1", mode: "auto", instance: "srv_old"},
		{name: "dirty-server", client: "v1.2.0", server: "v1.1.0-dirty", clientCommit: "newnew1", serverCommit: "oldold1", mode: "auto", instance: "srv_old"},
		{name: "git-describe", client: "v1.2.0", server: "v1.0.0-6-gd0371ba", clientCommit: "newnew1", serverCommit: "d0371ba", mode: "auto", instance: "srv_old"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			iso := newIsolatedRoots(t)
			srv := startScriptedServer(t, iso.socket, testHello(tt.server, tt.serverCommit, tt.mode, tt.instance))
			var stdout, stderr bytes.Buffer
			code := Run(Env{
				Args:    []string{"--socket", iso.socket, "--json", "join", "alice"},
				Stdout:  &stdout,
				Stderr:  &stderr,
				Getenv:  iso.getenv(nil),
				Version: tt.client,
				Commit:  tt.clientCommit,
				StartDaemon: func(context.Context, DaemonSpec) (DaemonHandle, error) {
					return nil, errors.New("should not spawn")
				},
			})
			if code != 0 {
				t.Fatalf("code=%d stderr=%s", code, stderr.String())
			}
			if n := srv.count("POST", "/v1/admin/shutdown"); n != 0 {
				t.Fatalf("shutdowns=%d reqs=%v", n, srv.requests())
			}
			if srv.count("POST", "/v1/agents/join") != 1 {
				t.Fatalf("join not sent to running server: %v", srv.requests())
			}
		})
	}
}

func TestForegroundRestartRequired(t *testing.T) {
	iso := newIsolatedRoots(t)
	srv := startScriptedServer(t, iso.socket, testHello("v1.1.0", "oldold1", "foreground", "srv_fg"))
	var stdout, stderr bytes.Buffer
	code := Run(Env{
		Args:    []string{"--socket", iso.socket, "--json", "join", "alice"},
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
	if envelope.Error.Details["launch_mode"] != "foreground" {
		t.Fatalf("details=%v", envelope.Error.Details)
	}
	if !strings.Contains(envelope.Error.Message, "stop the foreground") {
		t.Fatalf("message=%q", envelope.Error.Message)
	}
	if srv.count("POST", "/v1/admin/shutdown") != 0 {
		t.Fatalf("shutdown called: %v", srv.requests())
	}
	if srv.count("POST", "/v1/agents/join") != 0 {
		t.Fatalf("join sent: %v", srv.requests())
	}
}

func TestSupervisedRestartRequiredNamesBrew(t *testing.T) {
	iso := newIsolatedRoots(t)
	srv := startScriptedServer(t, iso.socket, testHello("v1.1.0", "oldold1", "supervised", "srv_sup"))
	var stdout, stderr bytes.Buffer
	code := Run(Env{
		Args:    []string{"--socket", iso.socket, "--json", "join", "alice"},
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
	if !strings.Contains(envelope.Error.Message, "brew services restart comms") {
		t.Fatalf("message=%q", envelope.Error.Message)
	}
	if srv.count("POST", "/v1/admin/shutdown") != 0 {
		t.Fatalf("shutdown called: %v", srv.requests())
	}
}

func TestLegacyRestartRequiredDoesNotUnlinkSocket(t *testing.T) {
	iso := newIsolatedRoots(t)
	hello := testHello("v1.0.0", "unknown", "", "")
	hello.ServerInstanceID = ""
	hello.LaunchMode = ""
	srv := startScriptedServer(t, iso.socket, hello)
	var stdout, stderr bytes.Buffer
	code := Run(Env{
		Args:    []string{"--socket", iso.socket, "--json", "join", "alice"},
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
	if envelope.Error.Code != "legacy_server_restart_required" {
		t.Fatalf("error=%#v", envelope.Error)
	}
	if !strings.Contains(envelope.Error.Message, "stop that foreground process once") {
		t.Fatalf("message=%q", envelope.Error.Message)
	}
	if srv.count("POST", "/v1/admin/shutdown") != 0 {
		t.Fatalf("shutdown called: %v", srv.requests())
	}
	if _, err := os.Stat(iso.socket); err != nil {
		t.Fatalf("socket unlinked: %v", err)
	}
}

func TestIncompatibleProtocolDoesNotReplace(t *testing.T) {
	iso := newIsolatedRoots(t)
	hello := testHello("v1.1.0", "oldold1", "auto", "srv_old")
	hello.ProtocolVersion = 99
	srv := startScriptedServer(t, iso.socket, hello)
	var stdout, stderr bytes.Buffer
	code := Run(Env{
		Args:    []string{"--socket", iso.socket, "--json", "join", "alice"},
		Stdout:  &stdout,
		Stderr:  &stderr,
		Getenv:  iso.getenv(nil),
		Version: "v1.2.0",
		Commit:  "newnew1",
		StartDaemon: func(context.Context, DaemonSpec) (DaemonHandle, error) {
			return nil, errors.New("should not spawn")
		},
	})
	if code != 4 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if srv.count("POST", "/v1/admin/shutdown") != 0 {
		t.Fatalf("shutdown called: %v", srv.requests())
	}
}

func TestNoAutoStartDoesNotReplace(t *testing.T) {
	iso := newIsolatedRoots(t)
	srv := startScriptedServer(t, iso.socket, testHello("v1.1.0", "oldold1", "auto", "srv_old"))
	var stdout, stderr bytes.Buffer
	code := Run(Env{
		Args:    []string{"--no-auto-start", "--socket", iso.socket, "--json", "join", "alice"},
		Stdout:  &stdout,
		Stderr:  &stderr,
		Getenv:  iso.getenv(nil),
		Version: "v1.2.0",
		Commit:  "newnew1",
		StartDaemon: func(context.Context, DaemonSpec) (DaemonHandle, error) {
			return nil, errors.New("should not spawn")
		},
	})
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if srv.count("POST", "/v1/admin/shutdown") != 0 {
		t.Fatalf("shutdown called: %v", srv.requests())
	}
	if srv.count("POST", "/v1/agents/join") != 1 {
		t.Fatalf("join not sent: %v", srv.requests())
	}
}

func TestStaleInstanceDuringShutdownRechecks(t *testing.T) {
	iso := newIsolatedRoots(t)
	srv := startScriptedServer(t, iso.socket, testHello("v1.1.0", "oldold1", "auto", "srv_old"))
	srv.setShutdownMode("conflict-and-switch", testHello("v1.2.0", "newnew1", "auto", "srv_new"))
	var spawned atomic.Int32
	var stdout, stderr bytes.Buffer
	code := Run(Env{
		Args:    []string{"--socket", iso.socket, "--json", "join", "alice"},
		Stdout:  &stdout,
		Stderr:  &stderr,
		Getenv:  iso.getenv(nil),
		Version: "v1.2.0",
		Commit:  "newnew1",
		StartDaemon: func(context.Context, DaemonSpec) (DaemonHandle, error) {
			spawned.Add(1)
			return nil, errors.New("should not spawn")
		},
	})
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	if spawned.Load() != 0 {
		t.Fatal("spawned after compatible server appeared")
	}
	if srv.count("POST", "/v1/agents/join") != 1 {
		t.Fatalf("join missing: %v", srv.requests())
	}
}

func TestConcurrentUpgradeOneReplacement(t *testing.T) {
	iso := newIsolatedRoots(t)
	old := startScriptedServer(t, iso.socket, testHello("v1.1.0", "oldold1", "auto", "srv_old"))
	newHello := testHello("v1.2.0", "newnew1", "auto", "srv_new")
	var spawned atomic.Int32
	var once sync.Once
	var newSrv *scriptedServer
	start := func(_ context.Context, spec DaemonSpec) (DaemonHandle, error) {
		spawned.Add(1)
		once.Do(func() {
			newSrv = startScriptedServer(t, spec.SocketPath, newHello)
		})
		return &stubDaemon{done: make(chan struct{})}, nil
	}
	var wg sync.WaitGroup
	codes := make([]int, 2)
	errText := make([]string, 2)
	for i, handle := range []string{"alice", "bob"} {
		wg.Add(1)
		go func(i int, handle string) {
			defer wg.Done()
			var stdout, stderr bytes.Buffer
			codes[i] = Run(Env{
				Args:        []string{"--socket", iso.socket, "--json", "--timeout", "10s", "join", handle, "--context", filepath.Join(iso.state, handle+".json")},
				Stdout:      &stdout,
				Stderr:      &stderr,
				Getenv:      iso.getenv(nil),
				Version:     "v1.2.0",
				Commit:      "newnew1",
				StartDaemon: start,
			})
			errText[i] = stderr.String()
		}(i, handle)
	}
	wg.Wait()
	for i, code := range codes {
		if code != 0 {
			t.Fatalf("join %d code=%d stderr=%s", i, code, errText[i])
		}
	}
	if spawned.Load() != 1 {
		t.Fatalf("spawned=%d", spawned.Load())
	}
	if old.count("POST", "/v1/agents/join") != 0 {
		t.Fatalf("old received join: %v", old.requests())
	}
	if newSrv == nil || newSrv.count("POST", "/v1/agents/join") != 2 {
		t.Fatalf("new joins=%v", newSrv)
	}
}

func TestTimeoutDuringReplacement(t *testing.T) {
	iso := newIsolatedRoots(t)
	srv := startScriptedServer(t, iso.socket, testHello("v1.1.0", "oldold1", "auto", "srv_old"))
	srv.setShutdownMode("ignore", help.Handshake{})
	var stdout, stderr bytes.Buffer
	code := Run(Env{
		Args:    []string{"--socket", iso.socket, "--json", "--timeout", "500ms", "join", "alice"},
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
	if envelope.Error.Code != "timeout" {
		t.Fatalf("error=%#v", envelope.Error)
	}
	if srv.count("POST", "/v1/agents/join") != 0 {
		t.Fatalf("join sent: %v", srv.requests())
	}
}

func TestIdempotentMutationRetriesOnceWithSameRequestID(t *testing.T) {
	iso := newIsolatedRoots(t)
	srv := startScriptedServer(t, iso.socket, testHello("v1.2.0", "newnew1", "auto", "srv_cur"))
	srv.dropNext("POST", "/v1/agents/join")
	var stdout, stderr bytes.Buffer
	code := Run(Env{
		Args:    []string{"--socket", iso.socket, "--json", "join", "alice"},
		Stdout:  &stdout,
		Stderr:  &stderr,
		Getenv:  iso.getenv(nil),
		Version: "v1.2.0",
		Commit:  "newnew1",
		StartDaemon: func(context.Context, DaemonSpec) (DaemonHandle, error) {
			return nil, errors.New("should not spawn")
		},
	})
	if code != 0 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	joins := srv.bodies("POST", "/v1/agents/join")
	if len(joins) != 2 {
		t.Fatalf("joins=%d reqs=%v", len(joins), srv.requests())
	}
	id1, _ := joins[0]["request_id"].(string)
	id2, _ := joins[1]["request_id"].(string)
	if id1 == "" || id1 != id2 {
		t.Fatalf("request_id %q vs %q", id1, id2)
	}
}

func TestExportDoesNotRetry(t *testing.T) {
	iso := newIsolatedRoots(t)
	srv := startScriptedServer(t, iso.socket, testHello("v1.2.0", "newnew1", "auto", "srv_cur"))
	srv.dropNext("GET", "/v1/export")
	var stdout, stderr bytes.Buffer
	code := Run(Env{
		Args:    []string{"--socket", iso.socket, "--json", "export"},
		Stdout:  &stdout,
		Stderr:  &stderr,
		Getenv:  iso.getenv(nil),
		Version: "v1.2.0",
		Commit:  "newnew1",
		StartDaemon: func(context.Context, DaemonSpec) (DaemonHandle, error) {
			return nil, errors.New("should not spawn")
		},
	})
	if code == 0 {
		t.Fatalf("export succeeded after drop stdout=%s", stdout.String())
	}
	if srv.count("GET", "/v1/export") != 1 {
		t.Fatalf("export retried: %v", srv.requests())
	}
}

func TestRestartRequiredHumanMessageOmitsServeHint(t *testing.T) {
	iso := newIsolatedRoots(t)
	_ = startScriptedServer(t, iso.socket, testHello("v1.1.0", "oldold1", "foreground", "srv_fg"))
	var stderr bytes.Buffer
	code := Run(Env{
		Args:    []string{"--socket", iso.socket, "join", "alice"},
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
	if strings.Contains(stderr.String(), "Start the service with 'comms serve'") {
		t.Fatalf("stderr=%s", stderr.String())
	}
	if !strings.Contains(stderr.String(), "stop the foreground") {
		t.Fatalf("stderr=%s", stderr.String())
	}
}

func testHello(version, commit, mode, instance string) help.Handshake {
	return help.Handshake{
		Handshake: app.Handshake{
			StoreID:         "store_test",
			ProtocolVersion: app.ProtocolVersion,
			SchemaVersion:   app.SchemaVersion,
			ServerVersion:   version,
		},
		ServerInstanceID: instance,
		PID:              42,
		StartedAt:        time.Unix(1700000000, 0).UTC(),
		LaunchMode:       mode,
		Commit:           commit,
	}
}

type scriptedRequest struct {
	Method string
	Path   string
	Body   map[string]any
}

type scriptedServer struct {
	mu           sync.Mutex
	hello        help.Handshake
	switchTo     help.Handshake
	shutdownMode string
	reqs         []scriptedRequest
	shutdowns    []string
	dropMethod   string
	dropPath     string
	dropsLeft    int
	server       *http.Server
	closeOnce    sync.Once
}

func startScriptedServer(t *testing.T, socket string, hello help.Handshake) *scriptedServer {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(socket), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(socket); err == nil {
		_ = os.Remove(socket)
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(socket, 0o600); err != nil {
		_ = listener.Close()
		t.Fatal(err)
	}
	s := &scriptedServer{hello: hello}
	s.server = &http.Server{Handler: http.HandlerFunc(s.serve)}
	go func() { _ = s.server.Serve(listener) }()
	t.Cleanup(s.close)
	return s
}

func (s *scriptedServer) close() {
	s.closeOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = s.server.Shutdown(ctx)
	})
}

func (s *scriptedServer) setShutdownMode(mode string, next help.Handshake) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.shutdownMode = mode
	s.switchTo = next
}

func (s *scriptedServer) dropNext(method, path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dropMethod = method
	s.dropPath = path
	s.dropsLeft = 1
}

func (s *scriptedServer) serve(w http.ResponseWriter, r *http.Request) {
	body := map[string]any{}
	raw, _ := io.ReadAll(r.Body)
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &body)
	}
	s.mu.Lock()
	s.reqs = append(s.reqs, scriptedRequest{Method: r.Method, Path: r.URL.Path, Body: body})
	hello := s.hello
	mode := s.shutdownMode
	drop := s.dropsLeft > 0 && r.Method == s.dropMethod && r.URL.Path == s.dropPath
	if drop {
		s.dropsLeft--
	}
	s.mu.Unlock()
	if drop {
		hj, ok := w.(http.Hijacker)
		if !ok {
			http.Error(w, "hijack unsupported", http.StatusInternalServerError)
			return
		}
		conn, _, err := hj.Hijack()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_ = conn.Close()
		return
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/v1/hello":
		writeData(w, http.StatusOK, hello)
	case r.Method == http.MethodPost && r.URL.Path == "/v1/admin/shutdown":
		id, _ := body["server_instance_id"].(string)
		s.mu.Lock()
		s.shutdowns = append(s.shutdowns, id)
		s.mu.Unlock()
		switch mode {
		case "conflict-and-switch":
			s.mu.Lock()
			s.hello = s.switchTo
			s.mu.Unlock()
			writeErr(w, http.StatusConflict, "server_instance_changed", "server instance changed")
		case "ignore":
			writeAccepted(w, id)
		default:
			writeAccepted(w, id)
			go s.close()
		}
	case r.Method == http.MethodPost && r.URL.Path == "/v1/agents/join":
		handle, _ := body["handle"].(string)
		if handle == "" {
			handle = "alice"
		}
		now := time.Unix(1700000000, 0).UTC()
		writeData(w, http.StatusOK, map[string]any{
			"agent": map[string]any{
				"id":           "agt_aaaaaaaaaaaaaaaaaaaaaaaaaa",
				"handle":       handle,
				"created_at":   now,
				"updated_at":   now,
				"last_seen_at": now,
			},
			"rejoined": false,
		})
	case r.Method == http.MethodGet && r.URL.Path == "/v1/agents":
		writeData(w, http.StatusOK, map[string]any{"items": []any{}})
	case r.Method == http.MethodGet && r.URL.Path == "/v1/export":
		w.Header().Set("Content-Type", "application/x-ndjson")
		_, _ = io.WriteString(w, "{}\n")
	default:
		writeErr(w, http.StatusNotFound, "not_found", "route not found")
	}
}

func (s *scriptedServer) requests() []scriptedRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]scriptedRequest, len(s.reqs))
	copy(out, s.reqs)
	return out
}

func (s *scriptedServer) shutdownIDs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.shutdowns))
	copy(out, s.shutdowns)
	return out
}

func (s *scriptedServer) count(method, path string) int {
	n := 0
	for _, req := range s.requests() {
		if req.Method == method && req.Path == path {
			n++
		}
	}
	return n
}

func (s *scriptedServer) bodies(method, path string) []map[string]any {
	var out []map[string]any
	for _, req := range s.requests() {
		if req.Method == method && req.Path == path {
			out = append(out, req.Body)
		}
	}
	return out
}

func writeData(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"schema": help.ResponseSchema, "data": data})
}

func writeAccepted(w http.ResponseWriter, id string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"schema": help.ResponseSchema,
		"data":   help.ShutdownAccepted{Accepted: true, ServerInstanceID: id},
	})
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
}

func writeErr(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(httpapi.ErrorEnvelope{Error: httpapi.ErrorBody{Code: code, Message: message, Details: map[string]any{}}})
}

func decodeError(t *testing.T, raw []byte) httpapi.ErrorEnvelope {
	t.Helper()
	var envelope httpapi.ErrorEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatalf("decode error %q: %v", raw, err)
	}
	return envelope
}
