package prwatch

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

type HookDecision struct {
	Decision      string `json:"decision,omitempty"`
	Reason        string `json:"reason,omitempty"`
	SystemMessage string `json:"systemMessage,omitempty"`
}

type Status struct {
	WatchID          string       `json:"watch_id"`
	Stage            Stage        `json:"stage"`
	Ready            bool         `json:"ready"`
	Activity         string       `json:"activity"`
	Worker           WorkerStatus `json:"worker"`
	AgentID          string       `json:"agent_id"`
	PendingEventID   string       `json:"pending_event_id"`
	FailureCode      string       `json:"failure_code"`
	FailureDetail    string       `json:"failure_detail"`
	CleanupConfirmed bool         `json:"cleanup_confirmed"`
}

type stopMarker struct {
	WatchID string `json:"watch_id"`
}

func writeStopMarker(path, watchID string) ([]byte, error) {
	if watchID == "" {
		raw := []byte("stop\n")
		return raw, os.WriteFile(path, raw, 0600)
	}
	raw, err := encodeJSON(stopMarker{WatchID: watchID})
	if err != nil {
		return nil, err
	}
	if err := writeJSON0600(path, stopMarker{WatchID: watchID}); err != nil {
		return nil, err
	}
	return append(raw, '\n'), nil
}

func readStopMarker(path string) (watchID string, legacy, present bool, raw []byte, err error) {
	raw, err = os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", false, false, nil, nil
	}
	if err != nil {
		return "", false, false, nil, err
	}
	var marker stopMarker
	if decodeJSON(raw, &marker) == nil && validUUID(marker.WatchID) {
		return marker.WatchID, false, true, raw, nil
	}
	return "", true, true, raw, nil
}

func removeStopMarkerIf(path string, expected []byte) error {
	current, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !bytes.Equal(current, expected) {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return errors.New("managed stop marker is a symlink")
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func stopMarkerPending(s *Store) (bool, error) {
	watchID, legacy, present, raw, err := readStopMarker(s.StopPath)
	if err != nil || !present {
		return present, err
	}
	if s.Data.Control != nil && !legacy && watchID != s.Data.Control.WatchID {
		return false, removeStopMarkerIf(s.StopPath, raw)
	}
	if s.Data.Control != nil && legacy && len(bytes.TrimSpace(raw)) == 0 {
		return false, removeStopMarkerIf(s.StopPath, raw)
	}
	return true, nil
}

func (c *Controller) StopDecision(event RuntimeEvent) (*HookDecision, error) {
	if !validUUID(event.SessionID) {
		return nil, errors.New("stop session_id must be a UUID")
	}
	switch event.Name {
	case "SubagentStop":
		if !validUUID(event.AgentID) {
			return nil, errors.New("stop agent_id must be a UUID")
		}
		return c.childStopDecision(event.SessionID, event.AgentID)
	case "Stop":
		return c.parentStopDecision(event.SessionID)
	default:
		return nil, nil
	}
}

func (c *Controller) childStopDecision(parentID, agentID string) (*HookDecision, error) {
	paths, err := c.managedStatePaths(parentID)
	if err != nil {
		return nil, err
	}
	for _, path := range paths {
		state, err := ReadState(path)
		if err != nil {
			continue
		}
		control := state.Control
		if control == nil || control.ParentID != parentID || control.AgentID != agentID {
			continue
		}
		store, err := c.Open(parentID, state.PRURL)
		if err != nil {
			return nil, err
		}
		exhausted := false
		err = store.Update(func(tx *Store) error {
			control := tx.Data.Control
			if control.ParentID != parentID || control.AgentID != agentID {
				return errors.New("managed child binding changed")
			}
			if control.Stage == NeedsAttention {
				return nil
			}
			if control.LastStopProgress != control.Progress {
				control.NoProgressStops = 0
			}
			control.LastStopProgress = control.Progress
			if control.NoProgressStops >= 2 {
				control.Stage = NeedsAttention
				control.FailureCode = "no_progress"
				control.FailureDetail = "No measurable progress after two continuations"
				control.FaultSeen = false
				if control.Worker.ID != "" {
					control.Worker.Ended = true
				}
				exhausted = true
			} else {
				control.NoProgressStops++
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		if exhausted || store.Data.Control.Stage == NeedsAttention {
			return &HookDecision{SystemMessage: "Goldilocks PR watcher needs attention: " + store.Data.Control.FailureCode}, nil
		}
		return &HookDecision{Decision: "block", Reason: c.childRecoveryReason(store)}, nil
	}
	return nil, nil
}

func (c *Controller) parentStopDecision(parentID string) (*HookDecision, error) {
	paths, err := c.managedStatePaths(parentID)
	if err != nil {
		return nil, err
	}
	var warning string
	var blockReason string
	for _, path := range paths {
		state, err := ReadState(path)
		if err != nil {
			warning = "Goldilocks could not read managed PR state " + path + ": " + err.Error()
			continue
		}
		control := state.Control
		if control == nil || control.ParentID != parentID {
			continue
		}
		store, err := c.Open(parentID, state.PRURL)
		if err != nil {
			return nil, err
		}
		block := false
		exhausted := false
		reason := ""
		err = store.Update(func(tx *Store) error {
			control := tx.Data.Control
			if control.AgentID == "" && control.Stage == Starting && c.Now().UTC().After(control.BindAfter.Add(bindLifetime)) {
				control.Stage = NeedsAttention
				control.FailureCode = "startup_expired"
				control.FailureDetail = "Watcher child did not bind within ten minutes"
				control.FaultSeen = false
				if control.Worker.ID != "" {
					control.Worker.Ended = true
				}
			}
			block = (control.Stage == Starting || control.Stage == Initializing) && !control.Ready ||
				control.Stage == NeedsAttention && !control.FaultSeen
			if !block {
				return nil
			}
			if control.ParentStopProgress != control.Progress {
				control.ParentNoProgressStops = 0
			}
			control.ParentStopProgress = control.Progress
			if control.ParentNoProgressStops >= 2 {
				control.Stage = NeedsAttention
				control.FailureCode = "no_progress"
				control.FailureDetail = "No measurable progress after two continuations"
				control.FaultSeen = false
				if control.Worker.ID != "" {
					control.Worker.Ended = true
				}
				exhausted = true
				block = false
				return nil
			}
			control.ParentNoProgressStops++
			reason = c.parentRecoveryReason(tx.Data)
			return nil
		})
		if err != nil {
			return nil, err
		}
		if block && blockReason == "" {
			blockReason = reason
		}
		if exhausted {
			warning = "Goldilocks PR watcher needs attention: no progress after two continuations"
		}
	}
	if blockReason != "" {
		return &HookDecision{Decision: "block", Reason: blockReason, SystemMessage: warning}, nil
	}
	if warning != "" {
		return &HookDecision{SystemMessage: warning}, nil
	}
	return nil, nil
}

func (c *Controller) Status(parentID, prURL string) (Status, error) {
	store, err := c.Open(parentID, prURL)
	if err != nil {
		return Status{}, err
	}
	worker := WorkerStatus{}
	release, lockErr := store.Lock(false)
	if errors.Is(lockErr, ErrLocked) {
		worker.Locked = true
	} else if lockErr != nil {
		return Status{}, lockErr
	} else {
		err = store.Update(func(tx *Store) error {
			control := tx.Data.Control
			if control.ParentID != parentID {
				return errors.New("managed state identity mismatch")
			}
			if control.AgentID == "" && control.Stage == Starting && c.Now().UTC().After(control.BindAfter.Add(bindLifetime)) {
				control.Stage = NeedsAttention
				control.FailureCode = "startup_expired"
				control.FailureDetail = "Watcher child did not bind within ten minutes"
				control.FaultSeen = false
			}
			if control.Worker.ID != "" && !control.Worker.Ended {
				control.Worker.Ended = true
			}
			if control.Stage == NeedsAttention {
				control.FaultSeen = true
			}
			return nil
		})
		releaseErr := release()
		if err != nil {
			return Status{}, err
		}
		if releaseErr != nil {
			return Status{}, releaseErr
		}
	}
	state, err := ReadState(store.Path)
	if err != nil {
		return Status{}, err
	}
	if state.Control == nil || state.Control.ParentID != parentID {
		return Status{}, errors.New("managed state identity mismatch")
	}
	store.Data = state
	control := state.Control
	worker.ExecutionID = control.Worker.ID
	if worker.Locked && !control.Worker.HeartbeatAt.IsZero() {
		worker.Fresh = !c.Now().After(control.Worker.HeartbeatAt.Add(90 * time.Second))
	}
	activity := "idle"
	switch {
	case control.AgentID == "" && control.Stage == Starting:
		activity = "awaiting_binding"
	case hasPending(store) || control.Outbox != nil:
		activity = "delivery_pending"
	case worker.Locked && worker.Fresh:
		activity = "polling"
	case worker.Locked:
		activity = "unknown"
	}
	return Status{
		WatchID: control.WatchID, Stage: control.Stage, Ready: control.Ready,
		Activity: activity, Worker: worker, AgentID: control.AgentID,
		PendingEventID: pendingEventID(state), FailureCode: control.FailureCode,
		FailureDetail:    control.FailureDetail,
		CleanupConfirmed: !worker.Locked && (control.Worker.ID == "" || control.Worker.Ended),
	}, nil
}

func (c *Controller) Stop(parentID, prURL string) error {
	store, err := c.Open(parentID, prURL)
	if err != nil {
		return err
	}
	state, err := ReadState(store.Path)
	if err != nil {
		return err
	}
	if state.Control == nil || state.Control.ParentID != parentID {
		return errors.New("managed state identity mismatch")
	}
	if err := c.rejectSymlinkedFile(store.StopPath); err != nil {
		return err
	}
	watchID := state.Control.WatchID
	markerRaw, err := writeStopMarker(store.StopPath, watchID)
	if err != nil {
		return err
	}
	return managedUpdate(store, func(tx *Store) error {
		if tx.Data.Control == nil || tx.Data.Control.ParentID != parentID {
			return errors.New("managed state identity mismatch")
		}
		if tx.Data.Control.WatchID != watchID {
			return removeStopMarkerIf(store.StopPath, markerRaw)
		}
		tx.Data.Control.Stage = Stopping
		return nil
	})
}

func (c *Controller) Resume(parentID, prURL string, replace bool) (StartResult, error) {
	store, err := c.Open(parentID, prURL)
	if err != nil {
		return StartResult{}, err
	}
	release, err := store.Lock(false)
	if err != nil {
		return StartResult{}, err
	}
	defer release()

	state := store.Data
	if state.Control == nil || state.Control.ParentID != parentID {
		return StartResult{}, errors.New("managed state identity mismatch")
	}
	control := state.Control
	ticketFile := ticketPath(store.Path)
	stopPending, err := stopMarkerPending(store)
	if err != nil {
		return StartResult{}, err
	}
	rotate := replace || control.AgentID == "" && control.Stage == NeedsAttention
	spawn := rotate || stopPending && control.AgentID == ""
	if !rotate {
		ticket, stateFile, err := c.readManagedTicket(ticketFile)
		if err != nil || stateFile != store.Path || !ticketMatches(state, ticket) {
			if err == nil {
				err = errors.New("managed watch ticket does not match state")
			}
			return StartResult{}, fmt.Errorf("verified child ticket: %w; use replace", err)
		}
	}
	if control.AgentID == "" && control.Stage == Starting && !stopPending && !replace {
		return startResult(store, ticketFile, false), nil
	}

	var ticket Ticket
	if rotate {
		ticket, err = newTicket(control.WatchID, parentID, state.PRURL)
		if err != nil {
			return StartResult{}, err
		}
		if err := writeJSON0600(ticketFile, ticket); err != nil {
			return StartResult{}, err
		}
	}
	err = store.Update(func(tx *Store) error {
		control := tx.Data.Control
		if control.ParentID != parentID {
			return errors.New("managed state identity mismatch")
		}
		if rotate {
			control.TicketSHA256 = digest(ticket.Nonce)
			control.AgentID = ""
			control.BindAfter = c.Now().UTC()
		} else if stopPending && control.AgentID == "" {
			control.BindAfter = c.Now().UTC()
		}
		if stopPending {
			control.Stage = Stopping
		} else if rotate {
			control.Stage = Starting
		} else {
			control.Stage = Initializing
		}
		control.Ready = false
		control.LastStopProgress = control.Progress
		control.NoProgressStops = 0
		control.ParentStopProgress = control.Progress
		control.ParentNoProgressStops = 0
		control.FailureCode = ""
		control.FailureDetail = ""
		control.FaultSeen = false
		if control.Worker.ID != "" {
			control.Worker.Ended = true
		}
		return nil
	})
	if err != nil {
		return StartResult{}, err
	}
	return startResult(store, ticketFile, spawn), nil
}

func (c *Controller) managedStatePaths(parentID string) ([]string, error) {
	dir := c.parentDir(parentID)
	if err := c.validateManagedDir(dir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		if err := c.rejectSymlinkedFile(path); err != nil {
			return nil, err
		}
		paths = append(paths, path)
	}
	return paths, nil
}

func (c *Controller) childRecoveryReason(store *Store) string {
	control := store.Data.Control
	launcher := resolvedLauncher()
	ticket := ticketPath(store.Path)
	advance := managedCommand(launcher, "controller", "advance", "--ticket", ticket, "--agent-id", control.AgentID)
	switch {
	case hasPending(store) || control.Outbox != nil:
		return "Run " + advance + " to deliver the saved pending event."
	case store.Data.Finished || control.Stage == Stopping:
		return "Run " + advance + " to verify terminal cleanup."
	case control.Worker.HostHandle != "" && !control.Worker.Ended:
		return "Wait on the saved execution handle " + control.Worker.HostHandle + "."
	case control.Worker.ID != "" && !control.Worker.Ended:
		yield := managedCommand(launcher, "controller", "yield", "--ticket", ticket, "--agent-id", control.AgentID)
		return "Run " + yield + " for the lost execution, then wait for its worker lock to be released."
	default:
		return "Run " + advance + " for the saved watcher ticket."
	}
}

func (c *Controller) parentRecoveryReason(state State) string {
	control := state.Control
	launcher := resolvedLauncher()
	status := managedCommand(launcher, "controller", "status", "--parent-id", control.ParentID, "--pr", state.PRURL)
	if control.Stage == NeedsAttention {
		return "Run " + status + " and report the saved PR watcher failure and recovery details before stopping."
	}
	resume := managedCommand(launcher, "controller", "resume", "--parent-id", control.ParentID, "--pr", state.PRURL)
	return "Run " + status + " to check PR watcher startup; if it is still incomplete, run " + resume + "."
}

func pendingEventID(state State) string {
	if !hasPendingState(state) {
		return ""
	}
	fields, err := objectFields(state.Pending)
	if err != nil {
		return ""
	}
	var event Event
	if decodeJSON(fields["event"], &event) != nil {
		return ""
	}
	return event.EventID
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func managedCommand(launcher string, args ...string) string {
	quoted := []string{shellQuote(launcher)}
	for _, arg := range args {
		quoted = append(quoted, shellQuote(arg))
	}
	return strings.Join(quoted, " ")
}

func resolvedLauncher() string {
	name := "goldilocks.sh"
	if runtime.GOOS == "windows" {
		name = "goldilocks.ps1"
	}
	if root := os.Getenv("PLUGIN_ROOT"); root != "" {
		candidate := filepath.Join(root, "scripts", name)
		if resolved, err := filepath.EvalSymlinks(candidate); err == nil {
			return resolved
		}
		if absolute, err := filepath.Abs(candidate); err == nil {
			return absolute
		}
	}
	executable, err := os.Executable()
	if err != nil {
		return "goldilocks"
	}
	if resolved, err := filepath.EvalSymlinks(executable); err == nil {
		return resolved
	}
	return executable
}
