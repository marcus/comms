//go:build darwin || linux

package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

func startDetachedDaemon(spec DaemonSpec) (DaemonHandle, error) {
	if spec.Executable == "" {
		return nil, fmt.Errorf("daemon executable is required")
	}
	if spec.LogPath != "" {
		if err := os.MkdirAll(filepath.Dir(spec.LogPath), 0o700); err != nil {
			return nil, err
		}
	}
	logFile, err := os.OpenFile(spec.LogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	if err := logFile.Chmod(0o600); err != nil {
		_ = logFile.Close()
		return nil, err
	}
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		_ = logFile.Close()
		return nil, err
	}
	cmd := exec.Command(spec.Executable, spec.Args...)
	cmd.Stdin = devNull
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		_ = devNull.Close()
		return nil, err
	}
	_ = logFile.Close()
	_ = devNull.Close()
	handle := &detachedDaemon{proc: cmd.Process, done: make(chan struct{})}
	go func() {
		handle.waitErr = cmd.Wait()
		close(handle.done)
	}()
	return handle, nil
}

type detachedDaemon struct {
	proc    *os.Process
	done    chan struct{}
	waitErr error
}

func (d *detachedDaemon) PID() int { return d.proc.Pid }

func (d *detachedDaemon) Done() <-chan struct{} { return d.done }

func (d *detachedDaemon) Err() error {
	select {
	case <-d.done:
		return d.waitErr
	default:
		return nil
	}
}
