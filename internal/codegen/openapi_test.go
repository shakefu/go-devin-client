package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestNormalizeInventoryAndRepeatability(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "..", "v3-openapi.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	first, excluded, hoisted, err := normalize(source)
	if err != nil {
		t.Fatal(err)
	}
	second, _, _, err := normalize(source)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("normalization is not deterministic")
	}
	if len(excluded) != 4 {
		t.Fatalf("excluded %d operations, want 4", len(excluded))
	}
	if hoisted == 0 {
		t.Fatal("normalization did not hoist nested inline component schemas")
	}

	var document yaml.Node
	if err := yaml.Unmarshal(first, &document); err != nil {
		t.Fatal(err)
	}
	paths := mapValue(document.Content[0], "paths")
	if got := countOperations(paths); got != 280 {
		t.Fatalf("normalized operation count = %d, want 280", got)
	}
	for _, pending := range pendingOperations {
		for i := 0; i < len(paths.Content); i += 2 {
			pathItem := paths.Content[i+1]
			for j := 0; j < len(pathItem.Content); j += 2 {
				if scalar(mapValue(pathItem.Content[j+1], "operationId")) == pending.ID {
					t.Errorf("pending operation %q remains in generator input", pending.ID)
				}
			}
		}
	}

	schemas := mapValue(mapValue(document.Content[0], "components"), "schemas")
	for _, name := range []string{
		"DevinInlineAutomationUpdateRequestRunAsNullableValue",
		"DevinInlineOncallResponderUpdateRequestRunAsNullableValue",
	} {
		if mapValue(schemas, name) == nil {
			t.Errorf("nullable union branch %q was not hoisted", name)
		}
	}
	values := make(map[string]string)
	if err := collectOriginalEnumValues(document.Content[0], values); err != nil {
		t.Fatal(err)
	}
	foundWireValue := false
	for _, value := range values {
		if value == "token/s" {
			foundWireValue = true
		}
	}
	if !foundWireValue {
		t.Fatal("normalization did not record the original wire value for an unsafe enum identifier")
	}
}

func TestNormalizeRejectsChangedExceptionInventory(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "..", "v3-openapi.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	changed := bytes.Replace(source, []byte("handle_get_self_v3_self_get"), []byte("changed_self_operation_id"), 1)
	if bytes.Equal(source, changed) {
		t.Fatal("test fixture operationId was not found")
	}
	if _, _, _, err := normalize(changed); err == nil || !strings.Contains(err.Error(), "expected 4 exact pending operations") {
		t.Fatalf("normalize error = %v, want operation inventory precondition failure", err)
	}
}

func TestAdaptGeneratedClient(t *testing.T) {
	adapted, err := adaptGeneratedClient(generatedClientFixture())
	if err != nil {
		t.Fatal(err)
	}
	text := string(adapted)
	for _, expected := range []string{
		`"time"`,
		"Timeout: 60 * time.Second",
		"http.ErrUseLastResponse",
		`fmt.Sprintf("%s %s: %s", e.Method, e.URL, e.Status)`,
	} {
		if !strings.Contains(text, expected) {
			t.Errorf("adapted client missing %q", expected)
		}
	}
	if strings.Contains(text, "string(e.Body)") {
		t.Fatal("adapted APIError.Error still exposes the response body")
	}
}

func TestAdaptGeneratedFilesRestoresWireEnumValues(t *testing.T) {
	client, types, err := adaptGeneratedFiles(generatedClientFixture(), []byte(`package devin

type Unit string

const (
	UnitDevinEnumValueTest Unit = "devin_enum_value_test"
)
`), []byte(`openapi: 3.1.0
paths: {}
components:
  schemas:
    Unit:
      type: string
      enum: [devin_enum_value_test]
      x-devin-original-enum-values:
        devin_enum_value_test: "token/s"
`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(types), `UnitDevinEnumValueTest Unit = "token/s"`) {
		t.Fatalf("wire enum value was not restored: %s", types)
	}
	if !strings.Contains(string(client), "Timeout: 60 * time.Second") {
		t.Fatal("client adaptation did not run")
	}
}

func TestAdaptGeneratedQueriesUsesFormExplodeForCollections(t *testing.T) {
	client, err := adaptGeneratedClient(generatedClientFixture())
	if err != nil {
		t.Fatal(err)
	}
	client = append(client, []byte(`
func (c *Client) GetItems(ctx context.Context, tags []string, filter Filter) {
	query := url.Values{}
	query.Set("tags", formatPathValue(tags))
	query.Set("filter", formatPathValue(filter))
	_, _ = c.do(ctx, "GET", "/items", nil, query, nil, nil, "", "", nil)
}
`)...)
	spec := []byte(`openapi: 3.1.0
components:
  schemas:
    Filter:
      type: object
      properties:
        after: {type: string}
paths:
  /items:
    get:
      operationId: get_items
      parameters:
      - in: query
        name: tags
        schema: {type: array, items: {type: string}}
      - in: query
        name: filter
        schema: {$ref: '#/components/schemas/Filter'}
`)
	adapted, err := adaptGeneratedQueries(client, spec)
	if err != nil {
		t.Fatal(err)
	}
	text := string(adapted)
	for _, expected := range []string{
		`addFormExplodedQuery(query, "tags", tags)`,
		`addFormExplodedQuery(query, "filter", filter)`,
	} {
		if !strings.Contains(text, expected) {
			t.Errorf("adapted query method missing %q", expected)
		}
	}
}

func TestAdaptGeneratedQueriesRejectsUnsupportedCollectionStyle(t *testing.T) {
	spec := []byte(`openapi: 3.1.0
components: {schemas: {}}
paths:
  /items:
    get:
      operationId: get_items
      parameters:
      - in: query
        name: tags
        style: spaceDelimited
        explode: false
        schema: {type: array, items: {type: string}}
`)
	if _, err := collectionQueryParameters(spec); err == nil || !strings.Contains(err.Error(), "unsupported collection style") {
		t.Fatalf("collection query error = %v, want unsupported style error", err)
	}
}

func TestAdaptNullableMapValuesFromSpec(t *testing.T) {
	spec := []byte(`openapi: 3.1.0
components:
  schemas:
    UpdateRequest:
      type: object
      properties:
        metadata:
          anyOf:
          - type: object
            additionalProperties:
              anyOf:
              - type: string
              - type: 'null'
          - type: 'null'
    PlainRequest:
      type: object
      properties:
        metadata:
          type: object
          additionalProperties: {type: string}
`)
	source := []byte("package devin\n" + "type UpdateRequest struct { Metadata *map[string]string `json:\"metadata,omitempty\"` }\n" + "type PlainRequest struct { Metadata *map[string]string `json:\"metadata,omitempty\"` }\n")
	adapted, err := adaptNullableMapValues(source, spec)
	if err != nil {
		t.Fatal(err)
	}
	text := string(adapted)
	if !strings.Contains(text, "Metadata *map[string]*string `json:\"metadata,omitempty\"`") {
		t.Fatalf("nullable map value was not adapted: %s", text)
	}
	if !strings.Contains(text, "type PlainRequest struct {\n\tMetadata *map[string]string `json:\"metadata,omitempty\"`") {
		t.Fatalf("non-nullable map value was changed: %s", text)
	}
}

func generatedClientFixture() []byte {
	return []byte(`package devin

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

type Client struct {
	BaseURL string
	HTTP *http.Client
}
func NewClient(baseURL string) *Client {
	return &Client{BaseURL: strings.TrimRight(baseURL, "/")}
}
type APIResponse struct {
	StatusCode int
	Header     http.Header
}
type APIError struct {
	StatusCode int
	Status string
	Method string
	URL string
	Body []byte
	Header http.Header
}
func (e *APIError) Error() string {
	return fmt.Sprintf("%s %s: %s: %s", e.Method, e.URL, e.Status, string(e.Body))
}
`)
}

func TestAdaptGeneratedClientRejectsUnrecognizedResponseFields(t *testing.T) {
	generated := []byte(`type APIResponse struct { StatusCode int; Headers http.Header }`)
	if _, err := adaptGeneratedClient(generated); err == nil {
		t.Fatal("adapter accepted an unrecognized APIResponse shape")
	}
}
