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
	}, suppressUsageErrors)
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

func TestGeneratedFlagsAlignWithProjection(t *testing.T) {
	projection, err := embeddedProjection()
	if err != nil {
		t.Fatal(err)
	}
	commands, err := operationCommands(projection, func(Operation) cli.ActionFunc { return func(*cli.Context) error { return nil } }, suppressUsageErrors)
	if err != nil {
		t.Fatal(err)
	}
	byID := make(map[string]*cli.Command, len(commands))
	for _, command := range commands {
		byID[command.Name] = command
	}
	for _, operation := range projection.Operations {
		command := byID[operation.ID]
		if command == nil {
			t.Fatalf("missing command %s", operation.ID)
		}
		names := make(map[string]bool, len(command.Flags))
		for i, flag := range command.Flags {
			name := flag.Names()[0]
			names[name] = true
			switch typed := flag.(type) {
			case *cli.StringFlag:
				if typed.Required {
					t.Errorf("%s --%s remains parser-required", operation.ID, name)
				}
			case *cli.IntFlag:
				if typed.Required {
					t.Errorf("%s --%s remains parser-required", operation.ID, name)
				}
			case *cli.Int64Flag:
				if typed.Required {
					t.Errorf("%s --%s remains parser-required", operation.ID, name)
				}
			case *cli.Float64Flag:
				if typed.Required {
					t.Errorf("%s --%s remains parser-required", operation.ID, name)
				}
			case *cli.BoolFlag:
				if typed.Required {
					t.Errorf("%s --%s remains parser-required", operation.ID, name)
				}
			case *cli.StringSliceFlag:
				if typed.Required {
					t.Errorf("%s --%s remains parser-required", operation.ID, name)
				}
			}
			if i > 0 && command.Flags[i-1].Names()[0] > name {
				t.Errorf("%s flags are unsorted", operation.ID)
			}
		}
		for _, parameter := range operation.Parameters {
			if !names[parameter.Name] {
				t.Errorf("%s missing generated --%s", operation.ID, parameter.Name)
			}
		}
		switch requestBodyKind(operation) {
		case bodyJSON:
			if !names["data"] {
				t.Errorf("%s missing generated --data", operation.ID)
			}
		case bodyMultipart:
			if !names["file"] {
				t.Errorf("%s missing --file", operation.ID)
			}
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
