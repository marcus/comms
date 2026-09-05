package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/marcus/comms/internal/app"
	"github.com/marcus/comms/internal/domain"
	"github.com/marcus/comms/internal/help"
	"github.com/marcus/comms/internal/store"
)

func TestEveryGeneratedHTTPRouteIsMounted(t *testing.T) {
	adapter, err := store.Open(context.Background(), store.Options{Path: filepath.Join(t.TempDir(), "comms.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = adapter.Close() })
	server := httptest.NewServer(NewHandler(app.NewService(adapter, domain.UTCClock{})))
	t.Cleanup(server.Close)

	for _, operation := range help.Operations() {
		if operation.HTTP == nil || operation.UnixOnly {
			continue
		}
		t.Run(operation.ID, func(t *testing.T) {
			path := operation.HTTP.Path
			path = strings.ReplaceAll(path, "{agent}", "missing-agent")
			path = strings.ReplaceAll(path, "{topic}", "missing-topic")
			path = strings.ReplaceAll(path, "{message}", "msg_aaaaaaaaaaaaaaaaaaaaaaaaaa")
			if operation.ID == "message.search" {
				path += "?query=test"
			}
			var body io.Reader
			if operation.HTTP.Method != http.MethodGet {
				body = bytes.NewBufferString("{}")
			}
			request, err := http.NewRequest(operation.HTTP.Method, server.URL+path, body)
			if err != nil {
				t.Fatal(err)
			}
			request.Header.Set(AgentHeader, "missing-agent")
			if body != nil {
				request.Header.Set("Content-Type", "application/json")
			}
			response, err := server.Client().Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = response.Body.Close() }()
			if contentType := response.Header.Get("Content-Type"); !strings.HasPrefix(contentType, "application/json") && !strings.HasPrefix(contentType, "application/x-ndjson") {
				raw, _ := io.ReadAll(response.Body)
				t.Fatalf("route was not served as a Comms response: status=%d content-type=%q body=%q", response.StatusCode, contentType, raw)
			}
		})
	}
}

func TestNetworkBoundaryRejectsUnknownAndMalformedInputs(t *testing.T) {
	handler := NewHandler(nil)
	tests := []struct{ name, method, target, body string }{
		{name: "unknown query", method: http.MethodGet, target: "/v1/capabilities?surprise=true"},
		{name: "OpenAPI unknown query", method: http.MethodGet, target: "/v1/openapi.json?surprise=true"},
		{name: "bad query type", method: http.MethodGet, target: "/v1/agents?limit=many"},
		{name: "unknown body", method: http.MethodPost, target: "/v1/agents/join", body: `{"surprise":true}`},
		{name: "path duplicate agent", method: http.MethodPatch, target: "/v1/agents/example", body: `{"agent":"other"}`},
		{name: "path duplicate topic", method: http.MethodPatch, target: "/v1/topics/example", body: `{"topic":"other"}`},
		{name: "multiple objects", method: http.MethodPost, target: "/v1/agents/join", body: `{} {}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, test.target, strings.NewReader(test.body))
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			var envelope ErrorEnvelope
			if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
				t.Fatal(err)
			}
			if envelope.Error.Code != "invalid_argument" {
				t.Fatalf("error=%#v", envelope.Error)
			}
		})
	}
}

func TestUnknownRoutesAndMethodsUseJSONErrors(t *testing.T) {
	handler := NewHandler(nil)
	for _, test := range []struct {
		name, method, target, code string
		status                     int
	}{
		{name: "route", method: http.MethodGet, target: "/v1/missing", status: http.StatusNotFound, code: "not_found"},
		{name: "method", method: http.MethodPost, target: "/v1/capabilities", status: http.StatusMethodNotAllowed, code: "method_not_allowed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, httptest.NewRequest(test.method, test.target, nil))
			if recorder.Code != test.status {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			var envelope ErrorEnvelope
			if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
				t.Fatal(err)
			}
			if envelope.Error.Code != test.code {
				t.Fatalf("error=%#v", envelope.Error)
			}
		})
	}
}

type stubLifecycle struct {
	status    LifecycleStatus
	err       error
	mu        sync.Mutex
	shutdowns []string
}

func (s *stubLifecycle) Status() LifecycleStatus { return s.status }

func (s *stubLifecycle) RequestShutdown(expectedID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.shutdowns = append(s.shutdowns, expectedID)
	return s.err
}

func (s *stubLifecycle) shutdownCalls() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.shutdowns...)
}

func testService(t *testing.T) *app.Service {
	t.Helper()
	adapter, err := store.Open(context.Background(), store.Options{Path: filepath.Join(t.TempDir(), "comms.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = adapter.Close() })
	return app.NewService(adapter, domain.UTCClock{})
}

func TestTCPHandlerDoesNotExposeShutdown(t *testing.T) {
	life := &stubLifecycle{status: LifecycleStatus{ServerInstanceID: "srv_live"}}
	handlers := []struct {
		name    string
		handler http.Handler
	}{
		{name: "default", handler: NewHandler(testService(t))},
		{name: "tcp", handler: NewTCPHandler(testService(t), life)},
	}
	for _, test := range handlers {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/v1/admin/shutdown", strings.NewReader(`{"server_instance_id":"srv_live"}`))
			request.Header.Set("Content-Type", "application/json")
			test.handler.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusNotFound {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			var envelope ErrorEnvelope
			if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
				t.Fatal(err)
			}
			if envelope.Error.Code != "not_found" {
				t.Fatalf("error=%#v", envelope.Error)
			}
			if calls := life.shutdownCalls(); len(calls) != 0 {
				t.Fatalf("shutdown invoked on TCP handler: %v", calls)
			}
		})
	}
}

func TestUnixHandlerHandshakeAndConditionalShutdown(t *testing.T) {
	started := time.Now().UTC().Add(-time.Minute)
	life := &stubLifecycle{status: LifecycleStatus{
		ServerInstanceID: "srv_liveinstance0000000000001",
		PID:              4242,
		StartedAt:        started,
		LaunchMode:       "foreground",
		Version:          "test-version",
		Commit:           "abc1234",
		SocketPath:       "/tmp/comms.sock",
		DatabasePath:     "/tmp/comms.db",
	}}
	handler := NewUnixHandler(testService(t), life)

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/hello", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("hello status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var hello struct {
		Schema string         `json:"schema"`
		Data   help.Handshake `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &hello); err != nil {
		t.Fatal(err)
	}
	if hello.Schema != help.ResponseSchema {
		t.Fatalf("schema=%q", hello.Schema)
	}
	if hello.Data.ServerInstanceID != life.status.ServerInstanceID || hello.Data.PID != 4242 || hello.Data.LaunchMode != "foreground" {
		t.Fatalf("handshake=%#v", hello.Data)
	}
	if hello.Data.Commit != "abc1234" || hello.Data.SocketPath != "/tmp/comms.sock" || hello.Data.DatabasePath != "/tmp/comms.db" {
		t.Fatalf("handshake paths=%#v", hello.Data)
	}
	if hello.Data.StoreID == "" || hello.Data.ProtocolVersion != app.ProtocolVersion {
		t.Fatalf("store handshake=%#v", hello.Data)
	}
	if !hello.Data.StartedAt.Equal(started) {
		t.Fatalf("started_at=%v", hello.Data.StartedAt)
	}

	for _, test := range []struct {
		name, body, code string
		status           int
	}{
		{name: "missing body", body: `{}`, status: http.StatusBadRequest, code: "invalid_argument"},
		{name: "invalid json", body: `{`, status: http.StatusBadRequest, code: "invalid_argument"},
		{name: "stale", body: `{"server_instance_id":"srv_stale"}`, status: http.StatusConflict, code: "server_instance_changed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/v1/admin/shutdown", strings.NewReader(test.body))
			req.Header.Set("Content-Type", "application/json")
			handler.ServeHTTP(rec, req)
			if rec.Code != test.status {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			var envelope ErrorEnvelope
			if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
				t.Fatal(err)
			}
			if envelope.Error.Code != test.code {
				t.Fatalf("error=%#v", envelope.Error)
			}
		})
	}
	if calls := life.shutdownCalls(); len(calls) != 0 {
		t.Fatalf("failed shutdowns still signaled: %v", calls)
	}

	accepted := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/admin/shutdown", strings.NewReader(`{"server_instance_id":"srv_liveinstance0000000000001"}`))
	req.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(accepted, req)
	if accepted.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", accepted.Code, accepted.Body.String())
	}
	var envelope struct {
		Schema string                `json:"schema"`
		Data   help.ShutdownAccepted `json:"data"`
	}
	if err := json.Unmarshal(accepted.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Schema != help.ResponseSchema || !envelope.Data.Accepted || envelope.Data.ServerInstanceID != life.status.ServerInstanceID {
		t.Fatalf("accepted=%#v", envelope)
	}
	if calls := life.shutdownCalls(); len(calls) != 1 || calls[0] != life.status.ServerInstanceID {
		t.Fatalf("shutdown calls=%v", calls)
	}
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

func TestExportDoesNotReplayDroppedConnection(t *testing.T) {
	socket := filepath.Join(shortTempDir(t), "comms.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	var exports atomicInt
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/export" {
			http.NotFound(w, r)
			return
		}
		exports.add(1)
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Error("hijack unsupported")
			return
		}
		conn, _, err := hj.Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		_ = conn.Close()
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })

	client := NewUnixClient(socket, "")
	err = client.Export(context.Background(), io.Discard)
	if err == nil {
		t.Fatal("expected export error")
	}
	if got := exports.get(); got != 1 {
		t.Fatalf("export requests=%d want 1 err=%v", got, err)
	}
}

type atomicInt struct {
	mu sync.Mutex
	n  int
}

func (a *atomicInt) add(n int) { a.mu.Lock(); a.n += n; a.mu.Unlock() }
func (a *atomicInt) get() int  { a.mu.Lock(); defer a.mu.Unlock(); return a.n }

func TestClientDoPreservesDialCause(t *testing.T) {
	client := NewUnixClient(filepath.Join(shortTempDir(t), "missing.sock"), "")
	err := client.Do(context.Background(), http.MethodGet, "/v1/hello", nil, nil, &map[string]any{})
	if err == nil {
		t.Fatal("expected dial error")
	}
	if !errors.Is(err, app.ErrUnavailable) {
		t.Fatalf("unavailable wrap missing: %v", err)
	}
	var op *net.OpError
	if !errors.As(err, &op) {
		t.Fatalf("net.OpError not inspectable: %v", err)
	}
	if !IsAutoStartableDial(err) {
		t.Fatalf("ENOENT should be auto-startable: %v", err)
	}
}

func TestIsAutoStartableDial(t *testing.T) {
	t.Run("ENOENT", func(t *testing.T) {
		client := NewUnixClient(filepath.Join(shortTempDir(t), "missing.sock"), "")
		err := client.Do(context.Background(), http.MethodGet, "/v1/hello", nil, nil, &map[string]any{})
		if !IsAutoStartableDial(err) {
			t.Fatalf("err=%v", err)
		}
	})
	t.Run("ECONNREFUSED", func(t *testing.T) {
		path := filepath.Join(shortTempDir(t), "stale.sock")
		addr, err := net.ResolveUnixAddr("unix", path)
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
		client := NewUnixClient(path, "")
		err = client.Do(context.Background(), http.MethodGet, "/v1/hello", nil, nil, &map[string]any{})
		if !IsAutoStartableDial(err) {
			t.Fatalf("stale socket err=%v", err)
		}
	})
	t.Run("permission", func(t *testing.T) {
		dir := shortTempDir(t)
		blocked := filepath.Join(dir, "blocked")
		if err := os.Mkdir(blocked, 0o700); err != nil {
			t.Fatal(err)
		}
		socket := filepath.Join(blocked, "comms.sock")
		if err := os.Chmod(blocked, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(blocked, 0o700) })
		client := NewUnixClient(socket, "")
		err := client.Do(context.Background(), http.MethodGet, "/v1/hello", nil, nil, &map[string]any{})
		if err == nil {
			t.Fatal("expected permission error")
		}
		if IsAutoStartableDial(err) {
			t.Fatalf("permission should not auto-start: %v", err)
		}
	})
	t.Run("canceled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		client := NewUnixClient(filepath.Join(shortTempDir(t), "missing.sock"), "")
		err := client.Do(ctx, http.MethodGet, "/v1/hello", nil, nil, &map[string]any{})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err=%v", err)
		}
		if IsAutoStartableDial(err) {
			t.Fatalf("canceled should not auto-start: %v", err)
		}
	})
	t.Run("constructed errno", func(t *testing.T) {
		tests := []struct {
			name string
			err  error
			want bool
		}{
			{name: "ENOENT", err: wrapClientError(&net.OpError{Op: "dial", Net: "unix", Err: syscall.ENOENT}), want: true},
			{name: "ECONNREFUSED", err: wrapClientError(&net.OpError{Op: "dial", Net: "unix", Err: syscall.ECONNREFUSED}), want: true},
			{name: "EACCES", err: wrapClientError(&net.OpError{Op: "dial", Net: "unix", Err: syscall.EACCES}), want: false},
			{name: "timeout", err: wrapClientError(&net.OpError{Op: "dial", Net: "unix", Err: os.ErrDeadlineExceeded}), want: false},
			{name: "plain unavailable", err: app.ErrUnavailable, want: false},
			{name: "http message", err: errors.New("http 500"), want: false},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				if got := IsAutoStartableDial(tt.err); got != tt.want {
					t.Fatalf("IsAutoStartableDial(%v)=%v want %v", tt.err, got, tt.want)
				}
			})
		}
	})
}

func TestTopicMessagesAndThreadLatestNavigation(t *testing.T) {
	adapter, err := store.Open(context.Background(), store.Options{Path: filepath.Join(t.TempDir(), "comms.db")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = adapter.Close() })
	svc := app.NewService(adapter, domain.UTCClock{})
	server := httptest.NewServer(NewHandler(svc))
	t.Cleanup(server.Close)

	ctx := context.Background()
	_, err = svc.Join(ctx, app.JoinRequest{Handle: "sender"})
	if err != nil {
		t.Fatal(err)
	}
	topic, err := svc.CreateTopic(ctx, app.CreateTopicRequest{Name: "stream"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.Follow(ctx, app.FollowRequest{Agent: "sender", Topic: topic.Name})
	if err != nil {
		t.Fatal(err)
	}

	for i := 1; i <= 6; i++ {
		_, err := svc.Publish(ctx, app.PublishRequest{
			Author: "sender",
			Topic:  topic.Name,
			Title:  fmt.Sprintf("item-%02d", i),
			Body:   fmt.Sprintf("body %d", i),
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	// Test GET /v1/topics/{topic}/messages?latest=true&limit=3
	res, err := server.Client().Get(server.URL + "/v1/topics/stream/messages?latest=true&limit=3")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", res.StatusCode)
	}
	var doc struct {
		Data struct {
			Items      []map[string]any `json:"items"`
			NextCursor string           `json:"next_cursor"`
		} `json:"data"`
	}
	if err := json.NewDecoder(res.Body).Decode(&doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Data.Items) != 3 {
		t.Fatalf("got %d items, want 3", len(doc.Data.Items))
	}
	if doc.Data.Items[0]["title"] != "item-04" || doc.Data.Items[2]["title"] != "item-06" {
		t.Fatalf("items = %#v", doc.Data.Items)
	}
	if doc.Data.NextCursor == "" {
		t.Fatal("expected next cursor for backward pagination")
	}

	// Paginate backward with cursor:
	resPage2, err := server.Client().Get(server.URL + "/v1/topics/stream/messages?latest=true&limit=3&cursor=" + doc.Data.NextCursor)
	if err != nil {
		t.Fatal(err)
	}
	defer resPage2.Body.Close()
	if resPage2.StatusCode != http.StatusOK {
		t.Fatalf("page 2 status = %d", resPage2.StatusCode)
	}
	var docPage2 struct {
		Data struct {
			Items      []map[string]any `json:"items"`
			NextCursor string           `json:"next_cursor"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resPage2.Body).Decode(&docPage2); err != nil {
		t.Fatal(err)
	}
	if len(docPage2.Data.Items) != 3 || docPage2.Data.Items[0]["title"] != "item-01" || docPage2.Data.Items[2]["title"] != "item-03" {
		t.Fatalf("page 2 items = %#v", docPage2.Data.Items)
	}
	if docPage2.Data.NextCursor != "" {
		t.Fatalf("reached beginning, expected empty next_cursor, got %q", docPage2.Data.NextCursor)
	}

	// Test thread latest navigation
	rootID := docPage2.Data.Items[0]["id"].(string)
	for i := 1; i <= 5; i++ {
		_, err := svc.Reply(ctx, app.ReplyRequest{
			Author: "sender",
			Parent: rootID,
			Title:  fmt.Sprintf("reply-%02d", i),
			Body:   fmt.Sprintf("reply body %d", i),
		})
		if err != nil {
			t.Fatal(err)
		}
	}

	resThread, err := server.Client().Get(server.URL + "/v1/messages/" + rootID + "/thread?latest=true&limit=3")
	if err != nil {
		t.Fatal(err)
	}
	defer resThread.Body.Close()
	if resThread.StatusCode != http.StatusOK {
		t.Fatalf("thread status = %d", resThread.StatusCode)
	}
	var docThread struct {
		Data struct {
			Items []map[string]any `json:"items"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resThread.Body).Decode(&docThread); err != nil {
		t.Fatal(err)
	}
	if len(docThread.Data.Items) != 3 {
		t.Fatalf("got %d thread items, want 3", len(docThread.Data.Items))
	}
	if docThread.Data.Items[0]["title"] != "reply-03" || docThread.Data.Items[2]["title"] != "reply-05" {
		t.Fatalf("thread items = %#v", docThread.Data.Items)
	}
}
