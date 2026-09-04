package service

import (
	"errors"
	"strings"
	"testing"

	"github.com/marcus/comms/internal/httpapi"
)

func TestRequestShutdownFencesIncarnation(t *testing.T) {
	c, err := newController(Config{
		LaunchMode:   LaunchModeForeground,
		SocketPath:   "/tmp/comms.sock",
		DatabasePath: "/tmp/comms.db",
	})
	if err != nil {
		t.Fatal(err)
	}
	status := c.Status()
	if !strings.HasPrefix(status.ServerInstanceID, "srv_") {
		t.Fatalf("server_instance_id=%q", status.ServerInstanceID)
	}
	if status.LaunchMode != string(LaunchModeForeground) || status.SocketPath != "/tmp/comms.sock" || status.DatabasePath != "/tmp/comms.db" {
		t.Fatalf("status=%#v", status)
	}
	if err := c.RequestShutdown("srv_stale"); !errors.Is(err, httpapi.ErrServerInstanceChanged) {
		t.Fatalf("stale error=%v", err)
	}
	select {
	case <-c.Done():
		t.Fatal("stale shutdown signaled")
	default:
	}
	if err := c.RequestShutdown(status.ServerInstanceID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-c.Done():
	default:
		t.Fatal("matching shutdown did not signal")
	}
	if err := c.RequestShutdown(status.ServerInstanceID); err != nil {
		t.Fatal(err)
	}
}
