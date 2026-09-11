package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/baranwang/goldilocks/internal/prwatch"
)

var version = "dev"

func RunCLI(ctx context.Context, args []string, input io.Reader, output, diagnostics io.Writer) int {
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
	if args[0] == "pr-watch" {
		runCtx, cancel := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
		defer cancel()
		err := prwatch.RunCLI(runCtx, args[1:], output)
		if err == nil {
			return 0
		}
		fmt.Fprintln(diagnostics, "goldilocks:", err)
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return 130
		}
		if errors.Is(err, prwatch.ErrLocked) {
			return 3
		}
		return 2
	}
	if args[0] == "watcher" {
		runCtx, cancel := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
		defer cancel()
		if err := RunWatcherCLI(runCtx, args[1:], output); err != nil {
			fmt.Fprintln(diagnostics, "goldilocks:", err)
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return 130
			}
			if errors.Is(err, prwatch.ErrLocked) {
				return 3
			}
			return 2
		}
		return 0
	}
	fmt.Fprintln(diagnostics, "goldilocks: unknown or unsupported command")
	return 2
}

func main() { os.Exit(RunCLI(context.Background(), os.Args[1:], os.Stdin, os.Stdout, os.Stderr)) }
