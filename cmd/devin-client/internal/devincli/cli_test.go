package devincli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
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

type httpDispatcher struct {
	client   *http.Client
	settings settings
}

func (d httpDispatcher) Do(_ context.Context, request *http.Request) (*http.Response, error) {
	base, err := url.Parse(d.settings.BaseURL)
	if err == nil && request.URL.Scheme == base.Scheme && request.URL.Host == base.Host {
		request.Header.Set("Authorization", "Bearer "+d.settings.APIKey)
	}
	return d.client.Do(request)
}

func testOptions(server *httptest.Server, getenv func(string) string) Options {
	return Options{
		Getenv:     getenv,
		HTTPClient: server.Client(),
		sdkFactory: func(configuration settings, client *http.Client) (sdkDispatcher, error) {
			return httpDispatcher{client: client, settings: configuration}, nil
		},
	}
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
		if query.Get("created_after") != "0" || query.Get("first") != "0" || query.Get("is_archived") != "false" {
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
		insightsOperation, "--created_after", "0", "--first", "0", "--is_archived=false", "--tags", "alpha", "--tags", "beta"}
	var stdout, stderr bytes.Buffer
	options.Stdout, options.Stderr = &stdout, &stderr
	if code := Run(context.Background(), args, options); code != 0 {
		t.Fatalf("Run() code = %d, stderr: %s", code, stderr.String())
	}
	if stdout.String() != response {
		t.Fatalf("stdout = %q; want raw response %q", stdout.String(), response)
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
		_, _ = io.WriteString(writer, "uploaded")
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
	if stdout.String() != "uploaded" {
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
			factoryCalled := false
			options := Options{
				Stdin: stdin, Stdout: &stdout, Stderr: &stderr,
				Getenv: func(string) string { getenvCalls++; return "" },
				sdkFactory: func(settings, *http.Client) (sdkDispatcher, error) {
					factoryCalled = true
					return nil, errors.New("unexpected factory call")
				},
			}
			args := append([]string{"devin-client", test.operation, "--read-only", "--dry-run", "--timeout=not-a-duration", "--api-key=secret"}, test.flags...)
			if code := Run(context.Background(), args, options); code != 2 {
				t.Fatalf("Run() code = %d; want 2; stderr: %s", code, stderr.String())
			}
			if getenvCalls != 0 || stdin.reads != 0 || factoryCalled {
				t.Fatalf("read-only rejection side effects: getenv=%d stdin=%d factory=%t", getenvCalls, stdin.reads, factoryCalled)
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
	factoryCalled := false
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
		sdkFactory: func(settings, *http.Client) (sdkDispatcher, error) {
			factoryCalled = true
			return nil, errors.New("dry-run must not create an SDK")
		},
	}
	args := []string{"devin-client", createBlueprint, "--dry-run", "--api-key=argument-secret", "--data", `{"name":"preview"}`}
	if code := Run(context.Background(), args, options); code != 0 {
		t.Fatalf("Run() code = %d; stderr: %s", code, stderr.String())
	}
	if factoryCalled {
		t.Fatal("dry-run constructed an SDK dispatcher")
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
		sdkFactory: func(settings, *http.Client) (sdkDispatcher, error) {
			return nil, errors.New("dry-run must not create an SDK")
		},
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
		if len(args) > 1 && args[1] == "--version" && !strings.Contains(stdout.String(), "dev") {
			t.Errorf("unreleased version output = %q; want dev", stdout.String())
		}
	}
}

func TestErrorsAreClassifiedAndRedactSuppliedKey(t *testing.T) {
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
		getenvCalls, factoryCalls := 0, 0
		options := Options{
			Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr,
			Getenv: func(string) string { getenvCalls++; return "" },
			sdkFactory: func(settings, *http.Client) (sdkDispatcher, error) {
				factoryCalls++
				return nil, errors.New("unexpected factory call")
			},
		}
		args := append([]string{"devin-client", test.operation, "--api-key=test-key"}, test.flags...)
		if code := Run(context.Background(), args, options); code != 2 {
			t.Errorf("Run(%v) code = %d; want 2; stderr: %s", args, code, stderr.String())
		}
		if getenvCalls != 0 || factoryCalls != 0 {
			t.Errorf("Run(%v) side effects: getenv=%d factory=%d", args, getenvCalls, factoryCalls)
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

func TestProductionRequestsReportPendingSDKIntegration(t *testing.T) {
	var stdout, stderr bytes.Buffer
	args := []string{"devin-client", insightsOperation, "--api-key=test-key"}
	code := Run(context.Background(), args, Options{
		Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr, Getenv: func(string) string { return "" },
	})
	if code != 1 || !strings.Contains(stderr.String(), "SDK integration is pending") {
		t.Fatalf("stdout=%q stderr=%q code=%d", stdout.String(), stderr.String(), code)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestNetworkFailureReturnsExitOneAndRedactsKey(t *testing.T) {
	var stdout, stderr bytes.Buffer
	options := Options{
		Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: &stderr, Getenv: func(string) string { return "" },
		sdkFactory: func(settings, *http.Client) (sdkDispatcher, error) {
			return failingDispatcher{}, nil
		},
	}
	args := []string{"devin-client", insightsOperation, "--api-key=private-key"}
	if code := Run(context.Background(), args, options); code != 1 {
		t.Fatalf("Run() code = %d; want 1", code)
	}
	if strings.Contains(stderr.String(), "private-key") || !strings.Contains(stderr.String(), "API request failed") {
		t.Fatalf("unexpected diagnostic: %q", stderr.String())
	}
}

type failingDispatcher struct{}

func (failingDispatcher) Do(context.Context, *http.Request) (*http.Response, error) {
	return nil, errors.New("private-key network failure")
}
