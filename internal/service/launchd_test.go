package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/marcus/comms/internal/app"
)

func TestInstalledSupervisorMatchesConfiguredPaths(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "Library", "LaunchAgents", "homebrew.mxcl.comms.plist")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, extra, socket, db string
		want                    bool
	}{
		{"defaults", "", filepath.Join(home, ".local/state/comms/comms.sock"), filepath.Join(home, ".local/state/comms/comms.db"), true},
		{"different-store", "", "/tmp/isolated.sock", "/tmp/isolated.db", false},
		{"environment", "<key>EnvironmentVariables</key><dict><key>COMMS_STATE_DIR</key><string>/tmp/comms-custom</string><key>COMMS_SOCKET</key><string>/tmp/comms-custom.sock</string></dict>", "/tmp/comms-custom.sock", "/tmp/comms-custom/comms.db", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			data := fmt.Sprintf(`<plist><dict><key>ProgramArguments</key><array><string>/opt/homebrew/opt/comms/bin/comms</string><string>serve</string><string>--supervised</string></array>%s</dict></plist>`, test.extra)
			if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := InstalledSupervisor(test.socket, test.db, home)
			if err != nil || got != test.want {
				t.Fatal(got, err)
			}
		})
	}
	if err := os.WriteFile(path, []byte("malformed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := InstalledSupervisor("/tmp/x", "/tmp/y", home); !errors.Is(err, app.ErrConflict) {
		t.Fatal(err)
	}
}

func TestAutoChildCannotClearSupervisorIntent(t *testing.T) {
	root := t.TempDir()
	cfg := Config{DatabasePath: filepath.Join(root, "comms.db"), SocketPath: filepath.Join(root, "comms.sock"), LaunchMode: LaunchModeSupervised}
	// Failed startup must leave intent even without a successfully acquired lock.
	if err := os.WriteFile(cfg.DatabasePath, []byte("invalid database"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Run(context.Background(), cfg); err == nil {
		t.Fatal("expected invalid database failure")
	}
	if managed, err := SupervisedSocket(cfg.SocketPath); err != nil || !managed {
		t.Fatal(managed, err)
	}
	cfg.LaunchMode = LaunchModeAuto
	if err := Run(context.Background(), cfg); !errors.Is(err, app.ErrUnavailable) {
		t.Fatal(err)
	}
	if managed, err := SupervisedSocket(cfg.SocketPath); err != nil || !managed {
		t.Fatal("auto child removed marker", managed, err)
	}
}
