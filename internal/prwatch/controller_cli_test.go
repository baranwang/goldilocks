package prwatch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestControllerCLIRequiresRuntimeIdentity(t *testing.T) {
	c, err := NewController(t.TempDir(), func() time.Time { return specTime })
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err = c.RunCLI(context.Background(), []string{"start", "--pr", specPR}, "", t.TempDir(), &out)
	if err == nil || out.Len() != 0 {
		t.Fatal("missing runtime ID accepted")
	}
}

func TestControllerCLIStartDoesNotClaimRunning(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("managed POSIX CLI")
	}
	c, err := NewController(t.TempDir(), func() time.Time { return specTime })
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err = c.RunCLI(context.Background(), []string{"start", "--pr", specPR}, specParent, t.TempDir(), &out)
	if err != nil {
		t.Fatal(err)
	}
	var r StartResult
	if err := json.Unmarshal(out.Bytes(), &r); err != nil {
		t.Fatal(err)
	}
	if !r.Spawn || r.TicketFile == "" {
		t.Fatal(r)
	}
	out.Reset()
	err = c.RunCLI(context.Background(), []string{"status", "--pr", specPR}, specParent, t.TempDir(), &out)
	if err != nil {
		t.Fatal(err)
	}
	var status Status
	if err := json.Unmarshal(out.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.Ready || status.Stage != Starting {
		t.Fatal(status)
	}
}

func TestControllerCLIRetiresLegacyActions(t *testing.T) {
	for _, action := range []string{"register", "checkpoint", "finish", "fail"} {
		t.Run(action, func(t *testing.T) {
			c, err := NewController(t.TempDir(), func() time.Time { return specTime })
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			err = c.RunCLI(context.Background(), []string{action}, specParent, t.TempDir(), &out)
			if err == nil || err.Error() != legacyWatcherMessage || out.Len() != 0 {
				t.Fatalf("legacy action was not retired: %v %q", err, out.String())
			}
		})
	}
}

func TestControllerCLIImportsLegacyState(t *testing.T) {
	source := filepath.Join(t.TempDir(), "legacy.json")
	raw, err := os.ReadFile(filepath.Join("testdata", "v2-pending.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, raw, 0600); err != nil {
		t.Fatal(err)
	}
	c, err := NewController(t.TempDir(), func() time.Time { return specTime })
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err = c.RunCLI(context.Background(), []string{"import", "--pr", specPR, "--state-file", source}, specParent, t.TempDir(), &out)
	if err != nil {
		t.Fatal(err)
	}
	var result StartResult
	if err := json.Unmarshal(out.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if !result.Spawn || result.TicketFile == "" || result.StateFile == "" {
		t.Fatalf("unexpected import result: %#v", result)
	}
}

func TestControllerCLICancelledContextStopsProtocolAction(t *testing.T) {
	c, err := NewController(t.TempDir(), func() time.Time { return specTime })
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out bytes.Buffer
	err = c.RunCLI(ctx, []string{"start", "--pr", specPR}, specParent, t.TempDir(), &out)
	if !errors.Is(err, context.Canceled) || out.Len() != 0 {
		t.Fatalf("cancelled action was not stopped: %v %q", err, out.String())
	}
}

func TestControllerCLIRejectsInvalidActionFlags(t *testing.T) {
	for _, args := range [][]string{
		{"status", "--pr", specPR, "positional"},
		{"status", "--pr", specPR, "--interval", "1"},
		{"start", "--pr", specPR, "--interval", "0"},
		{"start", "--pr", specPR, "--quiet-seconds", "-1"},
		{"start", "--pr", specPR, "--quiet-seconds", "nan"},
		{"start", "--pr", specPR, "--quiet-seconds", "inf"},
		{"received", "--pr", specPR, "--event-id", "event"},
		{"received", "--pr", specPR, "--event-id", "event", "--part", "0"},
		{"advance", "--ticket-file", filepath.Join(t.TempDir(), "ticket"), "--inspect", "false"},
		{"start", "--pr", specPR, "--session-id", specParent},
	} {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			c, err := NewController(t.TempDir(), func() time.Time { return specTime })
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			if err := c.RunCLI(context.Background(), args, specParent, t.TempDir(), &out); err == nil || out.Len() != 0 {
				t.Fatalf("invalid action accepted: %v %q", err, out.String())
			}
		})
	}
}

func TestControllerCLIResumePreservesAndReplacesBinding(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("managed POSIX CLI")
	}
	c, err := NewController(t.TempDir(), func() time.Time { return specTime })
	if err != nil {
		t.Fatal(err)
	}
	start, err := c.Start(specParent, t.TempDir(), specPR, StartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.ObserveStart(specParent, specChild, specWatch); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Bind(start.TicketFile, specChild); err != nil {
		t.Fatal(err)
	}
	beforeTicket, err := os.ReadFile(start.TicketFile)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := c.RunCLI(context.Background(), []string{"resume", "--pr", specPR}, specParent, t.TempDir(), &out); err != nil {
		t.Fatal(err)
	}
	var retained StartResult
	if err := json.Unmarshal(out.Bytes(), &retained); err != nil {
		t.Fatal(err)
	}
	if retained.Spawn || retained.AgentID != specChild {
		t.Fatalf("resume unexpectedly replaced child: %+v", retained)
	}
	afterTicket, err := os.ReadFile(start.TicketFile)
	if err != nil || !bytes.Equal(beforeTicket, afterTicket) {
		t.Fatal("ordinary resume changed the verified ticket", err)
	}
	out.Reset()
	if err := c.RunCLI(context.Background(), []string{"resume", "--pr", specPR, "--replace"}, specParent, t.TempDir(), &out); err != nil {
		t.Fatal(err)
	}
	var replaced StartResult
	if err := json.Unmarshal(out.Bytes(), &replaced); err != nil {
		t.Fatal(err)
	}
	if !replaced.Spawn || replaced.AgentID != "" {
		t.Fatalf("replacement did not clear child binding: %+v", replaced)
	}
	newTicket, err := os.ReadFile(start.TicketFile)
	if err != nil || bytes.Equal(beforeTicket, newTicket) {
		t.Fatal("replacement retained the old ticket", err)
	}
	if err := os.WriteFile(start.TicketFile, beforeTicket, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Bind(start.TicketFile, specChild); err == nil {
		t.Fatal("old child rebound a replaced ticket without a new observation")
	}
}

func TestControllerCLIInspectDoesNotOfferOrAdvance(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("managed POSIX CLI")
	}
	c, start, store := boundFixture(t)
	state, err := ReadState(store.Path)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := c.RunCLI(context.Background(), []string{"advance", "--ticket-file", start.TicketFile, "--inspect"}, specParent, t.TempDir(), &out); err == nil || out.Len() != 0 {
		t.Fatal("inspect accepted the wrong bound child")
	}
	if err := c.RunCLI(context.Background(), []string{"advance", "--ticket-file", start.TicketFile, "--inspect"}, specChild, t.TempDir(), &out); err != nil {
		t.Fatal(err)
	}
	var action Action
	if err := json.Unmarshal(out.Bytes(), &action); err != nil {
		t.Fatal(err)
	}
	if action.Action != "wait" || action.Reason != "worker_released" || action.Prompt != "" || action.ThreadID != "" {
		t.Fatalf("unexpected inspect result: %+v", action)
	}
	after, err := ReadState(store.Path)
	if err != nil {
		t.Fatal(err)
	}
	if after.Control.Progress != state.Control.Progress {
		t.Fatal("inspect advanced durable progress")
	}
}

func TestControllerCLIInspectWaitsForTerminalCleanup(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("managed POSIX CLI")
	}
	c, start, store := boundFixture(t)
	if err := store.Update(func(tx *Store) error {
		tx.Data.Finished = true
		tx.Data.Control.Stage = Running
		tx.Data.Control.Worker = Execution{ID: specWatch}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	release, err := store.Lock(false)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := c.RunCLI(context.Background(), []string{"advance", "--ticket-file", start.TicketFile, "--inspect"}, specChild, t.TempDir(), &out); err != nil {
		release()
		t.Fatal(err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	var action Action
	if err := json.Unmarshal(out.Bytes(), &action); err != nil {
		t.Fatal(err)
	}
	if action.Action != "wait" || action.Reason != "worker_running" {
		t.Fatalf("inspect fabricated completion while worker was held: %+v", action)
	}
	out.Reset()
	if err := c.RunCLI(context.Background(), []string{"advance", "--ticket-file", start.TicketFile, "--inspect"}, specChild, t.TempDir(), &out); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(out.Bytes(), &action); err != nil {
		t.Fatal(err)
	}
	if action.Action != "wait" || action.Reason != "worker_released" {
		t.Fatalf("inspect fabricated completion before stage cleanup: %+v", action)
	}
}

func TestControllerCLIReopenedFinishedGenerationStartsNewWatch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("managed POSIX CLI")
	}
	c, start, store := boundFixture(t)
	if err := store.Update(func(tx *Store) error {
		tx.Data.Finished = true
		tx.Data.Control.Stage = Finished
		tx.Data.Control.Worker = Execution{ID: specWatch, Ended: true}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.StopPath, []byte("stale stop\n"), 0600); err != nil {
		t.Fatal(err)
	}
	release, err := store.Lock(false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Start(specParent, t.TempDir(), specPR, StartOptions{Reopened: true}); !errors.Is(err, ErrLocked) {
		release()
		t.Fatalf("reopened start ignored a live worker lock: %v", err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	oldTicket, err := os.ReadFile(start.TicketFile)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := c.RunCLI(context.Background(), []string{"start", "--pr", specPR, "--reopened"}, specParent, t.TempDir(), &out); err != nil {
		t.Fatal(err)
	}
	var reopened StartResult
	if err := json.Unmarshal(out.Bytes(), &reopened); err != nil {
		t.Fatal(err)
	}
	newTicket, err := os.ReadFile(reopened.TicketFile)
	if err != nil {
		t.Fatal(err)
	}
	if !reopened.Spawn || reopened.AgentID != "" || reopened.WatchID == start.WatchID || bytes.Equal(oldTicket, newTicket) {
		t.Fatalf("reopened start reused finished generation: old=%+v new=%+v", start, reopened)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(store.Path), "history", start.WatchID+".json")); err != nil {
		t.Fatalf("finished generation was not archived: %v", err)
	}
	if _, err := os.Stat(store.StopPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale stop marker survived reopened generation: %v", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(store.Path), "history", start.WatchID+".stop")); err != nil {
		t.Fatalf("old stop marker was not archived: %v", err)
	}
}

func TestControllerStopRaceWithReopenedGenerationDoesNotStopNewWatch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("managed POSIX controller")
	}
	c, start, store := boundFixture(t)
	if err := store.Update(func(tx *Store) error {
		tx.Data.Finished = true
		tx.Data.Control.Stage = Finished
		tx.Data.Control.Worker = Execution{ID: specWatch, Ended: true}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	reopenEntered := make(chan struct{})
	releaseReopen := make(chan struct{})
	c.beforeReopenCommit = func() {
		close(reopenEntered)
		<-releaseReopen
	}
	reopenedDone := make(chan struct {
		result StartResult
		err    error
	}, 1)
	go func() {
		result, err := c.Start(specParent, t.TempDir(), specPR, StartOptions{Reopened: true})
		reopenedDone <- struct {
			result StartResult
			err    error
		}{result, err}
	}()
	select {
	case <-reopenEntered:
	case <-time.After(time.Second):
		t.Fatal("reopened generation did not reach commit barrier")
	}
	stopDone := make(chan error, 1)
	go func() { stopDone <- c.Stop(specParent, specPR) }()
	markerSeen := false
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for !markerSeen {
		watchID, legacy, present, _, err := readStopMarker(store.StopPath)
		if err == nil && present && !legacy && watchID == start.WatchID {
			markerSeen = true
			break
		}
		select {
		case <-deadline.C:
			close(releaseReopen)
			<-reopenedDone
			<-stopDone
			t.Fatal("concurrent stop did not recreate the old-generation marker")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	close(releaseReopen)
	reopened := <-reopenedDone
	if reopened.err != nil {
		t.Fatal(reopened.err)
	}
	if err := <-stopDone; err != nil {
		t.Fatal(err)
	}
	state, err := ReadState(store.Path)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.result.WatchID == start.WatchID || state.Control.WatchID != reopened.result.WatchID || state.Control.Stage != Starting {
		t.Fatalf("old stop changed reopened generation: old=%s reopened=%s state=%#v", start.WatchID, reopened.result.WatchID, state.Control)
	}
	current, err := c.Open(specParent, specPR)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := stopMarkerPending(current)
	if err != nil || pending {
		t.Fatalf("stale stop marker was treated as current: pending=%v err=%v", pending, err)
	}
	if _, err := os.Stat(store.StopPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale stop marker survived reopened generation: %v", err)
	}
}

func TestControllerCLIReopenedFailurePreservesStopMarker(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("managed POSIX CLI")
	}
	c, start, store := boundFixture(t)
	if err := store.Update(func(tx *Store) error {
		tx.Data.Finished = true
		tx.Data.Control.Stage = Finished
		tx.Data.Control.Worker = Execution{}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.StopPath, []byte("stale stop\n"), 0600); err != nil {
		t.Fatal(err)
	}
	history := filepath.Join(filepath.Dir(store.Path), "history")
	if err := os.MkdirAll(history, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(history, start.WatchID+".stop"), []byte("occupied\n"), 0600); err != nil {
		t.Fatal(err)
	}
	oldTicket, err := os.ReadFile(start.TicketFile)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Start(specParent, t.TempDir(), specPR, StartOptions{Reopened: true}); err == nil {
		t.Fatal("reopened rotation ignored an occupied archive marker")
	}
	if _, err := os.Stat(store.StopPath); err != nil {
		t.Fatalf("failed reopen did not preserve stop marker: %v", err)
	}
	newTicket, err := os.ReadFile(start.TicketFile)
	if err != nil || !bytes.Equal(oldTicket, newTicket) {
		t.Fatalf("failed reopen changed the active ticket: %v", err)
	}
	state, err := ReadState(store.Path)
	if err != nil || state.Control.WatchID != start.WatchID || !state.Finished {
		t.Fatalf("failed reopen changed the active generation: state=%+v err=%v", state.Control, err)
	}
}

func TestControllerCLIChildCannotUseParentCommandTicketFlags(t *testing.T) {
	c, err := NewController(t.TempDir(), func() time.Time { return specTime })
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err = c.RunCLI(context.Background(), []string{"status", "--ticket-file", filepath.Join(t.TempDir(), "ticket")}, specChild, t.TempDir(), &out)
	if err == nil || out.Len() != 0 {
		t.Fatal("child ticket flag was accepted as a parent command")
	}
}

func TestControllerCLIChildRejectsParentPRFlag(t *testing.T) {
	if _, err := parseControllerCLI([]string{"advance", "--ticket-file", "/tmp/ticket", "--pr", specPR}); err == nil {
		t.Fatal("child advance accepted parent --pr flag")
	}
}

func TestControllerAdvancePropagatesCancellation(t *testing.T) {
	c, start, _ := boundFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := c.Advance(ctx, start.TicketFile, specChild, Dependencies{
		Read: func(ctx context.Context, _ PR, _ func() bool) (Snapshot, error) {
			return nil, ctx.Err()
		},
		Now:  func() time.Time { return specTime },
		Wait: func(ctx context.Context, _ time.Duration) error { return ctx.Err() },
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation was converted to a watcher fault: %v", err)
	}
}
