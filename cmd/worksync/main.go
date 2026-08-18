// Command worksync is the CLI for portable development environments
// (design §18).
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"worksync/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(cli.Run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}
