package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/marcus/comms/internal/app"
	"github.com/marcus/comms/internal/help"
	"strings"
	"testing"
)

func TestJSONErrorStreamContract(t *testing.T) {
	for _, tc := range []struct {
		err  error
		exit int
		code string
	}{
		{usage("bad flag"), 2, "invalid_argument"}, {app.ErrNotFound, 3, "not_found"}, {app.ErrConflict, 4, "conflict"},
		{context.DeadlineExceeded, 5, "timeout"}, {context.Canceled, 5, "canceled"}, {app.ErrUnavailable, 5, "unavailable"}, {errors.New("unexpected"), 1, "internal"},
	} {
		var out, stderr bytes.Buffer
		exit := fail(Env{Stdout: &out, Stderr: &stderr}, true, tc.err)
		var envelope struct {
			Error struct {
				Code    string
				Details map[string]any
			}
		}
		if err := json.Unmarshal(stderr.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		if exit != tc.exit || out.Len() != 0 || envelope.Error.Code != tc.code || envelope.Error.Details == nil {
			t.Fatalf("exit=%d out=%s err=%s", exit, &out, &stderr)
		}
	}
	var out, stderr bytes.Buffer
	if code := Run(Env{Args: []string{"--json", "--timeout", "nonsense", "wait"}, Stdout: &out, Stderr: &stderr}); code != 2 || !json.Valid(stderr.Bytes()) || out.Len() != 0 {
		t.Fatalf("global parse: code=%d out=%s err=%s", code, &out, &stderr)
	}
	for _, doc := range []string{help.CLIUsage("comms"), help.InstructionsText()} {
		if !strings.Contains(doc, "Parse stdout on exit 0 and stderr otherwise") {
			t.Fatal("missing stream contract")
		}
	}
}
