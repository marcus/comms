package cli

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

func TestBlackBoxAgentDiscoveryAndActivity(t *testing.T) {
	dir, socket := startDaemon(t)
	identity := filepath.Join(dir, "peer.json")
	runJSON(t, socket, nil, "join", "peer-lane", "--project", "Comms", "--purpose", "Parser owner", "--context", identity)
	runJSON(t, socket, nil, "join", "other-lane", "--project", "Elsewhere", "--context", filepath.Join(dir, "other.json"))
	page := runJSON(t, socket, nil, "agents", "--search", "PARSER", "--project", "comms", "--limit", "1")
	if items := arrayValue(t, page, "items"); len(items) != 1 || items[0].(map[string]any)["handle"] != "peer-lane" {
		t.Fatalf("discovery %#v", page)
	}
	runJSON(t, socket, map[string]string{"COMMS_CONTEXT": identity}, "inbox")
	peer := runJSON(t, socket, nil, "agent", "get", "peer-lane")
	activity, ok := peer["activity"].(map[string]any)
	if !ok || activity["last_inbox_at"] == nil || activity["open_waits"] != float64(0) {
		t.Fatalf("activity %#v", peer)
	}
	var stdout, stderr bytes.Buffer
	code := Run(Env{Args: []string{"--socket", socket, "--as", "peer-lane", "send", "@peer-lame", "--title", "hello", "--body", "hello", "--json"}, Stdout: &stdout, Stderr: &stderr})
	if code != 3 || !strings.Contains(stderr.String()+stdout.String(), "@peer-lane") || !strings.Contains(stderr.String()+stdout.String(), "not_found") {
		t.Fatalf("send code=%d out=%s err=%s", code, &stdout, &stderr)
	}
	stdout.Reset()
	stderr.Reset()
	code = Run(Env{Args: []string{"--socket", socket, "agent", "get", "peer-lane"}, Stdout: &stdout, Stderr: &stderr})
	if code != 0 || !strings.Contains(stdout.String(), "activity (advisory") || !strings.Contains(stdout.String(), "last inbox=") {
		t.Fatalf("human code=%d out=%s err=%s", code, &stdout, &stderr)
	}
}
