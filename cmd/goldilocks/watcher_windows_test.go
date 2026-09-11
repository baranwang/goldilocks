//go:build windows

package main

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/baranwang/goldilocks/internal/prwatch"
)

func TestRunWatcherCLIRejectsManagedExecutionOnWindows(t *testing.T) {
	t.Setenv("CODEX_HOME", t.TempDir())
	t.Setenv("CODEX_THREAD_ID", watcherParent)
	var out bytes.Buffer
	err := RunWatcherCLI(context.Background(), []string{"start", "--pr", watcherPR}, &out)
	if !errors.Is(err, prwatch.ErrUnsupportedPlatform) || out.Len() != 0 {
		t.Fatalf("managed execution reached Windows runtime: %v %q", err, out.String())
	}
}
