package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/marcus/comms/internal/app"
	"github.com/marcus/comms/internal/domain"
	"github.com/marcus/comms/internal/httpapi"
)

func TestGroupCLIAndAPIShareConversationAndVisibility(t *testing.T) {
	iso := newIsolatedRoots(t)
	start := inProcessAutoStart(t)
	run := func(args ...string) json.RawMessage {
		t.Helper()
		var out, err bytes.Buffer
		code := Run(Env{Args: append([]string{"--socket", iso.socket, "--json"}, args...), Stdout: &out, Stderr: &err, Stdin: strings.NewReader("group body from stdin"), Getenv: iso.getenv(nil), StartDaemon: start})
		if code != 0 || err.Len() != 0 {
			t.Fatalf("%v: exit=%d %s", args, code, err.String())
		}
		var envelope struct{ Data json.RawMessage }
		if err := json.Unmarshal(out.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		return envelope.Data
	}
	for _, handle := range []string{"alice", "bob", "cara", "outsider"} {
		run("join", handle, "--context", iso.root+"/"+handle+".json")
	}
	var first domain.Message
	if err := json.Unmarshal(run("--as", "alice", "send", "@bob", "@cara", "-", "--title", "group title"), &first); err != nil {
		t.Fatal(err)
	}
	if first.Topic.Kind != domain.TopicGroup || first.Body != "group body from stdin" {
		t.Fatalf("CLI group=%+v", first)
	}
	client := httpapi.NewUnixClient(iso.socket, "cara")
	defer client.CloseIdleConnections()
	var second domain.Message
	if err := client.Do(context.Background(), http.MethodPost, "/v1/direct-messages", nil, map[string]any{"agents": []string{"alice", "bob"}, "title": "API group", "body": "same participant set"}, &second); err != nil {
		t.Fatal(err)
	}
	if second.TopicID != first.TopicID || second.Topic.Kind != domain.TopicGroup {
		t.Fatalf("API group=%+v", second)
	}
	var details app.TopicDetails
	if err := json.Unmarshal(run("topic", "get", string(first.TopicID)), &details); err != nil || len(details.Members) != 3 {
		t.Fatal(details, err)
	}
	for _, who := range []string{"bob", "outsider"} {
		var topics app.Page[domain.Topic]
		if err := json.Unmarshal(run("--as", who, "topics", "--include-direct"), &topics); err != nil {
			t.Fatal(err)
		}
		want := 1
		if who == "outsider" {
			want = 0
		}
		if len(topics.Items) != want {
			t.Fatal(who, topics)
		}
	}
	for _, body := range []map[string]any{
		{"agent": "bob", "agents": nil, "title": "both-null", "body": "bad"},
		{"agent": nil, "agents": []string{"bob"}, "title": "both-null-agent", "body": "bad"},
		{"agents": nil, "title": "null", "body": "bad"},
		{"agent": "bob", "agents": []string{"bob", "alice"}, "title": "both", "body": "bad"},
		{"agents": []string{}, "title": "empty", "body": "bad"},
		{"agents": []string{"bob", "bob"}, "title": "duplicate", "body": "bad"},
	} {
		if err := client.Do(context.Background(), http.MethodPost, "/v1/direct-messages", nil, body, nil); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("%v: %v", body, err)
		}
	}
	var direct domain.Message
	if err := client.Do(context.Background(), http.MethodPost, "/v1/direct-messages", nil, map[string]any{"agent": "alice", "title": "legacy direct", "body": "unchanged"}, &direct); err != nil || direct.Topic.Kind != domain.TopicDirect {
		t.Fatal(direct, err)
	}
}
