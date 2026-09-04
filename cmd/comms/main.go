// Command comms is the operator and agent surface for Comms.
package main

import (
	"os"

	"github.com/marcus/comms/internal/cli"
)

func main() {
	os.Exit(cli.Run(cli.Env{
		Args:   os.Args[1:],
		Stdout: os.Stdout,
		Stderr: os.Stderr,
	}))
}
