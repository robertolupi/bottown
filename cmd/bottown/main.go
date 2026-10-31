// Command bottown manages and serves a bottown instance.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/robertolupi/bottown/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := cli.Execute(ctx)
	stop()
	os.Exit(code)
}
