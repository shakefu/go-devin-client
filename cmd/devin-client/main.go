package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/shakefu/go-devin-client/cmd/devin-client/internal/devincli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := devincli.Run(ctx, os.Args, devincli.Options{Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr})
	stop()
	os.Exit(code)
}
