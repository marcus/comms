package cli

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestCodexThreadsSharingInheritedPaneJoinIndependently(t *testing.T) {
	_, socket := startDaemon(t)
	state := t.TempDir()
	lane := func(thread string) map[string]string {
		return map[string]string{"COMMS_STATE_DIR": state, "CODEX_THREAD_ID": thread, "TMUX": "/tmp/stale/default,1,159", "TMUX_PANE": "%159"}
	}
	alice, bob := lane("conversation-a"), lane("conversation-b")
	first := runJSON(t, socket, alice, "join", "codex-a")
	second := runJSON(t, socket, bob, "join", "codex-b")
	if first["session"] != "CODEX_THREAD_ID" || second["session"] != "CODEX_THREAD_ID" || first["context"] == second["context"] {
		t.Fatalf("contexts: %#v %#v", first, second)
	}
	for _, env := range []map[string]string{alice, bob} {
		thread := env["CODEX_THREAD_ID"]
		env["CODEX_SESSION_ID"] = "new-execution"
		got := runJSON(t, socket, env, "whoami")
		want := "codex-a"
		if thread == "conversation-b" {
			want = "codex-b"
		}
		if stringValue(t, mapValue(t, got, "agent"), "handle") != want {
			t.Fatalf("identity = %#v", got)
		}
	}
	alice["COMMS_SESSION"] = "explicit-session"
	override := runJSON(t, socket, alice, "join", "explicit")
	if override["session"] != "COMMS_SESSION" {
		t.Fatalf("override = %#v", override)
	}
	alice["COMMS_CONTEXT"] = first["context"].(string)
	if got := runJSON(t, socket, alice, "whoami"); stringValue(t, mapValue(t, got, "agent"), "handle") != "codex-a" {
		t.Fatalf("context override = %#v", got)
	}
	alice["COMMS_AGENT_ID"] = "codex-b"
	if got := runJSON(t, socket, alice, "whoami"); stringValue(t, mapValue(t, got, "agent"), "handle") != "codex-b" {
		t.Fatalf("agent override = %#v", got)
	}
	if got := runJSON(t, socket, alice, "--as", "explicit", "whoami"); stringValue(t, mapValue(t, got, "agent"), "handle") != "explicit" {
		t.Fatalf("flag override = %#v", got)
	}
	explicitPath := filepath.Join(state, "chosen.json")
	if got := runJSON(t, socket, alice, "join", "chosen", "--context", explicitPath); got["context"] != explicitPath {
		t.Fatalf("join context override = %#v", got)
	}
}

func TestJoinSameContextReconnectsWithoutClaimingOtherIdentity(t *testing.T) {
	_, socket := startDaemon(t)
	state := t.TempDir()
	env := map[string]string{"COMMS_STATE_DIR": state, "COMMS_SESSION": "same-session"}
	first := runJSON(t, socket, env, "join", "alice", "--harness", "codex")
	second := runJSON(t, socket, env, "join", "ALICE", "--purpose", "resumed")
	a, b := mapValue(t, first, "agent"), mapValue(t, second, "agent")
	if second["rejoined"] != true || a["id"] != b["id"] || b["harness"] != "codex" || b["purpose"] != "resumed" {
		t.Fatalf("reconnect = %#v", second)
	}
	firstRecord, err := readContext(first["context"].(string))
	if err != nil {
		t.Fatal(err)
	}
	// Explicitly selecting that context also reconnects the same identity.
	third := runJSON(t, socket, nil, "join", "alice", "--context", first["context"].(string))
	if third["rejoined"] != true || mapValue(t, third, "agent")["id"] != a["id"] {
		t.Fatalf("explicit reconnect = %#v", third)
	}
	record, err := readContext(first["context"].(string))
	if err != nil || record.ClientID != firstRecord.ClientID || record.Harness != "codex" {
		t.Fatalf("context = %#v, err %v", record, err)
	}
	fresh := filepath.Join(state, "fresh.json")
	code, stderr := runCode(t, socket, nil, "join", "alice", "--context", fresh)
	if code != 4 || !strings.Contains(stderr, "original --context/COMMS_CONTEXT") || !strings.Contains(stderr, "--as alice") {
		t.Fatalf("fresh reconnect: code=%d stderr=%s", code, stderr)
	}
	if _, err := readContext(fresh); err == nil {
		t.Fatal("failed reconnect wrote a context")
	}
	code, stderr = runCode(t, socket, env, "join", "bob")
	if code != 4 || !strings.Contains(stderr, "--replace") {
		t.Fatalf("takeover: code=%d stderr=%s", code, stderr)
	}
	if got := runJSON(t, socket, env, "whoami"); mapValue(t, got, "agent")["id"] != a["id"] {
		t.Fatalf("refused takeover changed identity: %#v", got)
	}
}
