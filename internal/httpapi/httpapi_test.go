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
	"testing"

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
		if operation.HTTP == nil {
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
