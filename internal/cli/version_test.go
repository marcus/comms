package cli

import (
	"bytes"
	"testing"
)

func TestVersionAliasesOffline(t *testing.T) {
	for _, jsonMode := range []bool{false, true} {
		var want string
		for _, alias := range []string{"version", "--version", "-v", "-V"} {
			args := []string{alias}
			if jsonMode {
				args = append(args, "--json")
			}
			var out, stderr bytes.Buffer
			code := Run(Env{Args: args, Stdout: &out, Stderr: &stderr, Getenv: func(string) string { return "" }})
			if code != 0 || stderr.Len() != 0 || out.Len() == 0 {
				t.Fatalf("%s: code=%d out=%s err=%s", alias, code, &out, &stderr)
			}
			if want == "" {
				want = out.String()
			} else if out.String() != want {
				t.Fatalf("%s differs from version", alias)
			}
		}
	}
}
