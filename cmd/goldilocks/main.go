package main

import (
	"context"
	"fmt"
	"io"
	"os"
)

var version = "dev"

func RunCLI(ctx context.Context, args []string, input io.Reader, output, diagnostics io.Writer) int {
	_ = ctx
	if len(args) == 1 && args[0] == "--version" {
		fmt.Fprintln(output, version)
		return 0
	}
	if len(args) == 0 || args[0] == "--help" {
		fmt.Fprintln(output, "Usage: goldilocks hook | pr-watch ACTION | watcher ACTION | --version")
		return 0
	}
	if args[0] == "hook" && len(args) == 1 {
		if err := RunHook(input, output, os.Getenv("PLUGIN_ROOT")); err != nil {
			fmt.Fprintln(diagnostics, "goldilocks:", err)
		}
		return 0
	}
	fmt.Fprintln(diagnostics, "goldilocks: unknown or unsupported command")
	return 2
}

func main() { os.Exit(RunCLI(context.Background(), os.Args[1:], os.Stdin, os.Stdout, os.Stderr)) }
