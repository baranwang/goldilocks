package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/baranwang/goldilocks/internal/prwatch"
)

const runtimeParent = "00000000-0000-4000-8000-000000000011"
const runtimeChild = "00000000-0000-4000-8000-000000000012"
const runtimeTurn = "00000000-0000-4000-8000-000000000013"

func routingFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "skills", "model-routing")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("# Router"), 0600); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestRoutingInjection(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "skills", "model-routing")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	body := "# Model Routing\n\n保留 \"quotes\"、\\ 和换行。"
	raw := "\ufeff---\r\nname: model-routing\r\n---\r\n\r\n" + body + "\r\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"SessionStart", "SubagentStart"} {
		var out bytes.Buffer
		input := strings.NewReader(fmt.Sprintf(`{"hook_event_name":%q}`, name))
		if err := RunHook(input, &out, root); err != nil {
			t.Fatal(err)
		}
		var got struct {
			HookSpecificOutput struct{ HookEventName, AdditionalContext string }
		}
		if err := json.Unmarshal(out.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if got.HookSpecificOutput.HookEventName != name || got.HookSpecificOutput.AdditionalContext != body {
			t.Fatalf("unexpected injection: %s", out.String())
		}
	}
}

func TestRoutingInvalidInput(t *testing.T) {
	for _, tc := range []struct {
		name, skill, input string
		missing            bool
	}{
		{"frontmatter", "---\nname: broken\n", `{"hook_event_name":"SessionStart"}`, false},
		{"empty", " \n", `{"hook_event_name":"SessionStart"}`, false},
		{"missing", "", `{"hook_event_name":"SessionStart"}`, true},
		{"json", "# Router", `{`, false},
		{"trailing", "# Router", `{"hook_event_name":"SessionStart"} {}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, "skills", "model-routing")
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			if !tc.missing {
				if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(tc.skill), 0600); err != nil {
					t.Fatal(err)
				}
			}
			var out bytes.Buffer
			if err := RunHook(strings.NewReader(tc.input), &out, root); err == nil || out.Len() != 0 {
				t.Fatalf("invalid input must diagnose without injection: %v %q", err, out.String())
			}
		})
	}
}

func TestSubagentStartRecordsMembershipAndKeepsRouting(t *testing.T) {
	if testing.Short() {
		t.Skip("disk-backed controller")
	}
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	controllerRoot := filepath.Join(home, "goldilocks", "pr-watch")
	c, err := prwatch.NewController(controllerRoot, time.Now)
	if err != nil {
		t.Skipf("managed controller unsupported: %v", err)
	}
	start, err := c.Start(runtimeParent, t.TempDir(), "https://github.com/example/project/pull/17", prwatch.StartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	event := HookEvent{Name: "SubagentStart", SessionID: runtimeParent, AgentID: runtimeChild, TurnID: runtimeTurn}
	raw, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := RunHook(bytes.NewReader(raw), &out, routingFixture(t)); err != nil {
		t.Fatal(err)
	}
	var got struct {
		HookSpecificOutput struct{ AdditionalContext string }
		SystemMessage      string `json:"systemMessage"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.HookSpecificOutput.AdditionalContext != "# Router" || got.SystemMessage != "" {
		t.Fatalf("routing composition failed: %s", out.String())
	}
	if _, err := c.Bind(start.TicketFile, runtimeChild); err != nil {
		t.Fatalf("membership was not recorded before routing: %v", err)
	}
}

func TestManagedObservationFailureIsFailOpen(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	controllerRoot := filepath.Join(home, "goldilocks", "pr-watch")
	c, err := prwatch.NewController(controllerRoot, time.Now)
	if err != nil {
		t.Skipf("managed controller unsupported: %v", err)
	}
	if _, err := c.Start(runtimeParent, t.TempDir(), "https://github.com/example/project/pull/17", prwatch.StartOptions{}); err != nil {
		t.Fatal(err)
	}
	event := HookEvent{Name: "SubagentStart", SessionID: runtimeParent, AgentID: "invalid", TurnID: runtimeTurn}
	raw, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := RunHook(bytes.NewReader(raw), &out, routingFixture(t)); err != nil {
		t.Fatal(err)
	}
	var got struct {
		HookSpecificOutput struct{ AdditionalContext string }
		SystemMessage      string `json:"systemMessage"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.HookSpecificOutput.AdditionalContext != "# Router" || !strings.Contains(got.SystemMessage, "observation failed") {
		t.Fatalf("managed failure discarded routing or diagnostic: %s", out.String())
	}
}

func TestWindowsManagedDiagnosticRequiresMatchingIntent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	c, err := prwatch.NewController(filepath.Join(home, "goldilocks", "pr-watch"), time.Now)
	if err != nil {
		t.Skipf("fixture requires managed controller: %v", err)
	}
	start, err := c.Start(runtimeParent, t.TempDir(), "https://github.com/example/project/pull/17", prwatch.StartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	unrelated := HookEvent{
		Name: "SubagentStart", SessionID: "00000000-0000-4000-8000-000000000014",
		AgentID: runtimeChild, TurnID: runtimeTurn,
	}
	if err := observeManagedRuntimeOn(unrelated, "windows"); err != nil {
		t.Fatalf("unrelated Windows lifecycle event diagnosed: %v", err)
	}
	managed := unrelated
	managed.SessionID = runtimeParent
	if err := observeManagedRuntimeOn(managed, "windows"); !errors.Is(err, prwatch.ErrUnsupportedPlatform) {
		t.Fatalf("managed Windows lifecycle event returned %v", err)
	}
	if err := c.ObserveStart(runtimeParent, runtimeChild, runtimeTurn); err != nil {
		t.Fatal(err)
	}
	store, err := c.Bind(start.TicketFile, runtimeChild)
	if err != nil {
		t.Fatal(err)
	}
	tool := HookEvent{
		Name: "PostToolUse", AgentID: runtimeChild,
		ToolName: "mcp__codex_app__send_message_to_thread", ToolUseID: "call-1",
		ToolInput: json.RawMessage(`{"threadId":"00000000-0000-4000-8000-000000000014","prompt":"marker"}`),
	}
	if err := observeManagedRuntimeOn(tool, "windows"); err != nil {
		t.Fatalf("unrelated Windows tool event diagnosed: %v", err)
	}
	tool.ToolInput = json.RawMessage(`{"threadId":"` + runtimeParent + `","prompt":"marker"}`)
	if err := observeManagedRuntimeOn(tool, "windows"); err != nil {
		t.Fatalf("unoffered Windows tool event diagnosed: %v", err)
	}
	var action prwatch.Action
	if err := store.Update(func(tx *prwatch.Store) error {
		snapshot := prwatch.Snapshot{
			"head_sha": "abc123", "state": "OPEN", "draft": false,
			"mergeable": "MERGEABLE", "merge_state": "CLEAN", "checks": []any{},
			"comments": map[string]any{}, "reviews": map[string]any{}, "threads": map[string]any{},
		}
		if _, err := tx.Stage("initial", snapshot, prwatch.Changes{}, nil, nil, time.Now()); err != nil {
			return err
		}
		var err error
		action, err = tx.Offer(time.Now())
		return err
	}); err != nil {
		t.Fatal(err)
	}
	tool.ToolInput = json.RawMessage(`{"threadId":"` + runtimeParent + `","prompt":` + mustJSON(t, action.Prompt) + `}`)
	if err := observeManagedRuntimeOn(tool, "windows"); !errors.Is(err, prwatch.ErrUnsupportedPlatform) {
		t.Fatalf("managed Windows tool event returned %v", err)
	}
}

func TestWindowsStopHookIsInertWhenControllerRootExists(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	if err := os.MkdirAll(filepath.Join(home, "goldilocks", "pr-watch"), 0700); err != nil {
		t.Fatal(err)
	}
	decision, err := managedStopDecisionOn(HookEvent{Name: "Stop", SessionID: runtimeParent}, "windows")
	if err != nil || decision != nil {
		t.Fatal("unsupported Windows stop hook was not inert", decision, err)
	}
}

func mustJSON(t *testing.T, value string) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestUnrelatedRuntimeEventDoesNotCreateControllerState(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	event := HookEvent{Name: "PostToolUse", ToolName: "mcp__codex_app__read_thread"}
	raw, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := RunHook(bytes.NewReader(raw), &out, ""); err != nil || out.Len() != 0 {
		t.Fatalf("unrelated event was handled: %v %q", err, out.String())
	}
	if _, err := os.Stat(filepath.Join(home, "goldilocks")); !os.IsNotExist(err) {
		t.Fatalf("unrelated event created controller state: %v", err)
	}
}

func TestHookManifestComposesManagedRuntimeHooksWithExistingLaunchers(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "hooks", "hooks.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Hooks map[string][]struct {
			Matcher string `json:"matcher"`
			Hooks   []struct {
				Command, CommandWindows, StatusMessage string
				Timeout                                int
			}
		}
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	post := manifest.Hooks["PostToolUse"]
	start := manifest.Hooks["SubagentStart"]
	stop := manifest.Hooks["Stop"]
	childStop := manifest.Hooks["SubagentStop"]
	if len(post) != 1 || len(post[0].Hooks) != 1 || post[0].Matcher != "mcp__codex_app__send_message_to_thread" ||
		len(start) != 1 || len(start[0].Hooks) != 1 || post[0].Hooks[0].Command != start[0].Hooks[0].Command ||
		post[0].Hooks[0].CommandWindows != start[0].Hooks[0].CommandWindows || post[0].Hooks[0].Timeout != 150 ||
		post[0].Hooks[0].StatusMessage != "Recording PR watcher delivery" || len(stop) != 1 || len(stop[0].Hooks) != 1 ||
		len(childStop) != 1 || len(childStop[0].Hooks) != 1 || stop[0].Hooks[0].Command != childStop[0].Hooks[0].Command ||
		stop[0].Hooks[0].CommandWindows != childStop[0].Hooks[0].CommandWindows || stop[0].Hooks[0].Timeout != childStop[0].Hooks[0].Timeout ||
		stop[0].Hooks[0].StatusMessage != "Checking PR watcher startup" {
		t.Fatalf("unexpected managed runtime hooks: post=%+v stop=%+v", post, stop)
	}
}

func TestVersionDoesNotNeedTools(t *testing.T) {
	t.Setenv("PATH", "")
	var out, diagnostic bytes.Buffer
	code := RunCLI(context.Background(), []string{"--version"}, strings.NewReader(""), &out, &diagnostic)
	if code != 0 || strings.TrimSpace(out.String()) != version || diagnostic.Len() != 0 {
		t.Fatal(code, out.String(), diagnostic.String())
	}
	out.Reset()
	if RunCLI(context.Background(), []string{"unknown"}, strings.NewReader(""), &out, &diagnostic) != 2 {
		t.Fatal("unknown action must fail")
	}
}

func TestWatcherDeadlineUsesCancellationExitCode(t *testing.T) {
	t.Setenv("CODEX_HOME", t.TempDir())
	t.Setenv("CODEX_THREAD_ID", runtimeParent)
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	var out, diagnostics bytes.Buffer
	if code := RunCLI(ctx, []string{"watcher", "start", "--pr", "https://github.com/example/project/pull/17"}, strings.NewReader(""), &out, &diagnostics); code != 130 {
		t.Fatalf("deadline watcher exit code: %d (%q)", code, diagnostics.String())
	}
	if out.Len() != 0 || !strings.Contains(diagnostics.String(), "context deadline exceeded") {
		t.Fatalf("unexpected deadline output: %q %q", out.String(), diagnostics.String())
	}
}
