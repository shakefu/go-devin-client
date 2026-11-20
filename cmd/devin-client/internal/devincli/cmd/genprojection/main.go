package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/shakefu/go-devin-client/cmd/devin-client/internal/devincli"
)

func main() {
	sourcePath := flag.String("source", "", "vendored Devin OpenAPI source")
	outputPath := flag.String("output", "", "generated CLI projection output")
	flag.Parse()
	if *sourcePath == "" || *outputPath == "" {
		fmt.Fprintln(os.Stderr, "source and output are required")
		os.Exit(2)
	}
	source, err := os.ReadFile(*sourcePath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "could not read OpenAPI source")
		os.Exit(1)
	}
	projection, err := devincli.GenerateProjection(source)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := os.WriteFile(*outputPath, projection, 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "could not write CLI projection")
		os.Exit(1)
	}
}
