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

	"github.com/marcus/comms/internal/app"
)

func TestRun(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.sock")
	tests := []struct {
		name       string
		args       []string
		wantCode   int
		wantStdout string
		wantStderr string
	}{
		{name: "default help", wantCode: 0, wantStdout: "Usage:\n"},
		{name: "help", args: []string{"help"}, wantCode: 0, wantStdout: "short-lived local topics"},
		{name: "subcommand help", args: []string{"topic", "create", "--help"}, wantCode: 0, wantStdout: "topic create NAME [--description TEXT]"},
		{name: "wait trailing help", args: []string{"wait", "--help"}, wantCode: 0, wantStdout: "Wait for matching incoming messages"},
		{name: "comms help wait", args: []string{"help", "wait"}, wantCode: 0, wantStdout: "Wait for matching incoming messages"},
		{name: "topic messages trailing help", args: []string{"topic", "messages", "--help"}, wantCode: 0, wantStdout: "Read a topic"},
		{name: "comms help topic messages", args: []string{"help", "topic", "messages"}, wantCode: 0, wantStdout: "Read a topic"},
		{name: "group help trailing", args: []string{"topic", "--help"}, wantCode: 0, wantStdout: "comms topic COMMAND"},
		{name: "group help subcommand", args: []string{"help", "topic"}, wantCode: 0, wantStdout: "comms topic COMMAND"},
		{name: "unknown trailing help", args: []string{"bogus", "--help"}, wantCode: 2, wantStderr: "unknown command \"bogus\""},
		{name: "unknown comms help", args: []string{"help", "bogus"}, wantCode: 2, wantStderr: "unknown command \"bogus\""},
		{name: "help with missing socket has zero side effects", args: []string{"--socket", missing, "wait", "--help"}, wantCode: 0, wantStdout: "Wait for matching incoming messages"},
		{name: "hello requires service", args: []string{"--socket", missing, "hello"}, wantCode: 5, wantStderr: "Start the service with 'comms serve'."},
		{name: "version", args: []string{"version"}, wantCode: 0, wantStdout: "comms dev (unknown)\n"},
		{name: "unknown", args: []string{"nope"}, wantCode: 2, wantStderr: "unknown command \"nope\""},
		{name: "serve exclusive launch flags", args: []string{"serve", "--daemon-child", "--supervised"}, wantCode: 2, wantStderr: "mutually exclusive"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			got := Run(Env{Args: tt.args, Stdout: &stdout, Stderr: &stderr})
			if got != tt.wantCode {
				t.Fatalf("Run() code = %d, want %d", got, tt.wantCode)
			}
			if !strings.Contains(stdout.String(), tt.wantStdout) {
				t.Errorf("stdout = %q, want it to contain %q", stdout.String(), tt.wantStdout)
			}
			if !strings.Contains(stderr.String(), tt.wantStderr) {
				t.Errorf("stderr = %q, want it to contain %q", stderr.String(), tt.wantStderr)
			}
		})
	}
}

func TestExportFailurePreservesExistingFile(t *testing.T) {
	dir := t.TempDir()
	output := filepath.Join(dir, "existing.jsonl")
	if err := os.WriteFile(output, []byte("keep me\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	code := Run(Env{Args: []string{"--no-auto-start", "--socket", filepath.Join(dir, "missing.sock"), "export", "--output", output}, Stderr: &stderr})
	if code != 5 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	data, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "keep me\n" {
		t.Fatalf("existing export was changed: %q", data)
	}
}

func TestTopicUpdateRejectsExtraArgumentBeforeMutation(t *testing.T) {
	var stderr bytes.Buffer
	code := Run(Env{Args: []string{"topic", "update", "demo", "--name", "renamed", "UNEXPECTED"}, Stderr: &stderr})
	if code != 2 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
}

// Both stop the command with exit 5, but a caller that went away and a
// deadline that expired are separate stable codes so a waiting caller can tell
// which happened.
func TestJSONSeparatesCancellationFromDeadline(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		context func() (context.Context, context.CancelFunc)
		want    string
	}{
		{"cancelled", func() (context.Context, context.CancelFunc) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			return ctx, func() {}
		}, "canceled"},
		{"deadline", func() (context.Context, context.CancelFunc) {
			return context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		}, "timeout"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			ctx, cancel := testCase.context()
			defer cancel()
			var stderr bytes.Buffer
			code := Run(Env{Args: []string{"--json", "--socket", filepath.Join(t.TempDir(), "missing.sock"), "health"}, Context: ctx, Stderr: &stderr})
			if code != 5 {
				t.Fatalf("code=%d stderr=%s", code, stderr.String())
			}
			var envelope struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal(stderr.Bytes(), &envelope); err != nil {
				t.Fatal(err)
			}
			if envelope.Error.Code != testCase.want {
				t.Fatalf("error=%s, want %s; body=%s", envelope.Error.Code, testCase.want, stderr.String())
			}
		})
	}
}

// A long wait bound is patience for the operation the caller asked for, not
// for bringing a daemon up, and the transport margin must not be spent on
// readiness before the request is even issued.
func TestWaitDeadlinesDoNotWidenServiceReadiness(t *testing.T) {
	r := &runner{env: Env{Context: context.Background()}, g: globals{timeout: time.Hour, timeoutSet: true}}
	bound, err := r.waitBound()
	if err != nil {
		t.Fatal(err)
	}
	if bound != time.Hour {
		t.Fatalf("wait bound=%s, want the global --timeout", bound)
	}
	if r.cmdCtx != nil {
		t.Fatal("the request deadline started before any readiness work")
	}

	before := time.Now()
	readyDeadline, ok := r.readyContext().Deadline()
	if !ok || readyDeadline.After(before.Add(defaultCommandTimeout+time.Second)) {
		t.Fatalf("readiness deadline=%v, want it capped at %s", readyDeadline, defaultCommandTimeout)
	}
	requestDeadline, ok := r.commandContext().Deadline()
	if !ok || requestDeadline.Before(before.Add(time.Hour)) {
		t.Fatalf("request deadline=%v, want it past the %s wait bound", requestDeadline, bound)
	}
	r.readyCancel()
	r.cmdCancel()

	// An ordinary command keeps one bound for everything.
	ordinary := &runner{env: Env{Context: context.Background()}, g: globals{timeout: 2 * time.Second}}
	ordinaryDeadline, _ := ordinary.commandContext().Deadline()
	if ordinaryDeadline.After(time.Now().Add(3 * time.Second)) {
		t.Fatalf("ordinary request deadline=%v", ordinaryDeadline)
	}
	ordinary.cmdCancel()

	// A wait can never be unbounded.
	for name, g := range map[string]globals{
		"zero":     {timeout: 0, timeoutSet: true},
		"negative": {timeout: -time.Second, timeoutSet: true},
		"too long": {timeout: 2 * app.MaxWaitTimeout, timeoutSet: true},
	} {
		unbounded := &runner{env: Env{Context: context.Background()}, g: g}
		if _, err := unbounded.waitBound(); err == nil {
			t.Fatalf("%s wait bound was accepted", name)
		}
	}
	// Without an explicit --timeout a wait still gets a documented bound.
	defaulted := &runner{env: Env{Context: context.Background()}, g: globals{timeout: defaultCommandTimeout}}
	if got, err := defaulted.waitBound(); err != nil || got != app.DefaultWaitTimeout {
		t.Fatalf("default wait bound=%s err=%v", got, err)
	}
}

func TestCommandHelpGolden(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		contains []string
	}{
		{
			name: "wait help golden",
			args: []string{"wait", "--help"},
			contains: []string{
				"Usage:\n  comms wait [--from AGENT] [--thread MESSAGE_ID] [--after CURSOR] [--include-self] [--limit N] [--timeout DURATION]\n",
				"Summary:\n  Wait for matching incoming messages\n",
				"Preexisting unread matches return immediately",
				"distinct from a subscription read cursor",
				"advance a read-through cursor",
				"Parameters / Flags:",
				"--from AGENT",
				"--thread MESSAGE_ID",
				"--after CURSOR",
				"--include-self",
				"--limit N",
				"--timeout DURATION",
				"Identity:\n  Requires an active agent session",
			},
		},
		{
			name: "topic messages help golden",
			args: []string{"topic", "messages", "--help"},
			contains: []string{
				"Usage:\n  comms topic messages TOPIC [--latest] [--limit N] [--cursor CURSOR]\n",
				"Summary:\n  Read a topic\n",
				"ascending sequence order",
				"--latest",
				"Cursors are opaque sequence tokens",
				"Parameters / Flags:",
				"TOPIC (positional)",
				"--latest",
				"--limit N",
				"--cursor CURSOR",
				"Identity:\n  CLI-only; does not require an agent identity",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := Run(Env{Args: tt.args, Stdout: &stdout, Stderr: &stderr})
			if code != 0 {
				t.Fatalf("exit code = %d, stderr = %s", code, stderr.String())
			}
			out := stdout.String()
			for _, piece := range tt.contains {
				if !strings.Contains(out, piece) {
					t.Errorf("output missing %q:\n%s", piece, out)
				}
			}
		})
	}
}

func TestHumanMessageOutputRoutingContextAndGolden(t *testing.T) {
	// 1. Public topic root message with author context
	publicRoot := map[string]any{
		"id":             "msg_01j70000000000000000000001",
		"topic_id":       "top_01j70000000000000000000001",
		"sequence":       float64(64),
		"author_id":      "agt_01j70000000000000000000001",
		"author_context": map[string]any{"harness": "codex", "project": "intersections"},
		"title":          "Public Root Title",
		"body":           "This is the full multiline body\nof the public root message.\nLine 3.",
		"created_at":     "2026-09-04T12:00:00Z",
	}

	var buf bytes.Buffer
	if err := renderHuman(&buf, publicRoot, false); err != nil {
		t.Fatal(err)
	}
	wantPublicRoot := "msg_01j70000000000000000000001  #64  topic:top_01j70000000000000000000001  author:agt_01j70000000000000000000001 (codex/intersections)  2026-09-04T12:00:00Z\n" +
		"Title: Public Root Title\n" +
		"This is the full multiline body\nof the public root message.\nLine 3.\n\n"
	if buf.String() != wantPublicRoot {
		t.Fatalf("public root rendered =\n%q\nwant:\n%q", buf.String(), wantPublicRoot)
	}

	// 2. Direct topic root message
	directRoot := map[string]any{
		"id":         "msg_01j70000000000000000000002",
		"topic_id":   "top_01j70000000000000000000002",
		"sequence":   float64(1),
		"author_id":  "agt_01j70000000000000000000002",
		"title":      "Direct Check",
		"body":       "Direct message body between two agents.",
		"created_at": "2026-09-04T12:05:00Z",
	}
	buf.Reset()
	if err := renderHuman(&buf, directRoot, false); err != nil {
		t.Fatal(err)
	}
	wantDirectRoot := "msg_01j70000000000000000000002  #1  topic:top_01j70000000000000000000002  author:agt_01j70000000000000000000002  2026-09-04T12:05:00Z\n" +
		"Title: Direct Check\n" +
		"Direct message body between two agents.\n\n"
	if buf.String() != wantDirectRoot {
		t.Fatalf("direct root rendered =\n%q\nwant:\n%q", buf.String(), wantDirectRoot)
	}

	// 3. Reply message with reply linkage
	replyMsg := map[string]any{
		"id":             "msg_01j70000000000000000000003",
		"topic_id":       "top_01j70000000000000000000001",
		"sequence":       float64(65),
		"author_id":      "agt_01j70000000000000000000003",
		"author_context": map[string]any{"harness": "gemini"},
		"in_reply_to":    "msg_01j70000000000000000000001",
		"thread_root_id": "msg_01j70000000000000000000001",
		"title":          "Public Root Title",
		"body":           "Reply agreeing to ownership handoff.",
		"created_at":     "2026-09-04T12:10:00Z",
	}
	buf.Reset()
	if err := renderHuman(&buf, replyMsg, false); err != nil {
		t.Fatal(err)
	}
	wantReply := "msg_01j70000000000000000000003  #65  topic:top_01j70000000000000000000001  author:agt_01j70000000000000000000003 (gemini)  reply-to:msg_01j70000000000000000000001  2026-09-04T12:10:00Z\n" +
		"Title: Public Root Title\n" +
		"Reply agreeing to ownership handoff.\n\n"
	if buf.String() != wantReply {
		t.Fatalf("reply rendered =\n%q\nwant:\n%q", buf.String(), wantReply)
	}

	// 4. Large multiline body in default mode: retains entire full body
	largeLines := make([]string, 50)
	for i := range largeLines {
		largeLines[i] = fmt.Sprintf("Log output line %02d with test data", i+1)
	}
	largeBodyText := strings.Join(largeLines, "\n")
	largeMsg := map[string]any{
		"id":         "msg_01j70000000000000000000004",
		"topic_id":   "top_01j70000000000000000000001",
		"sequence":   float64(66),
		"author_id":  "agt_01j70000000000000000000001",
		"title":      "Build Log",
		"body":       largeBodyText,
		"created_at": "2026-09-04T12:15:00Z",
	}
	buf.Reset()
	if err := renderHuman(&buf, largeMsg, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "Log output line 50 with test data") {
		t.Fatal("default human rendering silently truncated large body")
	}

	// 5. Large multiline body in compact mode: truncated with explicit marker and hint
	buf.Reset()
	if err := renderHuman(&buf, largeMsg, true); err != nil {
		t.Fatal(err)
	}
	compactOut := buf.String()
	if !strings.Contains(compactOut, "... [truncated; use 'comms peek msg_01j70000000000000000000004' for full body]") {
		t.Fatalf("compact output missing explicit truncation marker:\n%s", compactOut)
	}
	if strings.Contains(compactOut, "Log output line 50") {
		t.Fatalf("compact output did not truncate:\n%s", compactOut)
	}

	// 6. Sequence collision: two messages with #64 from different topics in a multi-topic list
	collidingMsg := map[string]any{
		"id":         "msg_01j70000000000000000000099",
		"topic_id":   "top_01j70000000000000000000099",
		"sequence":   float64(64),
		"author_id":  "agt_01j70000000000000000000099",
		"title":      "Different Topic Message",
		"body":       "Different topic content.",
		"created_at": "2026-09-04T12:20:00Z",
	}
	list := map[string]any{
		"items": []any{publicRoot, collidingMsg},
	}
	buf.Reset()
	if err := renderHuman(&buf, list, false); err != nil {
		t.Fatal(err)
	}
	listOut := buf.String()
	if !strings.Contains(listOut, "topic:top_01j70000000000000000000001") ||
		!strings.Contains(listOut, "topic:top_01j70000000000000000000099") {
		t.Fatalf("multi-topic list did not disambiguate topics:\n%s", listOut)
	}
	if !strings.Contains(listOut, "author:agt_01j70000000000000000000001") ||
		!strings.Contains(listOut, "author:agt_01j70000000000000000000099") {
		t.Fatalf("multi-topic list did not disambiguate authors:\n%s", listOut)
	}
}
