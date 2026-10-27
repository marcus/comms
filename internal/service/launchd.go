package service

import (
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"

	"github.com/marcus/comms/internal/app"
)

// launchdValue preserves dictionary key/value ordering without depending on
// macOS commands. Homebrew writes XML property lists, including environment
// overrides and explicit command-line paths.
type launchdValue struct {
	XMLName  xml.Name
	Text     string         `xml:",chardata"`
	Children []launchdValue `xml:",any"`
}

func (v launchdValue) get(key string) launchdValue {
	for i := 0; i+1 < len(v.Children); i += 2 {
		if v.Children[i].XMLName.Local == "key" && v.Children[i].Text == key {
			return v.Children[i+1]
		}
	}
	return launchdValue{}
}

// InstalledSupervisor matches the installed Homebrew job to this exact store
// and socket. A different test/custom store must not inherit live ownership.
// Presence is supervisor intent, not job health; a failed job still owns it.
func InstalledSupervisor(socket, database, home string) (bool, error) {
	paths := []string{filepath.Join(home, "Library", "LaunchAgents", "homebrew.mxcl.comms.plist"), "/Library/LaunchAgents/homebrew.mxcl.comms.plist", "/Library/LaunchDaemons/homebrew.mxcl.comms.plist"}
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return false, err
		}
		var root launchdValue
		if err = xml.Unmarshal(data, &root); err != nil || len(root.Children) != 1 || root.Children[0].XMLName.Local != "dict" {
			return false, fmt.Errorf("%w: cannot inspect installed comms supervisor configuration %s", app.ErrConflict, path)
		}
		job := root.Children[0]
		args := job.get("ProgramArguments").Children
		if len(args) < 3 || filepath.Base(args[0].Text) != "comms" || args[1].Text != "serve" {
			continue
		}
		env := job.get("EnvironmentVariables")
		jobHome := env.get("HOME").Text
		if jobHome == "" {
			jobHome = home
		}
		state := env.get("COMMS_STATE_DIR").Text
		if state == "" {
			state = env.get("XDG_STATE_HOME").Text
			if state == "" {
				state = filepath.Join(jobHome, ".local", "state")
			}
			state = filepath.Join(state, "comms")
		}
		jobDB := filepath.Join(state, "comms.db")
		jobSocket := env.get("COMMS_SOCKET").Text
		if jobSocket == "" {
			if runtimeDir := env.get("XDG_RUNTIME_DIR").Text; runtimeDir != "" {
				jobSocket = filepath.Join(runtimeDir, "comms", "comms.sock")
			} else {
				jobSocket = filepath.Join(state, "comms.sock")
			}
		}
		supervised, tcp := false, false
		for i := 2; i < len(args); i++ {
			switch args[i].Text {
			case "--supervised":
				supervised = true
			case "--listen":
				tcp = true
			case "--socket", "--db":
				if i+1 == len(args) {
					return false, fmt.Errorf("%w: incomplete supervisor path option", app.ErrConflict)
				}
				if args[i].Text == "--socket" {
					jobSocket = args[i+1].Text
				} else {
					jobDB = args[i+1].Text
				}
				i++
			}
		}
		if supervised && !tcp && filepath.Clean(socket) == filepath.Clean(jobSocket) && filepath.Clean(database) == filepath.Clean(jobDB) {
			return true, nil
		}
	}
	return false, nil
}

func SupervisorPresent(socket, database, home string) (bool, error) {
	marked, err := SupervisedSocket(socket)
	if err != nil || marked {
		return marked, err
	}
	return InstalledSupervisor(socket, database, home)
}
