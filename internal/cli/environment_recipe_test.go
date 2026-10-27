package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marcus/comms/internal/help"
)

func TestDocumentedIsolationRecipeOverridesInheritedSelectors(t *testing.T) {
	binary := commsBinary(t)
	env, state, socket := isolatedProcEnv(t)
	env = append(env,
		"COMMS_SOCKET="+socket,
		"COMMS_CONTEXT="+filepath.Join(state, "inherited-context.json"),
		"COMMS_AGENT_ID=unrelated-real-agent",
		"COMMS_AUTO_START=off",
		"PATH="+filepath.Dir(binary)+string(os.PathListSeparator)+os.Getenv("PATH"),
	)
	_, block, ok := strings.Cut(help.EnvironmentReference, "```sh\n")
	if !ok {
		t.Fatal("missing isolation recipe")
	}
	block, _, ok = strings.Cut(block, "```")
	if !ok {
		t.Fatal("unterminated recipe")
	}
	cmd := exec.Command("sh", "-eu", "-c", block)
	cmd.Env = env
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("documented recipe failed: %v\n%s", err, out)
	}
	for _, path := range []string{socket, filepath.Join(state, "comms.db"), filepath.Join(state, "inherited-context.json")} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("recipe touched inherited selector path %s: %v", path, err)
		}
	}
}
