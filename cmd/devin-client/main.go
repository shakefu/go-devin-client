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
	finished := make(chan int, 1)
	go func() {
		finished <- devincli.Run(ctx, os.Args, devincli.Options{Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr})
	}()
	select {
	case code := <-finished:
		stop()
		os.Exit(code)
	case <-ctx.Done():
		stop()
		os.Exit(1)
	}
}
