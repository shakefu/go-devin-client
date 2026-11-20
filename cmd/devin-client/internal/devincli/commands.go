package devincli

import (
	"fmt"
	"sort"
	"strings"

	openapi "github.com/jathanism/okapi"
	okcli "github.com/jathanism/okapi/cli"
	"github.com/urfave/cli/v2"
)

func operationCommands(projection Projection, action func(Operation) cli.ActionFunc) ([]*cli.Command, error) {
	if len(projection.Operations) != operationCount {
		return nil, fmt.Errorf("CLI projection has %d operations; want %d", len(projection.Operations), operationCount)
	}
	source, err := projectionTreeSource(projection)
	if err != nil {
		return nil, fmt.Errorf("build okapi operation index: %w", err)
	}
	api, err := (*openapi.OpenApi)(nil).NewFromBytes(source)
	if err != nil {
		return nil, fmt.Errorf("load okapi operation index: %w", err)
	}
	generated := (&okcli.OpenApiCli{
		Api:      api,
		AppName:  "devin-client",
		App:      &cli.App{DisableSliceFlagSeparator: true, HideHelpCommand: true},
		HostFlag: &cli.StringFlag{Name: "base-url"},
	}).New().Commands()

	byID := make(map[string]Operation, len(projection.Operations))
	for _, operation := range projection.Operations {
		byID[operation.ID] = operation
	}
	flattened := make(map[string]struct{}, len(projection.Operations))
	flatCommands := make([]*cli.Command, 0, len(projection.Operations))
	var visit func([]*cli.Command, []string) error
	visit = func(generatedCommands []*cli.Command, prefix []string) error {
		for _, generatedCommand := range generatedCommands {
			segments := append(append([]string(nil), prefix...), generatedCommand.Name)
			if generatedCommand.Action != nil {
				id := strings.Join(segments, "_")
				if _, exists := flattened[id]; exists {
					return fmt.Errorf("okapi command tree repeats operationId %q", id)
				}
				operation, exists := byID[id]
				if !exists {
					return fmt.Errorf("okapi command tree contains unknown operationId %q", id)
				}
				flattened[id] = struct{}{}
				flatCommands = append(flatCommands, &cli.Command{
					Name:            operation.ID,
					Usage:           operation.Summary,
					HideHelpCommand: true,
					OnUsageError:    suppressUsageErrors,
					Flags:           operationFlags(operation),
					Action:          action(operation),
				})
				continue
			}
			if err := visit(generatedCommand.Subcommands, segments); err != nil {
				return err
			}
		}
		return nil
	}
	if err := visit(generated, nil); err != nil {
		return nil, err
	}
	if len(flattened) != len(projection.Operations) {
		return nil, fmt.Errorf("okapi command tree has %d leaves; projection has %d operations", len(flattened), len(projection.Operations))
	}

	for id := range byID {
		if _, exists := flattened[id]; !exists {
			return nil, fmt.Errorf("okapi command tree does not preserve operationId %q", id)
		}
	}
	sort.Slice(flatCommands, func(i, j int) bool { return flatCommands[i].Name < flatCommands[j].Name })
	return flatCommands, nil
}

func operationFlags(operation Operation) []cli.Flag {
	flags := commonFlags()
	for _, parameter := range operation.Parameters {
		var flag cli.Flag
		switch parameter.Type {
		case "integer":
			flag = &cli.Int64Flag{Name: parameter.Name, Usage: parameter.Description}
		case "number":
			flag = &cli.Float64Flag{Name: parameter.Name, Usage: parameter.Description}
		case "boolean":
			flag = &cli.BoolFlag{Name: parameter.Name, Usage: parameter.Description}
		case "array":
			flag = &cli.StringSliceFlag{Name: parameter.Name, Usage: parameter.Description}
		default:
			flag = &cli.StringFlag{Name: parameter.Name, Usage: parameter.Description}
		}
		flags = append(flags, flag)
	}
	if operation.RequestBody != nil {
		switch requestBodyKind(operation) {
		case bodyJSON:
			flags = append(flags, &cli.StringFlag{Name: "data", Usage: "JSON request body: inline JSON, @file, or - for stdin."})
		case bodyMultipart:
			flags = append(flags, &cli.StringFlag{Name: "file", Usage: "File to upload as multipart field file."})
		}
	}
	sort.SliceStable(flags, func(i, j int) bool { return flags[i].Names()[0] < flags[j].Names()[0] })
	return flags
}

func commonFlags() []cli.Flag {
	flags := []cli.Flag{
		&cli.StringFlag{Name: "api-key", Usage: "Devin API bearer key."},
		&cli.StringFlag{Name: "base-url", Usage: "Devin API origin."},
		&cli.StringFlag{Name: "timeout", Usage: "Request timeout as a Go duration."},
		&cli.BoolFlag{Name: "allow-insecure-http", Usage: "Allow an HTTP API origin."},
		&cli.BoolFlag{Name: "read-only", Usage: "Reject operations other than GET and HEAD."},
		&cli.BoolFlag{Name: "dry-run", Usage: "Print the request without sending it."},
	}
	sort.SliceStable(flags, func(i, j int) bool { return flags[i].Names()[0] < flags[j].Names()[0] })
	return flags
}

type bodyKind uint8

const (
	bodyNone bodyKind = iota
	bodyJSON
	bodyMultipart
)

func requestBodyKind(operation Operation) bodyKind {
	if operation.RequestBody == nil {
		return bodyNone
	}
	for _, mediaType := range operation.RequestBody.MediaTypes {
		if mediaType == "multipart/form-data" {
			return bodyMultipart
		}
	}
	for _, mediaType := range operation.RequestBody.MediaTypes {
		if mediaType == "application/json" {
			return bodyJSON
		}
	}
	return bodyNone
}
