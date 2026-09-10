package prwatch_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	pw "github.com/baranwang/goldilocks/internal/prwatch"
)

func TestPRWatchCLIFlagsOfflineActionsAndExitCodes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("PR polling is not supported on Windows in this release")
	}
	binary := buildCLI(t)
	emptyPath := replaceEnv(os.Environ(), "PATH", "")
	dir := t.TempDir()
	base := []string{"--pr", testPR, "--state-dir", dir}

	code, output, diagnostic := runCLI(t, binary, emptyPath, append([]string{"status"}, base...)...)
	if code != 0 || diagnostic != "" {
		t.Fatalf("status: code=%d output=%s diagnostic=%s", code, output, diagnostic)
	}
	var status map[string]any
	decodeJSONText(t, output, &status)
	if status["poller_running"] != false || status["pending_event"] != nil {
		t.Fatalf("unexpected new status: %#v", status)
	}

	code, _, diagnostic = runCLI(t, binary, emptyPath, append([]string{"stop"}, base...)...)
	if code != 0 || diagnostic != "" {
		t.Fatalf("stop: code=%d diagnostic=%s", code, diagnostic)
	}
	code, output, diagnostic = runCLI(t, binary, emptyPath, append([]string{"watch"}, base...)...)
	if code != 0 || diagnostic != "" {
		t.Fatalf("offline stopped watch: code=%d diagnostic=%s", code, diagnostic)
	}
	var event pw.Event
	decodeJSONText(t, output, &event)
	if event.Type != "stopped" || event.PRURL != testPR {
		t.Fatalf("unexpected stopped event: %#v", event)
	}
	code, output, diagnostic = runCLI(t, binary, emptyPath, append([]string{"prepare"}, base...)...)
	if code != 0 || !strings.Contains(output, `"type":"prepared"`) || diagnostic != "" {
		t.Fatalf("prepare: code=%d output=%s diagnostic=%s", code, output, diagnostic)
	}
	code, output, diagnostic = runCLI(t, binary, emptyPath, append([]string{"message", "--part", "1"}, base...)...)
	if code != 0 || !strings.Contains(output, "Monitoring stopped.") || diagnostic != "" {
		t.Fatalf("message: code=%d output=%s diagnostic=%s", code, output, diagnostic)
	}
	code, _, diagnostic = runCLI(t, binary, emptyPath, append([]string{"ack", "--event-id", event.EventID}, base...)...)
	if code != 0 || diagnostic != "" {
		t.Fatalf("ack: code=%d diagnostic=%s", code, diagnostic)
	}
	code, output, diagnostic = runCLI(t, binary, emptyPath, append([]string{"watch"}, base...)...)
	if code != 0 || !strings.Contains(output, `"type":"finished"`) || diagnostic != "" {
		t.Fatalf("finished watch: code=%d output=%s diagnostic=%s", code, output, diagnostic)
	}

	for _, args := range [][]string{
		{"watch", "--pr", testPR, "--state-dir", t.TempDir(), "--quiet-seconds", "0"},
		{"watch", "--pr", testPR, "--state-dir", t.TempDir(), "--quiet-seconds", "-1"},
		{"watch", "--pr", testPR, "--state-dir", t.TempDir(), "--quiet-seconds", "nan"},
		{"watch", "--pr", testPR, "--state-dir", t.TempDir(), "--quiet-seconds", "inf"},
		{"watch", "--pr", testPR, "--state-dir", t.TempDir(), "--interval", "0"},
		{"message", "--pr", testPR, "--state-dir", dir},
		{"ack", "--pr", testPR, "--state-dir", dir},
		{"unknown", "--pr", testPR, "--state-dir", dir},
		{"watch", "--pr", testPR, "--state-dir", dir, "legacy-position"},
		{"message", "--pr", testPR, "--state-dir", dir, "--thread-id", "child"},
	} {
		code, _, _ = runCLI(t, binary, emptyPath, args...)
		if code != 2 {
			t.Fatalf("invalid args returned %d: %q", code, args)
		}
	}

	openDir := t.TempDir()
	code, _, diagnostic = runCLI(t, binary, emptyPath, "watch", "--pr", testPR, "--state-dir", openDir, "--interval", "0.01")
	if code != 2 || diagnostic == "" {
		t.Fatalf("missing gh: code=%d diagnostic=%q", code, diagnostic)
	}
	if _, err := os.Stat(filepath.Join(openDir, "db205ddecb4ad216.json")); err != nil {
		t.Fatalf("watch did not save state before GitHub read: %v", err)
	}

	lockedDir := t.TempDir()
	store := mustStore(t, lockedDir)
	release, err := store.Lock(true)
	check(t, err)
	code, _, _ = runCLI(t, binary, emptyPath, "prepare", "--pr", testPR, "--state-dir", lockedDir)
	check(t, release())
	if code != 3 {
		t.Fatalf("locked action returned %d", code)
	}

	invalidDir := t.TempDir()
	store = mustStore(t, invalidDir)
	release, err = store.Lock(true)
	check(t, err)
	_, err = store.Stage("initial", emptySnapshot(), pw.Changes{}, nil, nil, time.Unix(0, 0))
	check(t, err)
	check(t, release())
	invalidBody := filepath.Join(t.TempDir(), "invalid.txt")
	check(t, os.WriteFile(invalidBody, []byte{0xff}, 0600))
	code, _, diagnostic = runCLI(t, binary, emptyPath, "prepare", "--pr", testPR, "--state-dir", invalidDir, "--body-file", invalidBody)
	if code != 2 || !strings.Contains(diagnostic, "UTF-8") {
		t.Fatalf("invalid body: code=%d diagnostic=%q", code, diagnostic)
	}
}

func TestBuiltCLIWaitsAndExitsOnMergeOrCancellation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("PR polling is not supported on Windows in this release")
	}
	binary := buildCLI(t)
	fakeDir := t.TempDir()
	gh := filepath.Join(fakeDir, "gh")
	script := `#!/bin/sh
if [ "$1" = "pr" ] && [ "$2" = "view" ]; then
  if [ ! -s "$PR_WATCH_STATE" ]; then
    printf '%s\n' 'state missing before GitHub read' >&2
    exit 17
  fi
  while IFS= read -r line || [ -n "$line" ]; do printf '%s\n' "$line"; done < "$PR_WATCH_FIXTURE"
  exit 0
fi
case "$*" in
  *graphql*) printf '%s\n' '{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[],"pageInfo":{"hasNextPage":false,"endCursor":null}}}}}}' ;;
  *check-runs*) printf '%s\n' '[{"check_runs":[]}]' ;;
  *'/status?'*) printf '%s\n' '[{"statuses":[]}]' ;;
  *) printf '%s\n' '[[]]' ;;
esac
`
	check(t, os.WriteFile(gh, []byte(script), 0700))

	for _, terminal := range []string{"MERGED", "STOP"} {
		t.Run(terminal, func(t *testing.T) {
			dir := t.TempDir()
			fixture := filepath.Join(dir, "github.json")
			writeMetadata(t, fixture, "OPEN")
			pr, err := pw.ParsePR(testPR)
			check(t, err)
			env := replaceEnv(os.Environ(), "PATH", fakeDir)
			env = replaceEnv(env, "PR_WATCH_FIXTURE", fixture)
			env = replaceEnv(env, "PR_WATCH_STATE", filepath.Join(dir, pr.Key+".json"))
			base := []string{"--pr", testPR, "--state-dir", dir, "--interval", "0.05", "--quiet-seconds", "0.05"}

			code, output, diagnostic := runCLI(t, binary, env, append([]string{"watch"}, base...)...)
			if code != 0 {
				t.Fatalf("initial watch: code=%d diagnostic=%s", code, diagnostic)
			}
			var initial pw.Event
			decodeJSONText(t, output, &initial)
			code, _, diagnostic = runCLI(t, binary, env, "ack", "--pr", testPR, "--state-dir", dir, "--event-id", initial.EventID)
			if code != 0 {
				t.Fatalf("initial ack: code=%d diagnostic=%s", code, diagnostic)
			}

			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			args := append([]string{"pr-watch", "watch"}, base...)
			cmd := exec.CommandContext(ctx, binary, args...)
			cmd.Env = env
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			check(t, cmd.Start())
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			defer func() {
				if cmd.ProcessState == nil {
					_ = cmd.Process.Kill()
					<-done
				}
			}()
			select {
			case err := <-done:
				t.Fatalf("unchanged OPEN PR exited early: %v %s", err, stderr.String())
			case <-time.After(250 * time.Millisecond):
			}
			code, runningOutput, runningDiagnostic := runCLI(t, binary, env, "status", "--pr", testPR, "--state-dir", dir)
			if code != 0 || runningDiagnostic != "" {
				t.Fatalf("running status: code=%d diagnostic=%s", code, runningDiagnostic)
			}
			var running map[string]any
			decodeJSONText(t, runningOutput, &running)
			if running["poller_running"] != true {
				t.Fatalf("poller not reported running: %#v", running)
			}

			if terminal == "STOP" {
				code, _, diagnostic = runCLI(t, binary, env, "stop", "--pr", testPR, "--state-dir", dir)
				if code != 0 {
					t.Fatalf("stop: code=%d diagnostic=%s", code, diagnostic)
				}
			} else {
				writeMetadata(t, fixture, terminal)
			}
			if err := <-done; err != nil {
				t.Fatalf("watch exit: %v %s", err, stderr.String())
			}
			if ctx.Err() != nil {
				t.Fatal(ctx.Err())
			}
			var result pw.Event
			decodeJSONText(t, stdout.String(), &result)
			want := strings.ToLower(terminal)
			if terminal == "STOP" {
				want = "stopped"
			}
			if result.Type != want {
				t.Fatalf("want %s, got %#v", want, result)
			}
			code, output, diagnostic = runCLI(t, binary, env, "status", "--pr", testPR, "--state-dir", dir)
			if code != 0 || diagnostic != "" {
				t.Fatalf("final status: code=%d diagnostic=%s", code, diagnostic)
			}
			decodeJSONText(t, output, &running)
			if running["poller_running"] != false {
				t.Fatalf("poller still reported running: %#v", running)
			}
		})
	}
}

func buildCLI(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("../..")
	check(t, err)
	binary := filepath.Join(t.TempDir(), "goldilocks")
	build := exec.Command("go", "build", "-o", binary, "./cmd/goldilocks")
	build.Dir = root
	output, err := build.CombinedOutput()
	if err != nil {
		t.Fatalf("build CLI: %v %s", err, output)
	}
	return binary
}

func runCLI(t *testing.T, binary string, env []string, args ...string) (int, string, string) {
	t.Helper()
	cmd := exec.Command(binary, append([]string{"pr-watch"}, args...)...)
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if err == nil {
		return 0, stdout.String(), stderr.String()
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		t.Fatal(err)
	}
	return exit.ExitCode(), stdout.String(), stderr.String()
}

func replaceEnv(env []string, name, value string) []string {
	prefix := name + "="
	result := make([]string, 0, len(env)+1)
	for _, entry := range env {
		if !strings.HasPrefix(entry, prefix) {
			result = append(result, entry)
		}
	}
	return append(result, prefix+value)
}

func decodeJSONText(t *testing.T, text string, destination any) {
	t.Helper()
	decoder := json.NewDecoder(strings.NewReader(text))
	decoder.UseNumber()
	check(t, decoder.Decode(destination))
}

func writeMetadata(t *testing.T, path, state string) {
	t.Helper()
	raw := fmt.Sprintf("{\"url\":%q,\"number\":7,\"state\":%q,\"isDraft\":false,\"mergeable\":\"MERGEABLE\",\"mergeStateStatus\":\"CLEAN\",\"headRefOid\":\"abc123\"}\n", testPR, state)
	temporary := path + ".tmp"
	check(t, os.WriteFile(temporary, []byte(raw), 0600))
	check(t, os.Rename(temporary, path))
}
