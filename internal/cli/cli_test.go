package cli

import (
	"bytes"
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
		{name: "help", args: []string{"help"}, wantCode: 0, wantStdout: "durable local topics"},
		{name: "hello", args: []string{"hello"}, wantCode: 0, wantStdout: "hello from comms\n"},
		{name: "version", args: []string{"version"}, wantCode: 0, wantStdout: "comms dev (unknown)\n"},
		{name: "unknown", args: []string{"nope"}, wantCode: 2, wantStderr: "unknown command \"nope\""},
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
