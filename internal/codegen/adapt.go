package main

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

func adaptGeneratedFiles(clientSource, typesSource, normalizedSpec []byte) ([]byte, []byte, error) {
	var document yaml.Node
	if err := yaml.Unmarshal(normalizedSpec, &document); err != nil {
		return nil, nil, fmt.Errorf("parse normalized OpenAPI for enum adaptations: %w", err)
	}
	values := make(map[string]string)
	if err := collectOriginalEnumValues(document.Content[0], values); err != nil {
		return nil, nil, err
	}
	clientText, typesText := string(clientSource), string(typesSource)
	placeholders := make([]string, 0, len(values))
	for placeholder := range values {
		placeholders = append(placeholders, placeholder)
	}
	sort.Strings(placeholders)
	for _, placeholder := range placeholders {
		literal := strconv.Quote(placeholder)
		count := strings.Count(clientText, literal) + strings.Count(typesText, literal)
		if count != 1 {
			return nil, nil, fmt.Errorf("expected exactly one generated enum literal for %q, found %d", placeholder, count)
		}
		replacement := strconv.Quote(values[placeholder])
		if strings.Contains(clientText, literal) {
			clientText = strings.Replace(clientText, literal, replacement, 1)
		} else {
			typesText = strings.Replace(typesText, literal, replacement, 1)
		}
	}
	client, err := adaptGeneratedClient([]byte(clientText))
	if err != nil {
		return nil, nil, err
	}
	return client, []byte(typesText), nil
}

func collectOriginalEnumValues(node *yaml.Node, values map[string]string) error {
	if node == nil {
		return nil
	}
	if node.Kind == yaml.MappingNode {
		if mapping := mapValue(node, "x-devin-original-enum-values"); mapping != nil {
			if mapping.Kind != yaml.MappingNode {
				return fmt.Errorf("x-devin-original-enum-values must be a mapping")
			}
			enum := mapValue(node, "enum")
			for i := 0; i+1 < len(mapping.Content); i += 2 {
				placeholder, original := scalar(mapping.Content[i]), scalar(mapping.Content[i+1])
				if placeholder == "" {
					return fmt.Errorf("normalized enum adaptation contains an empty placeholder")
				}
				found := false
				if enum != nil && enum.Kind == yaml.SequenceNode {
					for _, value := range enum.Content {
						if scalar(value) == placeholder {
							found = true
							break
						}
					}
				}
				if !found {
					return fmt.Errorf("normalized enum placeholder %q is absent from its enum", placeholder)
				}
				if prior, exists := values[placeholder]; exists && prior != original {
					return fmt.Errorf("normalized enum placeholder %q maps to multiple wire values", placeholder)
				}
				values[placeholder] = original
			}
		}
	}
	for _, child := range node.Content {
		if err := collectOriginalEnumValues(child, values); err != nil {
			return err
		}
	}
	return nil
}

func adaptGeneratedClient(source []byte) ([]byte, error) {
	text := string(source)
	if strings.Count(text, "type APIResponse struct {") != 1 {
		return nil, fmt.Errorf("expected one generated APIResponse struct")
	}
	responseStart := strings.Index(text, "type APIResponse struct {")
	responseEnd := strings.Index(text[responseStart:], "\n}")
	if responseEnd < 0 {
		return nil, fmt.Errorf("generated APIResponse struct is malformed")
	}
	response := text[responseStart : responseStart+responseEnd]
	if !hasStructField(response, "StatusCode", "int") || !hasStructField(response, "Header", "http.Header") || hasStructField(response, "Headers", "http.Header") {
		return nil, fmt.Errorf("generated APIResponse fields do not match StatusCode and Header contract")
	}
	if strings.Count(text, "type Client struct {") != 1 {
		return nil, fmt.Errorf("expected one generated Client struct")
	}
	clientStart := strings.Index(text, "type Client struct {")
	clientEnd := strings.Index(text[clientStart:], "\n}")
	if clientEnd < 0 || !hasStructField(text[clientStart:clientStart+clientEnd], "HTTP", "*http.Client") {
		return nil, fmt.Errorf("generated Client does not expose the expected HTTP client field")
	}
	if strings.Count(text, "type APIError struct {") != 1 {
		return nil, fmt.Errorf("expected one generated APIError struct")
	}
	errorStructStart := strings.Index(text, "type APIError struct {")
	errorStructEnd := strings.Index(text[errorStructStart:], "\n}")
	if errorStructEnd < 0 {
		return nil, fmt.Errorf("generated APIError struct is malformed")
	}
	errorStruct := text[errorStructStart : errorStructStart+errorStructEnd]
	if !hasStructField(errorStruct, "StatusCode", "int") || !hasStructField(errorStruct, "Status", "string") || !hasStructField(errorStruct, "Method", "string") || !hasStructField(errorStruct, "URL", "string") || !hasStructField(errorStruct, "Body", "[]byte") || !hasStructField(errorStruct, "Header", "http.Header") {
		return nil, fmt.Errorf("generated APIError does not preserve status, headers, and raw Body")
	}

	constructor := "\treturn &Client{BaseURL: strings.TrimRight(baseURL, \"/\")}"
	if strings.Count(text, constructor) != 1 {
		return nil, fmt.Errorf("expected exactly one native NewClient return expression")
	}
	text = strings.Replace(text, constructor, "\treturn &Client{BaseURL: strings.TrimRight(baseURL, \"/\"), HTTP: &http.Client{Timeout: 60 * time.Second, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}}", 1)
	if strings.Count(text, "func NewClient(baseURL string) *Client {") != 1 {
		return nil, fmt.Errorf("generated NewClient signature changed")
	}

	importStart := strings.Index(text, "import (\n")
	if importStart < 0 || strings.Count(text, "import (\n") != 1 {
		return nil, fmt.Errorf("generated import block does not match adaptation preconditions")
	}
	importEndOffset := strings.Index(text[importStart:], "\n)")
	if importEndOffset < 0 {
		return nil, fmt.Errorf("generated import block is malformed")
	}
	importEnd := importStart + importEndOffset
	if strings.Contains(text[importStart:importEnd], "\"time\"") {
		return nil, fmt.Errorf("generated import block unexpectedly already imports time")
	}
	if !strings.Contains(text[importStart:importEnd], "\"net/http\"") {
		return nil, fmt.Errorf("generated import block does not import net/http")
	}
	text = text[:importEnd] + "\n\t\"time\"" + text[importEnd:]

	errorSignature := "func (e *APIError) Error() string {"
	if strings.Count(text, errorSignature) != 1 {
		return nil, fmt.Errorf("expected exactly one generated APIError.Error method")
	}
	methodStart := strings.Index(text, errorSignature)
	errorClose := strings.Index(text[methodStart:], "\n}")
	if errorClose < 0 {
		return nil, fmt.Errorf("generated APIError.Error method is malformed")
	}
	errorEnd := methodStart + errorClose + len("\n}")
	text = text[:methodStart] + "func (e *APIError) Error() string {\n\treturn fmt.Sprintf(\"%s %s: %s\", e.Method, e.URL, e.Status)\n}" + text[errorEnd:]
	if strings.Contains(text[methodStart:], "string(e.Body)") {
		return nil, fmt.Errorf("APIError.Error still includes response body")
	}
	return []byte(text), nil
}

func hasStructField(structSource, name, fieldType string) bool {
	for _, line := range strings.Split(structSource, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == name && fields[1] == fieldType {
			return true
		}
	}
	return false
}
