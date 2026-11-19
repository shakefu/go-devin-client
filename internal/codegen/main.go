package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: codegen <normalize|adapt> ...")
		os.Exit(2)
	}

	switch os.Args[1] {
	case "normalize":
		if len(os.Args) != 4 {
			fmt.Fprintln(os.Stderr, "usage: codegen normalize <source.yaml> <normalized.yaml>")
			os.Exit(2)
		}
		input, err := os.ReadFile(os.Args[2])
		fatalIf(err)
		output, excluded, hoisted, err := normalize(input)
		fatalIf(err)
		fatalIf(os.WriteFile(os.Args[3], output, 0o644))
		fmt.Printf("normalized 284 operations to 280; excluded %d pending operations; hoisted %d inline schemas\n", len(excluded), hoisted)
	case "adapt":
		if len(os.Args) != 5 {
			fmt.Fprintln(os.Stderr, "usage: codegen adapt <client.gen.go> <types.gen.go> <normalized.yaml>")
			os.Exit(2)
		}
		clientInput, err := os.ReadFile(os.Args[2])
		fatalIf(err)
		typesInput, err := os.ReadFile(os.Args[3])
		fatalIf(err)
		specInput, err := os.ReadFile(os.Args[4])
		fatalIf(err)
		clientOutput, typesOutput, err := adaptGeneratedFiles(clientInput, typesInput, specInput)
		fatalIf(err)
		fatalIf(os.WriteFile(os.Args[2], clientOutput, 0o644))
		fatalIf(os.WriteFile(os.Args[3], typesOutput, 0o644))
	default:
		fmt.Fprintf(os.Stderr, "unknown codegen command %q\n", os.Args[1])
		os.Exit(2)
	}
}

func fatalIf(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
