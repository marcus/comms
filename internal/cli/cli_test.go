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
