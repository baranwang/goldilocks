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
	"strconv"
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

func TestBuiltWatcherControllerStartAndStatus(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("managed POSIX CLI")
	}
	binary := buildCLI(t)
	home := t.TempDir()
	env := replaceEnv(os.Environ(), "CODEX_HOME", home)
	env = replaceEnv(env, "CODEX_THREAD_ID", "00000000-0000-4000-8000-000000000001")
	run := func(args ...string) (int, string, string) {
		t.Helper()
		cmd := exec.Command(binary, append([]string{"watcher"}, args...)...)
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
	code, output, diagnostic := run("start", "--pr", testPR)
	if code != 0 || diagnostic != "" {
		t.Fatalf("managed start: code=%d output=%s diagnostic=%s", code, output, diagnostic)
	}
	var started pw.StartResult
	decodeJSONText(t, output, &started)
	if !started.Spawn || started.TicketFile == "" || started.StateFile == "" {
		t.Fatalf("unexpected start result: %+v", started)
	}
	code, output, diagnostic = run("status", "--pr", testPR)
	if code != 0 || diagnostic != "" {
		t.Fatalf("managed status: code=%d output=%s diagnostic=%s", code, output, diagnostic)
	}
	var status pw.Status
	decodeJSONText(t, output, &status)
	if status.Ready || status.Stage != pw.Starting || status.Activity != "awaiting_binding" {
		t.Fatalf("start was reported active: %+v", status)
	}
}

func TestBuiltWatcherControllerConcurrentStartAndReceiptLifecycle(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("managed POSIX CLI")
	}
	binary := buildCLI(t)
	parentID := "00000000-0000-4000-8000-000000000021"
	childID := "00000000-0000-4000-8000-000000000022"
	turnID := "00000000-0000-4000-8000-000000000023"
	home := t.TempDir()
	fakeDir := t.TempDir()
	graphqlFile := filepath.Join(fakeDir, "threads.json")
	metaCount := filepath.Join(fakeDir, "metadata-count")
	nodes := make([]map[string]any, 16)
	for i := range nodes {
		label := fmt.Sprintf("thread-body-%02d", i+1)
		body := label + " " + strings.Repeat("unresolved review evidence ", 120)
		nodes[i] = map[string]any{
			"id":         fmt.Sprintf("THREAD-%02d", i+1),
			"isResolved": false,
			"isOutdated": false,
			"comments": map[string]any{
				"nodes": []any{map[string]any{
					"id": fmt.Sprintf("COMMENT-%02d", i+1), "body": body,
					"author": map[string]any{"login": "reviewer"},
				}},
				"pageInfo": map[string]any{"hasNextPage": false, "endCursor": nil},
			},
		}
	}
	graphqlRaw, err := json.Marshal(map[string]any{"data": map[string]any{
		"repository": map[string]any{"pullRequest": map[string]any{
			"reviewThreads": map[string]any{
				"nodes": nodes, "pageInfo": map[string]any{"hasNextPage": false, "endCursor": nil},
			},
		}},
	}})
	check(t, err)
	check(t, os.WriteFile(graphqlFile, graphqlRaw, 0600))
	gh := filepath.Join(fakeDir, "gh")
	script := `#!/bin/sh
if [ "$1" = "pr" ] && [ "$2" = "view" ]; then
  count=$(cat "$PR_WATCH_META_COUNT" 2>/dev/null || echo 0)
  count=$((count + 1))
  printf '%s' "$count" > "$PR_WATCH_META_COUNT"
  head=abc123
  if [ "$count" -gt 1 ]; then head=def456; fi
  printf '%s\n' "{\"url\":\"https://github.com/example/project/pull/7\",\"number\":7,\"state\":\"OPEN\",\"isDraft\":false,\"mergeable\":\"MERGEABLE\",\"mergeStateStatus\":\"CLEAN\",\"headRefOid\":\"$head\"}"
  exit 0
fi
case "$*" in
  *graphql*) cat "$PR_WATCH_GRAPHQL" ;;
  *check-runs*) printf '%s\n' '[{"check_runs":[]}]' ;;
  *'/status?'*) printf '%s\n' '[{"statuses":[]}]' ;;
  *) printf '%s\n' '[[]]' ;;
esac
`
	check(t, os.WriteFile(gh, []byte(script), 0700))
	env := replaceEnv(os.Environ(), "CODEX_HOME", home)
	env = replaceEnv(env, "CODEX_THREAD_ID", parentID)
	env = replaceEnv(env, "PATH", fakeDir+":/bin:/usr/bin")
	env = replaceEnv(env, "PR_WATCH_GRAPHQL", graphqlFile)
	env = replaceEnv(env, "PR_WATCH_META_COUNT", metaCount)
	pluginRoot := t.TempDir()
	check(t, os.MkdirAll(filepath.Join(pluginRoot, "skills", "model-routing"), 0700))
	check(t, os.WriteFile(filepath.Join(pluginRoot, "skills", "model-routing", "SKILL.md"), []byte("# Router"), 0600))
	env = replaceEnv(env, "PLUGIN_ROOT", pluginRoot)

	type result struct {
		code, errCode   int
		out, diagnostic string
	}
	start := func() result {
		cmd := exec.Command(binary, "watcher", "start", "--pr", testPR, "--interval", "0.01", "--quiet-seconds", "0.01")
		cmd.Env = env
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		code := 0
		if err != nil {
			var exit *exec.ExitError
			if !errors.As(err, &exit) {
				t.Fatalf("concurrent start: %v", err)
			}
			code = exit.ExitCode()
		}
		return result{code: code, out: stdout.String(), diagnostic: stderr.String()}
	}
	gate := make(chan struct{})
	results := make(chan result, 2)
	go func() { <-gate; results <- start() }()
	go func() { <-gate; results <- start() }()
	close(gate)
	first, second := <-results, <-results
	if first.code != 0 || second.code != 0 || first.diagnostic != "" || second.diagnostic != "" {
		t.Fatalf("concurrent starts failed: %#v %#v", first, second)
	}
	var started, duplicate pw.StartResult
	decodeJSONText(t, first.out, &started)
	decodeJSONText(t, second.out, &duplicate)
	if started.WatchID == "" || started.WatchID != duplicate.WatchID || started.TicketFile == "" || started.TicketFile != duplicate.TicketFile || started.StateFile != duplicate.StateFile {
		t.Fatalf("concurrent starts diverged: %+v %+v", started, duplicate)
	}

	run := func(runEnv []string, args ...string) result {
		t.Helper()
		cmd := exec.Command(binary, args...)
		cmd.Env = runEnv
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		code := 0
		if err != nil {
			var exit *exec.ExitError
			if !errors.As(err, &exit) {
				t.Fatal(err)
			}
			code = exit.ExitCode()
		}
		return result{code: code, out: stdout.String(), diagnostic: stderr.String()}
	}
	statusResult := run(env, "watcher", "status", "--pr", testPR)
	if statusResult.code != 0 || statusResult.diagnostic != "" {
		t.Fatalf("startup status: %#v", statusResult)
	}
	var status pw.Status
	decodeJSONText(t, statusResult.out, &status)
	if status.Ready || status.Stage != pw.Starting || status.Activity != "awaiting_binding" {
		t.Fatalf("startup passed before child binding: %+v", status)
	}

	childEnv := replaceEnv(env, "CODEX_THREAD_ID", childID)
	startEvent, err := json.Marshal(map[string]any{
		"hook_event_name": "SubagentStart", "session_id": parentID, "agent_id": childID, "turn_id": turnID,
	})
	check(t, err)
	hook := func(input []byte) result {
		cmd := exec.Command(binary, "hook")
		cmd.Env = env
		cmd.Stdin = bytes.NewReader(input)
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		code := 0
		if err != nil {
			var exit *exec.ExitError
			if !errors.As(err, &exit) {
				t.Fatal(err)
			}
			code = exit.ExitCode()
		}
		return result{code: code, out: stdout.String(), diagnostic: stderr.String()}
	}
	if got := hook(startEvent); got.code != 0 || got.diagnostic != "" {
		t.Fatalf("subagent start hook: %#v", got)
	}

	advance := func() (pw.Action, result) {
		got := run(childEnv, "watcher", "advance", "--ticket-file", started.TicketFile)
		if got.code != 0 || got.diagnostic != "" {
			t.Fatalf("advance: %#v", got)
		}
		var action pw.Action
		decodeJSONText(t, got.out, &action)
		return action, got
	}
	receipt := func(action pw.Action, call int) {
		input, err := json.Marshal(map[string]any{"threadId": parentID, "prompt": action.Prompt})
		check(t, err)
		response, err := json.Marshal(map[string]any{"isError": false, "content": []any{map[string]any{
			"type": "text", "text": fmt.Sprintf(`{"threadId":%q}`, parentID),
		}}})
		check(t, err)
		event, err := json.Marshal(map[string]any{
			"hook_event_name": "PostToolUse", "agent_id": childID,
			"tool_name": "mcp__codex_app__send_message_to_thread", "tool_use_id": "receipt-" + strconv.Itoa(call),
			"tool_input": json.RawMessage(input), "tool_response": json.RawMessage(response),
		})
		check(t, err)
		got := hook(event)
		if got.code != 0 || got.diagnostic != "" {
			t.Fatalf("post-tool receipt: %#v", got)
		}
	}

	action, _ := advance()
	if action.Action != "send" || action.Parts < 2 || action.EventID == "" {
		t.Fatalf("initial advance did not offer multipart event: %+v", action)
	}
	initialEvent := action.EventID
	initialParts := action.Parts
	var prompts []string
	for part := 1; part <= initialParts; part++ {
		if action.Action != "send" || action.EventID != initialEvent || action.Part != part {
			t.Fatalf("initial part %d was not offered in order: %+v", part, action)
		}
		prompts = append(prompts, action.Prompt)
		receipt(action, part)
		if part < initialParts {
			action, _ = advance()
		}
	}
	statusResult = run(env, "watcher", "status", "--pr", testPR)
	if statusResult.code != 0 || statusResult.diagnostic != "" {
		t.Fatalf("pre-readiness status: %#v", statusResult)
	}
	decodeJSONText(t, statusResult.out, &status)
	if status.Ready {
		t.Fatalf("startup became ready before the next poll: %+v", status)
	}
	action, _ = advance()
	if action.Action != "send" || action.EventID == "" || action.EventID == initialEvent {
		t.Fatalf("next poll did not produce a new event: %+v", action)
	}
	allPrompts := strings.Join(prompts, "\n")
	for i := 1; i <= 16; i++ {
		if !strings.Contains(allPrompts, fmt.Sprintf("thread-body-%02d", i)) {
			t.Fatalf("multipart frozen messages lost unresolved thread body %02d", i)
		}
	}
	statusResult = run(env, "watcher", "status", "--pr", testPR)
	if statusResult.code != 0 || statusResult.diagnostic != "" {
		t.Fatalf("ready status: %#v", statusResult)
	}
	decodeJSONText(t, statusResult.out, &status)
	if !status.Ready || status.Stage != pw.Running {
		t.Fatalf("watcher did not become ready after all parts and a next poll: %+v", status)
	}
	receipt(action, initialParts+1)
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
