package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"testing"

	"github.com/marcus/comms/internal/app"
	"github.com/marcus/comms/internal/domain"
	"github.com/marcus/comms/internal/store"
)

func TestObserveHeadHTTPContract(t *testing.T) {
	ctx := context.Background()
	adapter, err := store.Open(ctx, store.Options{Path: filepath.Join(t.TempDir(), "comms.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = adapter.Close() }()
	svc := app.NewService(adapter, domain.UTCClock{})
	if _, err = svc.Join(ctx, app.JoinRequest{Handle: "alice"}); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Join(ctx, app.JoinRequest{Handle: "bob"}); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.DirectSend(ctx, app.DirectSendRequest{Author: "alice", Recipient: "bob", Title: "backlog", Body: "old"}); err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(svc)
	head := httptest.NewRecorder()
	handler.ServeHTTP(head, httptest.NewRequest(http.MethodGet, "/v1/observe?head=true", nil))
	var envelope struct {
		Data app.ObserveResponse `json:"data"`
	}
	if err = json.Unmarshal(head.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if head.Code != http.StatusOK || envelope.Data.Items == nil || len(envelope.Data.Items) != 0 || envelope.Data.After == "" {
		t.Fatalf("head=%d %s", head.Code, head.Body)
	}
	msg, err := svc.DirectSend(ctx, app.DirectSendRequest{Author: "alice", Recipient: "bob", Title: "after head", Body: "new"})
	if err != nil {
		t.Fatal(err)
	}
	batch := httptest.NewRecorder()
	handler.ServeHTTP(batch, httptest.NewRequest(http.MethodGet, "/v1/observe?after="+url.QueryEscape(envelope.Data.After), nil))
	if err = json.Unmarshal(batch.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if batch.Code != http.StatusOK || len(envelope.Data.Items) != 1 || envelope.Data.Items[0].ID != msg.ID {
		t.Fatalf("batch=%d %s", batch.Code, batch.Body)
	}
	for _, query := range []string{"head=true&wait=true", "head=true&after=anything", "head=true&cursor=anything"} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/observe?"+query, nil))
		var failure ErrorEnvelope
		if err = json.Unmarshal(rec.Body.Bytes(), &failure); err != nil {
			t.Fatal(err)
		}
		if rec.Code != http.StatusBadRequest || failure.Error.Code != "invalid_argument" {
			t.Fatalf("%s: %d %s", query, rec.Code, rec.Body)
		}
	}
}
