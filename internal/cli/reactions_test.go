package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/marcus/comms/internal/httpapi"
)

func TestReactionsThroughCLIAndHTTP(t *testing.T) {
	dir, socket := startDaemon(t)
	alice := map[string]string{"COMMS_CONTEXT": dir + "/alice.json"}
	bob := map[string]string{"COMMS_CONTEXT": dir + "/bob.json"}
	cara := map[string]string{"COMMS_CONTEXT": dir + "/cara.json"}
	for name, env := range map[string]map[string]string{"alice": alice, "bob": bob, "cara": cara} {
		runJSON(t, socket, nil, "join", name, "--context", env["COMMS_CONTEXT"])
	}
	root := runJSON(t, socket, alice, "send", "@bob", "--title", "Check", "--body", "Please review", "--kind", "ready")
	id := stringValue(t, root, "id")
	reaction := runJSON(t, socket, bob, "react", id, "👍")
	if reaction["changed"] != true || reaction["removed"] != false {
		t.Fatalf("add=%v", reaction)
	}
	duplicate := runJSON(t, socket, bob, "react", id, "👍")
	if duplicate["changed"] != false {
		t.Fatalf("duplicate=%v", duplicate)
	}
	for _, args := range [][]string{{"peek", id}, {"thread", id}, {"inbox", "--full"}} {
		result := runJSON(t, socket, bob, args...)
		if args[0] != "peek" {
			result = arrayValue(t, result, "items")[0].(map[string]any)
		}
		reactions := arrayValue(t, result, "reactions")
		if len(reactions) != 1 || reactions[0].(map[string]any)["reaction"] != "👍" {
			t.Fatalf("%v: %v", args, result)
		}
	}
	ordinary := runJSON(t, socket, alice, "inbox")
	if len(arrayValue(t, ordinary, "items")) != 0 || ordinary["reaction_events"] != nil {
		t.Fatalf("ordinary inbox=%v", ordinary)
	}
	opted := runJSON(t, socket, alice, "inbox", "--include-reactions", "--kind", "ready")
	events := arrayValue(t, opted, "reaction_events")
	if len(events) != 1 || events[0].(map[string]any)["type"] != "reaction" {
		t.Fatalf("opted=%v", opted)
	}
	cursor := stringValue(t, opted, "reactions_after")
	if other := runJSON(t, socket, cara, "inbox", "--include-reactions"); other["reaction_events"] != nil {
		t.Fatalf("unrelated author=%v", other)
	}
	client := httpapi.NewUnixClient(socket, "bob")
	defer client.CloseIdleConnections()
	var removed map[string]any
	if err := client.Do(context.Background(), http.MethodPost, "/v1/messages/"+id+"/reactions", nil, map[string]any{"reaction": "👍", "remove": true}, &removed); err != nil {
		t.Fatal(err)
	}
	if removed["changed"] != true || removed["removed"] != true {
		t.Fatalf("HTTP remove=%v", removed)
	}
	afterRemove := runJSON(t, socket, alice, "wait", "--include-reactions", "--reactions-after", cursor, "--from", "@bob", "--thread", id)
	events = arrayValue(t, afterRemove, "reaction_events")
	if len(arrayValue(t, afterRemove, "items")) != 0 || len(events) != 1 || events[0].(map[string]any)["removed"] != true {
		t.Fatalf("wait remove=%v", afterRemove)
	}
	if got := runJSON(t, socket, bob, "react", id, "👍", "--remove"); got["changed"] != false {
		t.Fatalf("duplicate removal=%v", got)
	}
	head := runJSON(t, socket, nil, "observe", "--head", "--include-reactions")
	if stringValue(t, head, "after") == "" || stringValue(t, head, "reactions_after") == "" || len(arrayValue(t, head, "items")) != 0 {
		t.Fatalf("head=%v", head)
	}
	runJSON(t, socket, bob, "react", id, "understood")
	observed := runJSON(t, socket, nil, "observe", "--include-reactions", "--after", stringValue(t, head, "after"), "--reactions-after", stringValue(t, head, "reactions_after"))
	if len(arrayValue(t, observed, "items")) != 0 || len(arrayValue(t, observed, "reaction_events")) != 1 {
		t.Fatalf("observe=%v", observed)
	}
	var rendered bytes.Buffer
	if err := renderHuman(&rendered, runJSON(t, socket, nil, "peek", id), false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rendered.String(), "Reaction: understood by agt_") {
		t.Fatalf("human=%s", rendered.String())
	}
	rendered.Reset()
	if err := renderHuman(&rendered, afterRemove, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rendered.String(), "reaction removed: 👍") || !strings.Contains(rendered.String(), "reactions after cursor:") {
		t.Fatalf("events human=%s", rendered.String())
	}
	var output bytes.Buffer
	if code := Run(Env{Args: []string{"--socket", socket, "export"}, Stdout: &output, Getenv: func(string) string { return "" }}); code != 0 {
		t.Fatalf("export exit %d", code)
	}
	var eventCount int
	for _, line := range strings.Split(strings.TrimSpace(output.String()), "\n") {
		var row map[string]any
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			t.Fatal(err)
		}
		if row["type"] == "reaction_event" {
			eventCount++
		}
	}
	if eventCount != 3 {
		t.Fatalf("export events=%d", eventCount)
	}
}
