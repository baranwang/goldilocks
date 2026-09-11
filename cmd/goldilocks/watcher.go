package main

import (
	"context"
	"io"
	"os"
	"time"

	"github.com/baranwang/goldilocks/internal/prwatch"
)

func RunWatcherCLI(ctx context.Context, args []string, output io.Writer) error {
	root, err := prwatch.DefaultControllerRoot()
	if err != nil {
		return err
	}
	controller, err := prwatch.NewController(root, time.Now)
	if err != nil {
		return err
	}
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	return controller.RunCLI(ctx, args, os.Getenv("CODEX_THREAD_ID"), cwd, output)
}
