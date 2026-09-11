package main

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/baranwang/goldilocks/internal/prwatch"
)

type HookEvent = prwatch.RuntimeEvent

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
	event, err := prwatch.DecodeRuntime(input)
	if err != nil {
		return err
	}
	switch event.Name {
	case "SessionStart", "SubagentStart":
		var observationErr error
		if event.Name == "SubagentStart" {
			observationErr = observeManagedRuntime(event)
		}
		body, err := routingContext(root)
		if err != nil {
			return err
		}
		result := map[string]any{
			"hookSpecificOutput": map[string]string{
				"hookEventName": event.Name, "additionalContext": body,
			},
		}
		if observationErr != nil {
			result["systemMessage"] = "Goldilocks PR watcher observation failed: " + observationErr.Error()
		}
		return json.NewEncoder(output).Encode(result)
	case "Stop", "SubagentStop":
		decision, err := managedStopDecision(event)
		if err != nil {
			return err
		}
		if decision == nil {
			return nil
		}
		return json.NewEncoder(output).Encode(decision)
	case "PostToolUse":
		if err := observeManagedRuntime(event); err != nil {
			return json.NewEncoder(output).Encode(map[string]string{
				"systemMessage": "Goldilocks PR watcher observation failed: " + err.Error(),
			})
		}
		return nil
	default:
		return nil
	}
}

func managedStopDecision(event HookEvent) (*prwatch.HookDecision, error) {
	return managedStopDecisionOn(event, runtime.GOOS)
}

func managedStopDecisionOn(event HookEvent, goos string) (*prwatch.HookDecision, error) {
	if goos == "windows" {
		return nil, nil
	}
	root, err := prwatch.DefaultControllerRoot()
	if err != nil {
		return nil, err
	}
	if _, err := os.Stat(root); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	controller, err := prwatch.NewController(root, nil)
	if err != nil {
		return nil, err
	}
	return controller.StopDecision(event)
}

func observeManagedRuntime(event HookEvent) error {
	return observeManagedRuntimeOn(event, runtime.GOOS)
}

func observeManagedRuntimeOn(event HookEvent, goos string) error {
	if event.Name == "PostToolUse" && event.ToolName != "mcp__codex_app__send_message_to_thread" {
		return nil
	}
	root, err := prwatch.DefaultControllerRoot()
	if err != nil {
		return err
	}
	if _, err := os.Stat(root); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	managed, err := prwatch.RuntimeManaged(root, event)
	if err != nil || !managed {
		return err
	}
	if goos == "windows" {
		return prwatch.ErrUnsupportedPlatform
	}
	controller, err := prwatch.NewController(root, nil)
	if err != nil {
		return err
	}
	return controller.ObserveRuntime(event)
}
