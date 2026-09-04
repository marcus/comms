package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/marcus/comms/internal/service"
)

func TestBlackBoxThreeSessionConversation(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "comms-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "comms.sock")
	ctx, cancel := context.WithCancel(context.Background())
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- service.Run(ctx, service.Config{
			DatabasePath: filepath.Join(dir, "comms.db"),
			SocketPath:   socket,
		})
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-serverDone:
			if err != nil {
				t.Errorf("service shutdown: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("service did not shut down")
		}
	})
	waitForSocket(t, socket)

	alpha := filepath.Join(dir, "alpha.json")
	beta := filepath.Join(dir, "beta.json")
	gamma := filepath.Join(dir, "gamma.json")
	joinedAlpha := runJSON(t, socket, nil, "join", "alpha", "--harness", "codex", "--external-namespace", "test", "--external-key", "alpha", "--context", alpha)
	runJSON(t, socket, nil, "join", "beta", "--harness", "claude-code", "--context", beta)
	runJSON(t, socket, nil, "join", "gamma", "--harness", "gemini", "--context", gamma)

	created := runJSON(t, socket, map[string]string{"COMMS_CONTEXT": alpha}, "topic", "create", "project-comms")
	if created["name"] != "project-comms" {
		t.Fatalf("created topic = %#v", created)
	}
	for _, path := range []string{alpha, beta, gamma} {
		runJSON(t, socket, map[string]string{"COMMS_CONTEXT": path}, "topic", "follow", "project-comms")
	}
	root := runJSON(t, socket, map[string]string{"COMMS_CONTEXT": alpha}, "publish", "project-comms", "--title", "Core ready", "--body", "Review td-8f5777")
	rootID := stringValue(t, root, "id")
	runJSONWithStdin(t, socket, map[string]string{"COMMS_CONTEXT": beta}, "Multiline reply\nfrom stdin\n", "reply", rootID, "-")

	gammaInbox := runJSON(t, socket, map[string]string{"COMMS_CONTEXT": gamma}, "inbox", "--unread")
	if items := arrayValue(t, gammaInbox, "items"); len(items) != 2 {
		t.Fatalf("gamma inbox has %d messages, want 2", len(items))
	}
	runJSON(t, socket, map[string]string{"COMMS_CONTEXT": beta}, "read-through", rootID)
	receipts := runJSONArray(t, socket, nil, "receipts", rootID)
	foundRead := false
	for _, receipt := range receipts {
		if receipt["state"] == "read" {
			foundRead = true
		}
	}
	if !foundRead {
		t.Fatalf("receipts have no read subscriber: %#v", receipts)
	}

	direct := runJSON(t, socket, map[string]string{"COMMS_CONTEXT": alpha}, "send", "@beta", "--title", "Direct check", "--body", "Only beta inbox route")
	directID := stringValue(t, direct, "id")
	gammaInbox = runJSON(t, socket, map[string]string{"COMMS_CONTEXT": gamma}, "inbox")
	for _, item := range arrayValue(t, gammaInbox, "items") {
		if item.(map[string]any)["id"] == directID {
			t.Fatal("direct message leaked into unrelated inbox")
		}
	}
	observed := runJSON(t, socket, nil, "observe")
	seenDirect := false
	for _, item := range arrayValue(t, observed, "items") {
		if item.(map[string]any)["id"] == directID {
			seenDirect = true
		}
	}
	if !seenDirect {
		t.Fatal("operator observe omitted direct message")
	}

	rejoinPath := filepath.Join(dir, "alpha-rejoin.json")
	rejoined := runJSON(t, socket, nil, "join", "ignored-new-handle", "--external-namespace", "test", "--external-key", "alpha", "--context", rejoinPath)
	firstID := stringValue(t, mapValue(t, joinedAlpha, "agent"), "id")
	secondID := stringValue(t, mapValue(t, rejoined, "agent"), "id")
	if firstID != secondID || rejoined["rejoined"] != true {
		t.Fatalf("rejoin = %#v; first id %s", rejoined, firstID)
	}

	var exported bytes.Buffer
	code := Run(Env{Args: []string{"--socket", socket, "export"}, Stdout: &exported, Stderr: &bytes.Buffer{}})
	if code != 0 || !strings.Contains(exported.String(), `"type":"message"`) {
		t.Fatalf("export code=%d body=%q", code, exported.String())
	}
}

func waitForSocket(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("socket was not created: %s", path)
}

func runJSON(t *testing.T, socket string, environment map[string]string, args ...string) map[string]any {
	t.Helper()
	return runJSONWithStdin(t, socket, environment, "", args...)
}

func runJSONWithStdin(t *testing.T, socket string, environment map[string]string, stdin string, args ...string) map[string]any {
	t.Helper()
	var stdout, stderr bytes.Buffer
	full := append([]string{"--socket", socket, "--json"}, args...)
	code := Run(Env{Args: full, Stdin: strings.NewReader(stdin), Stdout: &stdout, Stderr: &stderr, Getenv: func(key string) string { return environment[key] }})
	if code != 0 {
		t.Fatalf("comms %v: code=%d stderr=%s", args, code, stderr.String())
	}
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		t.Fatalf("decode %v output %q: %v", args, stdout.String(), err)
	}
	return envelope.Data
}

func runJSONArray(t *testing.T, socket string, environment map[string]string, args ...string) []map[string]any {
	t.Helper()
	var stdout, stderr bytes.Buffer
	full := append([]string{"--socket", socket, "--json"}, args...)
	code := Run(Env{Args: full, Stdout: &stdout, Stderr: &stderr, Getenv: func(key string) string { return environment[key] }})
	if code != 0 {
		t.Fatalf("comms %v: code=%d stderr=%s", args, code, stderr.String())
	}
	var envelope struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	return envelope.Data
}

func mapValue(t *testing.T, object map[string]any, key string) map[string]any {
	t.Helper()
	value, ok := object[key].(map[string]any)
	if !ok {
		t.Fatalf("%s is not an object in %#v", key, object)
	}
	return value
}
func arrayValue(t *testing.T, object map[string]any, key string) []any {
	t.Helper()
	value, ok := object[key].([]any)
	if !ok {
		t.Fatalf("%s is not an array in %#v", key, object)
	}
	return value
}
func stringValue(t *testing.T, object map[string]any, key string) string {
	t.Helper()
	value, ok := object[key].(string)
	if !ok {
		t.Fatalf("%s is not a string in %#v", key, object)
	}
	return value
}
