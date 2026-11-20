package devincli

import (
	"bytes"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/urfave/cli/v2"
)

func TestProjectionFreshnessAndOperationCoverage(t *testing.T) {
	source, err := os.ReadFile("../../../../v3-openapi.yaml")
	if err != nil {
		t.Fatal(err)
	}
	generated, err := GenerateProjection(source)
	if err != nil {
		t.Fatal(err)
	}
	embedded, err := projectionFiles.ReadFile("projection.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(generated, embedded) {
		t.Fatal("embedded CLI projection is stale; run go generate ./cmd/devin-client/internal/devincli")
	}
	projection, err := embeddedProjection()
	if err != nil {
		t.Fatal(err)
	}
	if len(projection.Operations) != operationCount {
		t.Fatalf("projection has %d operations; want %d", len(projection.Operations), operationCount)
	}
	seenEmptyTreeSegment := false
	for _, operation := range projection.Operations {
		if strings.Contains(operation.ID, "__") {
			seenEmptyTreeSegment = true
			break
		}
	}
	if !seenEmptyTreeSegment {
		t.Fatal("projection does not cover operationIds with empty underscore tree segments")
	}

	commands, err := operationCommands(projection, func(Operation) cli.ActionFunc {
		return func(*cli.Context) error { return nil }
	})
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, len(commands))
	for index, command := range commands {
		got[index] = command.Name
	}
	want := make([]string, len(projection.Operations))
	for index, operation := range projection.Operations {
		want[index] = operation.ID
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatal("generated CLI command names do not match projection operationIds")
	}
}

func TestGenerateProjectionRejectsReservedCollisions(t *testing.T) {
	tests := []struct {
		name   string
		source string
		want   string
	}{
		{
			name: "reserved command tree",
			source: `openapi: 3.1.0
info: {title: test, version: '1'}
paths:
  /ops/child:
    get:
      operationId: ops_child
      responses: {'200': {description: ok}}
`,
			want: "reserved command",
		},
		{
			name: "global flag",
			source: `openapi: 3.1.0
info: {title: test, version: '1'}
paths:
  /items:
    get:
      operationId: list_items
      parameters:
        - name: api-key
          in: query
          schema: {type: string}
      responses: {'200': {description: ok}}
`,
			want: "global flag",
		},
		{
			name: "body flag",
			source: `openapi: 3.1.0
info: {title: test, version: '1'}
paths:
  /items:
    post:
      operationId: create_item
      parameters:
        - name: data
          in: query
          schema: {type: string}
      requestBody:
        required: true
        content:
          application/json: {schema: {type: object}}
      responses: {'200': {description: ok}}
`,
			want: "request body flag",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := GenerateProjection([]byte(test.source))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("GenerateProjection() error = %v; want %q", err, test.want)
			}
		})
	}
}

func TestOperationFlagsAreSorted(t *testing.T) {
	flags := operationFlags(Operation{
		Parameters: []Parameter{
			{Name: "zeta", Type: "string"},
			{Name: "alpha", Type: "integer"},
		},
		RequestBody: &RequestBody{Required: true, MediaTypes: []string{"application/json"}},
	})
	for index := 1; index < len(flags); index++ {
		if flags[index-1].Names()[0] > flags[index].Names()[0] {
			t.Fatalf("flags are not sorted: %s precedes %s", flags[index-1].Names()[0], flags[index].Names()[0])
		}
	}
}

func TestSchemaTypePreservesNullableAndUnionTypes(t *testing.T) {
	tests := []struct {
		name   string
		schema map[string]any
		want   string
	}{
		{name: "nullable", schema: map[string]any{"type": []any{"null", "string"}}, want: "string"},
		{name: "union", schema: map[string]any{"type": []any{"string", "integer", "null"}}, want: "union"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := schemaType(test.schema, nil, make(map[string]bool)); got != test.want {
				t.Fatalf("schemaType() = %q; want %q", got, test.want)
			}
		})
	}
}
