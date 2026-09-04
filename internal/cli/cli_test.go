package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRun(t *testing.T) {
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
		{name: "hello requires service", args: []string{"hello"}, wantCode: 5, wantStderr: "Start the service with 'comms serve'."},
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
	code := Run(Env{Args: []string{"--socket", filepath.Join(dir, "missing.sock"), "export", "--output", output}, Stderr: &stderr})
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

func TestJSONDeadlineUsesTimeoutCode(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
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
	if envelope.Error.Code != "timeout" {
		t.Fatalf("error=%s body=%s", envelope.Error.Code, stderr.String())
	}
}
