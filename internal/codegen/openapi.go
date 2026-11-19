package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"gopkg.in/yaml.v3"
)

type operation struct {
	ID     string
	Method string
	Path   string
}

var pendingOperations = []operation{
	{ID: "handle_get_self_v3_self_get", Method: "get", Path: "/v3/self"},
	{ID: "handle_upload_attachment_v3_organizations__org_id__attachments_post", Method: "post", Path: "/v3/organizations/{org_id}/attachments"},
	{ID: "upload_enterprise_blueprint_file_endpoint_v3_enterprise_snapshot_setup_blueprints__blueprint_id__files_post", Method: "post", Path: "/v3/enterprise/snapshot-setup/blueprints/{blueprint_id}/files"},
	{ID: "upload_org_blueprint_file_v3_organizations__org_id__snapshot_setup_blueprints__blueprint_id__files_post", Method: "post", Path: "/v3/organizations/{org_id}/snapshot-setup/blueprints/{blueprint_id}/files"},
}

var httpMethods = map[string]bool{
	"get": true, "post": true, "put": true, "patch": true, "delete": true,
	"head": true, "options": true, "trace": true, "connect": true,
}

func normalize(source []byte) ([]byte, []operation, int, error) {
	var document yaml.Node
	if err := yaml.Unmarshal(source, &document); err != nil {
		return nil, nil, 0, fmt.Errorf("parse OpenAPI source: %w", err)
	}
	if len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return nil, nil, 0, fmt.Errorf("OpenAPI source must have one mapping document")
	}
	root := document.Content[0]
	if got := scalar(mapValue(root, "openapi")); got != "3.1.0" {
		return nil, nil, 0, fmt.Errorf("expected OpenAPI 3.1.0, got %q", got)
	}
	paths := mapValue(root, "paths")
	if paths == nil || paths.Kind != yaml.MappingNode {
		return nil, nil, 0, fmt.Errorf("OpenAPI source has no paths mapping")
	}

	expected := make(map[string]operation, len(pendingOperations))
	for _, op := range pendingOperations {
		expected[op.ID] = op
	}
	seen := make(map[string]operation)
	filteredPaths := make([]*yaml.Node, 0, len(paths.Content))
	excluded := make([]operation, 0, len(pendingOperations))
	operationCount := 0

	for i := 0; i < len(paths.Content); i += 2 {
		pathNode, pathItem := paths.Content[i], paths.Content[i+1]
		path := pathNode.Value
		filteredItem := make([]*yaml.Node, 0, len(pathItem.Content))
		for j := 0; j < len(pathItem.Content); j += 2 {
			methodNode, operationNode := pathItem.Content[j], pathItem.Content[j+1]
			method := strings.ToLower(methodNode.Value)
			if !httpMethods[method] {
				filteredItem = append(filteredItem, methodNode, operationNode)
				continue
			}
			operationCount++
			id := scalar(mapValue(operationNode, "operationId"))
			if id == "" {
				return nil, nil, 0, fmt.Errorf("%s %s has no operationId", method, path)
			}
			if prior, ok := seen[id]; ok {
				return nil, nil, 0, fmt.Errorf("duplicate operationId %q at %s %s and %s %s", id, prior.Method, prior.Path, method, path)
			}
			seen[id] = operation{ID: id, Method: method, Path: path}

			if exception, ok := expected[id]; ok {
				if exception.Method != method || exception.Path != path {
					return nil, nil, 0, fmt.Errorf("pending operation %q moved: expected %s %s, got %s %s", id, exception.Method, exception.Path, method, path)
				}
				if err := verifyPendingOperation(exception, operationNode); err != nil {
					return nil, nil, 0, err
				}
				excluded = append(excluded, exception)
				continue
			}
			filteredItem = append(filteredItem, methodNode, operationNode)
		}
		if len(filteredItem) != 0 {
			pathItem.Content = filteredItem
			filteredPaths = append(filteredPaths, pathNode, pathItem)
		}
	}
	if operationCount != 284 {
		return nil, nil, 0, fmt.Errorf("expected 284 source operations, found %d", operationCount)
	}
	if len(seen) != 284 {
		return nil, nil, 0, fmt.Errorf("expected 284 unique operationIds, found %d", len(seen))
	}
	if len(excluded) != len(pendingOperations) {
		return nil, nil, 0, fmt.Errorf("expected %d exact pending operations, found %d", len(pendingOperations), len(excluded))
	}
	for id := range expected {
		if _, ok := seen[id]; !ok {
			return nil, nil, 0, fmt.Errorf("required pending operationId %q is missing", id)
		}
	}
	paths.Content = filteredPaths
	if got := countOperations(paths); got != 280 {
		return nil, nil, 0, fmt.Errorf("expected 280 generated operations after exclusions, found %d", got)
	}

	hoisted, err := normalizeComponentSchemas(root)
	if err != nil {
		return nil, nil, 0, err
	}
	var output bytes.Buffer
	encoder := yaml.NewEncoder(&output)
	encoder.SetIndent(2)
	if err := encoder.Encode(&document); err != nil {
		return nil, nil, 0, fmt.Errorf("encode normalized OpenAPI: %w", err)
	}
	if err := encoder.Close(); err != nil {
		return nil, nil, 0, fmt.Errorf("close normalized OpenAPI encoder: %w", err)
	}
	return output.Bytes(), excluded, hoisted, nil
}

func verifyPendingOperation(expected operation, node *yaml.Node) error {
	if expected.ID == "handle_get_self_v3_self_get" {
		responses := mapValue(node, "responses")
		response := mapValue(responses, "200")
		content := mapValue(response, "content")
		media := mapValue(content, "application/json")
		schema := mapValue(media, "schema")
		union := mapValue(schema, "anyOf")
		if union == nil {
			union = mapValue(schema, "oneOf")
		}
		if union == nil || union.Kind != yaml.SequenceNode || len(union.Content) != 4 {
			return fmt.Errorf("pending operation %q no longer has the expected four-way 200 response union", expected.ID)
		}
		for _, member := range union.Content {
			if scalar(mapValue(member, "$ref")) == "" {
				return fmt.Errorf("pending operation %q response union contains a non-reference branch", expected.ID)
			}
		}
		return nil
	}
	body := mapValue(node, "requestBody")
	content := mapValue(body, "content")
	if mapValue(content, "multipart/form-data") == nil {
		return fmt.Errorf("pending operation %q no longer declares multipart/form-data", expected.ID)
	}
	return nil
}

func normalizeComponentSchemas(root *yaml.Node) (int, error) {
	components := mapValue(root, "components")
	schemas := mapValue(components, "schemas")
	if schemas == nil || schemas.Kind != yaml.MappingNode {
		return 0, fmt.Errorf("OpenAPI source has no components.schemas mapping")
	}
	existing := make(map[string]bool, len(schemas.Content)/2)
	for i := 0; i < len(schemas.Content); i += 2 {
		existing[schemas.Content[i].Value] = true
	}
	additions := make(map[string]*yaml.Node)
	count := 0
	for i := 0; i < len(schemas.Content); i += 2 {
		name, schema := schemas.Content[i].Value, schemas.Content[i+1]
		if err := normalizeSchema(schema, name, nil, true, existing, additions, &count); err != nil {
			return 0, fmt.Errorf("normalize component %s: %w", name, err)
		}
	}
	paths := mapValue(root, "paths")
	for i := 0; i < len(paths.Content); i += 2 {
		pathItem := paths.Content[i+1]
		for j := 0; j < len(pathItem.Content); j += 2 {
			method := strings.ToLower(pathItem.Content[j].Value)
			if !httpMethods[method] {
				continue
			}
			operationNode := pathItem.Content[j+1]
			id := scalar(mapValue(operationNode, "operationId"))
			if err := normalizeOperationSchemas(operationNode, id, nil, existing, additions, &count); err != nil {
				return 0, fmt.Errorf("normalize operation %s: %w", id, err)
			}
		}
	}
	names := make([]string, 0, len(additions))
	for name := range additions {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		schemas.Content = append(schemas.Content, stringNode(name), additions[name])
	}
	return count, nil
}

func normalizeSchema(node *yaml.Node, parent string, path []string, root bool, existing map[string]bool, additions map[string]*yaml.Node, hoisted *int) error {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	if err := normalizeEnumValues(node, parent, path); err != nil {
		return err
	}
	if !root && mapValue(node, "$ref") == nil && mapValue(node, "properties") != nil {
		name := inlineName(parent, path)
		if err := addInlineSchema(name, node, existing, additions, hoisted); err != nil {
			return err
		}
		return nil
	}
	if _, valueIndex, ok := nullableComposedUnion(node); ok {
		members := mapValue(node, "anyOf")
		if members == nil {
			members = mapValue(node, "oneOf")
		}
		name := inlineName(parent, appendPath(path, "NullableValue"))
		copy := cloneNode(members.Content[valueIndex])
		if err := normalizeSchema(copy, name, nil, true, existing, additions, hoisted); err != nil {
			return err
		}
		if err := reserveSchema(name, existing, additions); err != nil {
			return err
		}
		additions[name] = copy
		ref := refNode(name)
		members.Content[valueIndex] = ref
		*hoisted = *hoisted + 1
	}

	for i := 0; i < len(node.Content); i += 2 {
		key, value := node.Content[i].Value, node.Content[i+1]
		switch key {
		case "properties":
			for j := 0; j < len(value.Content); j += 2 {
				if err := normalizeSchema(value.Content[j+1], parent, appendPath(path, value.Content[j].Value), false, existing, additions, hoisted); err != nil {
					return err
				}
			}
		case "items", "additionalProperties", "not", "if", "then", "else":
			if err := normalizeSchema(value, parent, appendPath(path, key), false, existing, additions, hoisted); err != nil {
				return err
			}
		case "anyOf", "oneOf", "allOf", "prefixItems":
			if value.Kind == yaml.SequenceNode {
				for j, member := range value.Content {
					if err := normalizeSchema(member, parent, appendPath(path, key, fmt.Sprint(j+1)), false, existing, additions, hoisted); err != nil {
						return err
					}
				}
			} else if value.Kind == yaml.MappingNode {
				if err := normalizeSchema(value, parent, appendPath(path, key), false, existing, additions, hoisted); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func normalizeOperationSchemas(node *yaml.Node, parent string, path []string, existing map[string]bool, additions map[string]*yaml.Node, hoisted *int) error {
	if node == nil {
		return nil
	}
	switch node.Kind {
	case yaml.MappingNode:
		for i := 0; i+1 < len(node.Content); i += 2 {
			key, value := node.Content[i].Value, node.Content[i+1]
			if key == "schema" {
				if err := normalizeSchema(value, parent, appendPath(path, key), false, existing, additions, hoisted); err != nil {
					return err
				}
				continue
			}
			if err := normalizeOperationSchemas(value, parent, appendPath(path, key), existing, additions, hoisted); err != nil {
				return err
			}
		}
	case yaml.SequenceNode:
		for i, child := range node.Content {
			if err := normalizeOperationSchemas(child, parent, appendPath(path, fmt.Sprint(i+1)), existing, additions, hoisted); err != nil {
				return err
			}
		}
	}
	return nil
}

func normalizeEnumValues(node *yaml.Node, parent string, path []string) error {
	enum := mapValue(node, "enum")
	if enum == nil || enum.Kind != yaml.SequenceNode {
		return nil
	}
	if mapValue(node, "x-devin-original-enum-values") != nil {
		return fmt.Errorf("source already contains x-devin-original-enum-values")
	}
	used := make(map[string]bool, len(enum.Content))
	for _, value := range enum.Content {
		used[value.Value] = true
	}
	metadata := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	for i, value := range enum.Content {
		if value.Kind != yaml.ScalarNode || value.Tag != "!!str" || !needsEnumIdentifier(value.Value) {
			continue
		}
		original := value.Value
		location := strings.Join(appendPath(append([]string{parent}, path...), fmt.Sprint(i)), "/")
		digest := sha256.Sum256([]byte(location + "\x00" + original))
		placeholder := "devin_enum_value_" + hex.EncodeToString(digest[:8])
		for used[placeholder] {
			placeholder += "_"
		}
		used[placeholder] = true
		enum.Content[i] = stringNode(placeholder)
		metadata.Content = append(metadata.Content, stringNode(placeholder), stringNode(original))
	}
	if len(metadata.Content) != 0 {
		node.Content = append(node.Content, stringNode("x-devin-original-enum-values"), metadata)
	}
	return nil
}

func needsEnumIdentifier(value string) bool {
	if strings.Trim(value, "_-") == "" {
		return true
	}
	for _, r := range value {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_' && r != '-' {
			return true
		}
	}
	return false
}

func addInlineSchema(name string, node *yaml.Node, existing map[string]bool, additions map[string]*yaml.Node, hoisted *int) error {
	copy := cloneNode(node)
	if err := normalizeSchema(copy, name, nil, true, existing, additions, hoisted); err != nil {
		return err
	}
	if err := reserveSchema(name, existing, additions); err != nil {
		return err
	}
	additions[name] = copy
	*node = *refNode(name)
	*hoisted = *hoisted + 1
	return nil
}

func reserveSchema(name string, existing map[string]bool, additions map[string]*yaml.Node) error {
	if existing[name] || additions[name] != nil {
		return fmt.Errorf("normalized inline schema name %q collides with an existing schema", name)
	}
	existing[name] = true
	return nil
}

func nullableComposedUnion(node *yaml.Node) (int, int, bool) {
	for _, key := range []string{"anyOf", "oneOf"} {
		members := mapValue(node, key)
		if members == nil || members.Kind != yaml.SequenceNode || len(members.Content) != 2 {
			continue
		}
		nullIndex, valueIndex := -1, -1
		for i, member := range members.Content {
			if scalar(mapValue(member, "type")) == "null" {
				nullIndex = i
			} else if mapValue(member, "oneOf") != nil || mapValue(member, "anyOf") != nil || mapValue(member, "allOf") != nil {
				valueIndex = i
			}
		}
		if nullIndex >= 0 && valueIndex >= 0 {
			return nullIndex, valueIndex, true
		}
	}
	return 0, 0, false
}

func inlineName(parent string, path []string) string {
	var b strings.Builder
	b.WriteString("DevinInline")
	b.WriteString(pascal(parent))
	for _, part := range path {
		b.WriteString(pascal(part))
	}
	return b.String()
}

func pascal(value string) string {
	words := strings.FieldsFunc(value, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	var b strings.Builder
	for _, word := range words {
		runes := []rune(word)
		if len(runes) == 0 {
			continue
		}
		if unicode.IsDigit(runes[0]) {
			b.WriteString("Index")
		}
		b.WriteRune(unicode.ToUpper(runes[0]))
		b.WriteString(string(runes[1:]))
	}
	return b.String()
}

func appendPath(path []string, segments ...string) []string {
	result := make([]string, 0, len(path)+len(segments))
	result = append(result, path...)
	return append(result, segments...)
}

func mapValue(node *yaml.Node, key string) *yaml.Node {
	if node == nil || node.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value == key {
			return node.Content[i+1]
		}
	}
	return nil
}

func scalar(node *yaml.Node) string {
	if node == nil || node.Kind != yaml.ScalarNode {
		return ""
	}
	return node.Value
}

func stringNode(value string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}
}

func refNode(name string) *yaml.Node {
	return &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: []*yaml.Node{
		stringNode("$ref"), stringNode("#/components/schemas/" + name),
	}}
}

func cloneNode(source *yaml.Node) *yaml.Node {
	if source == nil {
		return nil
	}
	copy := *source
	copy.Content = make([]*yaml.Node, len(source.Content))
	for i, child := range source.Content {
		copy.Content[i] = cloneNode(child)
	}
	return &copy
}

func countOperations(paths *yaml.Node) int {
	count := 0
	if paths == nil || paths.Kind != yaml.MappingNode {
		return count
	}
	for i := 1; i < len(paths.Content); i += 2 {
		item := paths.Content[i]
		for j := 0; j < len(item.Content); j += 2 {
			if httpMethods[strings.ToLower(item.Content[j].Value)] {
				count++
			}
		}
	}
	return count
}
