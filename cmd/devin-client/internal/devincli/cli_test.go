package devincli

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/urfave/cli/v2"
)

const (
	insightsOperation = "handle_get_account_sessions_insights_v3_enterprise_sessions_insights_get"
	messagesOperation = "handle_get_account_session_messages_v3_enterprise_sessions__devin_id__messages_get"
	profileOperation  = "handle_get_code_scan_profile_v3_enterprise_code_scans_profiles__profile_id__get"
	createBlueprint   = "create_enterprise_blueprint_endpoint_v3_enterprise_snapshot_setup_blueprints_post"
	appendTags        = "handle_append_account_session_tags_v3_enterprise_sessions__devin_id__tags_post"
	uploadAttachment  = "handle_upload_attachment_v3_organizations__org_id__attachments_post"
	indexRepository   = "handle_index_single_repository_v3_organizations__org_id__repositories__repository_path__indexing_put"
)

func testOptions(server *httptest.Server, getenv func(string) string) Options {
	return Options{Getenv: getenv, HTTPClient: server.Client()}
}

func runOperation(ctx context.Context, operation string, flags ...string) ([]byte, []byte, int) {
	var stdout, stderr bytes.Buffer
	args := append([]string{"devin-client", operation}, flags...)
	code := Run(ctx, args, Options{Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr, Getenv: func(string) string { return "" }})
	return stdout.Bytes(), stderr.Bytes(), code
}

func TestRunPreservesTypedQueryValuesAndRawResponse(t *testing.T) {
	const response = "{\"raw\": true}\n"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		query := request.URL.Query()
		if request.Method != http.MethodGet || request.URL.Path != "/v3/enterprise/sessions/insights" {
			t.Errorf("unexpected request %s %s", request.Method, request.URL.String())
		}
		if query.Get("created_after") != "0" || query.Get("first") != "17" || query.Get("is_archived") != "false" {
			t.Errorf("typed query values were lost: %v", query)
		}
		if got, want := query["tags"], []string{"alpha", "beta"}; !reflect.DeepEqual(got, want) {
			t.Errorf("repeated array values = %v; want %v", got, want)
		}
		if request.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("Authorization = %q", request.Header.Get("Authorization"))
		}
		if request.Header.Get("Accept") != "application/json" {
			t.Errorf("Accept = %q", request.Header.Get("Accept"))
		}
		writer.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(writer, response)
	}))
	defer server.Close()
	options := testOptions(server, func(name string) string {
		if name == "DEVIN_BASE_URL" {
			return "https://wrong.invalid"
		}
		if name == "DEVIN_API_KEY" {
			return "wrong-key"
		}
		return ""
	})
	args := []string{"devin-client", "--allow-insecure-http", "--base-url", server.URL, "--api-key", "test-key", "--read-only",
		insightsOperation, "--created_after", "0", "--first", "17", "--is_archived=false", "--tags", "alpha", "--tags", "beta"}
	var stdout, stderr bytes.Buffer
	options.Stdout, options.Stderr = &stdout, &stderr
	if code := Run(context.Background(), args, options); code != 0 {
		t.Fatalf("Run() code = %d, stderr: %s", code, stderr.String())
	}
	if stdout.String() != response {
		t.Fatalf("stdout = %q; want raw response %q", stdout.String(), response)
	}
}

func TestEnabledReadOnlyAndDryRunFormsNeverReadEnvironment(t *testing.T) {
	values := []string{"1", "t", "T", "TRUE", "true", "True"}
	runWithoutEnvironment := func(args []string) int {
		return Run(context.Background(), args, Options{
			Stdin: strings.NewReader(""), Stdout: io.Discard, Stderr: io.Discard,
			Getenv: func(name string) string { t.Fatalf("Getenv(%q) called", name); return "" },
		})
	}
	for _, value := range values {
		for _, prefix := range []string{"--", "-"} {
			flag := prefix + "read-only=" + value
			if code := runWithoutEnvironment([]string{"devin-client", flag, appendTags}); code != 2 {
				t.Errorf("global %s exit=%d; want read-only rejection", flag, code)
			}
			if code := runWithoutEnvironment([]string{"devin-client", appendTags, flag}); code != 2 {
				t.Errorf("leaf %s exit=%d; want read-only rejection", flag, code)
			}
			flag = prefix + "dry-run=" + value
			global := []string{"devin-client", flag, insightsOperation, "--base-url=" + defaultBaseURL, "--timeout=1s"}
			leaf := []string{"devin-client", insightsOperation, flag, "--base-url=" + defaultBaseURL, "--timeout=1s"}
			if code := runWithoutEnvironment(global); code != 0 {
				t.Errorf("global %s exit=%d; want dry-run success", flag, code)
			}
			if code := runWithoutEnvironment(leaf); code != 0 {
				t.Errorf("leaf %s exit=%d; want dry-run success", flag, code)
			}
		}
	}
}

func TestRunEscapesPathParameter(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if got, want := request.URL.EscapedPath(), "/v3/enterprise/code-scans/profiles/alpha%2Fbeta%20gamma"; got != want {
			t.Errorf("escaped path = %q; want %q", got, want)
		}
		if got := request.Header.Get("Authorization"); got != "Bearer environment-key" {
			t.Errorf("Authorization = %q; want environment key", got)
		}
		writer.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(writer, "ok")
	}))
	defer server.Close()
	var stdout, stderr bytes.Buffer
	options := testOptions(server, func(name string) string {
		if name == "DEVIN_API_KEY" {
			return "environment-key"
		}
		return ""
	})
	options.Stdout, options.Stderr = &stdout, &stderr
	args := []string{"devin-client", profileOperation, "--allow-insecure-http", "--base-url", server.URL,
		"--profile_id", "alpha/beta gamma"}
	if code := Run(context.Background(), args, options); code != 0 {
		t.Fatalf("Run() code = %d, stderr: %s", code, stderr.String())
	}
	if stdout.String() != "ok" {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestRunObjectQueryParameter(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Query().Get("author") != "devin" || request.URL.Query().Get("message_type") != "assistant" {
			t.Errorf("object query parameter was not exploded: %v", request.URL.Query())
		}
		writer.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	var stdout, stderr bytes.Buffer
	options := testOptions(server, func(string) string { return "" })
	options.Stdout, options.Stderr = &stdout, &stderr
	args := []string{"devin-client", messagesOperation, "--allow-insecure-http", "--base-url", server.URL,
		"--api-key=test-key", "--devin_id=devin-1", "--qs", `{"author":"devin","message_type":"assistant"}`}
	if code := Run(context.Background(), args, options); code != 0 {
		t.Fatalf("Run() code = %d, stderr: %s", code, stderr.String())
	}
}

func TestOptionalJSONBodyCanBeOmitted(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		if len(body) != 0 || request.Header.Get("Content-Type") != "" {
			t.Errorf("optional body was not omitted: body=%q content-type=%q", body, request.Header.Get("Content-Type"))
		}
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	var stdout, stderr bytes.Buffer
	options := testOptions(server, func(string) string { return "" })
	options.Stdout, options.Stderr = &stdout, &stderr
	args := []string{"devin-client", indexRepository, "--allow-insecure-http", "--base-url", server.URL,
		"--api-key=test-key", "--org_id=org-1", "--repository_path=owner/repo"}
	if code := Run(context.Background(), args, options); code != 0 {
		t.Fatalf("Run() code = %d; stderr: %s", code, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q; want no content for 204", stdout.String())
	}
}

func TestRunJSONBody(t *testing.T) {
	const body = `{"name":"cli","enabled":false}`
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		got, err := io.ReadAll(request.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		if string(got) != body {
			t.Errorf("body = %q; want %q", got, body)
		}
		if request.Header.Get("Content-Type") != "application/json" {
			t.Errorf("Content-Type = %q", request.Header.Get("Content-Type"))
		}
		writer.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(writer, "{\"created\":true}")
	}))
	defer server.Close()
	var stdout, stderr bytes.Buffer
	options := testOptions(server, func(string) string { return "" })
	options.Stdout, options.Stderr = &stdout, &stderr
	args := []string{"devin-client", createBlueprint, "--allow-insecure-http", "--base-url", server.URL,
		"--api-key=test-key", "--data", body}
	if code := Run(context.Background(), args, options); code != 0 {
		t.Fatalf("Run() code = %d, stderr: %s", code, stderr.String())
	}
	if stdout.String() != "{\"created\":true}" {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestRunJSONBodyFromFileAndStdin(t *testing.T) {
	for _, test := range []struct {
		name  string
		value func(t *testing.T) (string, io.Reader)
	}{
		{
			name: "file",
			value: func(t *testing.T) (string, io.Reader) {
				path := filepath.Join(t.TempDir(), "body.json")
				if err := os.WriteFile(path, []byte(`{"source":"file"}`), 0o600); err != nil {
					t.Fatal(err)
				}
				return "@" + path, strings.NewReader("")
			},
		},
		{
			name: "stdin",
			value: func(*testing.T) (string, io.Reader) {
				return "-", strings.NewReader(`{"source":"stdin"}`)
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			dataFlag, stdin := test.value(t)
			want := `{"source":"` + test.name + `"}`
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				body, _ := io.ReadAll(request.Body)
				if string(body) != want {
					t.Errorf("body = %q; want %q", body, want)
				}
				writer.WriteHeader(http.StatusOK)
			}))
			defer server.Close()
			var stdout, stderr bytes.Buffer
			options := testOptions(server, func(string) string { return "" })
			options.Stdin, options.Stdout, options.Stderr = stdin, &stdout, &stderr
			args := []string{"devin-client", createBlueprint, "--allow-insecure-http", "--base-url", server.URL,
				"--api-key=test-key", "--data", dataFlag}
			if code := Run(context.Background(), args, options); code != 0 {
				t.Fatalf("Run() code = %d, stderr: %s", code, stderr.String())
			}
			if stdout.Len() != 0 {
				t.Fatalf("empty successful response wrote %q", stdout.String())
			}
		})
	}
}

func TestRunMultipartUploadStreamsFile(t *testing.T) {
	const content = "local attachment payload"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if !strings.HasPrefix(request.Header.Get("Content-Type"), "multipart/form-data; boundary=") {
			t.Errorf("Content-Type = %q", request.Header.Get("Content-Type"))
		}
		if err := request.ParseMultipartForm(1024); err != nil {
			t.Errorf("ParseMultipartForm: %v", err)
		} else {
			file, header, err := request.FormFile("file")
			if err != nil {
				t.Errorf("FormFile: %v", err)
			} else {
				defer file.Close()
				data, _ := io.ReadAll(file)
				if string(data) != content || header.Filename != "evidence.bin" {
					t.Errorf("uploaded filename/content = %q/%q", header.Filename, data)
				}
			}
		}
		writer.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(writer, `{"attachment_id":"att-1"}`)
	}))
	defer server.Close()
	filePath := filepath.Join(t.TempDir(), "evidence.bin")
	if err := os.WriteFile(filePath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	options := testOptions(server, func(string) string { return "" })
	options.Stdout, options.Stderr = &stdout, &stderr
	args := []string{"devin-client", uploadAttachment, "--allow-insecure-http", "--base-url", server.URL,
		"--api-key=test-key", "--org_id=org-1", "--file", filePath}
	if code := Run(context.Background(), args, options); code != 0 {
		t.Fatalf("Run() code = %d, stderr: %s", code, stderr.String())
	}
	if stdout.String() != `{"attachment_id":"att-1"}` {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestReadOnlyRejectsWritesBeforeReadingInputsOrEnvironment(t *testing.T) {
	for _, test := range []struct {
		name      string
		operation string
		flags     []string
	}{
		{
			name:      "JSON stdin",
			operation: appendTags,
			flags:     []string{"--devin_id=devin-1", "--data=-"},
		},
		{
			name:      "multipart file",
			operation: uploadAttachment,
			flags:     []string{"--org_id=org-1", "--file=/path/that/must/not/be-statted"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			getenvCalls := 0
			stdin := &countingReader{reader: strings.NewReader(`{"tag":"x"}`)}
			options := Options{
				Stdin: stdin, Stdout: &stdout, Stderr: &stderr,
				Getenv: func(string) string { getenvCalls++; return "" },
			}
			args := append([]string{"devin-client", test.operation, "--read-only", "--dry-run", "--timeout=not-a-duration", "--api-key=secret"}, test.flags...)
			if code := Run(context.Background(), args, options); code != 2 {
				t.Fatalf("Run() code = %d; want 2; stderr: %s", code, stderr.String())
			}
			if getenvCalls != 0 || stdin.reads != 0 {
				t.Fatalf("read-only rejection side effects: getenv=%d stdin=%d", getenvCalls, stdin.reads)
			}
			if !strings.Contains(stderr.String(), "read-only mode") || strings.Contains(stderr.String(), "secret") {
				t.Fatalf("unexpected diagnostic: %q", stderr.String())
			}
			if stdout.Len() != 0 {
				t.Fatalf("stdout = %q", stdout.String())
			}
		})
	}
}

func TestDryRunNeedsNoAPIKeyAndReportsRequest(t *testing.T) {
	var stdout, stderr bytes.Buffer
	var getenvCalls []string
	options := Options{
		Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr,
		Getenv: func(name string) string {
			getenvCalls = append(getenvCalls, name)
			if name == "DEVIN_API_KEY" {
				return "environment-secret"
			}
			if name == "DEVIN_READ_ONLY" {
				return "1"
			}
			return ""
		},
	}
	args := []string{"devin-client", createBlueprint, "--dry-run", "--api-key=argument-secret", "--data", `{"name":"preview"}`}
	if code := Run(context.Background(), args, options); code != 0 {
		t.Fatalf("Run() code = %d; stderr: %s", code, stderr.String())
	}
	if strings.Contains(strings.Join(getenvCalls, ","), "DEVIN_API_KEY") {
		t.Fatalf("dry-run read API key environment variable: %v", getenvCalls)
	}
	if strings.Contains(strings.Join(getenvCalls, ","), "DEVIN_READ_ONLY") {
		t.Fatalf("read-only policy was enabled from the environment: %v", getenvCalls)
	}
	if strings.Contains(stdout.String()+stderr.String(), "argument-secret") || strings.Contains(stdout.String()+stderr.String(), "environment-secret") {
		t.Fatal("dry-run read or used API key material")
	}
	for _, want := range []string{
		"POST https://api.devin.ai/v3/enterprise/snapshot-setup/blueprints",
		"Authorization: Bearer [REDACTED]",
		`{"name":"preview"}`,
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("dry-run output %q does not contain %q", stdout.String(), want)
		}
	}
}

func TestDryRunMultipartUsesStatMetadata(t *testing.T) {
	filePath := filepath.Join(t.TempDir(), "preview.bin")
	if err := os.WriteFile(filePath, []byte("multipart contents"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	options := Options{
		Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr,
		Getenv: func(string) string { return "" },
	}
	args := []string{"devin-client", uploadAttachment, "--dry-run", "--org_id=org-1", "--file", filePath}
	if code := Run(context.Background(), args, options); code != 0 {
		t.Fatalf("Run() code = %d; stderr: %s", code, stderr.String())
	}
	if want := fmt.Sprintf("--file %q (%d bytes; filename %q)", filePath, len("multipart contents"), "preview.bin"); !strings.Contains(stdout.String(), want) {
		t.Fatalf("dry-run output %q does not contain %q", stdout.String(), want)
	}
	if strings.Contains(stdout.String(), "multipart contents") {
		t.Fatal("dry-run printed multipart file contents")
	}
}

func TestOpsAndVersionDoNotParseTimeoutOrReadEnvironment(t *testing.T) {
	for _, args := range [][]string{
		{"devin-client"},
		{"devin-client", "ops", "--timeout=invalid"},
		{"devin-client", "--version", "--timeout=invalid"},
		{"devin-client", "--help", "--timeout=invalid"},
		{"devin-client", insightsOperation, "--help", "--timeout=invalid"},
	} {
		var stdout, stderr bytes.Buffer
		getenvCalls := 0
		options := Options{
			Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr,
			Getenv: func(string) string { getenvCalls++; return "" },
		}
		if code := Run(context.Background(), args, options); code != 0 {
			t.Fatalf("Run(%v) code = %d; stderr: %s", args, code, stderr.String())
		}
		if getenvCalls != 0 {
			t.Errorf("Run(%v) read environment %d times", args, getenvCalls)
		}
		if len(args) > 1 && args[1] == "ops" && strings.Count(stdout.String(), "\n") != operationCount {
			t.Errorf("ops output has %d lines; want %d", strings.Count(stdout.String(), "\n"), operationCount)
		}
		if len(args) > 1 && args[1] == "--version" && stdout.String() != "dev\n" {
			t.Errorf("unreleased version output = %q; want exactly dev newline", stdout.String())
		}
	}
}

func TestReleaseVersionPrintsOnlyTheReleaseValue(t *testing.T) {
	previous := version
	version = "v1.2.3"
	defer func() { version = previous }()
	var stdout, stderr bytes.Buffer
	if code := Run(context.Background(), []string{"devin-client", "--version"}, Options{Stdout: &stdout, Stderr: &stderr, Getenv: func(string) string { t.Fatal("version read environment"); return "" }}); code != 0 {
		t.Fatalf("version code=%d stderr=%q", code, stderr.String())
	}
	if stdout.String() != "v1.2.3\n" {
		t.Fatalf("version output=%q", stdout.String())
	}
}

func TestExplicitEmptyTimeoutAndPositionalsAreRejectedBeforeSideEffects(t *testing.T) {
	t.Run("empty timeout", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := Run(context.Background(), []string{"devin-client", insightsOperation, "--dry-run", "--timeout="}, Options{Stdout: &stdout, Stderr: &stderr, Getenv: func(string) string { return "" }})
		if code != 1 || !strings.Contains(stderr.String(), "timeout must be a Go duration") {
			t.Fatalf("code=%d stderr=%q", code, stderr.String())
		}
	})
	for _, terminator := range []bool{false, true} {
		var stdout, stderr bytes.Buffer
		getenvCalls, requests := 0, 0
		server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests++ }))
		args := []string{"devin-client", insightsOperation, "--allow-insecure-http", "--base-url", server.URL, "--api-key=secret"}
		if terminator {
			args = append(args, "--", "unexpected")
		} else {
			args = append(args, "unexpected")
		}
		options := testOptions(server, func(string) string { getenvCalls++; return "" })
		options.Stdout, options.Stderr = &stdout, &stderr
		if code := Run(context.Background(), args, options); code != 2 {
			t.Errorf("terminator=%v code=%d stderr=%q", terminator, code, stderr.String())
		}
		if requests != 0 || getenvCalls != 0 {
			t.Errorf("terminator=%v requests=%d getenv=%d", terminator, requests, getenvCalls)
		}
		server.Close()
	}
}

func TestMultipartAlwaysRequiresFileEvenWhenBodyOptional(t *testing.T) {
	context := cli.NewContext(&cli.App{}, flag.NewFlagSet("test", flag.ContinueOnError), nil)
	err := checkRequiredInputs(context, Operation{RequestBody: &RequestBody{Required: false, MediaTypes: []string{"multipart/form-data"}}})
	if err == nil || !strings.Contains(err.Error(), "--file") {
		t.Fatalf("error=%v; want required --file", err)
	}
}

func TestErrorsAreClassifiedAndRedactSuppliedKey(t *testing.T) {
	t.Run("credential equals diagnostic", func(t *testing.T) {
		key := "invalid command syntax"
		var stdout, stderr bytes.Buffer
		args := []string{"devin-client", "--api-key", "old-key", insightsOperation, "-api-key", key, "--unknown-flag"}
		if code := Run(context.Background(), args, Options{Stdout: &stdout, Stderr: &stderr, Getenv: func(string) string { return "" }}); code != 2 {
			t.Fatalf("code=%d stderr=%q", code, stderr.String())
		}
		if strings.Contains(stderr.String(), key) {
			t.Fatalf("diagnostic leaked exact credential: %q", stderr.String())
		}
	})
	t.Run("flag parse error", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		args := []string{"devin-client", insightsOperation, "--api-key=private-key", "--created_after=private-key"}
		code := Run(context.Background(), args, Options{Stdout: &stdout, Stderr: &stderr, Stdin: strings.NewReader("")})
		if code != 2 {
			t.Fatalf("Run() code = %d; want 2; stderr: %s", code, stderr.String())
		}
		if strings.Contains(stdout.String()+stderr.String(), "private-key") {
			t.Fatal("CLI leaked the supplied API key in a parse diagnostic")
		}
	})
	t.Run("invalid JSON", func(t *testing.T) {
		stdout, stderr, code := runOperation(context.Background(), createBlueprint, "--dry-run", "--data", "{")
		if code != 2 || !strings.Contains(string(stderr), "one valid JSON document") || len(stdout) != 0 {
			t.Fatalf("stdout=%q stderr=%q code=%d", stdout, stderr, code)
		}
	})
	t.Run("invalid JSON before pending SDK integration", func(t *testing.T) {
		stdout, stderr, code := runOperation(context.Background(), createBlueprint, "--data", "{")
		if code != 2 || !strings.Contains(string(stderr), "one valid JSON document") || strings.Contains(string(stderr), "SDK integration is pending") {
			t.Fatalf("stdout=%q stderr=%q code=%d", stdout, stderr, code)
		}
	})
	t.Run("missing JSON file", func(t *testing.T) {
		stdout, stderr, code := runOperation(context.Background(), createBlueprint, "--dry-run", "--data", "@/path/that/does/not/exist")
		if code != 1 || !strings.Contains(string(stderr), "could not read JSON file") || len(stdout) != 0 {
			t.Fatalf("stdout=%q stderr=%q code=%d", stdout, stderr, code)
		}
	})
	t.Run("missing required body", func(t *testing.T) {
		stdout, stderr, code := runOperation(context.Background(), appendTags, "--dry-run", "--devin_id=devin-1")
		if code != 2 || !strings.Contains(string(stderr), "required input missing: --data") || len(stdout) != 0 {
			t.Fatalf("stdout=%q stderr=%q code=%d", stdout, stderr, code)
		}
	})
	t.Run("missing required path parameter", func(t *testing.T) {
		stdout, stderr, code := runOperation(context.Background(), profileOperation, "--dry-run")
		if code != 2 || !strings.Contains(string(stderr), "required input missing: --profile_id") || len(stdout) != 0 {
			t.Fatalf("stdout=%q stderr=%q code=%d", stdout, stderr, code)
		}
	})
	t.Run("invalid timeout", func(t *testing.T) {
		stdout, stderr, code := runOperation(context.Background(), createBlueprint, "--dry-run", "--timeout=invalid", "--data", `{"valid":true}`)
		if code != 1 || !strings.Contains(string(stderr), "timeout must be a Go duration") || len(stdout) != 0 {
			t.Fatalf("stdout=%q stderr=%q code=%d", stdout, stderr, code)
		}
	})
}

func TestWrongBodyFlagIsRejectedBeforeSDKOrEnvironment(t *testing.T) {
	for _, test := range []struct {
		operation string
		flags     []string
	}{
		{operation: uploadAttachment, flags: []string{"--org_id=org-1", "--data", `{"x":1}`}},
		{operation: createBlueprint, flags: []string{"--file", "/must/not/be/read", "--data", `{"x":1}`}},
	} {
		var stdout, stderr bytes.Buffer
		getenvCalls := 0
		options := Options{
			Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr,
			Getenv: func(string) string { getenvCalls++; return "" },
		}
		args := append([]string{"devin-client", test.operation, "--api-key=test-key"}, test.flags...)
		if code := Run(context.Background(), args, options); code != 2 {
			t.Errorf("Run(%v) code = %d; want 2; stderr: %s", args, code, stderr.String())
		}
		if getenvCalls != 0 {
			t.Errorf("Run(%v) side effects: getenv=%d", args, getenvCalls)
		}
		if strings.Contains(stdout.String()+stderr.String(), "test-key") {
			t.Errorf("Run(%v) leaked the API key", args)
		}
	}
}

func TestAPIErrorsAreNotPrintedAndRedirectsAreNotFollowed(t *testing.T) {
	followed := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/followed" {
			followed++
			writer.WriteHeader(http.StatusOK)
			return
		}
		writer.Header().Set("Location", "/followed")
		writer.WriteHeader(http.StatusFound)
		_, _ = io.WriteString(writer, "private response text")
	}))
	defer server.Close()
	var stdout, stderr bytes.Buffer
	options := testOptions(server, func(string) string { return "" })
	options.Stdout, options.Stderr = &stdout, &stderr
	args := []string{"devin-client", insightsOperation, "--allow-insecure-http", "--base-url", server.URL, "--api-key=private-key"}
	if code := Run(context.Background(), args, options); code != 1 {
		t.Fatalf("Run() code = %d; want 1; stderr: %s", code, stderr.String())
	}
	if followed != 0 {
		t.Fatalf("redirect target was called %d times", followed)
	}
	if strings.Contains(stdout.String()+stderr.String(), "private-key") || strings.Contains(stdout.String()+stderr.String(), "private response text") {
		t.Fatal("CLI leaked key or API error body")
	}
}

type countingReader struct {
	reader io.Reader
	reads  int
}

func (r *countingReader) Read(data []byte) (int, error) {
	r.reads++
	return r.reader.Read(data)
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }

func TestOutputFailureReturnsExitOne(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(writer, "response")
	}))
	defer server.Close()
	var stderr bytes.Buffer
	options := testOptions(server, func(string) string { return "" })
	options.Stdout, options.Stderr = failingWriter{}, &stderr
	args := []string{"devin-client", insightsOperation, "--allow-insecure-http", "--base-url", server.URL, "--api-key=test-key"}
	if code := Run(context.Background(), args, options); code != 1 {
		t.Fatalf("Run() code = %d; want 1; stderr: %s", code, stderr.String())
	}
}

func TestInfoCommandOutputFailureReturnsExitOne(t *testing.T) {
	var stderr bytes.Buffer
	if code := Run(context.Background(), []string{"devin-client", "ops"}, Options{Stdout: failingWriter{}, Stderr: &stderr, Getenv: func(string) string { return "" }}); code != 1 {
		t.Fatalf("ops writer failure code=%d stderr=%q", code, stderr.String())
	}
}

func TestSDKTimeoutReturnsExitOne(t *testing.T) {
	entered := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		close(entered)
		<-request.Context().Done()
	}))
	defer server.Close()
	args := []string{"devin-client", insightsOperation, "--allow-insecure-http", "--base-url", server.URL, "--api-key=test-key", "--timeout=10ms"}
	var stdout, stderr bytes.Buffer
	options := testOptions(server, func(string) string { return "" })
	options.Stdout, options.Stderr = &stdout, &stderr
	if code := Run(context.Background(), args, options); code != 1 {
		t.Fatalf("timeout code=%d stderr=%q", code, stderr.String())
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("request did not reach local server")
	}
}

func TestSDKCancellationReturnsExitOne(t *testing.T) {
	entered := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) { entered <- struct{}{}; <-request.Context().Done() }))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { <-entered; cancel() }()
	args := []string{"devin-client", insightsOperation, "--allow-insecure-http", "--base-url", server.URL, "--api-key=test-key"}
	var stdout, stderr bytes.Buffer
	options := testOptions(server, func(string) string { return "" })
	options.Stdout, options.Stderr = &stdout, &stderr
	if code := Run(ctx, args, options); code != 1 {
		t.Fatalf("cancellation code=%d stderr=%q", code, stderr.String())
	}
}

func TestProductionRequestsUseAuthenticatedSDKTransportAndPreserveRawResponse(t *testing.T) {
	var stdout, stderr bytes.Buffer
	const raw = "{ \"raw\": true }\n"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("Authorization=%q", request.Header.Get("Authorization"))
		}
		_, _ = io.WriteString(writer, raw)
	}))
	defer server.Close()
	options := testOptions(server, func(string) string { return "" })
	options.Stdout, options.Stderr = &stdout, &stderr
	args := []string{"devin-client", insightsOperation, "--allow-insecure-http", "--base-url", server.URL, "--api-key=test-key"}
	code := Run(context.Background(), args, options)
	if code != 0 {
		t.Fatalf("stdout=%q stderr=%q code=%d", stdout.String(), stderr.String(), code)
	}
	if stdout.String() != raw {
		t.Fatalf("stdout=%q want raw %q", stdout.String(), raw)
	}
}

func TestSpecialSDKOperationsUseSDKMethodsAndPreserveRawOutput(t *testing.T) {
	filePath := filepath.Join(t.TempDir(), "upload.txt")
	if err := os.WriteFile(filePath, []byte("upload-data"), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		id, method, path, query, response string
		flags                             []string
		multipart                         bool
	}{
		{id: "handle_get_self_v3_self_get", method: http.MethodGet, path: "/v3/self", response: "{ \"self\": true }\n"},
		{id: uploadAttachment, method: http.MethodPost, path: "/v3/organizations/org-1/attachments", response: "{\"attachment_id\":\"att-1\"}\n", flags: []string{"--org_id=org-1", "--file", filePath}, multipart: true},
		{id: "upload_enterprise_blueprint_file_endpoint_v3_enterprise_snapshot_setup_blueprints__blueprint_id__files_post", method: http.MethodPost, path: "/v3/enterprise/snapshot-setup/blueprints/bp-1/files", query: "name=blueprint", response: "{\"name\":\"file\"}\n", flags: []string{"--blueprint_id=bp-1", "--name=blueprint", "--file", filePath}, multipart: true},
		{id: "upload_org_blueprint_file_v3_organizations__org_id__snapshot_setup_blueprints__blueprint_id__files_post", method: http.MethodPost, path: "/v3/organizations/org-1/snapshot-setup/blueprints/bp-1/files", query: "name=blueprint", response: "{\"name\":\"file\"}\n", flags: []string{"--org_id=org-1", "--blueprint_id=bp-1", "--name=blueprint", "--file", filePath}, multipart: true},
	}
	for _, test := range cases {
		t.Run(test.id, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != test.method || r.URL.Path != test.path || r.URL.RawQuery != test.query {
					t.Errorf("request=%s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery)
				}
				if r.Header.Get("Authorization") != "Bearer test-key" {
					t.Errorf("authorization=%q", r.Header.Get("Authorization"))
				}
				if test.multipart {
					file, header, err := r.FormFile("file")
					if err != nil {
						t.Errorf("FormFile: %v", err)
					} else {
						data, _ := io.ReadAll(file)
						file.Close()
						if header.Filename != "upload.txt" || string(data) != "upload-data" {
							t.Errorf("upload=%s %q", header.Filename, data)
						}
					}
				}
				_, _ = io.WriteString(w, test.response)
			}))
			defer server.Close()
			args := []string{"devin-client", test.id, "--allow-insecure-http", "--base-url", server.URL, "--api-key=test-key"}
			args = append(args, test.flags...)
			var stdout, stderr bytes.Buffer
			options := testOptions(server, func(string) string { return "" })
			options.Stdout, options.Stderr = &stdout, &stderr
			if code := Run(context.Background(), args, options); code != 0 {
				t.Fatalf("code=%d stderr=%q", code, stderr.String())
			}
			if stdout.String() != test.response {
				t.Fatalf("stdout=%q want=%q", stdout.String(), test.response)
			}
		})
	}
}

func TestAllOperationCommandsSupportHelpDryRunAndReadOnly(t *testing.T) {
	projection, err := embeddedProjection()
	if err != nil {
		t.Fatal(err)
	}
	filePath := filepath.Join(t.TempDir(), "empty-upload")
	if err := os.WriteFile(filePath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	r := &runner{ctx: context.Background(), opts: Options{Stdin: strings.NewReader(""), Getenv: func(name string) string {
		if name == "DEVIN_API_KEY" {
			t.Fatal("offline command read API key")
		}
		return ""
	}}, stdout: &trackingWriter{writer: io.Discard}, stderr: &trackingWriter{writer: io.Discard}}
	app, err := r.newApp()
	if err != nil {
		t.Fatal(err)
	}
	run := func(args []string) int {
		err := app.RunContext(context.Background(), args)
		if err == nil {
			return 0
		}
		var commandErr *commandFailure
		if errors.As(err, &commandErr) {
			return commandErr.code
		}
		return 2
	}
	for _, operation := range projection.Operations {
		if code := run([]string{"devin-client", operation.ID, "--help"}); code != 0 {
			t.Errorf("%s help exit=%d", operation.ID, code)
		}
		args := []string{"devin-client", operation.ID}
		for _, parameter := range operation.Parameters {
			if !parameter.Required {
				continue
			}
			value := "x"
			switch parameter.Type {
			case "integer", "number":
				value = "1"
			case "boolean":
				value = "true"
			case "object", "union":
				value = "{}"
			}
			args = append(args, "--"+parameter.Name+"="+value)
		}
		if operation.RequestBody != nil {
			switch requestBodyKind(operation) {
			case bodyJSON:
				if operation.RequestBody.Required {
					args = append(args, "--data={}")
				}
			case bodyMultipart:
				args = append(args, "--file", filePath)
			}
		}
		args = append(args, "--dry-run")
		if code := run(args); code != 0 {
			t.Errorf("%s dry-run exit=%d", operation.ID, code)
		}
		readonlyArgs := append(append([]string(nil), args...), "--read-only")
		want := 0
		if operation.Method != http.MethodGet && operation.Method != http.MethodHead {
			want = 2
		}
		if code := run(readonlyArgs); code != want {
			t.Errorf("%s read-only exit=%d want=%d", operation.ID, code, want)
		}
	}
}

func TestNetworkFailureReturnsExitOneAndRedactsKey(t *testing.T) {
	var stdout, stderr bytes.Buffer
	options := Options{
		Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr, Getenv: func(string) string { return "" },
		HTTPClient: &http.Client{Transport: failingRoundTripper{}},
	}
	args := []string{"devin-client", insightsOperation, "--api-key=private-key"}
	if code := Run(context.Background(), args, options); code != 1 {
		t.Fatalf("Run() code = %d; want 1", code)
	}
	if strings.Contains(stderr.String(), "private-key") || !strings.Contains(stderr.String(), "API request failed") {
		t.Fatalf("unexpected diagnostic: %q", stderr.String())
	}
}

type failingRoundTripper struct{}

func (failingRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("private-key network failure")
}
