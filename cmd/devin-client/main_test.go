package main

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestSubprocessVersionAndUsageExitCodes(t *testing.T) {
	binary := buildCLI(t)
	for _, test := range []struct {
		args []string
		code int
		out  string
	}{{[]string{"--version"}, 0, "dev\n"}, {[]string{"unknown-command"}, 2, ""}} {
		command := exec.Command(binary, test.args...)
		command.Env = append(os.Environ(), "DEVIN_API_KEY=")
		output, err := command.Output()
		code := 0
		if err != nil {
			if exit, ok := err.(*exec.ExitError); ok {
				code = exit.ExitCode()
			} else {
				t.Fatal(err)
			}
		}
		if code != test.code || string(output) != test.out {
			t.Errorf("%v: code=%d stdout=%q; want code=%d stdout=%q", test.args, code, output, test.code, test.out)
		}
	}
}

func TestSubprocessSignalInterruptsOpenStdin(t *testing.T) {
	binary := buildCLI(t)
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	command := exec.Command(binary, "create_enterprise_blueprint_endpoint_v3_enterprise_snapshot_setup_blueprints_post", "--api-key=test-key", "--allow-insecure-http", "--base-url=http://127.0.0.1:1", "--data=-")
	command.Stdin = reader
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Start(); err != nil {
		reader.Close()
		t.Fatal(err)
	}
	_ = reader.Close()
	writeResult := make(chan error, 1)
	go func() {
		data := bytes.Repeat([]byte(" "), 4<<20)
		n, writeErr := writer.Write(data)
		if writeErr == nil && n != len(data) {
			writeErr = io.ErrShortWrite
		}
		writeResult <- writeErr
	}()
	select {
	case err := <-writeResult:
		if err != nil {
			_ = command.Process.Kill()
			_ = command.Wait()
			t.Fatalf("write open-pipe input: %v", err)
		}
	case <-time.After(5 * time.Second):
		_ = command.Process.Kill()
		_ = writer.Close()
		_ = command.Wait()
		t.Fatal("CLI did not consume pipe input")
	}
	if err := command.Process.Signal(os.Interrupt); err != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		t.Fatalf("send interrupt: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	select {
	case err := <-done:
		exit, ok := err.(*exec.ExitError)
		if !ok || exit.ExitCode() != 1 {
			t.Fatalf("exit=%v stdout=%q stderr=%q; want handled signal exit 1", err, stdout.String(), stderr.String())
		}
	case <-time.After(5 * time.Second):
		_ = command.Process.Kill()
		<-done
		t.Fatalf("CLI did not exit on interrupt while stdin remained open; stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func buildCLI(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "devin-client")
	build := exec.Command("go", "build", "-o", binary, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %v\n%s", err, output)
	}
	return binary
}
