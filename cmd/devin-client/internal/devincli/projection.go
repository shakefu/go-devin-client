package devincli

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

//go:generate go run ./cmd/genprojection -source ../../../../v3-openapi.yaml -output projection.json
//go:embed projection.json
var projectionFiles embed.FS

const operationCount = 284

var globalFlags = map[string]struct{}{
	"api-key":             {},
	"base-url":            {},
	"timeout":             {},
	"allow-insecure-http": {},
	"read-only":           {},
	"dry-run":             {},
	"help":                {},
	"h":                   {},
	"version":             {},
	"v":                   {},
}

type Projection struct {
	Version    int         `json:"version"`
	Operations []Operation `json:"operations"`
}

type Operation struct {
	ID                 string       `json:"id"`
	Method             string       `json:"method"`
	Path               string       `json:"path"`
	Summary            string       `json:"summary,omitempty"`
	Parameters         []Parameter  `json:"parameters,omitempty"`
	RequestBody        *RequestBody `json:"requestBody,omitempty"`
	ResponseMediaTypes []string     `json:"responseMediaTypes,omitempty"`
}

type Parameter struct {
	Name        string `json:"name"`
	In          string `json:"in"`
	Type        string `json:"type"`
	ItemsType   string `json:"itemsType,omitempty"`
	Required    bool   `json:"required,omitempty"`
	Style       string `json:"style,omitempty"`
	Explode     bool   `json:"explode,omitempty"`
	Description string `json:"description,omitempty"`
}

type RequestBody struct {
	Required   bool     `json:"required,omitempty"`
	MediaTypes []string `json:"mediaTypes"`
}

func embeddedProjection() (Projection, error) {
	data, err := projectionFiles.ReadFile("projection.json")
	if err != nil {
		return Projection{}, err
	}
	var p Projection
	if err := json.Unmarshal(data, &p); err != nil {
		return Projection{}, fmt.Errorf("decode CLI projection: %w", err)
	}
	if p.Version != 1 {
		return Projection{}, fmt.Errorf("unsupported CLI projection version %d", p.Version)
	}
	return p, nil
}

func GenerateProjection(source []byte) ([]byte, error) {
	var document map[string]any
	if err := yaml.Unmarshal(source, &document); err != nil {
		return nil, fmt.Errorf("parse Devin OpenAPI source: %w", err)
	}
	paths := asMap(document["paths"])
	if paths == nil {
		return nil, fmt.Errorf("Devin OpenAPI source has no paths")
	}
	schemas := asMap(asMap(document["components"])["schemas"])
	pathNames := sortedKeys(paths)
	var operations []Operation
	seenIDs := make(map[string]struct{})
	for _, path := range pathNames {
		pathItem := asMap(paths[path])
		methodNames := sortedKeys(pathItem)
		for _, method := range methodNames {
			if !httpMethods[strings.ToUpper(method)] {
				continue
			}
			raw := asMap(pathItem[method])
			id := asString(raw["operationId"])
			if id == "" {
				return nil, fmt.Errorf("operation %s %s has no operationId", strings.ToUpper(method), path)
			}
			rootCommand := strings.Split(id, "_")[0]
			if rootCommand == "ops" || rootCommand == "help" || rootCommand == "version" {
				return nil, fmt.Errorf("operationId %q collides with a reserved command", id)
			}
			if _, exists := seenIDs[id]; exists {
				return nil, fmt.Errorf("duplicate operationId %q", id)
			}
			seenIDs[id] = struct{}{}

			op := Operation{
				ID:      id,
				Method:  strings.ToUpper(method),
				Path:    path,
				Summary: asString(raw["summary"]),
			}
			params, err := operationParameters(pathItem, raw, schemas)
			if err != nil {
				return nil, fmt.Errorf("operation %s: %w", id, err)
			}
			op.Parameters = params
			if body, ok, err := requestBody(asMap(raw["requestBody"])); err != nil {
				return nil, fmt.Errorf("operation %s: %w", id, err)
			} else if ok {
				op.RequestBody = body
			}
			for _, param := range params {
				if _, collision := globalFlags[param.Name]; collision {
					return nil, fmt.Errorf("operation %s parameter %q collides with a global flag", id, param.Name)
				}
				if op.RequestBody != nil && ((param.Name == "data" && requestBodyKind(op) == bodyJSON) || (param.Name == "file" && requestBodyKind(op) == bodyMultipart)) {
					return nil, fmt.Errorf("operation %s parameter %q collides with a request body flag", id, param.Name)
				}
			}
			op.ResponseMediaTypes = responseMediaTypes(asMap(raw["responses"]))
			operations = append(operations, op)
		}
	}
	sort.Slice(operations, func(i, j int) bool { return operations[i].ID < operations[j].ID })
	p := Projection{Version: 1, Operations: operations}
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode CLI projection: %w", err)
	}
	return append(data, '\n'), nil
}

func operationParameters(pathItem, operation map[string]any, schemas map[string]any) ([]Parameter, error) {
	byKey := make(map[string]Parameter)
	add := func(value any) error {
		raw := asMap(value)
		name, in := asString(raw["name"]), asString(raw["in"])
		if name == "" || in == "" {
			return fmt.Errorf("parameter has no name or location")
		}
		schema := asMap(raw["schema"])
		parameter := Parameter{
			Name:        name,
			In:          in,
			Type:        schemaType(schema, schemas, make(map[string]bool)),
			Required:    asBool(raw["required"]),
			Description: asString(raw["description"]),
		}
		if parameter.Type == "array" {
			parameter.ItemsType = schemaType(asMap(schema["items"]), schemas, make(map[string]bool))
		}
		parameter.Style = asString(raw["style"])
		if parameter.Style == "" {
			if in == "query" {
				parameter.Style = "form"
			} else {
				parameter.Style = "simple"
			}
		}
		if explode, exists := raw["explode"]; exists {
			parameter.Explode = asBool(explode)
		} else {
			parameter.Explode = parameter.Style == "form"
		}
		byKey[in+"\x00"+name] = parameter
		return nil
	}
	for _, value := range asSlice(pathItem["parameters"]) {
		if err := add(value); err != nil {
			return nil, err
		}
	}
	for _, value := range asSlice(operation["parameters"]) {
		if err := add(value); err != nil {
			return nil, err
		}
	}
	keys := make([]string, 0, len(byKey))
	for key := range byKey {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	params := make([]Parameter, 0, len(keys))
	for _, key := range keys {
		params = append(params, byKey[key])
	}
	return params, nil
}

func requestBody(raw map[string]any) (*RequestBody, bool, error) {
	if raw == nil {
		return nil, false, nil
	}
	media := sortedKeys(asMap(raw["content"]))
	if len(media) == 0 {
		return nil, false, fmt.Errorf("request body has no media types")
	}
	for _, contentType := range media {
		if contentType != "application/json" && contentType != "multipart/form-data" {
			return nil, false, fmt.Errorf("unsupported request media type %q", contentType)
		}
	}
	return &RequestBody{Required: asBool(raw["required"]), MediaTypes: media}, true, nil
}

func responseMediaTypes(responses map[string]any) []string {
	var media []string
	for code, response := range responses {
		if len(code) == 3 && code[0] == '2' {
			media = append(media, sortedKeys(asMap(asMap(response)["content"]))...)
		}
	}
	if len(media) == 0 {
		media = append(media, sortedKeys(asMap(asMap(responses["default"])["content"]))...)
	}
	return uniqueSorted(media)
}

func schemaType(schema, schemas map[string]any, seen map[string]bool) string {
	if schema == nil {
		return "string"
	}
	if ref := asString(schema["$ref"]); ref != "" {
		if seen[ref] {
			return "object"
		}
		seen[ref] = true
		defer delete(seen, ref)
		name := strings.TrimPrefix(ref, "#/components/schemas/")
		name = strings.ReplaceAll(strings.ReplaceAll(name, "~1", "/"), "~0", "~")
		if resolved := asMap(schemas[name]); resolved != nil {
			return schemaType(resolved, schemas, seen)
		}
		return "object"
	}
	if value, ok := schema["type"]; ok {
		switch typed := value.(type) {
		case string:
			return typed
		case []any:
			found := ""
			for _, entry := range typed {
				name := asString(entry)
				if name == "" || name == "null" {
					continue
				}
				if found != "" && found != name {
					return "union"
				}
				found = name
			}
			if found != "" {
				return found
			}
		}
	}
	for _, keyword := range []string{"anyOf", "oneOf", "allOf"} {
		if variants := asSlice(schema[keyword]); len(variants) > 0 {
			found := ""
			for _, variant := range variants {
				name := schemaType(asMap(variant), schemas, seen)
				if name == "null" {
					continue
				}
				if found == "" {
					found = name
				} else if found != name {
					return "union"
				}
			}
			if found != "" {
				return found
			}
		}
	}
	if _, ok := schema["properties"]; ok {
		return "object"
	}
	if _, ok := schema["items"]; ok {
		return "array"
	}
	if _, ok := schema["enum"]; ok {
		return "string"
	}
	return "string"
}

func asMap(value any) map[string]any {
	if result, ok := value.(map[string]any); ok {
		return result
	}
	return nil
}

func asSlice(value any) []any {
	if result, ok := value.([]any); ok {
		return result
	}
	return nil
}

func asString(value any) string {
	result, _ := value.(string)
	return result
}

func asBool(value any) bool {
	result, _ := value.(bool)
	return result
}

func sortedKeys(values map[string]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func uniqueSorted(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	sort.Strings(values)
	out := values[:0]
	for _, value := range values {
		if len(out) == 0 || out[len(out)-1] != value {
			out = append(out, value)
		}
	}
	return out
}

var httpMethods = map[string]bool{
	"GET": true, "POST": true, "PUT": true, "PATCH": true, "DELETE": true,
	"OPTIONS": true, "HEAD": true, "TRACE": true,
}

func projectionTreeSource(p Projection) ([]byte, error) {
	paths := make(map[string]map[string]map[string]any)
	for _, operation := range p.Operations {
		if paths[operation.Path] == nil {
			paths[operation.Path] = make(map[string]map[string]any)
		}
		rawOperation := map[string]any{
			"operationId": operation.ID,
			"summary":     operation.Summary,
		}
		if len(operation.Parameters) > 0 {
			parameters := make([]any, 0, len(operation.Parameters))
			for _, parameter := range operation.Parameters {
				schema := map[string]any{"type": okapiParameterType(parameter.Type)}
				if parameter.Type == "array" && parameter.ItemsType != "" {
					schema["items"] = map[string]any{"type": okapiParameterType(parameter.ItemsType)}
				}
				parameters = append(parameters, map[string]any{
					"name": parameter.Name, "in": parameter.In, "description": parameter.Description,
					"required": parameter.Required, "style": parameter.Style, "explode": parameter.Explode, "schema": schema,
				})
			}
			rawOperation["parameters"] = parameters
		}
		if operation.RequestBody != nil {
			content := make(map[string]any, len(operation.RequestBody.MediaTypes))
			for _, mediaType := range operation.RequestBody.MediaTypes {
				content[mediaType] = map[string]any{"schema": map[string]any{"type": "object"}}
			}
			rawOperation["requestBody"] = map[string]any{"required": operation.RequestBody.Required, "content": content}
		}
		responseContent := make(map[string]any, len(operation.ResponseMediaTypes))
		for _, mediaType := range operation.ResponseMediaTypes {
			responseContent[mediaType] = map[string]any{"schema": map[string]any{"type": "object"}}
		}
		responses := map[string]any{"description": "CLI projection"}
		if len(responseContent) > 0 {
			responses["content"] = responseContent
		}
		rawOperation["responses"] = map[string]any{"200": responses}
		paths[operation.Path][strings.ToLower(operation.Method)] = rawOperation
	}
	doc := map[string]any{
		"openapi": "3.1.0",
		"info":    map[string]any{"title": "Devin v3 CLI operation index", "version": "1"},
		"paths":   paths,
	}
	var out bytes.Buffer
	if err := json.NewEncoder(&out).Encode(doc); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func okapiParameterType(parameterType string) string {
	switch parameterType {
	case "string", "integer", "number", "boolean", "array":
		return parameterType
	default:
		return "string"
	}
}
