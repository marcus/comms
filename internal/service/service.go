// Package service owns Comms process lifecycle, SQLite ownership, and listeners.
package service

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/marcus/comms/internal/app"
	"github.com/marcus/comms/internal/domain"
	"github.com/marcus/comms/internal/httpapi"
	"github.com/marcus/comms/internal/store"
)

type Config struct {
	DatabasePath string
	SocketPath   string
	Listen       string
	ReadPoolSize int
	WriterQueue  int
}

func ResolveSocketPath(create bool) (string, error) {
	if explicit := os.Getenv("COMMS_SOCKET"); explicit != "" {
		if create {
			if err := os.MkdirAll(filepath.Dir(explicit), 0o700); err != nil {
				return "", err
			}
		}
		return explicit, nil
	}
	if runtimeDir := os.Getenv("XDG_RUNTIME_DIR"); runtimeDir != "" {
		dir := filepath.Join(runtimeDir, "comms")
		if create {
			if err := os.MkdirAll(dir, 0o700); err != nil {
				return "", err
			}
		}
		return filepath.Join(dir, "comms.sock"), nil
	}
	database, err := store.ResolveStatePath(create)
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(database), "comms.sock"), nil
}

func Run(ctx context.Context, cfg Config) error {
	adapter, err := store.Open(ctx, store.Options{Path: cfg.DatabasePath, ReadConnections: cfg.ReadPoolSize, QueueDepth: cfg.WriterQueue})
	if err != nil {
		return err
	}
	defer adapter.Close()

	application := app.NewService(adapter, domain.UTCClock{})
	server := &http.Server{Handler: httpapi.NewHandler(application), ReadHeaderTimeout: 5 * time.Second}
	listener, cleanup, err := listen(cfg)
	if err != nil {
		return err
	}
	defer cleanup()

	serveResult := make(chan error, 1)
	go func() {
		err := server.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		serveResult <- err
	}()
	select {
	case err := <-serveResult:
		return err
	case <-ctx.Done():
		shutdownContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		shutdownErr := server.Shutdown(shutdownContext)
		serveErr := <-serveResult
		return errors.Join(shutdownErr, serveErr)
	}
}

func listen(cfg Config) (net.Listener, func(), error) {
	if cfg.Listen != "" {
		host, _, err := net.SplitHostPort(cfg.Listen)
		if err != nil {
			return nil, nil, fmt.Errorf("invalid listen address: %w", err)
		}
		if host == "" {
			host = "127.0.0.1"
			cfg.Listen = net.JoinHostPort(host, strings.TrimPrefix(cfg.Listen, ":"))
		}
		if host != "127.0.0.1" && host != "::1" && host != "localhost" {
			return nil, nil, fmt.Errorf("refusing non-loopback listen address %q", cfg.Listen)
		}
		listener, err := net.Listen("tcp", cfg.Listen)
		return listener, func() {}, err
	}
	path := cfg.SocketPath
	if path == "" {
		var err error
		path, err = ResolveSocketPath(true)
		if err != nil {
			return nil, nil, err
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, nil, err
	}
	if _, err := os.Lstat(path); err == nil {
		connection, dialErr := net.DialTimeout("unix", path, 250*time.Millisecond)
		if dialErr == nil {
			_ = connection.Close()
			return nil, nil, fmt.Errorf("%w: another service is listening on %s", app.ErrUnavailable, path)
		}
		if err := os.Remove(path); err != nil {
			return nil, nil, fmt.Errorf("remove stale socket: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, nil, err
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = listener.Close()
		_ = os.Remove(path)
		return nil, nil, err
	}
	return listener, func() { _ = listener.Close(); _ = os.Remove(path) }, nil
}
