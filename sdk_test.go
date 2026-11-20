package devin

import (
	"bytes"
	"context"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

type recordedTransport struct {
	calls   atomic.Int32
	request *http.Request
}

func (t *recordedTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	t.calls.Add(1)
	t.request = request
	return testResponse(request, http.StatusOK, "{}"), nil
}

func testResponse(request *http.Request, status int, body string) *http.Response {
	header := make(http.Header)
	return &http.Response{
		StatusCode:    status,
		Status:        http.StatusText(status),
		Header:        header,
		Body:          io.NopCloser(strings.NewReader(body)),
		ContentLength: -1,
		Request:       request,
	}
}

func newTestClient(t *testing.T, baseURL string, transport http.RoundTripper) *Client {
	t.Helper()
	client, err := New(Config{
		APIKey:            "test-api-key",
		BaseURL:           baseURL,
		HTTPClient:        &http.Client{Transport: transport},
		AllowInsecureHTTP: strings.HasPrefix(baseURL, "http://"),
	})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestNewRejectsInvalidConfiguration(t *testing.T) {
	cases := []struct {
		name      string
		config    Config
		wantError string
	}{
		{name: "empty API key", config: Config{}, wantError: "API key"},
		{name: "ASCII whitespace in API key", config: Config{APIKey: "secret key"}, wantError: "whitespace"},
		{name: "Unicode whitespace in API key", config: Config{APIKey: "secret\u00a0key"}, wantError: "whitespace"},
		{name: "control in API key", config: Config{APIKey: "secret\nkey"}, wantError: "control"},
		{name: "Unicode control in API key", config: Config{APIKey: "secret\u0090key"}, wantError: "control"},
		{name: "negative timeout", config: Config{APIKey: "secret", Timeout: -time.Second}, wantError: "timeout"},
		{name: "relative URL", config: Config{APIKey: "secret", BaseURL: "api.example.test"}, wantError: "absolute"},
		{name: "missing hostname", config: Config{APIKey: "secret", BaseURL: "https:///"}, wantError: "hostname"},
		{name: "userinfo", config: Config{APIKey: "secret", BaseURL: "https://secret@example.test"}, wantError: "user information"},
		{name: "query", config: Config{APIKey: "secret", BaseURL: "https://example.test?mode=1"}, wantError: "query"},
		{name: "bare query", config: Config{APIKey: "secret", BaseURL: "https://example.test?"}, wantError: "query"},
		{name: "fragment", config: Config{APIKey: "secret", BaseURL: "https://example.test/#fragment"}, wantError: "fragment"},
		{name: "bare fragment", config: Config{APIKey: "secret", BaseURL: "https://example.test/#"}, wantError: "fragment"},
		{name: "path", config: Config{APIKey: "secret", BaseURL: "https://example.test/api"}, wantError: "path"},
		{name: "encoded path", config: Config{APIKey: "secret", BaseURL: "https://example.test/%2F"}, wantError: "path"},
		{name: "HTTP without opt-in", config: Config{APIKey: "secret", BaseURL: "http://example.test"}, wantError: "AllowInsecureHTTP"},
		{name: "unsupported scheme", config: Config{APIKey: "secret", BaseURL: "ftp://example.test"}, wantError: "scheme"},
		{name: "nonnumeric port", config: Config{APIKey: "secret", BaseURL: "https://example.test:not-a-port"}, wantError: "port"},
		{name: "empty port", config: Config{APIKey: "secret", BaseURL: "https://example.test:"}, wantError: "port"},
		{name: "port out of range", config: Config{APIKey: "secret", BaseURL: "https://example.test:65536"}, wantError: "port"},
		{name: "userinfo is not echoed", config: Config{APIKey: "secret", BaseURL: "https://secret@example.test"}, wantError: "user information"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := New(tc.config)
			if err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("New error = %v, want message containing %q", err, tc.wantError)
			}
			if strings.Contains(err.Error(), "secret") {
				t.Fatalf("New error exposed a secret: %q", err)
			}
		})
	}
}

func TestNewDefaultsAndCopiesHTTPClient(t *testing.T) {
	rootSlash, err := New(Config{APIKey: "root-key", BaseURL: "https://example.test/"})
	if err != nil {
		t.Fatalf("root slash BaseURL was rejected: %v", err)
	}
	if rootSlash.BaseURL != "https://example.test" {
		t.Errorf("root slash BaseURL normalized to %q", rootSlash.BaseURL)
	}

	baseTransport := &recordedTransport{}
	redirectCalls := 0
	original := &http.Client{
		Transport: baseTransport,
		Timeout:   17 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			redirectCalls++
			return errors.New("original redirect policy")
		},
	}

	client, err := New(Config{APIKey: "test-key", HTTPClient: original})
	if err != nil {
		t.Fatal(err)
	}
	if client.BaseURL != DefaultBaseURL {
		t.Errorf("default BaseURL = %q, want %q", client.BaseURL, DefaultBaseURL)
	}
	if client.HTTP == original {
		t.Fatal("New reused the caller's HTTP client")
	}
	if client.HTTP.Timeout != 60*time.Second {
		t.Errorf("default timeout = %s, want 60s", client.HTTP.Timeout)
	}
	if client.HTTP.Transport.(*bearerTransport).base != baseTransport {
		t.Fatal("New did not preserve the caller's transport behind the bearer wrapper")
	}
	if err := client.HTTP.CheckRedirect(nil, nil); !errors.Is(err, http.ErrUseLastResponse) {
		t.Errorf("CheckRedirect error = %v, want ErrUseLastResponse", err)
	}
	if original.Timeout != 17*time.Second || original.Transport != baseTransport {
		t.Errorf("New mutated the caller's HTTP client: %#v", original)
	}
	if original.CheckRedirect == nil {
		t.Fatal("New cleared the caller's redirect policy")
	}
	if err := original.CheckRedirect(nil, nil); err == nil || err.Error() != "original redirect policy" || redirectCalls != 1 {
		t.Fatalf("caller redirect policy changed: error=%v calls=%d", err, redirectCalls)
	}
	if got := client.DefaultHeaders.Get("Authorization"); got != "" {
		t.Fatalf("bearer key stored in DefaultHeaders: %q", got)
	}

	customTimeoutClient := &http.Client{Timeout: 5 * time.Second}
	overridden, err := New(Config{APIKey: "test-key", HTTPClient: customTimeoutClient, Timeout: 9 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if overridden.HTTP.Timeout != 9*time.Second {
		t.Errorf("positive Config.Timeout = %s, want 9s", overridden.HTTP.Timeout)
	}
	if customTimeoutClient.Timeout != 5*time.Second {
		t.Errorf("positive timeout mutated caller's HTTP client: %s", customTimeoutClient.Timeout)
	}
}

func TestNewDoesNotReadEnvironmentOrPerformNetworkIO(t *testing.T) {
	t.Setenv("DEVIN_API_KEY", "environment-key")
	transport := &recordedTransport{}
	if _, err := New(Config{HTTPClient: &http.Client{Transport: transport}}); err == nil {
		t.Fatal("New accepted a key from the environment")
	}
	if transport.calls.Load() != 0 {
		t.Fatalf("invalid construction made %d requests", transport.calls.Load())
	}
	if _, err := New(Config{APIKey: "explicit-key", HTTPClient: &http.Client{Transport: transport}}); err != nil {
		t.Fatal(err)
	}
	if transport.calls.Load() != 0 {
		t.Fatalf("valid construction made %d requests", transport.calls.Load())
	}
}

func TestBearerTransportClonesRequestAndNormalizesOrigin(t *testing.T) {
	cases := []struct {
		name    string
		baseURL string
		request string
	}{
		{name: "HTTPS host case and explicit default port", baseURL: "https://EXAMPLE.test:0443", request: "https://example.TEST/path"},
		{name: "HTTP explicit default port", baseURL: "http://EXAMPLE.test", request: "http://example.test:80/path"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			inner := &recordedTransport{}
			client := newTestClient(t, tc.baseURL, inner)
			request, err := http.NewRequest(http.MethodGet, tc.request, nil)
			if err != nil {
				t.Fatal(err)
			}
			request.Header.Set("Authorization", "caller-value")
			request.Header.Set("X-Original", "preserved")
			response, err := client.HTTP.Transport.RoundTrip(request)
			if err != nil {
				t.Fatal(err)
			}
			response.Body.Close()
			if inner.request == request {
				t.Fatal("bearer transport passed the caller's request directly")
			}
			if got := inner.request.Header.Get("Authorization"); got != "Bearer test-api-key" {
				t.Errorf("forwarded Authorization = %q", got)
			}
			if got := inner.request.Header.Get("X-Original"); got != "preserved" {
				t.Errorf("forwarded X-Original = %q", got)
			}
			if got := request.Header.Get("Authorization"); got != "caller-value" {
				t.Errorf("caller Authorization was mutated to %q", got)
			}
			if got := request.Header.Get("X-Original"); got != "preserved" {
				t.Errorf("caller X-Original was mutated to %q", got)
			}
		})
	}
}

func TestBearerTransportRejectsOffOriginBeforeInnerTransport(t *testing.T) {
	inner := &recordedTransport{}
	client := newTestClient(t, "https://api.example.test", inner)
	transport := client.HTTP.Transport

	request, err := http.NewRequest(http.MethodGet, "https://other.example.test/v3/self", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = transport.RoundTrip(request)
	if err == nil || strings.Contains(err.Error(), "test-api-key") {
		t.Fatalf("off-origin error = %v", err)
	}
	if inner.calls.Load() != 0 {
		t.Fatalf("off-origin request reached inner transport %d times", inner.calls.Load())
	}

	client.BaseURL = "https://changed.example.test"
	_, _, err = client.GetQueueV3EnterpriseQueueGet(context.Background())
	if err == nil {
		t.Fatal("generated operation succeeded after BaseURL changed off-origin")
	}
	if inner.calls.Load() != 0 {
		t.Fatalf("changed BaseURL reached inner transport %d times", inner.calls.Load())
	}
}

func TestAuthenticatedClientUsesBearerForGeneratedOperation(t *testing.T) {
	gotAuthorization := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuthorization <- r.Header.Get("Authorization")
		_, _ = io.WriteString(w, `{}`)
	}))
	defer server.Close()

	client, err := New(Config{APIKey: "generated-key", BaseURL: server.URL, AllowInsecureHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	result, metadata, err := client.GetQueueV3EnterpriseQueueGet(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || metadata == nil || metadata.StatusCode != http.StatusOK {
		t.Fatalf("generated operation result=%#v metadata=%#v", result, metadata)
	}
	if got := <-gotAuthorization; got != "Bearer generated-key" {
		t.Errorf("Authorization = %q", got)
	}
	if got := client.DefaultHeaders.Get("Authorization"); got != "" {
		t.Errorf("generated client stored bearer key in DefaultHeaders: %q", got)
	}
}

func TestAuthenticatedClientDoesNotFollowRedirects(t *testing.T) {
	var secondServerCalls atomic.Int32
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		secondServerCalls.Add(1)
		_, _ = io.WriteString(w, `{}`)
	}))
	defer second.Close()

	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", second.URL)
		w.WriteHeader(http.StatusTemporaryRedirect)
		_, _ = io.WriteString(w, "redirect body")
	}))
	defer first.Close()

	client, err := New(Config{APIKey: "redirect-key", BaseURL: first.URL, AllowInsecureHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	_, metadata, err := client.GetQueueV3EnterpriseQueueGet(context.Background())
	var apiError *APIError
	if !errors.As(err, &apiError) {
		t.Fatalf("redirect error = %v, want *APIError", err)
	}
	if metadata == nil || metadata.StatusCode != http.StatusTemporaryRedirect || apiError.StatusCode != http.StatusTemporaryRedirect {
		t.Fatalf("redirect metadata=%#v error=%#v", metadata, apiError)
	}
	if string(apiError.Body) != "redirect body" {
		t.Errorf("redirect APIError body = %q", apiError.Body)
	}
	if secondServerCalls.Load() != 0 {
		t.Errorf("redirect target called %d times", secondServerCalls.Load())
	}
}

func TestAuthenticatedClientPreservesContextCancellationAndDeadline(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()
	client, err := New(Config{APIKey: "context-key", BaseURL: server.URL, AllowInsecureHTTP: true})
	if err != nil {
		t.Fatal(err)
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err = client.GetQueueV3EnterpriseQueueGet(canceled)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("canceled request error = %v, want context.Canceled", err)
	}

	deadline, cancelDeadline := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancelDeadline()
	_, _, err = client.GetQueueV3EnterpriseQueueGet(deadline)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("deadline request error = %v, want context.DeadlineExceeded", err)
	}
}

type trackedReader struct {
	io.Reader
	closed atomic.Int32
}

func (r *trackedReader) Close() error {
	r.closed.Add(1)
	return nil
}

type uploadObservation struct {
	method          string
	path            string
	rawQuery        string
	requestType     string
	partType        string
	formName        string
	fileName        string
	fileContents    string
	additionalParts bool
	err             string
}

func TestMultipartUploadsMatchContractsAndStreamReaders(t *testing.T) {
	cases := []struct {
		name         string
		wantPath     string
		wantQuery    string
		wantMethod   string
		wantStatus   int
		responseBody string
		checkResult  func(*testing.T, *Client, io.Reader) (*APIResponse, error)
	}{
		{
			name:         "enterprise blueprint",
			wantPath:     "/v3/enterprise/snapshot-setup/blueprints/blue%2Fone/files",
			wantQuery:    "name=",
			wantMethod:   http.MethodPost,
			wantStatus:   http.StatusCreated,
			responseBody: `{"name":"uploaded"}`,
			checkResult: func(t *testing.T, client *Client, file io.Reader) (*APIResponse, error) {
				got, metadata, err := client.UploadEnterpriseBlueprintFile(context.Background(), UploadEnterpriseBlueprintFileParams{
					BlueprintID: "blue/one", Name: "", FileName: `résumé "one".txt`, File: file,
				})
				if err == nil && (got == nil || got.Name != "uploaded") {
					t.Errorf("enterprise upload result = %#v", got)
				}
				return metadata, err
			},
		},
		{
			name:         "attachment",
			wantPath:     "/v3/organizations/org%2Fteam/attachments",
			wantMethod:   http.MethodPost,
			wantStatus:   http.StatusOK,
			responseBody: `{"attachment_id":"att-1"}`,
			checkResult: func(t *testing.T, client *Client, file io.Reader) (*APIResponse, error) {
				got, metadata, err := client.UploadAttachment(context.Background(), UploadAttachmentParams{
					OrgID: "org/team", FileName: `résumé "one".txt`, File: file,
				})
				if err == nil && (got == nil || got.AttachmentId != "att-1") {
					t.Errorf("attachment upload result = %#v", got)
				}
				return metadata, err
			},
		},
		{
			name:         "organization blueprint",
			wantPath:     "/v3/organizations/org%20team/snapshot-setup/blueprints/blue%2Fone/files",
			wantQuery:    "name=env+file",
			wantMethod:   http.MethodPost,
			wantStatus:   http.StatusCreated,
			responseBody: `{"name":"uploaded"}`,
			checkResult: func(t *testing.T, client *Client, file io.Reader) (*APIResponse, error) {
				got, metadata, err := client.UploadOrgBlueprintFile(context.Background(), UploadOrgBlueprintFileParams{
					OrgID: "org team", BlueprintID: "blue/one", Name: "env file", FileName: `résumé "one".txt`, File: file,
				})
				if err == nil && (got == nil || got.Name != "uploaded") {
					t.Errorf("organization blueprint upload result = %#v", got)
				}
				return metadata, err
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			observed := make(chan uploadObservation, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				observation := uploadObservation{
					method:      r.Method,
					path:        r.URL.EscapedPath(),
					rawQuery:    r.URL.RawQuery,
					requestType: r.Header.Get("Content-Type"),
				}
				mediaType, _, err := mime.ParseMediaType(observation.requestType)
				if err != nil {
					observation.err = err.Error()
				} else if mediaType != "multipart/form-data" {
					observation.err = "request is not multipart/form-data"
				}
				reader, err := r.MultipartReader()
				if err != nil {
					observation.err = err.Error()
				} else {
					part, err := reader.NextPart()
					if err != nil {
						observation.err = err.Error()
					} else {
						observation.formName = part.FormName()
						observation.fileName = part.FileName()
						observation.partType = part.Header.Get("Content-Type")
						contents, err := io.ReadAll(part)
						if err != nil {
							observation.err = err.Error()
						}
						observation.fileContents = string(contents)
						_, err = reader.NextPart()
						observation.additionalParts = err == nil
						if err != nil && !errors.Is(err, io.EOF) {
							observation.err = err.Error()
						}
					}
				}
				observed <- observation
				w.Header().Set("X-Upload", "received")
				w.WriteHeader(tc.wantStatus)
				_, _ = io.WriteString(w, tc.responseBody)
			}))
			defer server.Close()

			client, err := New(Config{APIKey: "upload-key", BaseURL: server.URL, AllowInsecureHTTP: true})
			if err != nil {
				t.Fatal(err)
			}
			file := &trackedReader{Reader: strings.NewReader("file payload")}
			metadata, err := tc.checkResult(t, client, file)
			if err != nil {
				t.Fatal(err)
			}
			observation := <-observed
			if observation.err != "" {
				t.Fatalf("multipart observation error: %s", observation.err)
			}
			if observation.method != tc.wantMethod || observation.path != tc.wantPath || observation.rawQuery != tc.wantQuery {
				t.Errorf("request = %s %s?%s; want %s %s?%s", observation.method, observation.path, observation.rawQuery, tc.wantMethod, tc.wantPath, tc.wantQuery)
			}
			if observation.formName != "file" || observation.fileName != `résumé "one".txt` || observation.partType != "application/octet-stream" {
				t.Errorf("file part name=%q filename=%q content-type=%q", observation.formName, observation.fileName, observation.partType)
			}
			if observation.fileContents != "file payload" || observation.additionalParts {
				t.Errorf("file contents=%q additional parts=%v", observation.fileContents, observation.additionalParts)
			}
			if metadata == nil || metadata.StatusCode != tc.wantStatus || metadata.Header.Get("X-Upload") != "received" {
				t.Errorf("upload response metadata = %#v", metadata)
			}
			if got := file.closed.Load(); got != 0 {
				t.Errorf("upload closed caller-owned reader %d times", got)
			}
		})
	}
}

func TestNilUploadReadersDoNotIssueRequests(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = io.WriteString(w, `{}`)
	}))
	defer server.Close()
	client, err := New(Config{APIKey: "nil-file-key", BaseURL: server.URL, AllowInsecureHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		call func() (*APIResponse, error)
	}{
		{name: "enterprise blueprint", call: func() (*APIResponse, error) {
			_, metadata, err := client.UploadEnterpriseBlueprintFile(context.Background(), UploadEnterpriseBlueprintFileParams{})
			return metadata, err
		}},
		{name: "attachment", call: func() (*APIResponse, error) {
			_, metadata, err := client.UploadAttachment(context.Background(), UploadAttachmentParams{})
			return metadata, err
		}},
		{name: "organization blueprint", call: func() (*APIResponse, error) {
			_, metadata, err := client.UploadOrgBlueprintFile(context.Background(), UploadOrgBlueprintFileParams{})
			return metadata, err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			metadata, err := tc.call()
			if err == nil || metadata != nil {
				t.Fatalf("nil upload returned metadata=%#v error=%v", metadata, err)
			}
		})
	}
	if requests.Load() != 0 {
		t.Fatalf("nil upload readers issued %d requests", requests.Load())
	}
}

var errUploadReader = errors.New("injected upload reader failure")

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errUploadReader }

func TestUploadReaderErrorsAreReturned(t *testing.T) {
	inner := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		_, err := io.Copy(io.Discard, request.Body)
		if err != nil {
			return nil, err
		}
		return testResponse(request, http.StatusOK, `{}`), nil
	})
	client := newTestClient(t, "https://upload.example.test", inner)
	_, _, err := client.UploadAttachment(context.Background(), UploadAttachmentParams{OrgID: "org-1", FileName: "file.bin", File: failingReader{}})
	if !errors.Is(err, errUploadReader) {
		t.Fatalf("upload error = %v, want injected reader failure", err)
	}
}

type gatedLargeReader struct {
	remaining int64
	started   chan struct{}
	release   chan struct{}
	once      sync.Once
}

func (r *gatedLargeReader) Read(buffer []byte) (int, error) {
	if r.remaining == 0 {
		return 0, io.EOF
	}
	r.once.Do(func() {
		close(r.started)
		<-r.release
	})
	n := len(buffer)
	if int64(n) > r.remaining {
		n = int(r.remaining)
	}
	for i := 0; i < n; i++ {
		buffer[i] = 'x'
	}
	r.remaining -= int64(n)
	return n, nil
}

func TestMultipartUploadStartsBeforeLargeReaderIsConsumed(t *testing.T) {
	requestStarted := make(chan struct{})
	bytesRead := make(chan int64, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(requestStarted)
		reader, err := r.MultipartReader()
		if err != nil {
			t.Errorf("create multipart reader: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		part, err := reader.NextPart()
		if err != nil {
			t.Errorf("read multipart part: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		count, err := io.Copy(io.Discard, part)
		if err != nil {
			t.Errorf("read multipart file: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		bytesRead <- count
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{}`)
	}))
	defer server.Close()

	client, err := New(Config{APIKey: "stream-key", BaseURL: server.URL, AllowInsecureHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	file := &gatedLargeReader{remaining: 8 << 20, started: make(chan struct{}), release: make(chan struct{})}
	result := make(chan error, 1)
	go func() {
		_, _, err := client.UploadAttachment(context.Background(), UploadAttachmentParams{OrgID: "org-1", FileName: "large.bin", File: file})
		result <- err
	}()

	select {
	case <-requestStarted:
	case <-time.After(2 * time.Second):
		close(file.release)
		t.Fatal("server did not receive request headers while file reader was gated")
	}
	select {
	case <-file.started:
	case <-time.After(2 * time.Second):
		close(file.release)
		t.Fatal("upload transport did not read the gated file")
	}
	close(file.release)
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("large upload did not finish")
	}
	if got := <-bytesRead; got != 8<<20 {
		t.Errorf("uploaded file bytes = %d, want %d", got, 8<<20)
	}
}

func TestUploadJSONErrorsPreserveResponseMetadata(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{name: "empty JSON", body: ""},
		{name: "malformed JSON", body: "{"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				w.Header().Set("X-Trace-ID", "upload-decode")
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			client, err := New(Config{APIKey: "decode-key", BaseURL: server.URL, AllowInsecureHTTP: true})
			if err != nil {
				t.Fatal(err)
			}
			result, metadata, err := client.UploadAttachment(context.Background(), UploadAttachmentParams{OrgID: "org-1", FileName: "file.bin", File: strings.NewReader("contents")})
			if err == nil || result != nil {
				t.Fatalf("upload result=%#v error=%v, want JSON decode error", result, err)
			}
			if metadata == nil || metadata.StatusCode != http.StatusOK || metadata.Header.Get("X-Trace-ID") != "upload-decode" {
				t.Errorf("decode error metadata = %#v", metadata)
			}
		})
	}
}

func TestAPIErrorPreservesRawBodyMetadataAndRedactsBodyFromError(t *testing.T) {
	const apiKey = "secret-api-key-value"
	const responseBody = `{"echo":"secret-api-key-value"}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Trace-ID", "api-error")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, responseBody)
	}))
	defer server.Close()
	client, err := New(Config{APIKey: apiKey, BaseURL: server.URL, AllowInsecureHTTP: true})
	if err != nil {
		t.Fatal(err)
	}
	_, metadata, err := client.GetQueueV3EnterpriseQueueGet(context.Background())
	var apiError *APIError
	if !errors.As(err, &apiError) {
		t.Fatalf("operation error = %v, want *APIError", err)
	}
	if !bytes.Equal(apiError.Body, []byte(responseBody)) {
		t.Errorf("APIError.Body = %q, want exact response body", apiError.Body)
	}
	if strings.Contains(apiError.Error(), apiKey) || strings.Contains(apiError.Error(), responseBody) {
		t.Errorf("APIError.Error exposed body or key: %q", apiError.Error())
	}
	if metadata == nil || metadata.StatusCode != http.StatusUnauthorized || metadata.Header.Get("X-Trace-ID") != "api-error" {
		t.Errorf("APIError metadata = %#v", metadata)
	}
}

func TestGetSelfPreservesExactJSONForAllUnionMembers(t *testing.T) {
	responses := []struct {
		name string
		body string
	}{
		{name: "service user", body: " {\"member\":\"ServiceUserSelf\",\"unknown\":{\"items\":[null,true]}} \n"},
		{name: "PAT user", body: `{"member":"PatUserSelf","large":900719925474099312345678901234567890}`},
		{name: "Devin Brain user", body: `{"member":"DevinBrainUserSelf","data":{"opaque":"value"}}`},
		{name: "Windsurf session user", body: `{"member":"WindsurfSessionUserSelf","array":[1,2,3]}`},
	}
	for _, tc := range responses {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("X-Self", "preserved")
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			client, err := New(Config{APIKey: "self-key", BaseURL: server.URL, AllowInsecureHTTP: true})
			if err != nil {
				t.Fatal(err)
			}
			got, metadata, err := client.GetSelf(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, []byte(tc.body)) {
				t.Errorf("GetSelf bytes = %q, want exact %q", got, tc.body)
			}
			if metadata == nil || metadata.StatusCode != http.StatusOK || metadata.Header.Get("X-Self") != "preserved" {
				t.Errorf("GetSelf metadata = %#v", metadata)
			}
		})
	}
}

func TestGetSelfMalformedAndEmptyJSONPreserveMetadata(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{name: "empty", body: ""},
		{name: "malformed", body: `{"unfinished":`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("X-Trace-ID", tc.name)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			client, err := New(Config{APIKey: "self-key", BaseURL: server.URL, AllowInsecureHTTP: true})
			if err != nil {
				t.Fatal(err)
			}
			got, metadata, err := client.GetSelf(context.Background())
			if err == nil || got != nil {
				t.Fatalf("GetSelf result=%q error=%v, want JSON error", got, err)
			}
			if metadata == nil || metadata.StatusCode != http.StatusOK || metadata.Header.Get("X-Trace-ID") != tc.name {
				t.Errorf("GetSelf decode metadata = %#v", metadata)
			}
		})
	}
}

var errResponseReader = errors.New("injected response reader failure")

type failingResponseBody struct {
	read bool
}

func (b *failingResponseBody) Read(buffer []byte) (int, error) {
	if !b.read {
		b.read = true
		return copy(buffer, `{"partial":`), nil
	}
	return 0, errResponseReader
}

func (*failingResponseBody) Close() error { return nil }

func TestGetSelfReadErrorPreservesMetadata(t *testing.T) {
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		response := testResponse(request, http.StatusOK, "")
		response.Header.Set("X-Trace-ID", "read-error")
		response.Body = &failingResponseBody{}
		return response, nil
	})
	client := newTestClient(t, "https://api.example.test", transport)
	got, metadata, err := client.GetSelf(context.Background())
	if !errors.Is(err, errResponseReader) || got != nil {
		t.Fatalf("GetSelf result=%q error=%v", got, err)
	}
	if metadata == nil || metadata.StatusCode != http.StatusOK || metadata.Header.Get("X-Trace-ID") != "read-error" {
		t.Errorf("GetSelf read-error metadata = %#v", metadata)
	}
}

func TestOpenAPISpecReturnsFreshCopies(t *testing.T) {
	first := OpenAPISpec()
	second := OpenAPISpec()
	if len(first) == 0 || len(second) == 0 {
		t.Fatal("OpenAPISpec returned an empty source")
	}
	if &first[0] == &second[0] || &first[0] == &openAPISource[0] {
		t.Fatal("OpenAPISpec did not return independent storage")
	}
	original := second[0]
	first[0] ^= 0xff
	third := OpenAPISpec()
	if second[0] != original || third[0] != original || openAPISource[0] != original {
		t.Fatal("mutating one OpenAPISpec result changed another copy or the embedded source")
	}
}

func TestEffectiveOriginComparisonRejectsPortAndSchemeChanges(t *testing.T) {
	inner := &recordedTransport{}
	client := newTestClient(t, "https://example.test", inner)
	for _, target := range []string{"https://example.test:444/path", "http://example.test/path", "https://other.example.test/path"} {
		request, err := http.NewRequest(http.MethodGet, target, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := client.HTTP.Transport.RoundTrip(request); err == nil {
			t.Errorf("off-origin target %q was accepted", target)
		}
	}
	if inner.calls.Load() != 0 {
		t.Errorf("off-origin requests reached inner transport %d times", inner.calls.Load())
	}
}
