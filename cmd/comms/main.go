// Command comms is the operator and agent surface for Comms.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/marcus/comms/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(cli.Run(cli.Env{
		Args:    os.Args[1:],
		Stdin:   os.Stdin,
		Stdout:  os.Stdout,
		Stderr:  os.Stderr,
		Context: ctx,
	}))
}
