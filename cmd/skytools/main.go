package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/sky87/skytools/internal/skytools"
)

var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(skytools.Run(ctx, os.Args[1:], os.Stdout, os.Stderr, version))
}
