package service

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
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/marcus/comms/internal/app"
	"github.com/marcus/comms/internal/help"
	"github.com/marcus/comms/internal/httpapi"
	"github.com/marcus/comms/internal/store"
	"github.com/marcus/comms/pkg/buildinfo"
)

type testServer struct {
	socket string
	db     string
	hello  help.Handshake
	client *httpapi.Client
	cancel context.CancelFunc
	wait   func() error
}

func startTestServer(t *testing.T, cfg Config) *testServer {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "comms-test-")
	if err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(dir, "comms.sock")
	db := filepath.Join(dir, "comms.db")
	cfg.SocketPath = socket
	cfg.DatabasePath = db
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, cfg)
	}()
	var once sync.Once
	var runErr error
	wait := func() error {
		once.Do(func() { runErr = <-done })
		return runErr
	}
	t.Cleanup(func() {
		cancel()
		if err := wait(); err != nil {
			t.Errorf("service: %v", err)
		}
		_ = os.RemoveAll(dir)
	})
	hello := waitReady(t, socket)
	return &testServer{
		socket: socket,
		db:     db,
		hello:  hello,
		client: httpapi.NewUnixClient(socket, ""),
		cancel: cancel,
		wait:   wait,
	}
}

func waitReady(t *testing.T, socket string) help.Handshake {
	t.Helper()
	client := httpapi.NewUnixClient(socket, "")
	deadline := time.Now().Add(5 * time.Second)
	var last error
	for time.Now().Before(deadline) {
		var hs help.Handshake
		err := client.Do(context.Background(), http.MethodGet, "/v1/hello", nil, nil, &hs)
		if err == nil && hs.ServerInstanceID != "" {
			return hs
		}
		last = err
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("service not ready: %v", last)
	return help.Handshake{}
}

func unixJSON(t *testing.T, socket, method, path string, body any) (int, []byte) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(raw)
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", socket)
	}}
	req, err := http.NewRequest(method, "http://comms"+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := (&http.Client{Transport: transport, Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, data
}

func TestHandshakeIncludesLifecycleFields(t *testing.T) {
	srv := startTestServer(t, Config{})
	hs := srv.hello
	if !strings.HasPrefix(hs.ServerInstanceID, "srv_") {
		t.Fatalf("server_instance_id=%q", hs.ServerInstanceID)
	}
	if hs.PID != os.Getpid() {
		t.Fatalf("pid=%d want %d", hs.PID, os.Getpid())
	}
	if hs.LaunchMode != string(LaunchModeForeground) {
		t.Fatalf("launch_mode=%q", hs.LaunchMode)
	}
	if hs.SocketPath != srv.socket || hs.DatabasePath != srv.db {
		t.Fatalf("paths socket=%q db=%q", hs.SocketPath, hs.DatabasePath)
	}
	if hs.Commit != buildinfo.Commit || hs.ServerVersion != buildinfo.Version {
		t.Fatalf("build identity=%#v", hs)
	}
	if hs.StoreID == "" || hs.ProtocolVersion != app.ProtocolVersion || hs.SchemaVersion != 1 {
		t.Fatalf("store handshake=%#v", hs)
	}
	if time.Since(hs.StartedAt) > time.Minute || hs.StartedAt.After(time.Now().UTC().Add(time.Second)) {
		t.Fatalf("started_at=%v", hs.StartedAt)
	}
	foundShutdown := false
	for _, capability := range hs.Capabilities {
		if capability == "service.shutdown" {
			foundShutdown = true
			break
		}
	}
	if !foundShutdown {
		t.Fatalf("capabilities omit service.shutdown: %v", hs.Capabilities)
	}
}

func TestLaunchModeReflectedInHandshake(t *testing.T) {
	for _, mode := range []LaunchMode{LaunchModeForeground, LaunchModeAuto, LaunchModeSupervised} {
		t.Run(string(mode), func(t *testing.T) {
			srv := startTestServer(t, Config{LaunchMode: mode})
			if srv.hello.LaunchMode != string(mode) {
				t.Fatalf("launch_mode=%q", srv.hello.LaunchMode)
			}
		})
	}
}

func TestMatchingShutdownExitsAndReleasesOwnerLock(t *testing.T) {
	srv := startTestServer(t, Config{})
	status, body := unixJSON(t, srv.socket, http.MethodPost, "/v1/admin/shutdown", map[string]string{"server_instance_id": srv.hello.ServerInstanceID})
	if status != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", status, body)
	}
	var envelope struct {
		Schema string                `json:"schema"`
		Data   help.ShutdownAccepted `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Schema != help.ResponseSchema || !envelope.Data.Accepted {
		t.Fatalf("envelope=%#v", envelope)
	}
	if err := srv.wait(); err != nil {
		t.Fatalf("service exit: %v", err)
	}
	if _, err := os.Lstat(srv.socket); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket still present: %v", err)
	}
	reopened, err := store.Open(context.Background(), store.Options{Path: srv.db})
	if err != nil {
		t.Fatalf("owner lock not released: %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestStaleShutdownIsRefused(t *testing.T) {
	srv := startTestServer(t, Config{})
	status, body := unixJSON(t, srv.socket, http.MethodPost, "/v1/admin/shutdown", map[string]string{"server_instance_id": "srv_staleinstance0000000000001"})
	if status != http.StatusConflict {
		t.Fatalf("status=%d body=%s", status, body)
	}
	var envelope httpapi.ErrorEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Error.Code != "server_instance_changed" {
		t.Fatalf("error=%#v", envelope.Error)
	}
	var hs help.Handshake
	if err := srv.client.Do(context.Background(), http.MethodGet, "/v1/hello", nil, nil, &hs); err != nil {
		t.Fatal(err)
	}
	if hs.ServerInstanceID != srv.hello.ServerInstanceID {
		t.Fatalf("instance changed after stale shutdown: %q -> %q", srv.hello.ServerInstanceID, hs.ServerInstanceID)
	}
}

func TestShutdownResponseIsReadableBeforeProcessExits(t *testing.T) {
	srv := startTestServer(t, Config{})
	status, body := unixJSON(t, srv.socket, http.MethodPost, "/v1/admin/shutdown", map[string]string{"server_instance_id": srv.hello.ServerInstanceID})
	if status != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", status, body)
	}
	var envelope struct {
		Schema string                `json:"schema"`
		Data   help.ShutdownAccepted `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatal(err)
	}
	if !envelope.Data.Accepted || envelope.Data.ServerInstanceID != srv.hello.ServerInstanceID {
		t.Fatalf("envelope=%#v", envelope)
	}
	if err := srv.wait(); err != nil {
		t.Fatalf("drain: %v", err)
	}
}

func TestShutdownDrainsInFlightAndQueuedWrites(t *testing.T) {
	srv := startTestServer(t, Config{WriterQueue: 8})
	var joined app.JoinResponse
	if err := srv.client.Do(context.Background(), http.MethodPost, "/v1/agents/join", nil, map[string]any{"handle": "writer", "client_id": "c1", "request_id": "join"}, &joined); err != nil {
		t.Fatal(err)
	}
	author := httpapi.NewUnixClient(srv.socket, string(joined.Agent.ID))
	if err := author.Do(context.Background(), http.MethodPost, "/v1/topics", nil, map[string]any{"name": "drain", "client_id": "c1", "request_id": "topic"}, &struct{}{}); err != nil {
		t.Fatal(err)
	}
	if err := author.Do(context.Background(), http.MethodPut, "/v1/topics/drain/subscription", nil, map[string]any{"client_id": "c1", "request_id": "follow"}, &struct{}{}); err != nil {
		t.Fatal(err)
	}

	body := strings.Repeat("x", 64<<10)
	results := make(chan error, 12)
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results <- author.Do(context.Background(), http.MethodPost, "/v1/messages", nil, map[string]any{
				"topic":      "drain",
				"title":      "queued",
				"body":       body,
				"client_id":  "c1",
				"request_id": "pub-" + strconv.Itoa(i),
			}, &struct{}{})
		}(i)
	}
	status, raw := unixJSON(t, srv.socket, http.MethodPost, "/v1/admin/shutdown", map[string]string{"server_instance_id": srv.hello.ServerInstanceID})
	if status != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", status, raw)
	}
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil && !errors.Is(err, app.ErrClosed) && !errors.Is(err, app.ErrUnavailable) && !errors.Is(err, app.ErrOverloaded) && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("publish error=%v", err)
		}
	}
	if err := srv.wait(); err != nil {
		t.Fatalf("service exit: %v", err)
	}
	reopened, err := store.Open(context.Background(), store.Options{Path: srv.db})
	if err != nil {
		t.Fatalf("owner lock not released: %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
}
