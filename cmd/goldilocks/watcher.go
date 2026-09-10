package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/baranwang/goldilocks/internal/prwatch"
)

type Registration struct {
	Version            int       `json:"version"`
	SessionID          string    `json:"session_id"`
	AgentID            string    `json:"agent_id"`
	PRURL              string    `json:"pr_url"`
	StateFile          string    `json:"state_file"`
	Status             string    `json:"status"`
	Phase              string    `json:"phase"`
	ExecutionHandle    string    `json:"execution_handle"`
	ExecutionEnded     bool      `json:"execution_ended"`
	Checkpoint         uint64    `json:"checkpoint"`
	LastStopCheckpoint uint64    `json:"last_stop_checkpoint"`
	NoProgressStops    int       `json:"no_progress_stops"`
	UpdatedAt          time.Time `json:"updated_at"`
	FailureReason      string    `json:"failure_reason,omitempty"`
}

type WatchState = prwatch.State

func nonNull(value json.RawMessage) bool {
	return len(value) > 0 && !bytes.Equal(bytes.TrimSpace(value), []byte("null"))
}

func registrationPath(cwd, sessionID, agentID string) string {
	sum := sha256.Sum256([]byte(sessionID + "\x00" + agentID))
	return filepath.Join(cwd, "work", "pr-watch", "agents", fmt.Sprintf("%x.json", sum))
}

func loadRegistration(path string) (Registration, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Registration{}, err
	}
	var value Registration
	if err := json.Unmarshal(raw, &value); err != nil {
		return Registration{}, err
	}
	if err := validateRegistration(value); err != nil {
		return Registration{}, err
	}
	return value, nil
}

func saveRegistration(path string, value Registration) error {
	value.UpdatedAt = time.Now().UTC()
	file, err := os.CreateTemp(filepath.Dir(path), ".registration-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if err := json.NewEncoder(file).Encode(value); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

func lockRegistration(path string) (func(), error) {
	name := path + ".lock"
	if err := os.Mkdir(name, 0700); err != nil {
		return nil, err
	}
	return func() {
		if err := os.Remove(name); err != nil {
			fmt.Fprintln(os.Stderr, "goldilocks: release lock:", err)
		}
	}, nil
}

func readWatchState(path string) (WatchState, error) {
	return prwatch.ReadState(path)
}

func validateUUID(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return false
	}
	_, err := hex.DecodeString(strings.ReplaceAll(value, "-", ""))
	return err == nil
}

func validateRegistration(value Registration) error {
	if value.Version != 1 {
		return errors.New("invalid registration version")
	}
	if !validateUUID(value.SessionID) {
		return errors.New("invalid registration session_id")
	}
	if !validateUUID(value.AgentID) {
		return errors.New("invalid registration agent_id")
	}
	pr, err := prwatch.ParsePR(value.PRURL)
	if err != nil || pr.URL != value.PRURL {
		return errors.New("invalid registration pr_url")
	}
	if !filepath.IsAbs(value.StateFile) {
		return errors.New("registration state_file must be absolute")
	}
	switch value.Status {
	case "active", "stopping", "finished", "failed", "interrupted":
	default:
		return errors.New("invalid registration status")
	}
	switch value.Phase {
	case "waiting", "delivery", "ack", "cleanup":
	default:
		return errors.New("invalid registration phase")
	}
	return nil
}

func validateIdentity(cwd, sessionID, agentID string) error {
	if !filepath.IsAbs(cwd) {
		return errors.New("watcher cwd must be absolute")
	}
	if !validateUUID(sessionID) {
		return errors.New("invalid watcher session_id")
	}
	if !validateUUID(agentID) {
		return errors.New("invalid watcher agent_id")
	}
	return nil
}

func loadRegisteredState(path, sessionID, agentID string) (Registration, WatchState, error) {
	registered, err := loadRegistration(path)
	if err != nil {
		return Registration{}, WatchState{}, err
	}
	if registered.SessionID != sessionID || registered.AgentID != agentID {
		return Registration{}, WatchState{}, errors.New("registration identity mismatch")
	}
	state, err := readWatchState(registered.StateFile)
	if err != nil {
		return Registration{}, WatchState{}, err
	}
	if state.PRURL != registered.PRURL {
		return Registration{}, WatchState{}, errors.New("registration PR does not match watcher state")
	}
	return registered, state, nil
}

func Register(cwd string, value Registration, resume bool) error {
	if err := validateIdentity(cwd, value.SessionID, value.AgentID); err != nil {
		return err
	}
	if err := validateRegistration(value); err != nil {
		return err
	}
	if value.Status != "active" {
		return errors.New("new registration must be active")
	}
	state, err := readWatchState(value.StateFile)
	if err != nil {
		return err
	}
	if state.PRURL != value.PRURL {
		return errors.New("registration PR does not match watcher state")
	}
	path := registrationPath(cwd, value.SessionID, value.AgentID)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	existing, err := loadRegistration(path)
	if err == nil {
		if existing.SessionID != value.SessionID || existing.AgentID != value.AgentID ||
			existing.PRURL != value.PRURL || existing.StateFile != value.StateFile {
			return errors.New("registration already exists with different identity")
		}
		state, err = readWatchState(existing.StateFile)
		if err != nil {
			return err
		}
		if state.PRURL != existing.PRURL {
			return errors.New("registration PR does not match watcher state")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	unlock, err := lockRegistration(path)
	if err != nil {
		return err
	}
	defer unlock()
	state, err = readWatchState(value.StateFile)
	if err != nil {
		return err
	}
	if state.PRURL != value.PRURL {
		return errors.New("registration PR does not match watcher state")
	}
	existing, err = loadRegistration(path)
	if errors.Is(err, os.ErrNotExist) {
		return saveRegistration(path, value)
	}
	if err != nil {
		return err
	}
	if existing.SessionID != value.SessionID || existing.AgentID != value.AgentID ||
		existing.PRURL != value.PRURL || existing.StateFile != value.StateFile {
		return errors.New("registration already exists with different identity")
	}
	state, err = readWatchState(existing.StateFile)
	if err != nil {
		return err
	}
	if state.PRURL != existing.PRURL {
		return errors.New("registration PR does not match watcher state")
	}
	switch existing.Status {
	case "active":
		return nil
	case "failed", "interrupted":
		if !resume {
			return errors.New("registration requires explicit resume")
		}
		existing.Status = "active"
		existing.Phase = value.Phase
		existing.ExecutionHandle = value.ExecutionHandle
		existing.ExecutionEnded = false
		existing.FailureReason = ""
		existing.Checkpoint++
		return saveRegistration(path, existing)
	case "finished":
		return errors.New("finished registration cannot be restarted")
	default:
		return errors.New("stopping registration cannot be restarted")
	}
}

func Checkpoint(cwd, sessionID, agentID, phase, handle string, stopping bool) error {
	if err := validateIdentity(cwd, sessionID, agentID); err != nil {
		return err
	}
	path := registrationPath(cwd, sessionID, agentID)
	if _, _, err := loadRegisteredState(path, sessionID, agentID); err != nil {
		return err
	}
	unlock, err := lockRegistration(path)
	if err != nil {
		return err
	}
	defer unlock()
	registered, _, err := loadRegisteredState(path, sessionID, agentID)
	if err != nil {
		return err
	}
	if registered.Status != "active" && registered.Status != "stopping" {
		return errors.New("terminal registration cannot checkpoint")
	}
	registered.Phase = phase
	registered.ExecutionHandle = handle
	registered.Checkpoint++
	if stopping {
		registered.Status = "stopping"
	}
	if err := validateRegistration(registered); err != nil {
		return err
	}
	return saveRegistration(path, registered)
}

func Finish(cwd, sessionID, agentID string, executionEnded bool) error {
	if err := validateIdentity(cwd, sessionID, agentID); err != nil {
		return err
	}
	path := registrationPath(cwd, sessionID, agentID)
	if _, _, err := loadRegisteredState(path, sessionID, agentID); err != nil {
		return err
	}
	unlock, err := lockRegistration(path)
	if err != nil {
		return err
	}
	defer unlock()
	registered, state, err := loadRegisteredState(path, sessionID, agentID)
	if err != nil {
		return err
	}
	if registered.Status != "active" && registered.Status != "stopping" && registered.Status != "finished" {
		return errors.New("failed registration cannot finish")
	}
	if !executionEnded || !state.Finished || nonNull(state.Pending) || state.Collecting != nil {
		return errors.New("finish requires terminal acknowledgement and confirmed execution cleanup")
	}
	registered.Status = "finished"
	registered.Phase = "cleanup"
	registered.ExecutionEnded = true
	return saveRegistration(path, registered)
}

func Fail(cwd, sessionID, agentID, reason string, executionEnded bool) error {
	if err := validateIdentity(cwd, sessionID, agentID); err != nil {
		return err
	}
	if strings.TrimSpace(reason) == "" {
		return errors.New("failure reason is required")
	}
	path := registrationPath(cwd, sessionID, agentID)
	if _, _, err := loadRegisteredState(path, sessionID, agentID); err != nil {
		return err
	}
	unlock, err := lockRegistration(path)
	if err != nil {
		return err
	}
	defer unlock()
	registered, _, err := loadRegisteredState(path, sessionID, agentID)
	if err != nil {
		return err
	}
	if registered.Status == "finished" {
		return errors.New("finished registration cannot fail")
	}
	registered.Status = "failed"
	registered.FailureReason = reason
	registered.ExecutionEnded = executionEnded
	return saveRegistration(path, registered)
}

func CheckStop(event HookEvent) (map[string]any, error) {
	if err := validateIdentity(event.Cwd, event.SessionID, event.AgentID); err != nil {
		return nil, err
	}
	path := registrationPath(event.Cwd, event.SessionID, event.AgentID)
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	if _, _, err := loadRegisteredState(path, event.SessionID, event.AgentID); err != nil {
		return nil, err
	}
	unlock, err := lockRegistration(path)
	if err != nil {
		return nil, err
	}
	defer unlock()
	registered, state, err := loadRegisteredState(path, event.SessionID, event.AgentID)
	if err != nil {
		return nil, err
	}
	if registered.Status != "active" && registered.Status != "stopping" {
		return nil, nil
	}
	if registered.LastStopCheckpoint != registered.Checkpoint {
		registered.NoProgressStops = 0
	}
	registered.LastStopCheckpoint = registered.Checkpoint
	if registered.NoProgressStops >= 2 {
		registered.Status = "interrupted"
		registered.FailureReason = "watcher made no progress across two hook continuations"
		if err := saveRegistration(path, registered); err != nil {
			return nil, err
		}
		return map[string]any{"systemMessage": "Goldilocks PR watcher interrupted: no progress after two continuations. Pending evidence is preserved."}, nil
	}
	registered.NoProgressStops++
	if err := saveRegistration(path, registered); err != nil {
		return nil, err
	}
	reason := "Resume this watcher SOP with its actual saved execution handle; do not start a duplicate poller."
	switch {
	case state.Finished:
		reason = "Terminal event is acknowledged. Verify the original execution has ended, then finish the registration. Do not poll GitHub."
	case registered.Status == "stopping":
		reason = "Complete the requested stop: preserve and deliver pending evidence, acknowledge it, and verify execution cleanup. Do not restart polling."
	case nonNull(state.Pending):
		reason = "Deliver the saved pending event with its existing manifest, acknowledge only after every part succeeds, then continue the watcher SOP."
	}
	return map[string]any{"decision": "block", "reason": reason}, nil
}

func RunWatcherCLI(args []string, output io.Writer) error {
	if len(args) == 0 {
		return errors.New("missing watcher action")
	}
	action := args[0]
	flags := flag.NewFlagSet(action, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	cwd := flags.String("cwd", "", "original task cwd")
	session := flags.String("session-id", "", "parent UUID")
	agent := flags.String("agent-id", "", "child UUID")
	pr := flags.String("pr", "", "canonical PR URL")
	stateFile := flags.String("state-file", "", "absolute helper state file")
	phase := flags.String("phase", "waiting", "watcher phase")
	handle := flags.String("handle", "", "actual execution handle, empty if none")
	resume := flags.Bool("resume", false, "explicitly resume failed/interrupted registration")
	stopping := flags.Bool("stopping", false, "only complete stop delivery and cleanup")
	ended := flags.Bool("execution-ended", false, "execution termination was verified")
	reason := flags.String("reason", "", "failure diagnosis")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected watcher arguments")
	}
	var err error
	switch action {
	case "register":
		err = Register(*cwd, Registration{Version: 1, SessionID: *session, AgentID: *agent,
			PRURL: *pr, StateFile: *stateFile, Status: "active", Phase: *phase,
			ExecutionHandle: *handle, Checkpoint: 1}, *resume)
	case "checkpoint":
		err = Checkpoint(*cwd, *session, *agent, *phase, *handle, *stopping)
	case "finish":
		err = Finish(*cwd, *session, *agent, *ended)
	case "fail":
		if strings.TrimSpace(*reason) == "" {
			return errors.New("failure reason is required")
		}
		err = Fail(*cwd, *session, *agent, *reason, *ended)
	default:
		return errors.New("unknown watcher action: " + action)
	}
	if err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(map[string]string{"type": action + "_ok"})
}
