package devincli

import (
	"fmt"
	"sort"
	"strings"

	openapi "github.com/jathanism/okapi"
	okcli "github.com/jathanism/okapi/cli"
	"github.com/urfave/cli/v2"
)

func operationCommands(projection Projection, action func(Operation) cli.ActionFunc, onUsageError func(*cli.Context, error, bool) error) ([]*cli.Command, error) {
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
				flags, err := adaptGeneratedFlags(generatedCommand.Flags, operation)
				if err != nil {
					return fmt.Errorf("adapt flags for %s: %w", id, err)
				}
				flatCommands = append(flatCommands, &cli.Command{
					Name:            operation.ID,
					Usage:           generatedCommand.Usage,
					HideHelpCommand: true,
					OnUsageError:    onUsageError,
					Flags:           flags,
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

func adaptGeneratedFlags(generated []cli.Flag, operation Operation) ([]cli.Flag, error) {
	byName := make(map[string]cli.Flag, len(generated)+len(operation.Parameters)+8)
	for _, flag := range generated {
		if flag == nil || len(flag.Names()) == 0 {
			continue
		}
		name := flag.Names()[0]
		if name == "data" && requestBodyKind(operation) != bodyJSON {
			continue
		}
		makeOptional(flag)
		if bodyFlag, ok := flag.(*cli.StringFlag); ok && name == "data" {
			bodyFlag.Value = ""
			bodyFlag.Usage = "JSON request body: inline JSON, @file, or - for stdin."
		}
		byName[name] = flag
	}
	for _, parameter := range operation.Parameters {
		if _, ok := byName[parameter.Name]; !ok {
			return nil, fmt.Errorf("okapi did not generate a flag for parameter %q", parameter.Name)
		}
	}
	if kind := requestBodyKind(operation); kind == bodyJSON {
		if _, ok := byName["data"]; !ok {
			byName["data"] = &cli.StringFlag{Name: "data", Usage: "JSON request body: inline JSON, @file, or - for stdin."}
		}
	} else if kind == bodyMultipart {
		byName["file"] = &cli.StringFlag{Name: "file", Usage: "Required file to upload as multipart field file."}
	}
	for _, flag := range commonFlags() {
		if _, ok := byName[flag.Names()[0]]; !ok {
			byName[flag.Names()[0]] = flag
		}
	}
	flags := make([]cli.Flag, 0, len(byName))
	for _, flag := range byName {
		makeOptional(flag)
		flags = append(flags, flag)
	}
	sort.SliceStable(flags, func(i, j int) bool { return flags[i].Names()[0] < flags[j].Names()[0] })
	for _, parameter := range operation.Parameters {
		if _, ok := byName[parameter.Name]; !ok {
			return nil, fmt.Errorf("no generated flag for parameter %q", parameter.Name)
		}
	}
	return flags, nil
}

func makeOptional(flag cli.Flag) {
	switch typed := flag.(type) {
	case *cli.StringFlag:
		typed.Required = false
	case *cli.IntFlag:
		typed.Required = false
	case *cli.Int64Flag:
		typed.Required = false
	case *cli.Float64Flag:
		typed.Required = false
	case *cli.BoolFlag:
		typed.Required = false
	case *cli.StringSliceFlag:
		typed.Required = false
	}
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
