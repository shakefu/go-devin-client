package devincli

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"

	devin "github.com/shakefu/go-devin-client"
)

type sdkDispatcher struct {
	client  *devin.Client
	capture *responseCapture
}

type responseCapture struct {
	mu     sync.Mutex
	body   []byte
	header http.Header
	err    error
}

type captureTransport struct {
	base    http.RoundTripper
	capture *responseCapture
}

func newSDKDispatcher(configuration settings, injected *http.Client) (*sdkDispatcher, error) {
	capture := &responseCapture{}
	client := http.Client{}
	if injected != nil {
		client = *injected
	}
	base := client.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	client.Transport = &captureTransport{base: base, capture: capture}
	apiClient, err := devin.New(devin.Config{
		APIKey: configuration.APIKey, BaseURL: configuration.BaseURL, Timeout: configuration.Timeout,
		HTTPClient: &client, AllowInsecureHTTP: configuration.AllowInsecureHTTP,
	})
	if err != nil {
		return nil, err
	}
	return &sdkDispatcher{client: apiClient, capture: capture}, nil
}

func (t *captureTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := t.base.RoundTrip(request)
	if err != nil || response == nil || response.Body == nil {
		return response, err
	}
	body, readErr := io.ReadAll(response.Body)
	_ = response.Body.Close()
	response.Body = io.NopCloser(bytes.NewReader(body))
	t.capture.mu.Lock()
	t.capture.body = body
	t.capture.header = response.Header.Clone()
	t.capture.err = readErr
	t.capture.mu.Unlock()
	return response, nil
}

func (d *sdkDispatcher) Do(ctx context.Context, operation Operation, request *http.Request, input requestBodyInput, values map[string]any) (*http.Response, error) {
	switch operation.ID {
	case "handle_get_self_v3_self_get":
		_, metadata, err := d.client.GetSelf(ctx)
		return d.specialResponse(request, metadata, err)
	case "handle_upload_attachment_v3_organizations__org_id__attachments_post":
		return d.upload(ctx, operation.ID, request, input, values)
	case "upload_enterprise_blueprint_file_endpoint_v3_enterprise_snapshot_setup_blueprints__blueprint_id__files_post":
		return d.upload(ctx, operation.ID, request, input, values)
	case "upload_org_blueprint_file_v3_organizations__org_id__snapshot_setup_blueprints__blueprint_id__files_post":
		return d.upload(ctx, operation.ID, request, input, values)
	default:
		response, err := d.client.HTTP.Do(request)
		if err == nil {
			if captureErr := d.capture.error(); captureErr != nil {
				return response, captureErr
			}
		}
		return response, err
	}
}

func (d *sdkDispatcher) upload(ctx context.Context, operationID string, request *http.Request, input requestBodyInput, values map[string]any) (*http.Response, error) {
	file, err := os.Open(input.filePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	fileName := filepath.Base(input.filePath)
	var metadata *devin.APIResponse
	switch operationID {
	case "handle_upload_attachment_v3_organizations__org_id__attachments_post":
		_, metadata, err = d.client.UploadAttachment(ctx, devin.UploadAttachmentParams{OrgID: values["org_id"].(string), FileName: fileName, File: file})
	case "upload_enterprise_blueprint_file_endpoint_v3_enterprise_snapshot_setup_blueprints__blueprint_id__files_post":
		_, metadata, err = d.client.UploadEnterpriseBlueprintFile(ctx, devin.UploadEnterpriseBlueprintFileParams{BlueprintID: values["blueprint_id"].(string), Name: values["name"].(string), FileName: fileName, File: file})
	case "upload_org_blueprint_file_v3_organizations__org_id__snapshot_setup_blueprints__blueprint_id__files_post":
		_, metadata, err = d.client.UploadOrgBlueprintFile(ctx, devin.UploadOrgBlueprintFileParams{OrgID: values["org_id"].(string), BlueprintID: values["blueprint_id"].(string), Name: values["name"].(string), FileName: fileName, File: file})
	}
	return d.specialResponse(request, metadata, err)
}

func (d *sdkDispatcher) specialResponse(request *http.Request, metadata *devin.APIResponse, callErr error) (*http.Response, error) {
	if metadata == nil {
		return nil, callErr
	}
	body, header, captureErr := d.capture.snapshot()
	response := &http.Response{StatusCode: metadata.StatusCode, Status: http.StatusText(metadata.StatusCode), Header: header, Body: io.NopCloser(bytes.NewReader(body)), Request: request}
	if metadata.StatusCode < 200 || metadata.StatusCode >= 300 {
		return response, nil
	}
	if callErr != nil {
		return response, callErr
	}
	if captureErr != nil {
		return response, captureErr
	}
	return response, nil
}

func (c *responseCapture) snapshot() ([]byte, http.Header, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return bytes.Clone(c.body), c.header.Clone(), c.err
}

func (c *responseCapture) error() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}
