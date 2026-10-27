package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestObserveHeadInstalledProcessContinuation(t *testing.T) {
	binary := commsBinary(t)
	env, state, socket := isolatedProcEnv(t)
	t.Cleanup(func() { shutdownIsolated(t, socket) })
	empty := runBinJSON(t, binary, env, "observe", "--head")
	if len(arrayValue(t, empty, "items")) != 0 || stringValue(t, empty, "after") == "" {
		t.Fatal(empty)
	}
	runBinJSON(t, binary, env, "join", "alice", "--context", filepath.Join(state, "alice.json"))
	runBinJSON(t, binary, env, "join", "bob", "--context", filepath.Join(state, "bob.json"))
	runBinJSON(t, binary, env, "--as", "alice", "send", "@bob", "--title", "backlog", "--body", "old")
	head := runBinJSON(t, binary, env, "observe", "--head")
	after := stringValue(t, head, "after")
	if len(arrayValue(t, head, "items")) != 0 {
		t.Fatal("head returned history", head)
	}
	out, stderr, code, err := execBin(binary, env, "--json", "observe", "--wait", "--after", after, "--timeout", "5ms")
	var envelope struct{ Error struct{ Code string } }
	if decodeErr := json.Unmarshal([]byte(stderr), &envelope); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if err != nil || code != 5 || out != "" || envelope.Error.Code != "timeout" {
		t.Fatalf("head wait: code=%d out=%s err=%s (%v)", code, out, stderr, err)
	}
	// Persist outside both processes, then let a publisher restart the daemon
	// while the bridge is offline. Reusing the saved cursor recovers that traffic.
	cursorFile := filepath.Join(state, "bridge.after")
	if err := os.WriteFile(cursorFile, []byte(after), 0o600); err != nil {
		t.Fatal(err)
	}
	shutdownIsolated(t, socket)
	arrived := runBinJSON(t, binary, env, "--as", "alice", "send", "@bob", "--title", "downtime", "--body", "new")
	saved, err := os.ReadFile(cursorFile)
	if err != nil {
		t.Fatal(err)
	}
	batch := runBinJSON(t, binary, env, "observe", "--wait", "--after", string(saved), "--timeout", "1s")
	items := arrayValue(t, batch, "items")
	if len(items) != 1 || items[0].(map[string]any)["id"] != arrived["id"] {
		t.Fatal(batch)
	}
	human, stderr, code, err := execBin(binary, env, "observe", "--head")
	if err != nil || code != 0 || stderr != "" || !strings.HasPrefix(human, "after cursor: ") {
		t.Fatalf("human head=%q stderr=%s code=%d err=%v", human, stderr, code, err)
	}
	for _, args := range [][]string{{"--head", "--wait"}, {"--head", "--after", after}, {"--head", "--cursor", "anything"}} {
		out, stderr, code, err = execBin(binary, env, append([]string{"--json", "observe"}, args...)...)
		if err != nil || code != 2 || out != "" || !strings.Contains(stderr, "invalid_argument") {
			t.Fatalf("%v: code=%d stdout=%s stderr=%s err=%v", args, code, out, stderr, err)
		}
	}
	inbox := runBinJSON(t, binary, env, "--as", "bob", "inbox", "--unread")
	if len(arrayValue(t, inbox, "items")) != 2 {
		t.Fatal("head/observation acknowledged messages", inbox)
	}
}
