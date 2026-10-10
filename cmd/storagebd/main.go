package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"storage-bd-client/internal/cli"
)

func main() {
	// Cancellation reaches restic as SIGINT (restic.Session.Run), so it can
	// release its repository lock before exiting.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := cli.NewRootCmd().ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		stop()
		os.Exit(1)
	}
}
