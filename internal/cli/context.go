package cli

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type SessionContext struct {
	AgentID    string `json:"agent_id"`
	ClientID   string `json:"client_id"`
	Harness    string `json:"harness,omitempty"`
	Project    string `json:"project,omitempty"`
	SessionRef string `json:"session_ref,omitempty"`
}

type selectedIdentity struct {
	Agent  string
	Source string
	Record SessionContext
}

func resolveIdentity(explicit string, getenv func(string) string) (selectedIdentity, error) {
	if explicit != "" {
		return selectedIdentity{Agent: explicit, Source: "--as"}, nil
	}
	if value := getenv("COMMS_AGENT_ID"); value != "" {
		return selectedIdentity{Agent: value, Source: "COMMS_AGENT_ID"}, nil
	}
	path := getenv("COMMS_CONTEXT")
	source := "COMMS_CONTEXT"
	if path == "" {
		selected, err := defaultContext(false, getenv)
		if err != nil {
			return selectedIdentity{}, err
		}
		path, source = selected.Path, selected.Source
	}
	record, err := readContext(path)
	if err != nil {
		if os.IsNotExist(err) {
			if source == "session_context" {
				return selectedIdentity{}, fmt.Errorf("no Comms identity selected for this session; run 'comms join HANDLE' here for a new identity, or use --as HANDLE for an existing one")
			}
			return selectedIdentity{}, fmt.Errorf("no Comms identity selected; run 'comms join HANDLE' or use --as")
		}
		return selectedIdentity{}, err
	}
	return selectedIdentity{Agent: record.AgentID, Source: source, Record: record}, nil
}

// implicitContext is the context file used when neither --context nor
// COMMS_CONTEXT names one. Source is "session_context" when the file belongs
// to one terminal session and "default_context" for the machine-wide file.
type implicitContext struct {
	Path    string
	Source  string
	Session string
}

// sessionKey names the terminal session this process runs in, so concurrent
// agents on one machine each get their own implicit identity. COMMS_SESSION
// lets any harness choose the scope; a tmux pane is the fallback because
// multi-agent setups run one agent per pane. The socket path is part of the
// key because pane IDs repeat across tmux servers.
func sessionKey(getenv func(string) string) (key, kind string) {
	if value := getenv("COMMS_SESSION"); value != "" {
		return "comms:" + value, "COMMS_SESSION"
	}
	if pane := getenv("TMUX_PANE"); pane != "" {
		socket, _, _ := strings.Cut(getenv("TMUX"), ",")
		return "tmux:" + socket + ":" + pane, "tmux"
	}
	return "", ""
}

func defaultContext(create bool, getenv func(string) string) (implicitContext, error) {
	dir := getenv("COMMS_STATE_DIR")
	if dir == "" {
		dir = getenv("XDG_STATE_HOME")
		if dir == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return implicitContext{}, err
			}
			dir = filepath.Join(home, ".local", "state")
		}
		dir = filepath.Join(dir, "comms")
	}
	selected := implicitContext{Path: filepath.Join(dir, "context.json"), Source: "default_context"}
	if key, kind := sessionKey(getenv); key != "" {
		sum := sha256.Sum256([]byte(key))
		dir = filepath.Join(dir, "sessions")
		selected = implicitContext{Path: filepath.Join(dir, hex.EncodeToString(sum[:12])+".json"), Source: "session_context", Session: kind}
	}
	if create {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return implicitContext{}, err
		}
	}
	return selected, nil
}

func readContext(path string) (SessionContext, error) {
	var record SessionContext
	data, err := os.ReadFile(path)
	if err != nil {
		return record, err
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil {
		return record, fmt.Errorf("read context %s: %w", path, err)
	}
	if record.AgentID == "" || record.ClientID == "" {
		return record, fmt.Errorf("read context %s: agent_id and client_id are required", path)
	}
	return record, nil
}

func writeContext(path string, record SessionContext) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	temporary, err := os.CreateTemp(filepath.Dir(path), ".context-*.json")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}

func newClientID() (string, error)  { value, err := randomToken(); return "cli_" + value, err }
func newRequestID() (string, error) { value, err := randomToken(); return "req_" + value, err }
func randomToken() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw)), nil
}
