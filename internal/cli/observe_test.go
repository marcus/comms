package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

func TestObserveBridgeCLI(t *testing.T) {
	dir, socket := startDaemon(t)
	env := map[string]string{"COMMS_CONTEXT": filepath.Join(dir, "alice.json")}
	runJSON(t, socket, env, "join", "alice")
	bob := runJSON(t, socket, nil, "join", "bob", "--context", filepath.Join(dir, "bob.json"))
	root := runJSON(t, socket, env, "send", "@bob", "--title", "bridge", "--body", "wake")
	first := runJSON(t, socket, nil, "observe", "--wait", "--limit", "1", "--timeout", "1s")
	items := arrayValue(t, first, "items")
	if len(items) != 1 || items[0].(map[string]any)["id"] != root["id"] {
		t.Fatal(first)
	}
	recipients := items[0].(map[string]any)["recipient_ids"].([]any)
	if len(recipients) != 1 || recipients[0] != bob["agent"].(map[string]any)["id"] {
		t.Fatal(recipients)
	}
	after := stringValue(t, first, "after")
	empty := runJSON(t, socket, nil, "observe", "--after", after)
	if len(arrayValue(t, empty, "items")) != 0 || empty["after"] != after {
		t.Fatal(empty)
	}
	for _, tc := range []struct {
		args      []string
		ctx       context.Context
		exit      int
		errorCode string
	}{
		{[]string{"observe", "--wait", "--after", after, "--timeout", "5ms"}, nil, 5, "timeout"},
		{[]string{"observe", "--wait", "--cursor", "bad"}, nil, 2, "invalid_argument"},
		{[]string{"observe", "--after", "bad"}, nil, 2, "invalid_argument"},
		{[]string{"observe", "--wait", "--timeout", "0s"}, nil, 2, "invalid_argument"},
	} {
		var out, stderr bytes.Buffer
		code := Run(Env{Args: append([]string{"--json", "--socket", socket}, tc.args...), Stdout: &out, Stderr: &stderr, Context: tc.ctx, Getenv: func(string) string { return "" }})
		var envelope struct{ Error struct{ Code string } }
		if err := json.Unmarshal(stderr.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		if code != tc.exit || out.Len() != 0 || envelope.Error.Code != tc.errorCode {
			t.Fatalf("%v: exit=%d out=%s err=%s", tc.args, code, &out, &stderr)
		}
	}
	// One outstanding operator request can wake on a new direct message, with
	// no per-agent identity or process. Service-level tests prove subscription
	// registration ordering and cancellation independently of transport timing.
	done := make(chan map[string]any, 1)
	go func() { done <- runJSON(t, socket, nil, "observe", "--wait", "--after", after, "--timeout", "2s") }()
	next := runJSON(t, socket, env, "send", "@bob", "--title", "next", "--body", "wake again")
	select {
	case result := <-done:
		if arrayValue(t, result, "items")[0].(map[string]any)["id"] != next["id"] {
			t.Fatal(result)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("observer did not return")
	}
	inbox := runJSON(t, socket, map[string]string{"COMMS_AGENT_ID": "bob"}, "inbox", "--unread")
	if len(arrayValue(t, inbox, "items")) != 2 {
		t.Fatal("observer acknowledged messages", inbox)
	}
}
