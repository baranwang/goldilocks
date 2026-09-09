package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
