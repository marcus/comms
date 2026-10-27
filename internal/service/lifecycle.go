package service

import (
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/marcus/comms/internal/domain"
	"github.com/marcus/comms/internal/httpapi"
	"github.com/marcus/comms/pkg/buildinfo"
)

type LaunchMode string

const (
	LaunchModeAuto       LaunchMode = "auto"
	LaunchModeForeground LaunchMode = "foreground"
	LaunchModeSupervised LaunchMode = "supervised"
)

type controller struct {
	status       httpapi.LifecycleStatus
	shutdownCh   chan struct{}
	shutdownOnce sync.Once
	ready        atomic.Bool
	stopping     atomic.Bool
}

func newController(cfg Config) (*controller, error) {
	id, err := domain.NewServerInstanceID()
	if err != nil {
		return nil, err
	}
	socket := ""
	if cfg.Listen == "" {
		socket = cfg.SocketPath
	}
	return &controller{
		status: httpapi.LifecycleStatus{
			ServerInstanceID: string(id),
			PID:              os.Getpid(),
			StartedAt:        time.Now().UTC(),
			LaunchMode:       string(cfg.LaunchMode),
			Version:          buildinfo.Version,
			Commit:           buildinfo.Commit,
			SocketPath:       socket,
			DatabasePath:     cfg.DatabasePath,
		},
		shutdownCh: make(chan struct{}),
	}, nil
}

func (c *controller) Status() httpapi.LifecycleStatus {
	status := c.status
	status.Readiness = "starting"
	if c.ready.Load() {
		status.Readiness = "ready"
	}
	if c.stopping.Load() {
		status.Readiness = "stopping"
	}
	return status
}

func (c *controller) Done() <-chan struct{} { return c.shutdownCh }

func (c *controller) RequestShutdown(expectedID string) error {
	if expectedID != c.status.ServerInstanceID {
		return httpapi.ErrServerInstanceChanged
	}
	c.shutdownOnce.Do(func() { c.stopping.Store(true); close(c.shutdownCh) })
	return nil
}
