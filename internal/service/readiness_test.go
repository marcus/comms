package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/marcus/comms/internal/app"
	"github.com/marcus/comms/internal/domain"
	"github.com/marcus/comms/internal/help"
	"github.com/marcus/comms/internal/httpapi"
	"github.com/marcus/comms/internal/store"
)

func TestHealthAndHelloDescribeReadiness(t *testing.T) {
	adapter, err := store.Open(context.Background(), store.Options{Path: filepath.Join(t.TempDir(), "comms.db")})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = adapter.Close() }()
	appService := app.NewService(adapter, domain.UTCClock{})
	defer func() { _ = appService.Close() }()
	controller, err := newController(Config{LaunchMode: LaunchModeSupervised})
	if err != nil {
		t.Fatal(err)
	}
	handler := httpapi.NewUnixHandler(appService, controller)
	for _, phase := range []string{"starting", "ready", "stopping"} {
		if phase == "ready" {
			controller.ready.Store(true)
		}
		if phase == "stopping" {
			if err = controller.RequestShutdown(controller.status.ServerInstanceID); err != nil {
				t.Fatal(err)
			}
		}
		hello := httptest.NewRecorder()
		handler.ServeHTTP(hello, httptest.NewRequest(http.MethodGet, "/v1/hello", nil))
		var payload struct{ Data help.Handshake }
		if err = json.Unmarshal(hello.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		if hello.Code != 200 || payload.Data.Readiness != phase {
			t.Fatalf("phase=%s hello=%d %s", phase, hello.Code, hello.Body)
		}
		health := httptest.NewRecorder()
		handler.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/v1/health", nil))
		expected := http.StatusServiceUnavailable
		if phase == "ready" {
			expected = http.StatusOK
		}
		if health.Code != expected {
			t.Fatalf("phase=%s health=%d %s", phase, health.Code, health.Body)
		}
	}
}

func TestExplicitOwnerCanClearSupervisionAfterStop(t *testing.T) {
	cfg := Config{SocketPath: filepath.Join(t.TempDir(), "comms.sock"), LaunchMode: LaunchModeSupervised}
	if err := rememberSupervision(cfg); err != nil {
		t.Fatal(err)
	}
	if managed, err := SupervisedSocket(cfg.SocketPath); err != nil || !managed {
		t.Fatal(managed, err)
	}
	cfg.LaunchMode = LaunchModeForeground
	if err := rememberSupervision(cfg); err != nil {
		t.Fatal(err)
	}
	if managed, err := SupervisedSocket(cfg.SocketPath); err != nil || managed {
		t.Fatal(managed, err)
	}
}
