package devincli

import (
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
)

type trackingWriter struct {
	writer io.Writer
	err    error
}

func (w *trackingWriter) Write(data []byte) (int, error) {
	count, err := w.writer.Write(data)
	if err != nil {
		w.err = err
	}
	return count, err
}

func writeResponse(writer io.Writer, response *http.Response) error {
	if response.Body == nil {
		response.Body = http.NoBody
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("API request failed with HTTP status %d", response.StatusCode)
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return fmt.Errorf("could not read API response")
	}
	if len(body) > 0 {
		if _, err := writer.Write(body); err != nil {
			return fmt.Errorf("could not write API response")
		}
	}
	return nil
}

func dryRunOutput(request *http.Request, body []byte, filePath string, fileSize int64) string {
	var output strings.Builder
	fmt.Fprintf(&output, "%s %s\n", request.Method, request.URL.String())
	keys := make([]string, 0, len(request.Header))
	for key := range request.Header {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		fmt.Fprintf(&output, "%s: %s\n", key, strings.Join(request.Header.Values(key), ", "))
	}
	output.WriteByte('\n')
	if filePath != "" {
		fmt.Fprintf(&output, "--file %q (%d bytes; filename %q)\n", filePath, fileSize, filepath.Base(filePath))
	} else if len(body) > 0 {
		output.Write(body)
		if body[len(body)-1] != '\n' {
			output.WriteByte('\n')
		}
	}
	return output.String()
}

func writeString(writer io.Writer, value string) error {
	_, err := io.WriteString(writer, value)
	return err
}

func (r *runner) report(code int, message string) int {
	message = redact(message, r.apiKey)
	if _, err := fmt.Fprintf(r.stderr, "devin-client: %s\n", message); err != nil {
		return 1
	}
	return code
}

func redact(message, key string) string {
	if key != "" {
		message = strings.ReplaceAll(message, key, "[REDACTED]")
	}
	return message
}
