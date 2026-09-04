// Package cli implements the Comms command-line transport.
package cli

import (
	"fmt"
	"io"

	"github.com/marcus/comms/pkg/buildinfo"
)

const usage = `Comms connects independent agents through durable local topics.

Usage:
  comms hello
  comms version
  comms help

The core is not implemented yet. See docs/plans/active/core.md.
`

// Env contains the process-facing dependencies needed by the CLI.
type Env struct {
	Args   []string
	Stdout io.Writer
	Stderr io.Writer
}

// Run executes one CLI invocation and returns its process exit code.
func Run(env Env) int {
	if env.Stdout == nil {
		env.Stdout = io.Discard
	}
	if env.Stderr == nil {
		env.Stderr = io.Discard
	}

	if len(env.Args) == 0 {
		_, _ = io.WriteString(env.Stdout, usage)
		return 0
	}

	switch env.Args[0] {
	case "help", "-h", "--help":
		_, _ = io.WriteString(env.Stdout, usage)
		return 0
	case "hello":
		_, _ = io.WriteString(env.Stdout, "hello from comms\n")
		return 0
	case "version":
		_, _ = fmt.Fprintf(env.Stdout, "comms %s (%s)\n", buildinfo.Version, buildinfo.Commit)
		return 0
	default:
		_, _ = fmt.Fprintf(env.Stderr, "comms: unknown command %q\n", env.Args[0])
		_, _ = io.WriteString(env.Stderr, "Run 'comms help' for usage.\n")
		return 2
	}
}
