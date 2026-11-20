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

	"github.com/urfave/cli/v2"
)

type Options struct {
	Stdin      io.Reader
	Stdout     io.Writer
	Stderr     io.Writer
	Getenv     func(string) string
	HTTPClient *http.Client

	sdkFactory sdkFactory
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

func (r *runner) newApp() (*cli.App, error) {
	projection, err := embeddedProjection()
	if err != nil {
		return nil, err
	}
	commands, err := operationCommands(projection, r.operationAction)
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
		HideHelpCommand:           true,
		Flags:                     commonFlags(),
		Commands:                  commands,
		Writer:                    r.stdout,
		ErrWriter:                 io.Discard,
		DisableSliceFlagSeparator: true,
		ExitErrHandler:            func(*cli.Context, error) {},
		OnUsageError:              suppressUsageErrors,
		Action: func(c *cli.Context) error {
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
		return info.Main.Version
	}
	return "dev"
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
