package main

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type HookEvent struct {
	Name       string `json:"hook_event_name"`
	Cwd        string `json:"cwd"`
	SessionID  string `json:"session_id"`
	AgentID    string `json:"agent_id"`
	AgentType  string `json:"agent_type"`
	StopActive bool   `json:"stop_hook_active"`
}

func routingContext(root string) (string, error) {
	if root == "" {
		return "", errors.New("PLUGIN_ROOT is missing")
	}
	raw, err := os.ReadFile(filepath.Join(root, "skills", "model-routing", "SKILL.md"))
	if err != nil {
		return "", err
	}
	text := strings.TrimPrefix(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\ufeff")
	if strings.HasPrefix(text, "---\n") {
		_, rest, ok := strings.Cut(text[4:], "\n---\n")
		if !ok {
			return "", errors.New("unclosed skill frontmatter")
		}
		text = rest
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return "", errors.New("empty routing policy")
	}
	return text, nil
}

func RunHook(input io.Reader, output io.Writer, root string) error {
	raw, err := io.ReadAll(io.LimitReader(input, (1<<20)+1))
	if err != nil {
		return err
	}
	if len(raw) > 1<<20 {
		return errors.New("hook input exceeds 1 MiB")
	}
	var event HookEvent
	if err := json.Unmarshal(raw, &event); err != nil {
		return err
	}
	switch event.Name {
	case "SessionStart", "SubagentStart":
		body, err := routingContext(root)
		if err != nil {
			return err
		}
		return json.NewEncoder(output).Encode(map[string]any{
			"hookSpecificOutput": map[string]string{
				"hookEventName": event.Name, "additionalContext": body,
			},
		})
	default:
		return nil
	}
}
