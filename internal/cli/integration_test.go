package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/marcus/comms/internal/service"
)

// startDaemon runs a real service over a Unix socket for a black-box test and
// returns its directory and socket path.
func startDaemon(t *testing.T) (string, string) {
	t.Helper()
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
	return dir, socket
}

func TestBlackBoxThreeSessionConversation(t *testing.T) {
	dir, socket := startDaemon(t)

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
	receipts := runJSON(t, socket, nil, "receipts", rootID)
	foundRead := false
	for _, receipt := range arrayValue(t, receipts, "subscribers") {
		if receipt.(map[string]any)["state"] == "read" {
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

// TestBlackBoxWaitingAndInboxNoiseAcrossSessions is the real two-agent CLI
// proof for the inbox default, the agent-registration wait, and the filtered
// message wait: separate contexts, one temporary socket and state directory,
// and no outer polling loop.
func TestBlackBoxWaitingAndInboxNoiseAcrossSessions(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "comms-wait-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "comms.sock")
	ctx, cancel := context.WithCancel(context.Background())
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- service.Run(ctx, service.Config{DatabasePath: filepath.Join(dir, "comms.db"), SocketPath: socket})
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

	lead := filepath.Join(dir, "lead.json")
	worker := filepath.Join(dir, "worker.json")
	leadEnv := map[string]string{"COMMS_CONTEXT": lead}
	workerEnv := map[string]string{"COMMS_CONTEXT": worker}
	runJSON(t, socket, nil, "join", "lead", "--context", lead)

	// The orchestrator wants to brief a session that has not joined yet. It
	// waits for registration instead of sleeping and retrying a send.
	waited := make(chan map[string]any, 1)
	waitFailed := make(chan string, 1)
	go func() {
		var stdout, stderr bytes.Buffer
		code := Run(Env{
			Args:   []string{"--socket", socket, "--json", "--timeout", "10s", "agent", "wait", "@publisher"},
			Stdout: &stdout, Stderr: &stderr,
			Getenv: func(key string) string { return leadEnv[key] },
		})
		if code != 0 {
			waitFailed <- stderr.String()
			return
		}
		var envelope struct {
			Data map[string]any `json:"data"`
		}
		if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
			waitFailed <- err.Error()
			return
		}
		waited <- envelope.Data
	}()
	time.Sleep(50 * time.Millisecond)
	joined := runJSON(t, socket, nil, "join", "publisher", "--context", worker)
	select {
	case message := <-waitFailed:
		t.Fatalf("agent wait failed: %s", message)
	case agent := <-waited:
		if agent["id"] != stringValue(t, mapValue(t, joined, "agent"), "id") {
			t.Fatalf("agent wait returned %#v, want the joined publisher", agent)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("agent wait did not observe the join")
	}

	// A briefing the orchestrator sends is conversation history, not inbox
	// noise: it must not come back as its own unread work.
	briefing := runJSON(t, socket, leadEnv, "send", "@publisher", "--title", "Code freeze", "--body", "hold the build")
	briefingID := stringValue(t, briefing, "id")
	if items := arrayValue(t, runJSON(t, socket, leadEnv, "inbox", "--unread"), "items"); len(items) != 0 {
		t.Fatalf("own outbound message appeared as unread inbox work: %#v", items)
	}
	found := false
	for _, item := range arrayValue(t, runJSON(t, socket, leadEnv, "inbox", "--include-self"), "items") {
		if item.(map[string]any)["id"] == briefingID {
			found = true
		}
	}
	if !found {
		t.Fatal("--include-self did not restore the sender's own message")
	}
	if items := arrayValue(t, runJSON(t, socket, workerEnv, "inbox", "--unread"), "items"); len(items) != 1 {
		t.Fatalf("recipient inbox = %#v, want the incoming briefing", items)
	}

	// The orchestrator awaits the answer on that thread, from that agent,
	// without an outer polling loop.
	answered := make(chan map[string]any, 1)
	answerFailed := make(chan string, 1)
	go func() {
		var stdout, stderr bytes.Buffer
		code := Run(Env{
			Args:   []string{"--socket", socket, "--json", "--timeout", "10s", "wait", "--from", "@publisher", "--thread", briefingID},
			Stdout: &stdout, Stderr: &stderr,
			Getenv: func(key string) string { return leadEnv[key] },
		})
		if code != 0 {
			answerFailed <- stderr.String()
			return
		}
		var envelope struct {
			Data map[string]any `json:"data"`
		}
		if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
			answerFailed <- err.Error()
			return
		}
		answered <- envelope.Data
	}()
	time.Sleep(50 * time.Millisecond)
	runJSONWithStdin(t, socket, workerEnv, "acknowledged\n", "reply", briefingID, "-")
	select {
	case message := <-answerFailed:
		t.Fatalf("message wait failed: %s", message)
	case result := <-answered:
		items := arrayValue(t, result, "items")
		if len(items) != 1 || !strings.Contains(items[0].(map[string]any)["body"].(string), "acknowledged") {
			t.Fatalf("message wait = %#v", items)
		}
		if stringValue(t, result, "after") == "" {
			t.Fatal("message wait returned no continuation cursor")
		}
	case <-time.After(15 * time.Second):
		t.Fatal("message wait did not observe the reply")
	}

	// Waiting acknowledges nothing; read-through remains the explicit action.
	if items := arrayValue(t, runJSON(t, socket, workerEnv, "inbox", "--unread"), "items"); len(items) != 1 {
		t.Fatalf("waiting changed the recipient's unread state: %#v", items)
	}

	// A wait that finds nothing ends at its bound with the stable timeout
	// contract rather than hanging or reporting success.
	var stdout, stderr bytes.Buffer
	code := Run(Env{
		Args:   []string{"--socket", socket, "--json", "--timeout", "200ms", "wait", "--after", "not a cursor"},
		Stdout: &stdout, Stderr: &stderr,
		Getenv: func(key string) string { return leadEnv[key] },
	})
	if code != 2 {
		t.Fatalf("malformed cursor code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	// The publisher's own messages never satisfy its own wait, so this one
	// runs out its bound instead of matching the reply it just wrote.
	code = Run(Env{
		Args:   []string{"--socket", socket, "--json", "--timeout", "200ms", "wait", "--from", "@publisher"},
		Stdout: &stdout, Stderr: &stderr,
		Getenv: func(key string) string { return workerEnv[key] },
	})
	if code != 5 {
		t.Fatalf("expired wait code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(stderr.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Error.Code != "timeout" {
		t.Fatalf("expired wait error=%s body=%s", envelope.Error.Code, stderr.String())
	}
	// Human output for an expired wait must read as the routine result it is:
	// no advice to start a service that is plainly running, and no repetition
	// of the sentinel text the service already put in its message.
	stdout.Reset()
	stderr.Reset()
	code = Run(Env{
		Args:   []string{"--socket", socket, "--timeout", "200ms", "wait", "--from", "@publisher"},
		Stdout: &stdout, Stderr: &stderr,
		Getenv: func(key string) string { return workerEnv[key] },
	})
	if code != 5 {
		t.Fatalf("human wait code=%d stderr=%s", code, stderr.String())
	}
	if strings.Contains(stderr.String(), "comms serve") {
		t.Fatalf("expired wait told the caller to start a running service: %s", stderr.String())
	}
	if stderr.String() != "comms: no matching message arrived within 200ms\n" {
		t.Fatalf("expired wait message=%q; want the service's own explanation, unrepeated", stderr.String())
	}

	// An agent that never joins times out the same way, and an unparsable
	// duration is rejected before any waiting starts.
	stderr.Reset()
	if code := Run(Env{Args: []string{"--socket", socket, "--json", "--timeout", "200ms", "agent", "wait", "@never"}, Stdout: &bytes.Buffer{}, Stderr: &stderr}); code != 5 {
		t.Fatalf("absent agent wait code=%d stderr=%s", code, stderr.String())
	}
	stderr.Reset()
	if code := Run(Env{Args: []string{"--socket", socket, "--timeout", "banana", "agent", "wait", "@never"}, Stdout: &bytes.Buffer{}, Stderr: &stderr}); code != 2 {
		t.Fatalf("invalid duration code=%d stderr=%s", code, stderr.String())
	}
}

func TestTwoAgentRecentHistoryJourney(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "comms-journey-")
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

	solContext := filepath.Join(dir, "sol.json")
	terraContext := filepath.Join(dir, "terra.json")
	runJSON(t, socket, nil, "join", "sol", "--harness", "codex", "--context", solContext)
	runJSON(t, socket, nil, "join", "terra", "--harness", "gemini", "--context", terraContext)

	solEnv := map[string]string{"COMMS_CONTEXT": solContext}
	terraEnv := map[string]string{"COMMS_CONTEXT": terraContext}

	runJSON(t, socket, solEnv, "topic", "create", "intersections")
	runJSON(t, socket, solEnv, "topic", "follow", "intersections")
	runJSON(t, socket, terraEnv, "topic", "follow", "intersections")

	// Publish 25 messages alternating between sol and terra
	var rootMsgID string
	for i := 1; i <= 25; i++ {
		sender := solEnv
		author := "sol"
		if i%2 == 0 {
			sender = terraEnv
			author = "terra"
		}
		title := fmt.Sprintf("coord-update-%02d", i)
		body := fmt.Sprintf("Update %d from %s regarding intersection ownership and reviews", i, author)
		res := runJSON(t, socket, sender, "publish", "intersections", "--title", title, "--body", body)
		if i == 1 {
			rootMsgID = stringValue(t, res, "id")
		}
	}

	// Terra checks recent history with --latest --limit 5
	latestRes := runJSON(t, socket, terraEnv, "topic", "messages", "intersections", "--latest", "--limit", "5")
	items := arrayValue(t, latestRes, "items")
	if len(items) != 5 {
		t.Fatalf("expected 5 items in latest page, got %d", len(items))
	}
	// Oldest-to-newest order within the latest window: updates 21, 22, 23, 24, 25
	wantTitles := []string{"coord-update-21", "coord-update-22", "coord-update-23", "coord-update-24", "coord-update-25"}
	for i, want := range wantTitles {
		item := items[i].(map[string]any)
		if item["title"] != want {
			t.Errorf("item %d title = %v, want %v", i, item["title"], want)
		}
	}

	nextCursor := stringValue(t, latestRes, "next_cursor")
	if nextCursor == "" {
		t.Fatal("expected next cursor on latest page 1")
	}

	// Verify Terra's read cursor was NOT advanced by reading latest history
	subsDoc := runJSON(t, socket, terraEnv, "subscriptions")
	subs := arrayValue(t, subsDoc, "items")
	if len(subs) == 0 {
		t.Fatal("expected terra subscription")
	}
	sub0 := subs[0].(map[string]any)
	if readSeq := sub0["read_through_sequence"].(float64); readSeq != 0 {
		t.Fatalf("read_through_sequence advanced to %v, want 0", readSeq)
	}

	// Terra pages back using cursor
	page2Res := runJSON(t, socket, terraEnv, "topic", "messages", "intersections", "--latest", "--limit", "5", "--cursor", nextCursor)
	items2 := arrayValue(t, page2Res, "items")
	if len(items2) != 5 {
		t.Fatalf("expected 5 items in latest page 2, got %d", len(items2))
	}
	wantTitles2 := []string{"coord-update-16", "coord-update-17", "coord-update-18", "coord-update-19", "coord-update-20"}
	for i, want := range wantTitles2 {
		item := items2[i].(map[string]any)
		if item["title"] != want {
			t.Errorf("page 2 item %d title = %v, want %v", i, item["title"], want)
		}
	}

	// Terra adds 6 replies in the thread of rootMsgID
	for i := 1; i <= 6; i++ {
		runJSON(t, socket, terraEnv, "reply", rootMsgID, "--title", fmt.Sprintf("review-note-%02d", i), "--body", fmt.Sprintf("review detail %d", i))
	}

	// Sol queries thread latest with limit 3
	threadRes := runJSON(t, socket, solEnv, "thread", rootMsgID, "--latest", "--limit", "3")
	threadItems := arrayValue(t, threadRes, "items")
	if len(threadItems) != 3 {
		t.Fatalf("expected 3 items in thread latest, got %d", len(threadItems))
	}
	wantThreadTitles := []string{"review-note-04", "review-note-05", "review-note-06"}
	for i, want := range wantThreadTitles {
		item := threadItems[i].(map[string]any)
		if item["title"] != want {
			t.Errorf("thread item %d title = %v, want %v", i, item["title"], want)
		}
	}

	// Test CLI human output with --latest
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Run(Env{
		Args:   []string{"--socket", socket, "topic", "messages", "intersections", "--latest", "--limit", "3"},
		Stdout: &stdout,
		Stderr: &stderr,
		Getenv: func(key string) string { return solEnv[key] },
	})
	if code != 0 {
		t.Fatalf("human topic messages --latest code=%d stderr=%s", code, stderr.String())
	}
	outStr := stdout.String()
	for _, title := range []string{"review-note-06", "review-note-05", "review-note-04"} {
		if !strings.Contains(outStr, title) {
			t.Errorf("human output omits %s:\n%s", title, outStr)
		}
	}
	if !strings.Contains(outStr, "topic:") || !strings.Contains(outStr, "author:") || !strings.Contains(outStr, "reply-to:") {
		t.Fatalf("human output missing routing context:\n%s", outStr)
	}

	// Test CLI human output with --compact
	stdout.Reset()
	stderr.Reset()
	code = Run(Env{
		Args:   []string{"--socket", socket, "--compact", "topic", "messages", "intersections", "--latest", "--limit", "3"},
		Stdout: &stdout,
		Stderr: &stderr,
		Getenv: func(key string) string { return solEnv[key] },
	})
	if code != 0 {
		t.Fatalf("human topic messages --compact code=%d stderr=%s", code, stderr.String())
	}
	compactStr := stdout.String()
	if !strings.Contains(compactStr, "topic:") || !strings.Contains(compactStr, "author:") {
		t.Fatalf("compact output missing routing context:\n%s", compactStr)
	}
}

// awaitReceipts polls until the receipts report satisfies want, proving the
// recorder's background flush reaches the store without a fixed sleep.
func awaitReceipts(t *testing.T, socket, message string, want func(map[string]any) bool) map[string]any {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var report map[string]any
	for time.Now().Before(deadline) {
		report = runJSON(t, socket, nil, "receipts", message)
		if want(report) {
			return report
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("receipts never reached the expected state: %#v", report)
	return nil
}

func receiptFor(t *testing.T, report map[string]any, list, handle string) map[string]any {
	t.Helper()
	for _, raw := range arrayValue(t, report, list) {
		entry := raw.(map[string]any)
		if mapValue(t, entry, "agent")["handle"] == handle {
			return entry
		}
	}
	return nil
}

func TestRetrievalTrackingSurvivesTheWholeStack(t *testing.T) {
	dir, socket := startDaemon(t)
	author := filepath.Join(dir, "author.json")
	reader := filepath.Join(dir, "reader.json")
	inspector := filepath.Join(dir, "inspector.json")
	watcher := filepath.Join(dir, "watcher.json")
	runJSON(t, socket, nil, "join", "author", "--harness", "codex", "--context", author)
	runJSON(t, socket, nil, "join", "reader", "--harness", "claude-code", "--context", reader)
	runJSON(t, socket, nil, "join", "inspector", "--context", inspector)
	runJSON(t, socket, nil, "join", "watcher", "--context", watcher)
	authorEnv := map[string]string{"COMMS_CONTEXT": author}
	readerEnv := map[string]string{"COMMS_CONTEXT": reader}

	runJSON(t, socket, authorEnv, "topic", "create", "retrieval")
	for _, path := range []string{author, reader} {
		runJSON(t, socket, map[string]string{"COMMS_CONTEXT": path}, "topic", "follow", "retrieval")
	}
	long := "Headline that fits\n\n" + strings.Repeat("detail ", 200)
	var lastID string
	for i := 0; i < 25; i++ {
		published := runJSON(t, socket, authorEnv, "publish", "retrieval", "--title", fmt.Sprintf("note %d", i), "--body", long)
		lastID = stringValue(t, published, "id")
	}

	// The default inbox is a lean page of previews.
	box := runJSON(t, socket, readerEnv, "inbox")
	items := arrayValue(t, box, "items")
	if len(items) != 20 {
		t.Fatalf("default inbox returned %d items, want 20", len(items))
	}
	first := items[0].(map[string]any)
	if first["body"] != "Headline that fits" || first["body_truncated"] != true {
		t.Fatalf("inbox item was not a preview: %#v", first)
	}
	full := runJSON(t, socket, readerEnv, "inbox", "--full", "--limit", "1")
	fullItem := arrayValue(t, full, "items")[0].(map[string]any)
	if fullItem["body"] != long {
		t.Fatalf("--full returned a preview: %#v", fullItem)
	}
	if _, marked := fullItem["body_truncated"]; marked {
		t.Fatalf("--full marked a body truncated: %#v", fullItem)
	}

	// The reader saw the newest message in both listings; the full listing
	// escalates it to an inspection.
	report := awaitReceipts(t, socket, lastID, func(report map[string]any) bool {
		entry := receiptFor(t, report, "subscribers", "reader")
		return entry != nil && entry["seen_at"] != nil && entry["inspected_at"] != nil
	})
	readerReceipt := receiptFor(t, report, "subscribers", "reader")
	if readerReceipt["state"] != "unread" {
		t.Fatalf("retrieval acknowledged the message: %#v", readerReceipt)
	}
	// Retrieval never moves a cursor: everything is still unread.
	unread := runJSON(t, socket, readerEnv, "inbox", "--unread", "--limit", "25")
	if got := len(arrayValue(t, unread, "items")); got != 25 {
		t.Fatalf("retrieval advanced a cursor: %d unread messages remain", got)
	}

	// A non-subscriber that peeks is an inspector, not a subscriber.
	runJSON(t, socket, map[string]string{"COMMS_CONTEXT": inspector}, "peek", lastID)
	// An operator surface records nothing, even with an identity attached.
	runJSON(t, socket, map[string]string{"COMMS_CONTEXT": watcher}, "observe", "--limit", "5")
	report = awaitReceipts(t, socket, lastID, func(report map[string]any) bool {
		return receiptFor(t, report, "inspectors", "inspector") != nil
	})
	peeked := receiptFor(t, report, "inspectors", "inspector")
	if peeked["inspected_at"] == nil {
		t.Fatalf("peek did not record a full inspection: %#v", peeked)
	}
	if entry := receiptFor(t, report, "inspectors", "watcher"); entry != nil {
		t.Fatalf("observe recorded a retrieval: %#v", entry)
	}
	if entry := receiptFor(t, report, "inspectors", "author"); entry != nil {
		t.Fatalf("the author was recorded as a reader: %#v", entry)
	}

	// Acknowledging adds the cursor fact without discarding the retrieval facts.
	runJSON(t, socket, readerEnv, "read-through", lastID)
	report = awaitReceipts(t, socket, lastID, func(report map[string]any) bool {
		entry := receiptFor(t, report, "subscribers", "reader")
		return entry != nil && entry["state"] == "read"
	})
	readerReceipt = receiptFor(t, report, "subscribers", "reader")
	if readerReceipt["read_at"] == nil || readerReceipt["seen_at"] == nil || readerReceipt["inspected_at"] == nil {
		t.Fatalf("acknowledgment dropped retrieval facts: %#v", readerReceipt)
	}

	// Human output names both dimensions.
	var human bytes.Buffer
	if code := Run(Env{Args: []string{"--socket", socket, "receipts", lastID}, Stdout: &human, Stderr: &bytes.Buffer{}}); code != 0 {
		t.Fatalf("human receipts code=%d", code)
	}
	for _, want := range []string{"Subscribers:", "@reader", "read ", "Inspectors (not subscribed):", "@inspector", "inspected full body"} {
		if !strings.Contains(human.String(), want) {
			t.Fatalf("human receipts omit %q:\n%s", want, human.String())
		}
	}

	// Retrieval rows are diagnostic output.
	var exported bytes.Buffer
	if code := Run(Env{Args: []string{"--socket", socket, "export"}, Stdout: &exported, Stderr: &bytes.Buffer{}}); code != 0 {
		t.Fatalf("export code=%d", code)
	}
	if !strings.Contains(exported.String(), `"type":"message_retrieval"`) {
		t.Fatal("export omitted retrieval rows")
	}

	// Doctor reports the recorder so shed bookkeeping is never silent.
	doctor := runJSON(t, socket, nil, "doctor")
	checks := mapValue(t, doctor, "checks")
	recorder, _ := checks["retrieval_recorder"].(string)
	if !strings.Contains(recorder, "dropped=0") {
		t.Fatalf("doctor recorder check=%q", recorder)
	}
}

// Two agents joining from separate tmux panes each keep their own identity,
// and a join that would switch a shared implicit context to another agent is
// refused until --replace (td-1c02ac).
func TestBlackBoxConcurrentSessionsKeepTheirIdentity(t *testing.T) {
	_, socket := startDaemon(t)
	state := t.TempDir()
	pane := func(id string) map[string]string {
		return map[string]string{"COMMS_STATE_DIR": state, "TMUX": "/tmp/tmux-test/default,1,0", "TMUX_PANE": id}
	}
	alice, bob := pane("%1"), pane("%2")
	joinedAlice := runJSON(t, socket, alice, "join", "alice")
	if joinedAlice["identity_source"] != "session_context" || joinedAlice["session"] != "tmux" {
		t.Fatalf("alice join = %#v, want a tmux session context", joinedAlice)
	}
	runJSON(t, socket, bob, "join", "bob")
	if got := stringValue(t, mapValue(t, runJSON(t, socket, alice, "whoami"), "agent"), "handle"); got != "alice" {
		t.Fatalf("alice's pane is %q after bob joined", got)
	}
	runJSON(t, socket, bob, "send", "@alice", "--title", "hi", "--body", "for alice")
	if items := arrayValue(t, runJSON(t, socket, alice, "inbox"), "items"); len(items) != 1 {
		t.Fatalf("alice inbox = %#v, want bob's message", items)
	}

	// A pane with no join of its own does not inherit anyone's identity.
	code, stderr := runCode(t, socket, pane("%3"), "whoami")
	if code != 2 || !strings.Contains(stderr, "this session") {
		t.Fatalf("unjoined pane whoami: code=%d stderr=%s", code, stderr)
	}

	// Outside tmux the machine-wide context is shared, so a second agent's
	// join must not silently take it over.
	shared := map[string]string{"COMMS_STATE_DIR": state}
	runJSON(t, socket, shared, "join", "carol")
	code, stderr = runCode(t, socket, shared, "join", "dave")
	if code != 4 || !strings.Contains(stderr, "@carol") || !strings.Contains(stderr, "--replace") {
		t.Fatalf("takeover join: code=%d stderr=%s", code, stderr)
	}
	if got := stringValue(t, mapValue(t, runJSON(t, socket, shared, "whoami"), "agent"), "handle"); got != "carol" {
		t.Fatalf("shared identity is %q after a refused takeover", got)
	}
	if code, _ := runCode(t, socket, shared, "agent", "wait", "@dave", "--timeout", "10ms"); code != 5 {
		t.Fatalf("a refused takeover registered @dave (agent wait code=%d)", code)
	}
	runJSON(t, socket, shared, "join", "dave", "--replace")
	if got := stringValue(t, mapValue(t, runJSON(t, socket, shared, "whoami"), "agent"), "handle"); got != "dave" {
		t.Fatalf("shared identity is %q after --replace", got)
	}
	// A retired agent leaves nothing to take over.
	runJSON(t, socket, shared, "agent", "retire", "dave")
	runJSON(t, socket, shared, "join", "frank")

	// COMMS_SESSION scopes identity without tmux.
	scoped := map[string]string{"COMMS_STATE_DIR": state, "COMMS_SESSION": "worker-1"}
	if out := runJSON(t, socket, scoped, "join", "erin"); out["session"] != "COMMS_SESSION" {
		t.Fatalf("COMMS_SESSION join = %#v", out)
	}
	if got := stringValue(t, mapValue(t, runJSON(t, socket, shared, "whoami"), "agent"), "handle"); got != "frank" {
		t.Fatalf("COMMS_SESSION join changed the shared identity to %q", got)
	}
}

func runCode(t *testing.T, socket string, environment map[string]string, args ...string) (int, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	full := append([]string{"--socket", socket}, args...)
	code := Run(Env{Args: full, Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr, Getenv: func(key string) string { return environment[key] }})
	return code, stderr.String()
}
