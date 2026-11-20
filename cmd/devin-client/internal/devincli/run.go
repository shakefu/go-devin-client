package devincli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"

	"github.com/urfave/cli/v2"
)

type Options struct {
	Stdin      io.Reader
	Stdout     io.Writer
	Stderr     io.Writer
	Getenv     func(string) string
	HTTPClient *http.Client
}

type runner struct {
	ctx    context.Context
	opts   Options
	stdout *trackingWriter
	stderr *trackingWriter
	apiKey string
}

var version string

func Run(ctx context.Context, args []string, options Options) int {
	if ctx == nil {
		ctx = context.Background()
	}
	if options.Stdin == nil {
		options.Stdin = os.Stdin
	}
	if options.Stdout == nil {
		options.Stdout = os.Stdout
	}
	if options.Stderr == nil {
		options.Stderr = os.Stderr
	}
	if options.Getenv == nil {
		options.Getenv = os.Getenv
	}
	if len(args) == 0 {
		args = []string{"devin-client"}
	}
	r := &runner{
		ctx:    ctx,
		opts:   options,
		stdout: &trackingWriter{writer: options.Stdout},
		stderr: &trackingWriter{writer: options.Stderr},
	}
	if needsEarlyCredentialRedaction(args) {
		if key, ok := apiKeyArgument(args); ok {
			r.apiKey = key
		} else {
			key := options.Getenv("DEVIN_API_KEY")
			r.apiKey = key
			getenv := r.opts.Getenv
			r.opts.Getenv = func(name string) string {
				if name == "DEVIN_API_KEY" {
					return key
				}
				return getenv(name)
			}
		}
	}
	app, err := r.newApp()
	if err != nil {
		return r.report(1, "could not initialize CLI")
	}
	if err := app.RunContext(ctx, args); err != nil {
		if r.stdout.err != nil {
			return r.report(1, "could not write CLI output")
		}
		var commandErr *commandFailure
		if errors.As(err, &commandErr) {
			return r.report(commandErr.code, commandErr.message)
		}
		return r.report(2, "invalid command syntax")
	}
	if r.stdout.err != nil {
		return r.report(1, "could not write CLI output")
	}
	return 0
}

func needsEarlyCredentialRedaction(args []string) bool {
	projection, err := embeddedProjection()
	if err != nil {
		return false
	}
	var operation *Operation
	commandIndex := -1
	for _, candidate := range projection.Operations {
		for index, argument := range args {
			if argument == candidate.ID {
				operationCopy := candidate
				operation = &operationCopy
				commandIndex = index
				break
			}
		}
		if operation != nil {
			break
		}
	}
	if operation == nil || operationHasPositionalArgument(args, commandIndex, *operation) {
		return false
	}
	booleanFlags := map[string]bool{
		"allow-insecure-http": true, "read-only": true, "dry-run": true,
		"help": true, "h": true, "version": true,
	}
	for _, parameter := range operation.Parameters {
		if parameter.Type == "boolean" {
			booleanFlags[parameter.Name] = true
		}
	}
	for index := 0; index < len(args); index++ {
		argument := args[index]
		if argument == "--" {
			break
		}
		if argument == "ops" {
			return false
		}
		name, value, inline, ok := longOption(argument)
		if !ok {
			continue
		}
		switch name {
		case "help", "h", "version":
			if !inline || boolOptionEnabled(value) {
				return false
			}
		case "dry-run", "read-only":
			if !inline || boolOptionEnabled(value) {
				return false
			}
		}
		if !inline && !booleanFlags[name] && index+1 < len(args) {
			index++
		}
	}
	return true
}

func operationHasPositionalArgument(args []string, commandIndex int, operation Operation) bool {
	booleanFlags := map[string]bool{"allow-insecure-http": true, "read-only": true, "dry-run": true}
	for _, parameter := range operation.Parameters {
		if parameter.Type == "boolean" {
			booleanFlags[parameter.Name] = true
		}
	}
	for index := commandIndex + 1; index < len(args); index++ {
		argument := args[index]
		if argument == "--" {
			return index+1 < len(args)
		}
		if !strings.HasPrefix(argument, "-") {
			return true
		}
		name, _, inline := strings.Cut(strings.TrimLeft(argument, "-"), "=")
		if inline || booleanFlags[name] {
			continue
		}
		if index+1 < len(args) {
			index++
		}
	}
	return false
}

func apiKeyArgument(args []string) (string, bool) {
	var key string
	found := false
	for index := 0; index < len(args); index++ {
		argument := args[index]
		if argument == "--" {
			break
		}
		name, value, inline, ok := longOption(argument)
		if ok && name == "api-key" {
			if !inline && index+1 < len(args) {
				index++
				value = args[index]
			}
			key, found = value, true
		}
	}
	return key, found
}

func longOption(argument string) (name, value string, inline, ok bool) {
	switch {
	case strings.HasPrefix(argument, "--"):
		argument = argument[2:]
	case strings.HasPrefix(argument, "-"):
		argument = argument[1:]
	default:
		return "", "", false, false
	}
	if argument == "" {
		return "", "", false, false
	}
	name, value, inline = strings.Cut(argument, "=")
	return name, value, inline, true
}

func boolOptionEnabled(value string) bool {
	enabled, err := strconv.ParseBool(value)
	return err == nil && enabled
}

func (r *runner) newApp() (*cli.App, error) {
	projection, err := embeddedProjection()
	if err != nil {
		return nil, err
	}
	commands, err := operationCommands(projection, r.operationAction, suppressUsageErrors)
	if err != nil {
		return nil, err
	}
	ops := &cli.Command{
		Name:            "ops",
		Usage:           "List all API operations.",
		HideHelpCommand: true,
		OnUsageError:    suppressUsageErrors,
		Flags:           commonFlags(),
		Action:          r.operationsAction(projection.Operations),
	}
	commands = append(commands, ops)
	sort.Slice(commands, func(i, j int) bool { return commands[i].Name < commands[j].Name })
	app := &cli.App{
		Name:                      "devin-client",
		Usage:                     "Call the Devin v3 API.",
		Version:                   cliVersion(),
		HideVersion:               true,
		HideHelpCommand:           true,
		Flags:                     append(commonFlags(), &cli.BoolFlag{Name: "version", Usage: "Print version."}),
		Commands:                  commands,
		Writer:                    r.stdout,
		ErrWriter:                 io.Discard,
		DisableSliceFlagSeparator: true,
		ExitErrHandler:            func(*cli.Context, error) {},
		OnUsageError:              suppressUsageErrors,
		Action: func(c *cli.Context) error {
			if c.Bool("version") {
				if err := writeString(r.stdout, cliVersion()+"\n"); err != nil {
					return failure(1, "could not write version")
				}
				return nil
			}
			if c.Args().Len() == 0 {
				if err := cli.ShowAppHelp(c); err != nil {
					return failure(1, "could not write CLI help")
				}
				return nil
			}
			return failure(2, "invalid command syntax")
		},
	}
	return app, nil
}

func suppressUsageErrors(*cli.Context, error, bool) error {
	return failure(2, "invalid command syntax")
}

func cliVersion() string {
	if version != "" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		if !isPseudoVersion(info.Main.Version) {
			return info.Main.Version
		}
	}
	return "dev"
}

func isPseudoVersion(value string) bool {
	for index := 0; index+15 < len(value); index++ {
		if value[index] != '-' || value[index+15] != '-' {
			continue
		}
		valid := true
		for _, digit := range value[index+1 : index+15] {
			if digit < '0' || digit > '9' {
				valid = false
				break
			}
		}
		if valid {
			return true
		}
	}
	return false
}

func (r *runner) operationsAction(operations []Operation) cli.ActionFunc {
	return func(c *cli.Context) error {
		if c.Args().Len() != 0 {
			return failure(2, "invalid command syntax")
		}
		for _, operation := range operations {
			if _, err := fmt.Fprintf(r.stdout, "%s %s %s\n", operation.ID, operation.Method, operation.Path); err != nil {
				return failure(1, "could not write operation list")
			}
		}
		return nil
	}
}
