package main

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
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
	client, err = adaptGeneratedQueries(client, normalizedSpec)
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
	if strings.Contains(text[importStart:importEnd], "\"reflect\"") {
		return nil, fmt.Errorf("generated import block unexpectedly already imports reflect")
	}
	if !strings.Contains(text[importStart:importEnd], "\"net/http\"") {
		return nil, fmt.Errorf("generated import block does not import net/http")
	}
	if !strings.Contains(text[importStart:importEnd], "\"net/url\"") {
		return nil, fmt.Errorf("generated import block does not import net/url")
	}
	text = text[:importEnd] + "\n\t\"reflect\"\n\t\"time\"" + text[importEnd:]

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
	if strings.Contains(text, "func addFormExplodedQuery(") {
		return nil, fmt.Errorf("generated client unexpectedly already defines addFormExplodedQuery")
	}
	text += formExplodedQueryHelper
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

type collectionQueryParameter struct {
	Name string
}

func adaptGeneratedQueries(source, normalizedSpec []byte) ([]byte, error) {
	parameters, err := collectionQueryParameters(normalizedSpec)
	if err != nil {
		return nil, err
	}
	if len(parameters) == 0 {
		return source, nil
	}
	if strings.Count(string(source), "func addFormExplodedQuery(") != 1 {
		return nil, fmt.Errorf("expected one generated addFormExplodedQuery helper")
	}

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "client.gen.go", source, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("parse generated client query methods: %w", err)
	}
	adapted := make(map[string]bool, len(parameters))
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Body == nil {
			continue
		}
		method, path, hasRequest, err := generatedOperationIdentity(function.Body)
		if err != nil {
			return nil, fmt.Errorf("inspect generated method %s: %w", function.Name.Name, err)
		}
		if !hasRequest {
			continue
		}
		key := method + " " + path
		wanted, exists := parameters[key]
		if !exists {
			continue
		}
		seen := make(map[string]int, len(wanted))
		var adaptationErr error
		ast.Inspect(function.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok || adaptationErr != nil {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || selector.Sel.Name != "Set" {
				return true
			}
			queryName, ok := selector.X.(*ast.Ident)
			if !ok || queryName.Name != "query" || len(call.Args) != 2 {
				return true
			}
			nameLiteral, ok := call.Args[0].(*ast.BasicLit)
			if !ok || nameLiteral.Kind != token.STRING {
				return true
			}
			name, err := strconv.Unquote(nameLiteral.Value)
			if err != nil {
				adaptationErr = fmt.Errorf("decode generated query parameter name: %w", err)
				return true
			}
			if _, ok := wanted[name]; !ok {
				return true
			}
			valueCall, ok := call.Args[1].(*ast.CallExpr)
			if !ok || identifier(valueCall.Fun) != "formatPathValue" || len(valueCall.Args) != 1 {
				adaptationErr = fmt.Errorf("query parameter %q does not use the expected formatPathValue call", name)
				return true
			}
			call.Fun = ast.NewIdent("addFormExplodedQuery")
			call.Args = []ast.Expr{
				ast.NewIdent("query"),
				&ast.BasicLit{Kind: token.STRING, Value: strconv.Quote(name)},
				valueCall.Args[0],
			}
			seen[name]++
			return true
		})
		if adaptationErr != nil {
			return nil, fmt.Errorf("adapt generated query method %s: %w", function.Name.Name, adaptationErr)
		}
		for name := range wanted {
			if seen[name] != 1 {
				return nil, fmt.Errorf("expected one generated setter for query parameter %q in %s, found %d", name, function.Name.Name, seen[name])
			}
		}
		adapted[key] = true
	}
	for key := range parameters {
		if !adapted[key] {
			return nil, fmt.Errorf("no generated client method found for collection query parameters in %s", key)
		}
	}

	var output bytes.Buffer
	if err := format.Node(&output, fset, file); err != nil {
		return nil, fmt.Errorf("format generated query adaptations: %w", err)
	}
	return output.Bytes(), nil
}

func generatedOperationIdentity(body *ast.BlockStmt) (string, string, bool, error) {
	method, path, matches := "", "", 0
	var identityErr error
	ast.Inspect(body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || selector.Sel.Name != "do" {
			return true
		}
		receiver, ok := selector.X.(*ast.Ident)
		if !ok || receiver.Name != "c" {
			return true
		}
		if len(call.Args) < 3 {
			identityErr = fmt.Errorf("generated c.do call has fewer than three arguments")
			return true
		}
		methodLiteral, methodOK := call.Args[1].(*ast.BasicLit)
		pathLiteral, pathOK := call.Args[2].(*ast.BasicLit)
		if !methodOK || methodLiteral.Kind != token.STRING || !pathOK || pathLiteral.Kind != token.STRING {
			identityErr = fmt.Errorf("generated c.do method and path are not string literals")
			return true
		}
		method, identityErr = strconv.Unquote(methodLiteral.Value)
		if identityErr != nil {
			return true
		}
		path, identityErr = strconv.Unquote(pathLiteral.Value)
		matches++
		return true
	})
	if identityErr != nil {
		return "", "", false, identityErr
	}
	if matches > 1 {
		return "", "", false, fmt.Errorf("generated method contains %d c.do calls", matches)
	}
	return strings.ToUpper(method), path, matches == 1, nil
}

func identifier(expression ast.Expr) string {
	name, _ := expression.(*ast.Ident)
	if name == nil {
		return ""
	}
	return name.Name
}

func collectionQueryParameters(spec []byte) (map[string]map[string]bool, error) {
	var document yaml.Node
	if err := yaml.Unmarshal(spec, &document); err != nil {
		return nil, fmt.Errorf("parse OpenAPI query parameters: %w", err)
	}
	if len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("normalized OpenAPI must have one mapping document")
	}
	root := document.Content[0]
	paths, schemas := mapValue(root, "paths"), mapValue(mapValue(root, "components"), "schemas")
	if paths == nil || paths.Kind != yaml.MappingNode || schemas == nil || schemas.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("normalized OpenAPI lacks paths or component schemas")
	}
	parameters := make(map[string]map[string]bool)
	for i := 0; i+1 < len(paths.Content); i += 2 {
		path, pathItem := paths.Content[i].Value, paths.Content[i+1]
		for j := 0; j+1 < len(pathItem.Content); j += 2 {
			method := strings.ToLower(pathItem.Content[j].Value)
			if !httpMethods[method] {
				continue
			}
			operationNode := pathItem.Content[j+1]
			operationID := scalar(mapValue(operationNode, "operationId"))
			key := strings.ToUpper(method) + " " + path
			collection := make(map[string]bool)
			for _, list := range []*yaml.Node{mapValue(pathItem, "parameters"), mapValue(operationNode, "parameters")} {
				if list == nil {
					continue
				}
				if list.Kind != yaml.SequenceNode {
					return nil, fmt.Errorf("operation %s parameters must be a sequence", operationID)
				}
				for _, parameter := range list.Content {
					if scalar(mapValue(parameter, "in")) != "query" {
						continue
					}
					if mapValue(parameter, "$ref") != nil {
						return nil, fmt.Errorf("operation %s has an unresolved query parameter reference", operationID)
					}
					name := scalar(mapValue(parameter, "name"))
					kind, err := querySchemaKind(mapValue(parameter, "schema"), schemas, make(map[string]bool))
					if err != nil {
						return nil, fmt.Errorf("operation %s query parameter %s: %w", operationID, name, err)
					}
					if kind != "array" && kind != "object" {
						continue
					}
					style := scalar(mapValue(parameter, "style"))
					if style == "" {
						style = "form"
					}
					explode := style == "form"
					if explodeNode := mapValue(parameter, "explode"); explodeNode != nil {
						switch scalar(explodeNode) {
						case "true":
							explode = true
						case "false":
							explode = false
						default:
							return nil, fmt.Errorf("operation %s query parameter %s has invalid explode value", operationID, name)
						}
					}
					if style != "form" || !explode {
						return nil, fmt.Errorf("operation %s query parameter %s uses unsupported collection style %s explode=%t", operationID, name, style, explode)
					}
					if name == "" {
						return nil, fmt.Errorf("operation %s has unnamed collection query parameter", operationID)
					}
					collection[name] = true
				}
			}
			if len(collection) != 0 {
				parameters[key] = collection
			}
		}
	}
	return parameters, nil
}

func querySchemaKind(schema, schemas *yaml.Node, seen map[string]bool) (string, error) {
	if schema == nil {
		return "", nil
	}
	if reference := scalar(mapValue(schema, "$ref")); reference != "" {
		prefix := "#/components/schemas/"
		if !strings.HasPrefix(reference, prefix) {
			return "", fmt.Errorf("unsupported schema reference %q", reference)
		}
		name := strings.TrimPrefix(reference, prefix)
		if seen[name] {
			return "", fmt.Errorf("cyclic schema reference %q", reference)
		}
		target := mapValue(schemas, name)
		if target == nil {
			return "", fmt.Errorf("schema reference %q does not exist", reference)
		}
		seen[name] = true
		kind, err := querySchemaKind(target, schemas, seen)
		delete(seen, name)
		return kind, err
	}
	for _, keyword := range []string{"anyOf", "oneOf"} {
		members := mapValue(schema, keyword)
		if members == nil || members.Kind != yaml.SequenceNode {
			continue
		}
		var nonNull []*yaml.Node
		for _, member := range members.Content {
			if scalar(mapValue(member, "type")) != "null" {
				nonNull = append(nonNull, member)
			}
		}
		if len(nonNull) == 1 {
			return querySchemaKind(nonNull[0], schemas, seen)
		}
		if len(nonNull) > 1 {
			for _, member := range nonNull {
				kind, err := querySchemaKind(member, schemas, seen)
				if err != nil {
					return "", err
				}
				if kind == "array" || kind == "object" {
					return "", fmt.Errorf("unsupported composed collection query schema")
				}
			}
		}
	}
	typeNode := mapValue(schema, "type")
	if typeNode != nil && typeNode.Kind == yaml.SequenceNode {
		var found string
		for _, member := range typeNode.Content {
			kind := scalar(member)
			if kind == "null" {
				continue
			}
			if kind == "array" || kind == "object" {
				if found != "" {
					return "", fmt.Errorf("unsupported union of query collection types")
				}
				found = kind
			}
		}
		return found, nil
	}
	kind := scalar(typeNode)
	if kind == "array" || kind == "object" {
		return kind, nil
	}
	return "", nil
}

const formExplodedQueryHelper = `

func addFormExplodedQuery(query url.Values, name string, value any) {
	v := reflect.ValueOf(value)
	for v.IsValid() && (v.Kind() == reflect.Pointer || v.Kind() == reflect.Interface) {
		if v.IsNil() {
			return
		}
		v = v.Elem()
	}
	if !v.IsValid() {
		return
	}
	switch v.Kind() {
	case reflect.Array, reflect.Slice:
		for i := 0; i < v.Len(); i++ {
			addFormExplodedQuery(query, name, v.Index(i).Interface())
		}
	case reflect.Struct:
		typeOf := v.Type()
		for i := 0; i < v.NumField(); i++ {
			fieldType := typeOf.Field(i)
			if fieldType.PkgPath != "" {
				continue
			}
			tag := strings.Split(fieldType.Tag.Get("json"), ",")
			fieldName := tag[0]
			if fieldName == "-" {
				continue
			}
			if fieldName == "" {
				fieldName = fieldType.Name
			}
			fieldValue := v.Field(i)
			for _, option := range tag[1:] {
				if option == "omitempty" && fieldValue.IsZero() {
					fieldName = ""
					break
				}
			}
			if fieldName != "" {
				addFormExplodedQuery(query, fieldName, fieldValue.Interface())
			}
		}
	case reflect.Map:
		if v.Type().Key().Kind() != reflect.String {
			return
		}
		iterator := v.MapRange()
		for iterator.Next() {
			addFormExplodedQuery(query, iterator.Key().String(), iterator.Value().Interface())
		}
	default:
		query.Add(name, formatPathValue(v.Interface()))
	}
}
`
