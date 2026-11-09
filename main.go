package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

var version = "0.1.0-dev"

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	os.Exit(run(ctx, os.Args[1:], os.Stdout))
}
