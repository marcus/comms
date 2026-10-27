package cli

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestStdinBeforeMessageFlags(t *testing.T) {
	dir, socket := startDaemon(t)
	env := map[string]string{"COMMS_CONTEXT": filepath.Join(dir, "alice.json")}
	runJSON(t, socket, env, "join", "alice")
	runJSON(t, socket, nil, "join", "bob", "--context", filepath.Join(dir, "bob.json"))
	runJSON(t, socket, env, "topic", "create", "dev")
	runJSON(t, socket, env, "topic", "follow", "dev")
	root := runJSON(t, socket, env, "publish", "dev", "--title", "root", "--body", "root")
	for _, command := range [][]string{{"publish", "dev"}, {"send", "@bob"}, {"reply", stringValue(t, root, "id")}} {
		for _, source := range [][]string{{"-"}, {"--body-file", "-"}} {
			args := append(append(append([]string{}, command...), source...), "--title", "stdin", "--metadata-json", `{"x":1}`)
			msg := runJSONWithStdin(t, socket, env, "Looks good\n", args...)
			if msg["body"] != "Looks good\n" || msg["metadata"].(map[string]any)["x"] != float64(1) {
				t.Fatalf("%v: %#v", args, msg)
			}
		}
	}
	r := runner{env: Env{Stdin: strings.NewReader("stdin")}}
	for _, args := range [][]string{{"--body", "-"}, {"--body=-"}} {
		msg, err := r.messageContent("reply", args, false)
		if err != nil || msg["body"] != "-" {
			t.Fatalf("literal dash: %#v %v", msg, err)
		}
	}
	for _, args := range [][]string{{"-", "--body", "other"}, {"-", "--body-file", "-"}, {"-", "-"}, {"-", "--bogus"}, {"unexpected"}} {
		if _, err := r.messageContent("reply", args, false); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}
