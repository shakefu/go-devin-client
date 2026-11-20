// Package devin provides a typed client for Devin's v3 API. Use New for
// authenticated access; NewClient is the unauthenticated generated constructor.
package devin

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// DefaultBaseURL is Devin's public API origin.
const DefaultBaseURL = "https://api.devin.ai"

// Config configures an authenticated Devin API client.
type Config struct {
	APIKey            string
	BaseURL           string
	HTTPClient        *http.Client
	Timeout           time.Duration
	AllowInsecureHTTP bool
}

// New constructs an authenticated client without performing network I/O.
func New(config Config) (*Client, error) {
	if config.APIKey == "" {
		return nil, errors.New("devin: API key must not be empty")
	}
	for _, r := range config.APIKey {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return nil, errors.New("devin: API key must not contain whitespace or control characters")
		}
	}

	baseURL, origin, err := parseAPIBaseURL(config.BaseURL, config.AllowInsecureHTTP)
	if err != nil {
		return nil, err
	}
	if config.Timeout < 0 {
		return nil, errors.New("devin: timeout must not be negative")
	}

	httpClient := http.Client{}
	if config.HTTPClient != nil {
		httpClient = *config.HTTPClient
	}
	if config.Timeout == 0 {
		httpClient.Timeout = 60 * time.Second
	} else {
		httpClient.Timeout = config.Timeout
	}
	httpClient.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}
	transport := httpClient.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	httpClient.Transport = &bearerTransport{base: transport, origin: origin, token: config.APIKey}

	client := NewClient(baseURL)
	client.HTTP = &httpClient
	return client, nil
}

type apiOrigin struct {
	scheme   string
	hostname string
	port     int
}

func parseAPIBaseURL(raw string, allowInsecureHTTP bool) (string, apiOrigin, error) {
	if strings.TrimSpace(raw) == "" {
		raw = DefaultBaseURL
	}
	if strings.Contains(raw, "?") {
		return "", apiOrigin{}, errors.New("devin: base URL must not include a query")
	}
	if strings.Contains(raw, "#") {
		return "", apiOrigin{}, errors.New("devin: base URL must not include a fragment")
	}

	u, err := url.Parse(raw)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "port") {
			return "", apiOrigin{}, errors.New("devin: base URL has an invalid port")
		}
		return "", apiOrigin{}, errors.New("devin: base URL must be a valid absolute origin")
	}
	if u.Opaque != "" || !u.IsAbs() {
		return "", apiOrigin{}, errors.New("devin: base URL must be an absolute origin")
	}
	if u.User != nil {
		return "", apiOrigin{}, errors.New("devin: base URL must not include user information")
	}
	if u.Host == "" || u.Hostname() == "" {
		return "", apiOrigin{}, errors.New("devin: base URL must include a hostname")
	}
	if u.RawQuery != "" || u.ForceQuery {
		return "", apiOrigin{}, errors.New("devin: base URL must not include a query")
	}
	if u.Fragment != "" || u.RawFragment != "" {
		return "", apiOrigin{}, errors.New("devin: base URL must not include a fragment")
	}
	if (u.Path != "" && u.Path != "/") || u.RawPath != "" {
		return "", apiOrigin{}, errors.New("devin: base URL must not include a path")
	}

	scheme := strings.ToLower(u.Scheme)
	switch scheme {
	case "https":
	case "http":
		if !allowInsecureHTTP {
			return "", apiOrigin{}, errors.New("devin: HTTP base URL requires AllowInsecureHTTP")
		}
	default:
		return "", apiOrigin{}, errors.New("devin: base URL scheme must be HTTPS or explicitly allowed HTTP")
	}

	portText, hasPort := explicitURLPort(u.Host)
	port := 443
	if scheme == "http" {
		port = 80
	}
	if hasPort {
		if portText == "" {
			return "", apiOrigin{}, errors.New("devin: base URL has an invalid port")
		}
		for _, r := range portText {
			if r < '0' || r > '9' {
				return "", apiOrigin{}, errors.New("devin: base URL has an invalid port")
			}
		}
		parsedPort, err := strconv.Atoi(portText)
		if err != nil || parsedPort < 0 || parsedPort > 65535 {
			return "", apiOrigin{}, errors.New("devin: base URL has an invalid port")
		}
		port = parsedPort
	}

	u.Scheme = scheme
	return strings.TrimRight(u.String(), "/"), apiOrigin{
		scheme:   scheme,
		hostname: u.Hostname(),
		port:     port,
	}, nil
}

func explicitURLPort(host string) (string, bool) {
	if strings.HasPrefix(host, "[") {
		end := strings.LastIndexByte(host, ']')
		if end < 0 {
			return "", false
		}
		suffix := host[end+1:]
		if suffix == "" {
			return "", false
		}
		if strings.HasPrefix(suffix, ":") {
			return suffix[1:], true
		}
		return "", false
	}
	if strings.Count(host, ":") == 1 {
		index := strings.LastIndexByte(host, ':')
		return host[index+1:], true
	}
	return "", false
}

func requestOrigin(u *url.URL) (apiOrigin, bool) {
	if u == nil || u.User != nil || u.Hostname() == "" {
		return apiOrigin{}, false
	}
	scheme := strings.ToLower(u.Scheme)
	portText, hasPort := explicitURLPort(u.Host)
	port := 0
	switch scheme {
	case "https":
		port = 443
	case "http":
		port = 80
	default:
		return apiOrigin{}, false
	}
	if hasPort {
		if portText == "" {
			return apiOrigin{}, false
		}
		for _, r := range portText {
			if r < '0' || r > '9' {
				return apiOrigin{}, false
			}
		}
		parsedPort, err := strconv.Atoi(portText)
		if err != nil || parsedPort < 0 || parsedPort > 65535 {
			return apiOrigin{}, false
		}
		port = parsedPort
	}
	return apiOrigin{scheme: scheme, hostname: u.Hostname(), port: port}, true
}

func (origin apiOrigin) matches(other apiOrigin) bool {
	return origin.scheme == other.scheme && strings.EqualFold(origin.hostname, other.hostname) && origin.port == other.port
}

type bearerTransport struct {
	base   http.RoundTripper
	origin apiOrigin
	token  string
}

func (t *bearerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	origin, ok := requestOrigin(request.URL)
	if !ok || !t.origin.matches(origin) {
		if request.Body != nil {
			_ = request.Body.Close()
		}
		return nil, errors.New("devin: refusing request outside configured API origin")
	}

	clone := request.Clone(request.Context())
	clone.Header = request.Header.Clone()
	if clone.Header == nil {
		clone.Header = make(http.Header)
	}
	clone.Header.Set("Authorization", "Bearer "+t.token)
	return t.base.RoundTrip(clone)
}

// UploadEnterpriseBlueprintFileParams contains the enterprise blueprint upload inputs.
type UploadEnterpriseBlueprintFileParams struct {
	BlueprintID string
	Name        string
	FileName    string
	File        io.Reader
}

// UploadEnterpriseBlueprintFile uploads a file to an enterprise blueprint.
func (c *Client) UploadEnterpriseBlueprintFile(ctx context.Context, params UploadEnterpriseBlueprintFileParams) (*BlueprintFile, *APIResponse, error) {
	response, metadata, err := c.sendMultipart(ctx, http.MethodPost, "/v3/enterprise/snapshot-setup/blueprints/{blueprint_id}/files", map[string]string{"blueprint_id": params.BlueprintID}, url.Values{"name": []string{params.Name}}, params.FileName, params.File)
	if err != nil {
		return nil, metadata, err
	}
	return decodeJSONResponse[BlueprintFile](response, metadata)
}

// UploadAttachmentParams contains the organization attachment upload inputs.
type UploadAttachmentParams struct {
	OrgID    string
	FileName string
	File     io.Reader
}

// UploadAttachment uploads an attachment to an organization.
func (c *Client) UploadAttachment(ctx context.Context, params UploadAttachmentParams) (*AttachmentResponse, *APIResponse, error) {
	response, metadata, err := c.sendMultipart(ctx, http.MethodPost, "/v3/organizations/{org_id}/attachments", map[string]string{"org_id": params.OrgID}, nil, params.FileName, params.File)
	if err != nil {
		return nil, metadata, err
	}
	return decodeJSONResponse[AttachmentResponse](response, metadata)
}

// UploadOrgBlueprintFileParams contains the organization blueprint upload inputs.
type UploadOrgBlueprintFileParams struct {
	OrgID       string
	BlueprintID string
	Name        string
	FileName    string
	File        io.Reader
}

// UploadOrgBlueprintFile uploads a file to an organization blueprint.
func (c *Client) UploadOrgBlueprintFile(ctx context.Context, params UploadOrgBlueprintFileParams) (*BlueprintFile, *APIResponse, error) {
	response, metadata, err := c.sendMultipart(ctx, http.MethodPost, "/v3/organizations/{org_id}/snapshot-setup/blueprints/{blueprint_id}/files", map[string]string{"org_id": params.OrgID, "blueprint_id": params.BlueprintID}, url.Values{"name": []string{params.Name}}, params.FileName, params.File)
	if err != nil {
		return nil, metadata, err
	}
	return decodeJSONResponse[BlueprintFile](response, metadata)
}

func (c *Client) sendMultipart(ctx context.Context, method, path string, pathParams map[string]string, query url.Values, fileName string, file io.Reader) (*http.Response, *APIResponse, error) {
	if file == nil {
		return nil, nil, errors.New("devin: upload file reader must not be nil")
	}
	body, contentType, err := multipartStream(fileName, file)
	if err != nil {
		return nil, nil, err
	}
	return c.send(ctx, method, path, pathParams, query, nil, body, contentType, "application/json")
}

func multipartStream(fileName string, file io.Reader) (io.Reader, string, error) {
	disposition := mime.FormatMediaType("form-data", map[string]string{"name": "file", "filename": fileName})
	if disposition == "" {
		return nil, "", errors.New("devin: invalid multipart filename")
	}

	var buffer bytes.Buffer
	writer := multipart.NewWriter(&buffer)
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", disposition)
	header.Set("Content-Type", "application/octet-stream")
	if _, err := writer.CreatePart(header); err != nil {
		return nil, "", fmt.Errorf("devin: create multipart file part: %w", err)
	}
	prefixLength := buffer.Len()
	contentType := writer.FormDataContentType()
	if err := writer.Close(); err != nil {
		return nil, "", fmt.Errorf("devin: close multipart writer: %w", err)
	}
	encoded := buffer.Bytes()
	prefix := bytes.Clone(encoded[:prefixLength])
	footer := bytes.Clone(encoded[prefixLength:])
	return io.MultiReader(bytes.NewReader(prefix), file, bytes.NewReader(footer)), contentType, nil
}

func decodeJSONResponse[T any](response *http.Response, metadata *APIResponse) (*T, *APIResponse, error) {
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, metadata, fmt.Errorf("devin: read response: %w", err)
	}
	var result T
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, metadata, fmt.Errorf("devin: decode response: %w", err)
	}
	return &result, metadata, nil
}

// GetSelf returns the exact JSON response for Devin's self endpoint.
func (c *Client) GetSelf(ctx context.Context) (json.RawMessage, *APIResponse, error) {
	response, metadata, err := c.send(ctx, http.MethodGet, "/v3/self", nil, nil, nil, nil, "", "application/json")
	if err != nil {
		return nil, metadata, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, metadata, fmt.Errorf("devin: read response: %w", err)
	}
	if !json.Valid(body) {
		return nil, metadata, errors.New("devin: decode response: invalid JSON")
	}
	return json.RawMessage(body), metadata, nil
}

//go:embed v3-openapi.yaml
var openAPISource []byte

// OpenAPISpec returns a fresh copy of the vendored Devin v3 OpenAPI source.
func OpenAPISpec() []byte {
	return bytes.Clone(openAPISource)
}
