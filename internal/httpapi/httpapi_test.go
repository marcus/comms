package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
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
