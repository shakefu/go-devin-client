package devin

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestGeneratedClientSendsRequestAndPreservesResponseMetadata(t *testing.T) {
	var gotMethod, gotPath, gotAuthorization string
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.EscapedPath()
		gotAuthorization = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		w.Header().Set("X-Trace-ID", "trace-123")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"name":"created"}`)
	}))
	defer server.Close()

	client := NewClient(server.URL + "/")
	got, response, err := client.HandleCreateNoteV3OrganizationsOrgIdKnowledgeNotesPost(context.Background(), "org/team", KnowledgeNoteCreateRequest{
		Body:    "note body",
		Name:    "test note",
		Trigger: "manual",
	})
	if err != nil {
		t.Fatalf("create note: %v", err)
	}
	if gotMethod != http.MethodPost || gotPath != "/v3/organizations/org%2Fteam/knowledge/notes" {
		t.Fatalf("request = %s %s, want POST /v3/organizations/org%%2Fteam/knowledge/notes", gotMethod, gotPath)
	}
	if gotAuthorization != "" {
		t.Fatalf("generated low-level client unexpectedly set Authorization: %q", gotAuthorization)
	}
	if gotBody["name"] != "test note" || gotBody["body"] != "note body" || gotBody["trigger"] != "manual" {
		t.Fatalf("request body = %#v", gotBody)
	}
	if got == nil || got.Name != "created" {
		t.Fatalf("decoded response = %#v, want name created", got)
	}
	if response == nil || response.StatusCode != http.StatusCreated || response.Header.Get("X-Trace-ID") != "trace-123" {
		t.Fatalf("response metadata = %#v, want 201 and X-Trace-ID", response)
	}
}

func TestGeneratedArrayQueryParameterUsesExplodeSerialization(t *testing.T) {
	var gotQuery map[string][]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		_, _ = io.WriteString(w, `{}`)
	}))
	defer server.Close()

	severity := []HandleListCodeScanFindingsV3organizationsOrgIdCodeScansFindingsGetSeverityItem{"critical", "high"}
	_, _, err := NewClient(server.URL).HandleListCodeScanFindingsV3OrganizationsOrgIdCodeScansFindingsGet(context.Background(), "org-1", nil, nil, nil, nil, nil, nil, nil, nil, &severity, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := gotQuery["severity"]; len(got) != 2 || got[0] != "critical" || got[1] != "high" {
		t.Fatalf("severity query values = %#v, want repeated critical and high values", got)
	}
}

func TestGeneratedObjectQueryParameterUsesFormExplodeSerialization(t *testing.T) {
	var gotQuery map[string][]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query()
		_, _ = io.WriteString(w, `{}`)
	}))
	defer server.Close()

	after := "cursor-123"
	first := int64(25)
	qs := SessionsQueryParams{After: &after, First: &first}
	_, _, err := NewClient(server.URL).HandleGetSessionsV3OrganizationsOrgIdSessionsGet(context.Background(), "org-1", nil, qs)
	if err != nil {
		t.Fatal(err)
	}
	if got := gotQuery["after"]; len(got) != 1 || got[0] != after {
		t.Errorf("after query values = %#v, want [%q]", got, after)
	}
	if got := gotQuery["first"]; len(got) != 1 || got[0] != "25" {
		t.Errorf("first query values = %#v, want [25]", got)
	}
	if _, exists := gotQuery["qs"]; exists {
		t.Errorf("object parameter was serialized under its container name: %#v", gotQuery["qs"])
	}
}

func TestGeneratedUnionRoundTripsArbitraryJSONData(t *testing.T) {
	const payload = `{"field":"priority","operator":"eq","value":{"nested":[true,2,"kept"],"future":"preserved"},"unknown_top_level":"ignored"}`
	var condition AutomationComparisonCondition
	if err := json.Unmarshal([]byte(payload), &condition); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(condition)
	if err != nil {
		t.Fatal(err)
	}
	var got, want map[string]any
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(payload), &want); err != nil {
		t.Fatal(err)
	}
	delete(want, "unknown_top_level")
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("union JSON after round trip = %#v, want %#v", got, want)
	}
}

func TestGeneratedNullableUnionCannotDistinguishMissingFromNull(t *testing.T) {
	var missing, explicitNull AutomationUpdateRequest
	if err := json.Unmarshal([]byte(`{}`), &missing); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(`{"run_as":null}`), &explicitNull); err != nil {
		t.Fatal(err)
	}
	if missing.RunAs != nil || explicitNull.RunAs != nil {
		t.Fatalf("RunAs values = missing:%#v null:%#v; generated pointers should document the shared nil representation", missing.RunAs, explicitNull.RunAs)
	}
	value := DevinInlineAutomationUpdateRequestRunAsNullableValue("service_user")
	request := AutomationUpdateRequest{RunAs: &value}
	encoded, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"run_as":"service_user"`) {
		t.Fatalf("non-null union value not encoded: %s", encoded)
	}
}

func TestGeneratedClientPreservesErrorBodyWithoutLeakingIt(t *testing.T) {
	const body = `{"type":"about:blank","detail":"private diagnostic"}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.Header().Set("Retry-After", "7")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, body)
	}))
	defer server.Close()

	client := NewClient(server.URL)
	_, response, err := client.GetOrganizationDefaultTagV3EnterpriseOrganizationsOrgIdTagsDefaultGet(context.Background(), "org-1")
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error = %v, want *APIError", err)
	}
	if string(apiErr.Body) != body {
		t.Fatalf("APIError.Body = %q, want exact response bytes %q", apiErr.Body, body)
	}
	if strings.Contains(apiErr.Error(), "private diagnostic") {
		t.Fatalf("APIError.Error leaked response body: %q", apiErr.Error())
	}
	if apiErr.StatusCode != http.StatusTooManyRequests || apiErr.Header.Get("Retry-After") != "7" {
		t.Fatalf("APIError metadata = status %d headers %#v", apiErr.StatusCode, apiErr.Header)
	}
	if response == nil || response.StatusCode != http.StatusTooManyRequests || response.Header.Get("Retry-After") != "7" {
		t.Fatalf("APIResponse metadata = %#v", response)
	}
}

func TestGeneratedClientHandlesEmptySuccessAndDisablesRedirects(t *testing.T) {
	t.Run("empty success", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodDelete || r.URL.Path != "/v3/organizations/org-1/code-scans/scan-1/auto-scan" {
				t.Errorf("request = %s %s", r.Method, r.URL.Path)
			}
			w.WriteHeader(http.StatusNoContent)
		}))
		defer server.Close()

		response, err := NewClient(server.URL).HandleDeleteAutoScanScheduleV3OrganizationsOrgIdCodeScansScanIdAutoScanDelete(context.Background(), "org-1", "scan-1")
		if err != nil {
			t.Fatalf("delete auto-scan schedule: %v", err)
		}
		if response == nil || response.StatusCode != http.StatusNoContent {
			t.Fatalf("response = %#v, want non-nil 204 metadata", response)
		}
	})

	t.Run("redirect not followed", func(t *testing.T) {
		var targetCalls atomic.Int32
		target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			targetCalls.Add(1)
			w.WriteHeader(http.StatusOK)
		}))
		defer target.Close()
		redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, target.URL, http.StatusFound)
		}))
		defer redirect.Close()

		client := NewClient(redirect.URL)
		if client.HTTP == nil || client.HTTP.Timeout != 60*time.Second || client.HTTP.CheckRedirect == nil {
			t.Fatalf("NewClient transport defaults = %#v", client.HTTP)
		}
		_, response, err := client.GetOrganizationDefaultTagV3EnterpriseOrganizationsOrgIdTagsDefaultGet(context.Background(), "org-1")
		var apiErr *APIError
		if !errors.As(err, &apiErr) {
			t.Fatalf("error = %v, want *APIError for redirect response", err)
		}
		if targetCalls.Load() != 0 {
			t.Fatalf("redirect target received %d requests", targetCalls.Load())
		}
		if response == nil || response.StatusCode != http.StatusFound || apiErr.StatusCode != http.StatusFound {
			t.Fatalf("redirect response metadata = %#v, APIError status %d", response, apiErr.StatusCode)
		}
	})
}
