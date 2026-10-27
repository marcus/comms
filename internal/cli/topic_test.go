package cli

import (
	"path/filepath"
	"testing"
)

func TestMessageTopicsAndTopicGet(t *testing.T) {
	dir, socket := startDaemon(t)
	ctx := map[string]string{"COMMS_CONTEXT": filepath.Join(dir, "alice.json")}
	runJSON(t, socket, ctx, "join", "alice")
	runJSON(t, socket, nil, "join", "bob", "--context", filepath.Join(dir, "bob.json"))
	runJSON(t, socket, ctx, "topic", "create", "dev", "--description", "Development")
	runJSON(t, socket, ctx, "topic", "follow", "dev")
	public := runJSON(t, socket, ctx, "publish", "dev", "--title", "Public", "--body", "public")
	direct := runJSON(t, socket, ctx, "send", "@bob", "--title", "Direct", "--body", "private")
	for _, msg := range []map[string]any{public, direct} {
		topic := msg["topic"].(map[string]any)
		if topic["id"] != msg["topic_id"] {
			t.Fatalf("topic identity: %#v", msg)
		}
		details := runJSON(t, socket, nil, "topic", "get", stringValue(t, msg, "topic_id"))
		if topic["kind"] == "direct" {
			if len(arrayValue(t, details, "members")) != 2 {
				t.Fatalf("direct members: %#v", details)
			}
		} else if topic["kind"] != "public" || details["description"] != "Development" {
			t.Fatalf("public details: %#v", details)
		}
	}
	runJSON(t, socket, nil, "topic", "update", "dev", "--name", "renamed")
	got := runJSON(t, socket, nil, "peek", stringValue(t, public, "id"))
	if got["topic"].(map[string]any)["name"] != "renamed" {
		t.Fatalf("read-time name: %#v", got)
	}
	details := runJSON(t, socket, nil, "topic", "get", "renamed")
	if details["id"] != public["topic_id"] {
		t.Fatal(details)
	}
}
