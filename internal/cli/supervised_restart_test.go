package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/marcus/comms/internal/app"
	"github.com/marcus/comms/internal/service"
)

func TestLosingAutoChildAcceptsSupervisedWinner(t *testing.T) {
	iso := newIsolatedRoots(t)
	var out, stderr bytes.Buffer
	var spawned atomic.Int32
	done := make(chan struct{})
	close(done)
	code := Run(Env{Args: []string{"--json", "--socket", iso.socket, "--timeout", "1s", "join", "scout"}, Getenv: iso.getenv(nil), Stdout: &out, Stderr: &stderr,
		Version: "v1.8.0", Commit: "client-build",
		StartDaemon: func(context.Context, DaemonSpec) (DaemonHandle, error) {
			spawned.Add(1)
			// The supervisor wins while the competing child reports exit 5. This
			// ordering is forced: the child is already done before readiness runs.
			hello := testHello("v1.8.0", "homebrew", "supervised", "srv_supervised_winner")
			hello.Readiness = "ready"
			startScriptedServer(t, iso.socket, hello)
			return &stubDaemon{done: done, err: app.ErrUnavailable}, nil
		},
	})
	if code != 0 || stderr.Len() != 0 || out.Len() == 0 || spawned.Load() != 1 {
		t.Fatalf("code=%d spawned=%d out=%s err=%s", code, spawned.Load(), &out, &stderr)
	}
}

func TestReadinessWaitsThroughStartingAndStopping(t *testing.T) {
	for _, state := range []string{"starting", "stopping"} {
		t.Run(state, func(t *testing.T) {
			iso := newIsolatedRoots(t)
			listener, err := net.Listen("unix", iso.socket)
			if err != nil {
				t.Fatal(err)
			}
			var probes, operations atomic.Int32
			hello := testHello("v1.8.0", "homebrew", "supervised", "srv_ready_boundary")
			server := &http.Server{ReadHeaderTimeout: time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v1/hello" {
					response := hello
					if probes.Add(1) == 1 {
						response.Readiness = state
					} else {
						response.Readiness = "ready"
					}
					writeData(w, http.StatusOK, response)
					return
				}
				if probes.Load() < 2 {
					t.Error("operation sent to an unready owner")
				}
				operations.Add(1)
				writeData(w, http.StatusOK, map[string]any{"items": []any{}, "after": "cursor"})
			})}
			go func() { _ = server.Serve(listener) }()
			t.Cleanup(func() { _ = server.Close() })
			var out, stderr bytes.Buffer
			code := Run(Env{Args: []string{"--json", "--socket", iso.socket, "--timeout", "1s", "observe", "--head"}, Getenv: iso.getenv(nil), Stdout: &out, Stderr: &stderr, Version: "v1.8.0"})
			if code != 0 || probes.Load() < 2 || operations.Load() != 1 {
				t.Fatalf("code=%d probes=%d operations=%d out=%s err=%s", code, probes.Load(), operations.Load(), &out, &stderr)
			}
		})
	}
}

func TestStartingOwnerNeverSpawnsCompetitorAndIsBounded(t *testing.T) {
	for _, mode := range []string{"supervised-marker", "database-owner"} {
		t.Run(mode, func(t *testing.T) {
			iso := newIsolatedRoots(t)
			if mode == "supervised-marker" {
				ctx, cancel := context.WithCancel(context.Background())
				done := make(chan error, 1)
				go func() {
					done <- service.Run(ctx, service.Config{DatabasePath: filepath.Join(iso.state, "comms.db"), SocketPath: iso.socket, LaunchMode: service.LaunchModeSupervised})
				}()
				waitForHello(t, iso.socket)
				cancel()
				select {
				case err := <-done:
					if err != nil {
						t.Fatal(err)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("fixture did not stop")
				}
				managed, err := service.SupervisedSocket(iso.socket)
				if err != nil || !managed {
					t.Fatalf("missing persisted supervision: %v", err)
				}
			} else {
				file, err := os.OpenFile(filepath.Join(iso.state, "comms.db.lock"), os.O_CREATE|os.O_RDWR, 0o600)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = unix.Flock(int(file.Fd()), unix.LOCK_UN); _ = file.Close() }()
				if err = unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
					t.Fatal(err)
				}
			}
			var spawned atomic.Int32
			var out, stderr bytes.Buffer
			started := time.Now()
			code := Run(Env{Args: []string{"--json", "--socket", iso.socket, "--timeout", "30ms", "observe", "--head"}, Getenv: iso.getenv(nil), Stdout: &out, Stderr: &stderr,
				StartDaemon: func(context.Context, DaemonSpec) (DaemonHandle, error) {
					spawned.Add(1)
					return nil, errors.New("must not spawn")
				},
			})
			var envelope struct {
				Error struct{ Code, Message string }
			}
			if err := json.Unmarshal(stderr.Bytes(), &envelope); err != nil {
				t.Fatal(err)
			}
			if code != 5 || spawned.Load() != 0 || out.Len() != 0 || envelope.Error.Code != "unavailable" || time.Since(started) > time.Second {
				t.Fatalf("code=%d spawned=%d out=%s err=%s", code, spawned.Load(), &out, &stderr)
			}
		})
	}
}

func TestPermissionWrappedAsUnavailableIsNotRestartTransient(t *testing.T) {
	err := fmt.Errorf("%w: %w", app.ErrUnavailable, &net.OpError{Op: "dial", Net: "unix", Err: syscall.EACCES})
	if transientReadinessError(err) {
		t.Fatal("permission denial must not start or wait for a competitor")
	}
}
