package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	testSessionID = "00000000-0000-4000-8000-000000000001"
	testAgentID   = "00000000-0000-4000-8000-000000000002"
	testPRURL     = "https://github.com/example/project/pull/7"
)

func setupRegistration(t *testing.T, cwd string) HookEvent {
	t.Helper()
	state := filepath.Join(cwd, "watch.json")
	raw := `{"version":3,"pr_url":"https://github.com/example/project/pull/7","snapshot":null,"pending":null,"collecting":null,"finished":false,"error":null,"last_ack":null}`
	if err := os.WriteFile(state, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	event := HookEvent{Name: "SubagentStop", Cwd: cwd, SessionID: testSessionID, AgentID: testAgentID}
	value := Registration{Version: 1, SessionID: event.SessionID, AgentID: event.AgentID,
		PRURL: testPRURL, StateFile: state, Status: "active", Phase: "waiting",
		ExecutionHandle: "exec:7", Checkpoint: 1}
	if err := Register(cwd, value, false); err != nil {
		t.Fatal(err)
	}
	return event
}

func TestStopBudgetResetsOnlyAfterCheckpoint(t *testing.T) {
	event := setupRegistration(t, t.TempDir())
	for i := 0; i < 2; i++ {
		event.StopActive = i > 0
		decision, err := CheckStop(event)
		if err != nil || decision["decision"] != "block" {
			t.Fatalf("%v %v", decision, err)
		}
		if _, exists := decision["continue"]; exists {
			t.Fatal("must not override other hooks")
		}
	}
	if err := Checkpoint(event.Cwd, event.SessionID, event.AgentID, "waiting", "exec:7", false); err != nil {
		t.Fatal(err)
	}
	decision, err := CheckStop(event)
	if err != nil || decision["decision"] != "block" {
		t.Fatalf("progress must renew budget: %v %v", decision, err)
	}
	if _, err := CheckStop(event); err != nil {
		t.Fatal(err)
	}
	decision, err = CheckStop(event)
	if err != nil || decision["decision"] == "block" || decision["systemMessage"] == nil {
		t.Fatalf("no progress must become interrupted: %v %v", decision, err)
	}
}

func TestStopReasonsFollowPersistedState(t *testing.T) {
	tests := []struct {
		name, status, old, replacement, want string
	}{
		{"pending", "active", `"pending":null`, `"pending":` + validPendingJSON(testPRURL), "Deliver the saved pending event"},
		{"stopping", "stopping", "", "", "Complete the requested stop"},
		{"finished", "active", `"finished":false`, `"finished":true`, "Terminal event is acknowledged"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			event := setupRegistration(t, t.TempDir())
			path := registrationPath(event.Cwd, event.SessionID, event.AgentID)
			reg, err := loadRegistration(path)
			if err != nil {
				t.Fatal(err)
			}
			if tc.status != "active" {
				reg.Status = tc.status
				if err := saveRegistration(path, reg); err != nil {
					t.Fatal(err)
				}
			}
			if tc.replacement != "" {
				raw, err := os.ReadFile(reg.StateFile)
				if err != nil {
					t.Fatal(err)
				}
				raw = bytes.Replace(raw, []byte(tc.old), []byte(tc.replacement), 1)
				if err := os.WriteFile(reg.StateFile, raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			decision, err := CheckStop(event)
			if err != nil || decision["decision"] != "block" || !strings.Contains(decision["reason"].(string), tc.want) {
				t.Fatalf("%v %v", decision, err)
			}
		})
	}
}

func TestUnregisteredChildIsUnaffected(t *testing.T) {
	cwd := t.TempDir()
	event := HookEvent{Name: "SubagentStop", Cwd: cwd, SessionID: testSessionID, AgentID: testAgentID}
	decision, err := CheckStop(event)
	if err != nil || decision != nil {
		t.Fatalf("%v %v", decision, err)
	}
	if _, err := os.Stat(filepath.Join(cwd, "work")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	event = setupRegistration(t, cwd)
	event.SessionID = "00000000-0000-4000-8000-000000000003"
	decision, err = CheckStop(event)
	if err != nil || decision != nil {
		t.Fatalf("wrong parent matched: %v %v", decision, err)
	}
}

func TestRegistrationValidationAndResume(t *testing.T) {
	cwd := t.TempDir()
	state := filepath.Join(cwd, "watch.json")
	if err := os.WriteFile(state, []byte(`{"version":3,"pr_url":"https://github.com/example/project/pull/7","snapshot":null,"pending":null,"collecting":null,"finished":false,"error":null,"last_ack":null}`), 0600); err != nil {
		t.Fatal(err)
	}
	valid := Registration{Version: 1, SessionID: testSessionID, AgentID: testAgentID, PRURL: testPRURL,
		StateFile: state, Status: "active", Phase: "waiting", ExecutionHandle: "exec:7", Checkpoint: 1}
	for name, mutate := range map[string]func(*Registration){
		"version": func(v *Registration) { v.Version = 2 },
		"session": func(v *Registration) { v.SessionID = "bad" },
		"agent":   func(v *Registration) { v.AgentID = "" },
		"pr":      func(v *Registration) { v.PRURL += "/" },
		"state":   func(v *Registration) { v.StateFile = "watch.json" },
		"status":  func(v *Registration) { v.Status = "unknown" },
		"phase":   func(v *Registration) { v.Phase = "unknown" },
	} {
		t.Run(name, func(t *testing.T) {
			value := valid
			mutate(&value)
			if err := Register(cwd, value, false); err == nil {
				t.Fatal("accepted invalid registration")
			}
		})
	}
	if _, err := os.Stat(filepath.Join(cwd, "work")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("invalid registration created storage", err)
	}
	if err := Register(cwd, valid, false); err != nil {
		t.Fatal(err)
	}
	event := HookEvent{Name: "SubagentStop", Cwd: cwd, SessionID: valid.SessionID, AgentID: valid.AgentID}
	if _, err := CheckStop(event); err != nil {
		t.Fatal(err)
	}
	if err := Register(cwd, valid, false); err != nil {
		t.Fatal("active registration must be idempotent", err)
	}
	path := registrationPath(cwd, valid.SessionID, valid.AgentID)
	registered, err := loadRegistration(path)
	if err != nil || registered.NoProgressStops != 1 {
		t.Fatalf("idempotent register reset progress: %+v %v", registered, err)
	}
	if err := Fail(cwd, valid.SessionID, valid.AgentID, "lost", false); err != nil {
		t.Fatal(err)
	}
	if err := Register(cwd, valid, false); err == nil {
		t.Fatal("failed registration resumed implicitly")
	}
	if err := Register(cwd, valid, true); err != nil {
		t.Fatal(err)
	}
	registered, err = loadRegistration(path)
	if err != nil || registered.Status != "active" || registered.FailureReason != "" || registered.Checkpoint != 2 {
		t.Fatalf("resume did not record progress: %+v %v", registered, err)
	}
}

func TestCheckpointPreservesStoppingAndRejectsTerminal(t *testing.T) {
	event := setupRegistration(t, t.TempDir())
	if err := Checkpoint(event.Cwd, event.SessionID, event.AgentID, "delivery", "exec:original", true); err != nil {
		t.Fatal(err)
	}
	if err := Checkpoint(event.Cwd, event.SessionID, event.AgentID, "ack", "exec:original", false); err != nil {
		t.Fatal(err)
	}
	reg, err := loadRegistration(registrationPath(event.Cwd, event.SessionID, event.AgentID))
	if err != nil || reg.Status != "stopping" || reg.Phase != "ack" || reg.Checkpoint != 3 || reg.ExecutionHandle != "exec:original" {
		t.Fatalf("stopping state lost: %+v %v", reg, err)
	}
	if err := Fail(event.Cwd, event.SessionID, event.AgentID, "stopped", true); err != nil {
		t.Fatal(err)
	}
	if err := Checkpoint(event.Cwd, event.SessionID, event.AgentID, "cleanup", "exec:original", false); err == nil {
		t.Fatal("terminal registration accepted checkpoint")
	}
}

func TestFinishRequiresAcknowledgementAndCleanup(t *testing.T) {
	event := setupRegistration(t, t.TempDir())
	if err := Finish(event.Cwd, event.SessionID, event.AgentID, true); err == nil {
		t.Fatal("open watch cannot finish")
	}
	if err := Fail(event.Cwd, event.SessionID, event.AgentID, "execution handle lost", false); err != nil {
		t.Fatal(err)
	}
	decision, err := CheckStop(event)
	if err != nil || decision["decision"] == "block" {
		t.Fatalf("explicit failure must exit: %v %v", decision, err)
	}
}

func TestFinishStateMatrix(t *testing.T) {
	for _, tc := range []struct{ finished, pending, collecting, ended, ok bool }{
		{false, false, false, true, false}, {true, true, false, true, false},
		{true, false, true, true, false}, {true, false, false, false, false},
		{true, false, false, true, true},
	} {
		event := setupRegistration(t, t.TempDir())
		path := registrationPath(event.Cwd, event.SessionID, event.AgentID)
		reg, err := loadRegistration(path)
		if err != nil {
			t.Fatal(err)
		}
		state := map[string]any{"version": 3, "pr_url": testPRURL, "snapshot": nil, "pending": nil,
			"collecting": nil, "finished": tc.finished, "error": nil, "last_ack": nil}
		if tc.pending {
			var pending any
			if err := json.Unmarshal([]byte(validPendingJSON(testPRURL)), &pending); err != nil {
				t.Fatal(err)
			}
			state["pending"] = pending
		}
		if tc.collecting {
			var collecting any
			if err := json.Unmarshal([]byte(validCollectingJSON()), &collecting); err != nil {
				t.Fatal(err)
			}
			state["collecting"] = collecting
		}
		raw, err := json.Marshal(state)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(reg.StateFile, raw, 0600); err != nil {
			t.Fatal(err)
		}
		if err := Finish(event.Cwd, event.SessionID, event.AgentID, tc.ended); (err == nil) != tc.ok {
			t.Fatalf("case %+v: %v", tc, err)
		}
	}
}

func TestFinishRejectsMalformedBusinessStateWithoutDeletingRegistration(t *testing.T) {
	event := setupRegistration(t, t.TempDir())
	path := registrationPath(event.Cwd, event.SessionID, event.AgentID)
	reg, err := loadRegistration(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(reg.StateFile, []byte(`{"version":3,"pr_url":"https://github.com/example/project/pull/7","snapshot":null,"pending":{},"collecting":null,"finished":true,"error":null,"last_ack":null}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Finish(event.Cwd, event.SessionID, event.AgentID, true); err == nil {
		t.Fatal("malformed pending state was accepted")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("registration was deleted", err)
	}
}

func TestCorruptRegistrationsNeverBlock(t *testing.T) {
	for _, field := range []string{"json", "version", "session_id", "agent_id", "pr_url", "missing-state", "busy-lock"} {
		t.Run(field, func(t *testing.T) {
			event := setupRegistration(t, t.TempDir())
			path := registrationPath(event.Cwd, event.SessionID, event.AgentID)
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var value map[string]any
			if err := json.Unmarshal(raw, &value); err != nil {
				t.Fatal(err)
			}
			switch field {
			case "json":
				raw = []byte("{")
			case "version":
				value[field] = 999
			case "pr_url":
				value[field] = "https://github.com/example/project/pull/8"
			case "session_id", "agent_id":
				value[field] = "00000000-0000-4000-8000-000000000009"
			case "missing-state":
				if err := os.Remove(value["state_file"].(string)); err != nil {
					t.Fatal(err)
				}
			case "busy-lock":
				unlock, err := lockRegistration(path)
				if err != nil {
					t.Fatal(err)
				}
				defer unlock()
			}
			if field != "json" {
				raw, err = json.Marshal(value)
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			decision, err := CheckStop(event)
			if decision["decision"] == "block" {
				t.Fatalf("invalid registration blocked: %v", decision)
			}
			if err == nil && decision["systemMessage"] == nil {
				t.Fatal("missing diagnostic")
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(after, raw) {
				t.Fatalf("invalid data must be preserved: %v", err)
			}
		})
	}
}

func TestRegistrationConcurrencyIsIsolated(t *testing.T) {
	event := setupRegistration(t, t.TempDir())
	firstPath := registrationPath(event.Cwd, event.SessionID, event.AgentID)
	second, err := loadRegistration(firstPath)
	if err != nil {
		t.Fatal(err)
	}
	second.AgentID = "00000000-0000-4000-8000-000000000003"
	second.PRURL = "https://github.com/example/project/pull/8"
	second.StateFile = filepath.Join(event.Cwd, "second.json")
	raw, err := os.ReadFile(filepath.Join(event.Cwd, "watch.json"))
	if err != nil {
		t.Fatal(err)
	}
	raw = bytes.ReplaceAll(raw, []byte("/pull/7"), []byte("/pull/8"))
	if err := os.WriteFile(second.StateFile, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := Register(event.Cwd, second, false); err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	for _, id := range []string{event.AgentID, second.AgentID} {
		go func(agentID string) {
			for i := 0; i < 10; i++ {
				if err := Checkpoint(event.Cwd, event.SessionID, agentID, "waiting", "exec:7", false); err != nil {
					results <- err
					return
				}
			}
			results <- nil
		}(id)
	}
	for i := 0; i < 2; i++ {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{event.AgentID, second.AgentID} {
		value, err := loadRegistration(registrationPath(event.Cwd, event.SessionID, id))
		if err != nil || value.Checkpoint != 11 || value.NoProgressStops != 0 {
			t.Fatalf("registrations leaked state: %+v %v", value, err)
		}
	}
}

func TestWatcherCLIAndHookDispatch(t *testing.T) {
	cwd := t.TempDir()
	state := filepath.Join(cwd, "watch.json")
	if err := os.WriteFile(state, []byte(`{"version":3,"pr_url":"https://github.com/example/project/pull/7","snapshot":null,"pending":null,"collecting":null,"finished":false,"error":null,"last_ack":null}`), 0600); err != nil {
		t.Fatal(err)
	}
	base := []string{"--cwd", cwd, "--session-id", testSessionID, "--agent-id", testAgentID}
	args := append([]string{"watcher", "register"}, base...)
	args = append(args, "--pr", testPRURL, "--state-file", state, "--phase", "waiting", "--handle", "exec:7")
	var out, diagnostics bytes.Buffer
	if code := RunCLI(context.Background(), args, strings.NewReader(""), &out, &diagnostics); code != 0 || !strings.Contains(out.String(), `"type":"register_ok"`) || diagnostics.Len() != 0 {
		t.Fatalf("register CLI: %d %q %q", code, out.String(), diagnostics.String())
	}
	out.Reset()
	event, err := json.Marshal(HookEvent{Name: "SubagentStop", Cwd: cwd, SessionID: testSessionID, AgentID: testAgentID})
	if err != nil {
		t.Fatal(err)
	}
	if err := RunHook(bytes.NewReader(event), &out, ""); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"decision":"block"`) {
		t.Fatalf("hook did not dispatch watcher guard: %s", out.String())
	}
	for _, bad := range [][]string{{"watcher"}, {"watcher", "unknown"}, {"watcher", "finish"}} {
		out.Reset()
		diagnostics.Reset()
		if code := RunCLI(context.Background(), bad, strings.NewReader(""), &out, &diagnostics); code != 2 || diagnostics.Len() == 0 {
			t.Fatalf("bad CLI accepted: %v %d %q", bad, code, diagnostics.String())
		}
	}
}

func validPendingJSON(prURL string) string {
	return `{"event":{"source":"pr-watch","watcher_id":"db205ddecb4ad216","event_id":"event-1","type":"update","pr_url":"` + prURL + `","observed_at":"2026-09-09T00:00:00Z","head_sha":"abc123","changes":{}},"snapshot":null,"error":null}`
}

func validCollectingJSON() string {
	return `{"snapshot":{"head_sha":"abc123","state":"OPEN","draft":false,"mergeable":"MERGEABLE","merge_state":"CLEAN","checks":[],"comments":{},"reviews":{},"threads":{}},"kind":"update","observations":[{"type":"update","observed_at":"2026-09-09T00:00:00Z","head_sha":"abc123","changes":{}}],"recovered_error":null}`
}
