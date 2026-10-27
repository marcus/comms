package cli

import (
	"path/filepath"
	"testing"
)

func TestMessageAuthorsResolveAtReadTime(t *testing.T) {
	dir, socket := startDaemon(t)
	ctx := map[string]string{"COMMS_CONTEXT": filepath.Join(dir, "alice.json")}
	author := runJSON(t, socket, ctx, "join", "alice", "--display-name", "Alice")
	runJSON(t, socket, nil, "join", "bob", "--context", filepath.Join(dir, "bob.json"))
	msg := runJSON(t, socket, ctx, "send", "@bob", "--title", "Hello", "--body", "uniqueauthor")
	id := stringValue(t, msg, "id")
	if msg["author_handle"] != "alice" || msg["author_display_name"] != "Alice" {
		t.Fatalf("send: %#v", msg)
	}
	runJSON(t, socket, nil, "agent", "update", "alice", "--handle", "alicia", "--display-name", "Alicia")
	for _, args := range [][]string{{"peek", id}, {"inbox", "--full"}, {"thread", id}, {"wait", "--timeout", "1s"}, {"search", "uniqueauthor"}, {"observe"}, {"topic", "messages", stringValue(t, msg, "topic_id")}} {
		data := runJSON(t, socket, map[string]string{"COMMS_AGENT_ID": "bob"}, args...)
		if items, ok := data["items"].([]any); ok {
			if len(items) != 1 {
				t.Fatalf("%v: %#v", args, data)
			}
			data = items[0].(map[string]any)
		}
		if data["author_handle"] != "alicia" || data["author_display_name"] != "Alicia" || data["author_id"] != author["agent"].(map[string]any)["id"] {
			t.Fatalf("%v: %#v", args, data)
		}
	}
}
