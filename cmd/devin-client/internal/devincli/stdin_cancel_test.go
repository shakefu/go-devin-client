package devincli

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

type blockingStdin struct {
	started      chan struct{}
	closed       chan struct{}
	finished     chan struct{}
	startOnce    sync.Once
	closeOnce    sync.Once
	finishedOnce sync.Once
}

func (r *blockingStdin) Read([]byte) (int, error) {
	r.startOnce.Do(func() { close(r.started) })
	<-r.closed
	r.finishedOnce.Do(func() { close(r.finished) })
	return 0, errors.New("stdin closed")
}

func (r *blockingStdin) Close() error {
	r.closeOnce.Do(func() { close(r.closed) })
	return nil
}

func TestCancellationInterruptsJSONStdinBeforeNetwork(t *testing.T) {
	requests := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests <- struct{}{}
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	stdin := &blockingStdin{started: make(chan struct{}), closed: make(chan struct{}), finished: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var stdout, stderr bytes.Buffer
	options := Options{
		Stdin: stdin, Stdout: &stdout, Stderr: &stderr,
		Getenv: func(string) string { return "" }, HTTPClient: server.Client(),
	}
	args := []string{"devin-client", createBlueprint, "--allow-insecure-http", "--base-url", server.URL, "--api-key=test-key", "--data=-"}
	result := make(chan int, 1)
	go func() { result <- Run(ctx, args, options) }()
	select {
	case <-stdin.started:
	case <-time.After(time.Second):
		t.Fatal("CLI did not begin reading stdin")
	}
	cancel()
	select {
	case code := <-result:
		if code != 1 {
			t.Fatalf("canceled stdin exit=%d stderr=%q", code, stderr.String())
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not return after stdin cancellation")
	}
	select {
	case <-stdin.finished:
	case <-time.After(time.Second):
		t.Fatal("stdin reader goroutine was not released")
	}
	if stdout.Len() != 0 || len(requests) != 0 {
		t.Fatalf("stdout=%q requests=%d", stdout.String(), len(requests))
	}
}
