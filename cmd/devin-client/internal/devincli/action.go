package devincli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/urfave/cli/v2"
)

type commandFailure struct {
	code    int
	message string
}

func (e *commandFailure) Error() string { return e.message }

func failure(code int, message string) error {
	return &commandFailure{code: code, message: message}
}

type preparedRequest struct {
	request    *http.Request
	preview    string
	uploadPath string
	uploadSize int64
	values     map[string]any
}

func (r *runner) operationAction(operation Operation) cli.ActionFunc {
	return func(c *cli.Context) error {
		if lookupBool(c, "read-only") && operation.Method != http.MethodGet && operation.Method != http.MethodHead {
			return failure(2, fmt.Sprintf("read-only mode: %s %s is not allowed", operation.Method, operation.Path))
		}
		if c.Args().Len() != 0 {
			return failure(2, "operation does not accept positional arguments")
		}
		if !lookupBool(c, "dry-run") {
			r.apiKey = settingValue(c, "api-key", "DEVIN_API_KEY", r.opts.Getenv)
		}
		if err := checkRequiredInputs(c, operation); err != nil {
			return err
		}
		input, err := loadRequestBodyInput(r.ctx, c, operation, r.opts.Stdin)
		if err != nil {
			return err
		}
		settings, err := resolveSettings(c, r.opts.Getenv)
		if err != nil {
			return failure(1, err.Error())
		}
		r.apiKey = settings.APIKey
		prepared, err := prepareRequest(r.ctx, c, operation, settings, input)
		if err != nil {
			return err
		}
		if settings.DryRun {
			if err := writeString(r.stdout, prepared.preview); err != nil {
				return failure(1, "could not write dry-run output")
			}
			return nil
		}

		dispatcher, err := newSDKDispatcher(settings, r.opts.HTTPClient)
		if err != nil {
			return failure(1, "could not initialize API client")
		}
		response, err := dispatcher.Do(r.ctx, operation, prepared.request, input, prepared.values)
		if err != nil {
			if response != nil && response.Body != nil {
				_ = response.Body.Close()
			}
			return failure(1, "API request failed")
		}
		if response == nil {
			return failure(1, "API request returned no response")
		}
		if err := writeResponse(r.stdout, response); err != nil {
			return failure(1, err.Error())
		}
		return nil
	}
}

func checkRequiredInputs(c *cli.Context, operation Operation) error {
	for _, parameter := range operation.Parameters {
		if parameter.Required && !c.IsSet(parameter.Name) {
			return failure(2, "required input missing: --"+parameter.Name)
		}
		if c.IsSet(parameter.Name) {
			if _, err := flagValue(c, parameter); err != nil {
				return failure(2, "invalid value for --"+parameter.Name)
			}
		}
	}
	if operation.RequestBody == nil {
		return nil
	}
	switch requestBodyKind(operation) {
	case bodyJSON:
		if operation.RequestBody.Required && !c.IsSet("data") {
			return failure(2, "required input missing: --data")
		}
	case bodyMultipart:
		if !c.IsSet("file") {
			return failure(2, "required input missing: --file")
		}
	default:
		return failure(1, "unsupported request body media type")
	}
	return nil
}

type requestBodyInput struct {
	json     []byte
	filePath string
	fileSize int64
}

func loadRequestBodyInput(ctx context.Context, c *cli.Context, operation Operation, stdin io.Reader) (requestBodyInput, error) {
	var input requestBodyInput
	switch requestBodyKind(operation) {
	case bodyJSON:
		if c.IsSet("data") {
			data, err := readJSONInput(ctx, c.String("data"), stdin)
			if err != nil {
				return requestBodyInput{}, err
			}
			input.json = data
		}
	case bodyMultipart:
		if c.IsSet("file") {
			input.filePath = c.String("file")
			info, err := os.Stat(input.filePath)
			if err != nil || !info.Mode().IsRegular() {
				return requestBodyInput{}, failure(1, "could not stat upload file")
			}
			input.fileSize = info.Size()
		}
	}
	return input, nil
}

func prepareRequest(ctx context.Context, c *cli.Context, operation Operation, settings settings, input requestBodyInput) (preparedRequest, error) {
	requestPath := operation.Path
	query := make(url.Values)
	header := make(http.Header)
	values := make(map[string]any, len(operation.Parameters))
	for _, parameter := range operation.Parameters {
		if !c.IsSet(parameter.Name) {
			continue
		}
		value, err := flagValue(c, parameter)
		if err != nil {
			return preparedRequest{}, failure(2, "invalid value for --"+parameter.Name)
		}
		values[parameter.Name] = value
		switch parameter.In {
		case "path":
			if strings.Contains(requestPath, "{"+parameter.Name+"}") {
				requestPath = strings.ReplaceAll(requestPath, "{"+parameter.Name+"}", url.PathEscape(fmt.Sprint(value)))
			}
		case "query":
			if err := addQueryValues(query, parameter, value); err != nil {
				return preparedRequest{}, failure(2, "invalid value for --"+parameter.Name)
			}
		case "header":
			for _, item := range stringValues(value) {
				header.Add(parameter.Name, item)
			}
		case "cookie":
			for _, item := range stringValues(value) {
				header.Add("Cookie", parameter.Name+"="+item)
			}
		}
	}
	if strings.Contains(requestPath, "{") {
		return preparedRequest{}, failure(2, "required path parameter is missing")
	}
	requestURL, err := url.Parse(settings.BaseURL + requestPath)
	if err != nil {
		return preparedRequest{}, failure(1, "could not construct API URL")
	}
	requestURL.RawQuery = query.Encode()
	if len(operation.ResponseMediaTypes) > 0 {
		header.Set("Accept", strings.Join(operation.ResponseMediaTypes, ", "))
	}
	if settings.DryRun {
		header.Set("Authorization", "Bearer [REDACTED]")
	}

	var body io.Reader
	var bodyBytes []byte
	switch requestBodyKind(operation) {
	case bodyJSON:
		if input.json != nil {
			bodyBytes = input.json
			body = bytes.NewReader(bodyBytes)
			header.Set("Content-Type", "application/json")
		}
	case bodyMultipart:
		if input.filePath != "" {
			if settings.DryRun {
				header.Set("Content-Type", "multipart/form-data")
			}
		}
	}

	request, err := http.NewRequestWithContext(ctx, operation.Method, requestURL.String(), body)
	if err != nil {
		if closer, ok := body.(io.Closer); ok {
			_ = closer.Close()
		}
		return preparedRequest{}, failure(1, "could not construct API request")
	}
	request.Header = header
	prepared := preparedRequest{request: request, uploadPath: input.filePath, uploadSize: input.fileSize, values: values}
	if settings.DryRun {
		prepared.preview = dryRunOutput(request, bodyBytes, input.filePath, input.fileSize)
	}
	return prepared, nil
}

func flagValue(c *cli.Context, parameter Parameter) (any, error) {
	switch parameter.Type {
	case "integer":
		return int64(c.Int(parameter.Name)), nil
	case "number":
		number := c.Float64(parameter.Name)
		if math.IsNaN(number) || math.IsInf(number, 0) {
			return nil, fmt.Errorf("number parameter must be finite")
		}
		return number, nil
	case "boolean":
		return c.Bool(parameter.Name), nil
	case "array":
		values := c.StringSlice(parameter.Name)
		parsed := make([]any, len(values))
		for index, value := range values {
			switch parameter.ItemsType {
			case "integer":
				integer, err := strconv.ParseInt(value, 10, 64)
				if err != nil {
					return nil, err
				}
				parsed[index] = integer
			case "number":
				number, err := strconv.ParseFloat(value, 64)
				if err != nil {
					return nil, err
				}
				if math.IsNaN(number) || math.IsInf(number, 0) {
					return nil, fmt.Errorf("number array items must be finite")
				}
				parsed[index] = number
			case "boolean":
				boolean, err := strconv.ParseBool(value)
				if err != nil {
					return nil, err
				}
				parsed[index] = boolean
			default:
				parsed[index] = value
			}
		}
		return parsed, nil
	case "object":
		var value map[string]any
		if err := json.Unmarshal([]byte(c.String(parameter.Name)), &value); err != nil || value == nil {
			return nil, fmt.Errorf("object parameter must be a JSON object")
		}
		return value, nil
	default:
		return c.String(parameter.Name), nil
	}
}

func addQueryValues(query url.Values, parameter Parameter, value any) error {
	if object, ok := value.(map[string]any); ok {
		keys := make([]string, 0, len(object))
		for key := range object {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		switch {
		case parameter.Style == "deepObject":
			for _, key := range keys {
				query.Add(parameter.Name+"["+key+"]", valueString(object[key]))
			}
		case parameter.Explode:
			for _, key := range keys {
				query.Add(key, valueString(object[key]))
			}
		default:
			parts := make([]string, 0, len(keys)*2)
			for _, key := range keys {
				parts = append(parts, key, valueString(object[key]))
			}
			query.Add(parameter.Name, strings.Join(parts, ","))
		}
		return nil
	}
	values := stringValues(value)
	if parameter.Type == "array" && !parameter.Explode {
		separator := ","
		if parameter.Style == "spaceDelimited" {
			separator = " "
		} else if parameter.Style == "pipeDelimited" {
			separator = "|"
		}
		query.Add(parameter.Name, strings.Join(values, separator))
		return nil
	}
	for _, item := range values {
		query.Add(parameter.Name, item)
	}
	return nil
}

func stringValues(value any) []string {
	switch typed := value.(type) {
	case []string:
		return typed
	case []any:
		values := make([]string, len(typed))
		for index, item := range typed {
			values[index] = valueString(item)
		}
		return values
	case int64:
		return []string{strconv.FormatInt(typed, 10)}
	case float64:
		return []string{strconv.FormatFloat(typed, 'f', -1, 64)}
	case bool:
		return []string{strconv.FormatBool(typed)}
	case map[string]any:
		return []string{valueString(typed)}
	default:
		return []string{fmt.Sprint(value)}
	}
}

func valueString(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case json.Number:
		return typed.String()
	default:
		data, err := json.Marshal(value)
		if err == nil {
			return string(data)
		}
		return fmt.Sprint(value)
	}
}

func readJSONInput(ctx context.Context, source string, stdin io.Reader) ([]byte, error) {
	var data []byte
	var err error
	switch {
	case source == "-":
		data, err = readJSONStdin(ctx, stdin)
		if err != nil {
			return nil, failure(1, "could not read JSON from stdin")
		}
	case strings.HasPrefix(source, "@"):
		if len(source) == 1 {
			return nil, failure(1, "could not read JSON file")
		}
		data, err = os.ReadFile(source[1:])
		if err != nil {
			return nil, failure(1, "could not read JSON file")
		}
	default:
		data = []byte(source)
	}
	if !json.Valid(data) {
		return nil, failure(2, "request body must be exactly one valid JSON document")
	}
	return data, nil
}

func readJSONStdin(ctx context.Context, stdin io.Reader) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	closer, ok := stdin.(io.ReadCloser)
	if !ok {
		return io.ReadAll(stdin)
	}
	type readResult struct {
		data []byte
		err  error
	}
	completed := make(chan readResult, 1)
	go func() {
		data, err := io.ReadAll(stdin)
		completed <- readResult{data: data, err: err}
	}()
	select {
	case result := <-completed:
		return result.data, result.err
	case <-ctx.Done():
		_ = closer.Close()
		<-completed
		return nil, ctx.Err()
	}
}
