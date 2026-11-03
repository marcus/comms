package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marcus/comms/internal/app"
	"github.com/marcus/comms/internal/domain"
	"github.com/marcus/comms/internal/store"
)

func TestReactionHTTPBoundary(t *testing.T) {
	ctx := context.Background()
	adapter, err := store.Open(ctx, store.Options{Path: filepath.Join(t.TempDir(), "comms.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = adapter.Close() }()
	svc := app.NewService(adapter, domain.UTCClock{})
	for _, handle := range []string{"alice", "bob"} {
		if _, err := svc.Join(ctx, app.JoinRequest{Handle: handle}); err != nil {
			t.Fatal(err)
		}
	}
	root, err := svc.DirectSend(ctx, app.DirectSendRequest{Author: "alice", Recipient: "bob", Title: "Ready", Body: "Review"})
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(svc)
	route := "/v1/messages/" + string(root.ID) + "/reactions"
	request := func(method, path, agent, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if agent != "" {
			req.Header.Set(AgentHeader, agent)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	for _, tc := range []struct {
		agent, body string
		status      int
	}{
		{"", `{"reaction":"👍"}`, http.StatusBadRequest},
		{"bob", `{"reaction":""}`, http.StatusBadRequest},
		{"bob", `{"reaction":" leading"}`, http.StatusBadRequest},
		{"bob", `{"reaction":"line\nbreak"}`, http.StatusBadRequest},
		{"bob", `{"reaction":"👍","agent":"alice"}`, http.StatusBadRequest},
		{"bob", `{"reaction":"👍","remove":"yes"}`, http.StatusBadRequest},
		{"bob", `{"reaction":"👍"}`, http.StatusOK},
		{"bob", `{"reaction":"👍"}`, http.StatusOK},
	} {
		rec := request(http.MethodPost, route, tc.agent, tc.body)
		if rec.Code != tc.status {
			t.Fatalf("%s %s: status=%d body=%s", tc.agent, tc.body, rec.Code, rec.Body.String())
		}
	}
	for _, path := range []string{
		"/v1/inbox?include_reactions=garbage",
		"/v1/inbox?reactions_after=cursor",
		"/v1/wait?reactions_after=cursor",
		"/v1/observe?reactions_after=cursor",
		"/v1/observe?head=true&include_reactions=true&reactions_after=cursor",
	} {
		rec := request(http.MethodGet, path, "alice", "")
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body.String())
		}
	}
	rec := request(http.MethodGet, "/v1/inbox?include_reactions=true", "alice", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("inbox: %d %s", rec.Code, rec.Body.String())
	}
	var envelope struct{ Data app.InboxResponse }
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope.Data.Items) != 0 || len(envelope.Data.ReactionEvents) != 1 || envelope.Data.ReactionsAfter == "" {
		t.Fatalf("inbox=%+v", envelope.Data)
	}
}
