package cli

import (
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/marcus/comms/internal/httpapi"
)

func TestRunClosesItsIdleHTTPConnections(t *testing.T) {
	socket := filepath.Join(shortTempDir(t), "comms.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	closed := make(chan struct{}, 1)
	server := &http.Server{
		ReadHeaderTimeout: time.Second,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{"schema": httpapi.ResponseSchema, "data": map[string]any{"healthy": true}})
		}),
		ConnState: func(_ net.Conn, state http.ConnState) {
			if state == http.StateClosed {
				select {
				case closed <- struct{}{}:
				default:
				}
			}
		},
	}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close(); <-done })
	if code := Run(Env{Args: []string{"--socket", socket, "health"}}); code != 0 {
		t.Fatalf("health exit=%d", code)
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Run returned with its private HTTP connection still open")
	}
}
