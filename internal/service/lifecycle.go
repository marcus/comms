package service

import (
	"os"
	"sync"
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

func (c *controller) Status() httpapi.LifecycleStatus { return c.status }

func (c *controller) Done() <-chan struct{} { return c.shutdownCh }

func (c *controller) RequestShutdown(expectedID string) error {
	if expectedID != c.status.ServerInstanceID {
		return httpapi.ErrServerInstanceChanged
	}
	c.shutdownOnce.Do(func() { close(c.shutdownCh) })
	return nil
}
