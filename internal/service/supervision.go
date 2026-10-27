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
		return false, err
	}
	if string(data) != supervisedMarker {
		return false, fmt.Errorf("%w: invalid supervised-service marker", app.ErrConflict)
	}
	return true, nil
}

// Called only after acquiring the sole database-owner lock. A successful
// explicit foreground/auto owner clears old supervisor intent.
func rememberSupervision(cfg Config) error {
	if cfg.Listen != "" {
		return nil
	}
	path := cfg.SocketPath + ".supervised"
	if cfg.LaunchMode != LaunchModeSupervised {
		err := os.Remove(path)
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
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
