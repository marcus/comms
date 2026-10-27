package service

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/marcus/comms/internal/app"
)

const supervisedMarker = "comms.supervised.v1\n"

// SupervisedSocket remembers supervisor ownership across socket removal during
// restart. It is an advisory startup rule, not a process-liveness assertion.
func SupervisedSocket(socket string) (bool, error) {
	data, err := os.ReadFile(socket + ".supervised")
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("%w: cannot inspect supervisor ownership: %w", app.ErrUnavailable, err)
	}
	if string(data) != supervisedMarker {
		return false, fmt.Errorf("%w: invalid supervised-service marker", app.ErrConflict)
	}
	return true, nil
}

// Supervised intent is written before acquiring the database lock. Only an
// explicit foreground owner may clear a marker after acquiring that lock.
func rememberSupervision(cfg Config) error {
	if cfg.Listen != "" {
		return nil
	}
	path := cfg.SocketPath + ".supervised"
	if cfg.LaunchMode == LaunchModeAuto {
		// A racing client must never erase supervisor intent.
		managed, err := SupervisedSocket(cfg.SocketPath)
		if err != nil {
			return err
		}
		if managed {
			return fmt.Errorf("%w: socket is owned by a supervisor; use brew services start/restart comms", app.ErrUnavailable)
		}
		return nil
	}
	if cfg.LaunchMode != LaunchModeSupervised {
		err := os.Remove(path)
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return RememberSupervisor(cfg.SocketPath)
}

// RememberSupervisor records intent before database ownership, including when
// the supervisor loses a race or fails to open the store.
func RememberSupervisor(socket string) error {
	if marked, err := SupervisedSocket(socket); err == nil && marked {
		return nil
	}
	path := socket + ".supervised"
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".comms-supervised-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(file.Name()) }()
	if _, err = file.WriteString(supervisedMarker); err != nil {
		_ = file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}
