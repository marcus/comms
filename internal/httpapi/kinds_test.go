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

func TestReplyAcceptsNullTitleAndKind(t *testing.T) {
	adapter, err := store.Open(context.Background(), store.Options{Path: filepath.Join(t.TempDir(), "comms.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = adapter.Close() }()
	svc := app.NewService(adapter, domain.UTCClock{})
	ctx := context.Background()
	if _, err := svc.Join(ctx, app.JoinRequest{Handle: "author"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CreateTopic(ctx, app.CreateTopicRequest{Name: "coord"}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Follow(ctx, app.FollowRequest{Agent: "author", Topic: "coord"}); err != nil {
		t.Fatal(err)
	}
	root, err := svc.Publish(ctx, app.PublishRequest{Author: "author", Topic: "coord", Title: "root", Body: "body"})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/messages/"+string(root.ID)+"/replies", strings.NewReader(`{"title":null,"kind":"verdict","body":"approved"}`))
	request.Header.Set(AgentHeader, "author")
	recorder := httptest.NewRecorder()
	NewHandler(svc).ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("reply=%d %s", recorder.Code, recorder.Body.String())
	}
	var envelope struct {
		Data domain.Message `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Data.Kind != "verdict" || envelope.Data.Title != "" {
		t.Fatalf("reply=%#v", envelope.Data)
	}
}
