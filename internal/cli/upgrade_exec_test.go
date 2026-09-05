package cli

import (
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	_ "modernc.org/sqlite"
)

var (
	versionedMu    sync.Mutex
	versionedCache = map[string]string{}
)

func versionedBinary(t *testing.T, version, commit string) string {
	t.Helper()
	key := version + "@" + commit
	versionedMu.Lock()
	defer versionedMu.Unlock()
	if path, ok := versionedCache[key]; ok {
		return path
	}
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("no caller")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	dir, err := os.MkdirTemp("/tmp", "comms-bin-")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "comms")
	ldflags := fmt.Sprintf("-X github.com/marcus/comms/pkg/buildinfo.Version=%s -X github.com/marcus/comms/pkg/buildinfo.Commit=%s", version, commit)
	cmd := exec.Command("go", "build", "-ldflags", ldflags, "-o", path, "./cmd/comms")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build %s: %v\n%s", version, err, out)
	}
	versionedCache[key] = path
	return path
}

func TestBlackBoxNewerCLIReplacesOlderAutoDaemonOnce(t *testing.T) {
	oldBin := versionedBinary(t, "v1.1.0", "oldold1")
	newBin := versionedBinary(t, "v1.2.0", "newnew1")
	env, state, socket := isolatedProcEnv(t)
	t.Cleanup(func() { shutdownIsolated(t, socket) })

	runBinJSON(t, oldBin, env, "join", "alice")
	runBinJSON(t, oldBin, env, "topic", "create", "project-comms")
	runBinJSON(t, oldBin, env, "topic", "follow", "project-comms")
	published := runBinJSON(t, newBin, env, "publish", "project-comms", "--title", "Upgrade", "--body", "once")
	if published["title"] != "Upgrade" {
		t.Fatalf("publish=%#v", published)
	}
	inbox := runBinJSON(t, newBin, env, "inbox", "--include-self")
	items, _ := inbox["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("inbox=%#v", inbox)
	}

	hello := runBinJSON(t, newBin, env, "hello")
	if hello["server_version"] != "v1.2.0" || hello["commit"] != "newnew1" || hello["launch_mode"] != "auto" {
		t.Fatalf("hello after upgrade=%#v", hello)
	}
	instance, _ := hello["server_instance_id"].(string)
	if instance == "" {
		t.Fatal("missing server_instance_id")
	}

	stdout, stderr, code, err := execBin(oldBin, env, "--json", "agents")
	if err != nil {
		t.Fatal(err)
	}
	if code != 0 {
		t.Fatalf("old agents code=%d stderr=%s stdout=%s", code, stderr, stdout)
	}
	hello = runBinJSON(t, newBin, env, "hello")
	if hello["server_version"] != "v1.2.0" || hello["commit"] != "newnew1" || hello["server_instance_id"] != instance {
		t.Fatalf("downgraded after old client: %#v", hello)
	}

	shutdownIsolated(t, socket)
	n, distinct := countIdempotency(t, filepath.Join(state, "comms.db"), "publish")
	if n != 1 || distinct != 1 {
		t.Fatalf("publish idempotency count=%d distinct=%d", n, distinct)
	}
}

func TestBlackBoxOlderCLIDoesNotDowngradeNewerAutoDaemon(t *testing.T) {
	oldBin := versionedBinary(t, "v1.1.0", "oldold1")
	newBin := versionedBinary(t, "v1.2.0", "newnew1")
	env, _, socket := isolatedProcEnv(t)
	t.Cleanup(func() { shutdownIsolated(t, socket) })

	runBinJSON(t, newBin, env, "join", "alice")
	hello := runBinJSON(t, newBin, env, "hello")
	if hello["server_version"] != "v1.2.0" || hello["launch_mode"] != "auto" {
		t.Fatalf("hello=%#v", hello)
	}
	instance, _ := hello["server_instance_id"].(string)

	stdout, stderr, code, err := execBin(oldBin, env, "--json", "agents")
	if err != nil {
		t.Fatal(err)
	}
	if code != 0 {
		t.Fatalf("old agents code=%d stderr=%s stdout=%s", code, stderr, stdout)
	}
	hello = runBinJSON(t, newBin, env, "hello")
	if hello["server_version"] != "v1.2.0" || hello["commit"] != "newnew1" || hello["server_instance_id"] != instance || hello["launch_mode"] != "auto" {
		t.Fatalf("server changed: %#v want instance %s", hello, instance)
	}
}

func countIdempotency(t *testing.T, dbPath, operation string) (count, distinct int) {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if err := db.QueryRow("SELECT count(*), count(DISTINCT request_id) FROM idempotency_keys WHERE operation=?", operation).Scan(&count, &distinct); err != nil {
		t.Fatal(err)
	}
	return count, distinct
}
