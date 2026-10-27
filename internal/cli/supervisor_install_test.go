package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/marcus/comms/internal/service"
)

// Force the exact first-supervised-start ordering: an old auto daemon is
// stopped, a retry runs while launchd is held back, then launchd starts.
// There is deliberately no marker from a successful supervised launch.
func TestInstalledSupervisorRestartGapNeverAutoStarts(t *testing.T) {
	iso := newIsolatedRoots(t)
	start := inProcessAutoStart(t)
	run := func(args []string, spawn func(context.Context, DaemonSpec) (DaemonHandle, error)) (int, string) {
		t.Helper()
		var out, err bytes.Buffer
		code := Run(Env{Args: append([]string{"--socket", iso.socket, "--json"}, args...), Stdout: &out, Stderr: &err, Getenv: iso.getenv(nil), StartDaemon: spawn})
		return code, out.String() + err.String()
	}
	if code, output := run([]string{"observe", "--head"}, start); code != 0 {
		t.Fatalf("old auto start: %d %s", code, output)
	}
	_ = installSupervisorJob(t, iso)
	if code, output := run([]string{"stop"}, start); code != 0 {
		t.Fatalf("stop: %d %s", code, output)
	}
	// Simulate the upgrade path where an older stop/client never wrote a marker.
	if err := os.Remove(iso.socket + ".supervised"); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	spawn := &spawnRecorder{start: start}
	code, output := run([]string{"--timeout", "40ms", "observe", "--wait"}, spawn.Start)
	if len(spawn.calls()) != 0 {
		// Demonstrate the observed consequence, not only the spawn decision.
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		err := service.Run(ctx, service.Config{DatabasePath: filepath.Join(iso.state, "comms.db"), SocketPath: iso.socket, LaunchMode: service.LaunchModeSupervised})
		t.Fatalf("retrying client auto-started before launchd: client exit=%d output=%s; supervised launch=%v", code, output, err)
	}
	if code != 5 {
		t.Fatalf("supervisor gap must stay unavailable: %d %s", code, output)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- service.Run(ctx, service.Config{DatabasePath: filepath.Join(iso.state, "comms.db"), SocketPath: iso.socket, LaunchMode: service.LaunchModeSupervised})
	}()
	t.Cleanup(func() { cancel(); <-done })
	waitForHello(t, iso.socket)
	if code, output := run([]string{"observe", "--head"}, spawn.Start); code != 0 {
		t.Fatalf("recovery: %d %s", code, output)
	}
	if len(spawn.calls()) != 0 {
		t.Fatal("client spawned during supervised recovery")
	}
}

func installSupervisorJob(t *testing.T, iso isolatedRoots) string {
	t.Helper()
	plist := filepath.Join(iso.root, "Library", "LaunchAgents", "homebrew.mxcl.comms.plist")
	if err := os.MkdirAll(filepath.Dir(plist), 0o700); err != nil {
		t.Fatal(err)
	}
	content := fmt.Sprintf(`<plist version="1.0"><dict><key>Label</key><string>homebrew.mxcl.comms</string><key>ProgramArguments</key><array><string>/opt/homebrew/opt/comms/bin/comms</string><string>serve</string><string>--supervised</string><string>--socket</string><string>%s</string><string>--db</string><string>%s</string></array></dict></plist>`, iso.socket, filepath.Join(iso.state, "comms.db"))
	if err := os.WriteFile(plist, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return plist
}

func TestUpgradeRemembersSupervisorBeforeStoppingAutoOwner(t *testing.T) {
	iso := newIsolatedRoots(t)
	plist := installSupervisorJob(t, iso)
	old := testHello("v1.8.0", "old", "auto", "srv_oldauto")
	old.SchemaVersion = 4
	server := startScriptedServer(t, iso.socket, old)
	observed := make(chan bool, 1)
	server.mu.Lock()
	server.beforeShutdown = func() {
		managed, _ := service.SupervisedSocket(iso.socket)
		observed <- managed
		// launchd's restart removes the plist after the client has decided
		// to replace the old auto owner, but before that owner releases.
		if err := os.Remove(plist); err != nil {
			t.Error(err)
		}
	}
	server.mu.Unlock()
	spawn := &spawnRecorder{}
	var out, err bytes.Buffer
	code := Run(Env{Args: []string{"--socket", iso.socket, "--json", "--timeout", "80ms", "observe", "--head"}, Stdout: &out, Stderr: &err, Getenv: iso.getenv(nil), StartDaemon: spawn.Start, Version: "v1.9.0"})
	select {
	case managed := <-observed:
		if !managed {
			t.Error("supervisor intent was missing before shutdown")
		}
	default:
		t.Fatal("old auto owner was not stopped")
	}
	if len(spawn.calls()) != 0 {
		t.Error("client attempted auto-start after supervisor job file disappeared")
	}
	if code != 5 || !strings.Contains(err.String(), `"phase":"readiness"`) {
		t.Fatalf("exit=%d %s %s", code, out.String(), err.String())
	}
}
