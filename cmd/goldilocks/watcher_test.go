package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/baranwang/goldilocks/internal/prwatch"
)

const (
	watcherParent = "00000000-0000-4000-8000-000000000011"
	watcherPR     = "https://github.com/example/project/pull/7"
)

func TestRunWatcherCLIDispatchesManagedController(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("managed POSIX CLI")
	}
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	t.Setenv("CODEX_THREAD_ID", watcherParent)
	var out bytes.Buffer
	if err := RunWatcherCLI(context.Background(), []string{"start", "--pr", watcherPR}, &out); err != nil {
		t.Fatal(err)
	}
	var started prwatch.StartResult
	if err := json.Unmarshal(out.Bytes(), &started); err != nil {
		t.Fatal(err)
	}
	if !started.Spawn || started.TicketFile == "" || started.StateFile == "" {
		t.Fatalf("unexpected start result: %+v", started)
	}
	if _, err := os.Stat(filepath.Join(home, "goldilocks", "pr-watch", "parents")); err != nil {
		t.Fatal(err)
	}
}

func TestRunWatcherCLIMissingRuntimeIDIsInputError(t *testing.T) {
	t.Setenv("CODEX_HOME", t.TempDir())
	t.Setenv("CODEX_THREAD_ID", "")
	var out bytes.Buffer
	err := RunWatcherCLI(context.Background(), []string{"start", "--pr", watcherPR}, &out)
	if err == nil || out.Len() != 0 || !strings.Contains(err.Error(), "runtime ID") {
		t.Fatalf("unexpected result: %v %q", err, out.String())
	}
}

func TestRunCLIWatcherRetiredRegistrationIsMigrationError(t *testing.T) {
	t.Setenv("CODEX_HOME", t.TempDir())
	t.Setenv("CODEX_THREAD_ID", watcherParent)
	var out, diagnostics bytes.Buffer
	if code := RunCLI(context.Background(), []string{"watcher", "register"}, strings.NewReader(""), &out, &diagnostics); code != 2 {
		t.Fatalf("retired watcher action exit code: %d", code)
	}
	if out.Len() != 0 || !strings.Contains(diagnostics.String(), "Legacy watcher registration commands are retired") {
		t.Fatalf("unexpected retired action output: %q %q", out.String(), diagnostics.String())
	}
}

func TestRunWatcherCLIUsesCancellation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("managed POSIX CLI")
	}
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	t.Setenv("CODEX_THREAD_ID", watcherParent)
	var out bytes.Buffer
	if err := RunWatcherCLI(context.Background(), []string{"start", "--pr", watcherPR}, &out); err != nil {
		t.Fatal(err)
	}
	var started prwatch.StartResult
	if err := json.Unmarshal(out.Bytes(), &started); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := RunWatcherCLI(ctx, []string{"advance", "--ticket-file", started.TicketFile}, &out); err == nil {
		t.Fatal("unbound advance unexpectedly succeeded")
	}
}
