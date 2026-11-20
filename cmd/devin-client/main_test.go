package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestSubprocessVersionAndUsageExitCodes(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "devin-client")
	build := exec.Command("go", "build", "-o", binary, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %v\n%s", err, output)
	}
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
