package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/marcus/comms/internal/help"
	"github.com/marcus/comms/internal/httpapi"
)

var (
	testBinOnce sync.Once
	testBinPath string
	testBinErr  error
)

func commsBinary(t *testing.T) string {
	t.Helper()
	testBinOnce.Do(func() {
		_, file, _, ok := runtime.Caller(0)
		if !ok {
			testBinErr = fmt.Errorf("no caller")
			return
		}
		root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
		dir, err := os.MkdirTemp("/tmp", "comms-bin-")
		if err != nil {
			testBinErr = err
			return
		}
		path := filepath.Join(dir, "comms")
		cmd := exec.Command("go", "build", "-o", path, "./cmd/comms")
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
		if out, err := cmd.CombinedOutput(); err != nil {
			testBinErr = fmt.Errorf("go build: %w\n%s", err, out)
			return
		}
		testBinPath = path
	})
	if testBinErr != nil {
		t.Fatal(testBinErr)
	}
	return testBinPath
}

func isolatedProcEnv(t *testing.T) (env []string, state, socket string) {
	t.Helper()
	root := shortTempDir(t)
	state = filepath.Join(root, "state")
	runtime := filepath.Join(root, "runtime")
	home := filepath.Join(root, "home")
	for _, dir := range []string{state, runtime, home} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	socket = filepath.Join(runtime, "comms", "comms.sock")
	env = []string{
		"HOME=" + home,
		"COMMS_STATE_DIR=" + state,
		"XDG_RUNTIME_DIR=" + runtime,
		"XDG_STATE_HOME=" + filepath.Join(root, "xdg-state"),
		"PATH=" + os.Getenv("PATH"),
		"TMPDIR=" + root,
	}
	return env, state, socket
}

func execBin(bin string, env []string, args ...string) (stdout, stderr string, code int, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = env
	var outBuf, errBuf bytes.Buffer
	cmd.Stdout = &outBuf
	cmd.Stderr = &errBuf
	runErr := cmd.Run()
	stdout, stderr = outBuf.String(), errBuf.String()
	if runErr == nil {
		return stdout, stderr, 0, nil
	}
	if ee, ok := runErr.(*exec.ExitError); ok {
		return stdout, stderr, ee.ExitCode(), nil
	}
	return stdout, stderr, -1, runErr
}

func runBinJSON(t *testing.T, bin string, env []string, args ...string) map[string]any {
	t.Helper()
	full := append([]string{"--json"}, args...)
	stdout, stderr, code, err := execBin(bin, env, full...)
	if err != nil {
		t.Fatalf("comms %v: %v", args, err)
	}
	if code != 0 {
		t.Fatalf("comms %v: code=%d stderr=%s stdout=%s", args, code, stderr, stdout)
	}
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
		t.Fatalf("decode %q: %v", stdout, err)
	}
	return envelope.Data
}

func shutdownIsolated(t *testing.T, socket string) {
	t.Helper()
	client := httpapi.NewUnixClient(socket, "")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var hs help.Handshake
	if err := client.Do(ctx, "GET", "/v1/hello", nil, nil, &hs); err != nil {
		return
	}
	var accepted help.ShutdownAccepted
	err := client.Do(ctx, "POST", "/v1/admin/shutdown", nil, map[string]string{"server_instance_id": hs.ServerInstanceID}, &accepted)
	if err != nil {
		if hs.PID > 0 {
			if proc, findErr := os.FindProcess(hs.PID); findErr == nil {
				_ = proc.Signal(syscall.SIGTERM)
			}
		}
		t.Errorf("shutdown: %v", err)
		return
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var again help.Handshake
		err := client.Do(context.Background(), "GET", "/v1/hello", nil, nil, &again)
		if err != nil && httpapi.IsAutoStartableDial(err) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("daemon still reachable after shutdown pid=%d", hs.PID)
}

func TestSteelThreadAutoStartFromJoin(t *testing.T) {
	bin := commsBinary(t)
	env, state, socket := isolatedProcEnv(t)
	t.Cleanup(func() { shutdownIsolated(t, socket) })

	stdout, stderr, code, err := execBin(bin, env, "join", "test-agent")
	if err != nil {
		t.Fatal(err)
	}
	if code != 0 {
		t.Fatalf("join code=%d stderr=%s stdout=%s", code, stderr, stdout)
	}
	if stderr != "" {
		t.Fatalf("join should be quiet on stderr: %q", stderr)
	}
	if bytes.Contains([]byte(stdout), []byte("started daemon")) || bytes.Contains([]byte(stdout), []byte("serving on")) {
		t.Fatalf("join stdout=%q", stdout)
	}

	runBinJSON(t, bin, env, "topic", "create", "project-comms")
	runBinJSON(t, bin, env, "topic", "follow", "project-comms")
	runBinJSON(t, bin, env, "publish", "project-comms", "--title", "Hello", "--body", "from auto-start")
	inbox := runBinJSON(t, bin, env, "inbox")
	items, _ := inbox["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("inbox=%#v", inbox)
	}

	hello := runBinJSON(t, bin, env, "hello")
	if hello["launch_mode"] != "auto" {
		t.Fatalf("hello=%#v", hello)
	}
	if hello["server_instance_id"] == "" || hello["pid"] == nil {
		t.Fatalf("hello missing lifecycle fields: %#v", hello)
	}
	data, err := os.ReadFile(filepath.Join(state, "server.log"))
	if err != nil {
		t.Fatalf("server.log: %v", err)
	}
	if bytes.Contains(data, []byte("serving on")) {
		t.Fatalf("daemon-child logged serving line: %s", data)
	}
}

func TestTwentyProcessesShareOneAutoDaemon(t *testing.T) {
	bin := commsBinary(t)
	env, _, socket := isolatedProcEnv(t)
	t.Cleanup(func() { shutdownIsolated(t, socket) })

	const n = 20
	var wg sync.WaitGroup
	errCh := make(chan error, n)
	idCh := make(chan string, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			stdout, stderr, code, err := execBin(bin, env, "--json", "--timeout", "20s", "agents")
			if err != nil {
				errCh <- fmt.Errorf("agents %d: %w", i, err)
				return
			}
			if code != 0 {
				errCh <- fmt.Errorf("agents %d: code=%d stderr=%s stdout=%s", i, code, stderr, stdout)
				return
			}
			stdout, stderr, code, err = execBin(bin, env, "--json", "hello")
			if err != nil {
				errCh <- fmt.Errorf("hello %d: %w", i, err)
				return
			}
			if code != 0 {
				errCh <- fmt.Errorf("hello %d: code=%d stderr=%s stdout=%s", i, code, stderr, stdout)
				return
			}
			var envelope struct {
				Data help.Handshake `json:"data"`
			}
			if err := json.Unmarshal([]byte(stdout), &envelope); err != nil {
				errCh <- fmt.Errorf("hello %d decode %q: %w", i, stdout, err)
				return
			}
			if envelope.Data.ServerInstanceID == "" || envelope.Data.LaunchMode != "auto" {
				errCh <- fmt.Errorf("hello %d: %#v", i, envelope.Data)
				return
			}
			idCh <- envelope.Data.ServerInstanceID
		}(i)
	}
	wg.Wait()
	close(errCh)
	close(idCh)
	for err := range errCh {
		t.Error(err)
	}
	ids := map[string]int{}
	for id := range idCh {
		ids[id]++
	}
	if len(ids) != 1 {
		t.Fatalf("server incarnations=%v", ids)
	}
}

func TestInspectOnlyDoesNotAutoStart(t *testing.T) {
	bin := commsBinary(t)
	env, state, socket := isolatedProcEnv(t)
	t.Cleanup(func() { shutdownIsolated(t, socket) })
	for _, command := range []string{"hello", "health", "doctor"} {
		t.Run(command, func(t *testing.T) {
			stdout, stderr, code, err := execBin(bin, env, command)
			if err != nil {
				t.Fatal(err)
			}
			if code != 5 {
				t.Fatalf("code=%d stderr=%s stdout=%s", code, stderr, stdout)
			}
		})
	}
	if _, err := os.Stat(socket); !os.IsNotExist(err) {
		t.Fatalf("socket created: %v", err)
	}
	if _, err := os.Stat(filepath.Join(state, "server.log")); !os.IsNotExist(err) {
		t.Fatalf("server.log created: %v", err)
	}
}

func TestNoAutoStartJoinDoesNotCreateDaemon(t *testing.T) {
	bin := commsBinary(t)
	env, state, socket := isolatedProcEnv(t)
	t.Cleanup(func() { shutdownIsolated(t, socket) })
	stdout, stderr, code, err := execBin(bin, env, "--no-auto-start", "--json", "join", "alice")
	if err != nil {
		t.Fatal(err)
	}
	if code != 5 {
		t.Fatalf("code=%d stderr=%s stdout=%s", code, stderr, stdout)
	}
	var envelope httpapi.ErrorEnvelope
	if err := json.Unmarshal([]byte(stderr), &envelope); err != nil {
		t.Fatalf("stderr=%s err=%v", stderr, err)
	}
	if envelope.Error.Code != "unavailable" {
		t.Fatalf("error=%#v", envelope.Error)
	}
	if _, err := os.Stat(socket); !os.IsNotExist(err) {
		t.Fatalf("socket created: %v", err)
	}
	if _, err := os.Stat(filepath.Join(state, "server.log")); !os.IsNotExist(err) {
		t.Fatalf("server.log created: %v", err)
	}
}
