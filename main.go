// Command subsarr is a self-hosted Subscene subtitle provider for Bazarr.
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/slimcdk/subsarr/cmd"
	"github.com/slimcdk/subsarr/internal/config"
)

func main() {
	// An import runs for hours; Ctrl-C or a container stop has to end it at the
	// next batch boundary rather than in the middle of one.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := cmd.Root(config.Load()).ExecuteContext(ctx); err != nil {
		log.Printf("error: %v", err)
		os.Exit(1)
	}
}
