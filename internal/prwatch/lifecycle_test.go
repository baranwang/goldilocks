package prwatch

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParentSeesChildThatNeverRegistered(t *testing.T) {
	c, _ := startedFixture(t)
	decision, err := c.StopDecision(RuntimeEvent{Name: "Stop", SessionID: specParent})
	if err != nil {
		t.Fatal(err)
	}
	if decision == nil || decision.Decision != "block" {
		t.Fatal("incomplete startup passed parent guard")
	}
	status, err := c.Status(specParent, specPR)
	if err != nil {
		t.Fatal(err)
	}
	if status.Ready || status.Stage == Running || status.Activity != "awaiting_binding" {
		t.Fatal("invented startup")
	}
}

func TestRepeatedChildStopsUseEvidenceBudget(t *testing.T) {
	c, _, _ := boundFixture(t)
	event := RuntimeEvent{Name: "SubagentStop", SessionID: specParent, AgentID: specChild}
	for i := 0; i < 2; i++ {
		event.StopActive = i > 0
		decision, err := c.StopDecision(event)
		if err != nil {
			t.Fatal(err)
		}
		if decision == nil || decision.Decision != "block" {
			t.Fatal("lost permitted continuation", i)
		}
		if _, err := c.Status(specParent, specPR); err != nil {
			t.Fatal(err)
		}
	}
	decision, err := c.StopDecision(event)
	if err != nil {
		t.Fatal(err)
	}
	if decision != nil && decision.Decision == "block" {
		t.Fatal("unbounded no-progress loop")
	}
	status, err := c.Status(specParent, specPR)
	if err != nil {
		t.Fatal(err)
	}
	if status.Stage != NeedsAttention {
		t.Fatal(status)
	}
}

func TestUnrelatedChildStopDoesNotChangeManagedState(t *testing.T) {
	c, _, store := boundFixture(t)
	before, err := os.ReadFile(store.Path)
	if err != nil {
		t.Fatal(err)
	}
	decision, err := c.StopDecision(RuntimeEvent{
		Name: "SubagentStop", SessionID: specParent,
		AgentID: "00000000-0000-4000-8000-000000000009",
	})
	if err != nil || decision != nil {
		t.Fatal(decision, err)
	}
	after, err := os.ReadFile(store.Path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("unrelated stop changed managed state", err)
	}
}

func TestManagedStateRejectsParentStopProgressAheadOfEvidence(t *testing.T) {
	store := managedFixture(t)
	store.Data.Control.ParentStopProgress = 1
	if err := store.Save(); err == nil {
		t.Fatal("parent stop progress ahead of durable progress was accepted")
	}
}

func TestChildBudgetResetsOnlyForAcceptedEvidence(t *testing.T) {
	c, _, store, action := deliveryFixture(t)
	event := RuntimeEvent{Name: "SubagentStop", SessionID: specParent, AgentID: specChild}
	if decision, err := c.StopDecision(event); err != nil || decision == nil || decision.Decision != "block" {
		t.Fatal(decision, err)
	}
	before, err := ReadState(store.Path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Status(specParent, specPR); err != nil {
		t.Fatal(err)
	}
	if err := store.Update(func(tx *Store) error {
		tx.Data.Control.Worker.HeartbeatAt = specTime.Add(time.Second)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if decision, err := c.StopDecision(event); err != nil || decision == nil || decision.Decision != "block" {
		t.Fatal(decision, err)
	}
	state, err := ReadState(store.Path)
	if err != nil {
		t.Fatal(err)
	}
	if state.Control.NoProgressStops != 2 || state.Control.Progress != before.Control.Progress {
		t.Fatal("status or heartbeat reset the child budget", state.Control)
	}
	if err := c.AcceptSend(ObservedSend{
		AgentID: specChild, ParentID: specParent, CallID: "accepted-progress",
		Prompt: action.Prompt, Accepted: true, ObservedAt: specTime.Add(2 * time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	if decision, err := c.StopDecision(event); err != nil || decision == nil || decision.Decision != "block" {
		t.Fatal("accepted receipt did not renew child budget", decision, err)
	}
	state, err = ReadState(store.Path)
	if err != nil || state.Control.NoProgressStops != 1 || state.Control.LastStopProgress != state.Control.Progress {
		t.Fatal(state.Control, err)
	}
}

func TestParentAndChildContinuationBudgetsAreIndependent(t *testing.T) {
	c, _, store := boundFixture(t)
	child := RuntimeEvent{Name: "SubagentStop", SessionID: specParent, AgentID: specChild}
	if _, err := c.StopDecision(child); err != nil {
		t.Fatal(err)
	}
	if _, err := c.StopDecision(RuntimeEvent{Name: "Stop", SessionID: specParent}); err != nil {
		t.Fatal(err)
	}
	state, err := ReadState(store.Path)
	if err != nil {
		t.Fatal(err)
	}
	if state.Control.NoProgressStops != 1 || state.Control.ParentNoProgressStops != 1 {
		t.Fatal("parent and child shared a continuation budget", state.Control)
	}
}

func TestParentBudgetTracksEachWatchProgressIndependently(t *testing.T) {
	c, first := startedFixture(t)
	secondPR := "https://github.com/example/project/pull/8"
	second, err := c.Start(specParent, t.TempDir(), secondPR, StartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	firstStore, err := c.Open(specParent, specPR)
	if err != nil {
		t.Fatal(err)
	}
	if err := firstStore.Update(func(tx *Store) error {
		tx.Data.Control.Progress = 4
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if decision, err := c.StopDecision(RuntimeEvent{Name: "Stop", SessionID: specParent}); err != nil || decision == nil || decision.Decision != "block" {
		t.Fatal(decision, err)
	}
	for _, item := range []struct {
		result   StartResult
		pr       string
		progress uint64
	}{{first, specPR, 4}, {second, secondPR, 0}} {
		store, err := c.Open(specParent, item.pr)
		if err != nil {
			t.Fatal(err)
		}
		state, err := ReadState(store.Path)
		if err != nil {
			t.Fatal(err)
		}
		if state.Control.WatchID != item.result.WatchID || state.Control.ParentStopProgress != item.progress || state.Control.ParentNoProgressStops != 1 {
			t.Fatal("parent budget did not visit each watch", state.Control)
		}
	}
}

func TestParentGuardIsBoundedAndHealthyReadyWatchDoesNotBlock(t *testing.T) {
	t.Run("bounded startup", func(t *testing.T) {
		c, _ := startedFixture(t)
		store, err := c.Open(specParent, specPR)
		if err != nil {
			t.Fatal(err)
		}
		event := RuntimeEvent{Name: "Stop", SessionID: specParent}
		for i := 0; i < 2; i++ {
			decision, err := c.StopDecision(event)
			if err != nil || decision == nil || decision.Decision != "block" {
				t.Fatal(decision, err)
			}
			if _, err := c.Status(specParent, specPR); err != nil {
				t.Fatal(err)
			}
			if i == 0 {
				if err := store.Update(func(tx *Store) error {
					tx.Data.Control.Worker.HeartbeatAt = specTime.Add(time.Second)
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			}
		}
		decision, err := c.StopDecision(event)
		if err != nil || decision == nil || decision.Decision == "block" || decision.SystemMessage == "" {
			t.Fatal(decision, err)
		}
		status, err := c.Status(specParent, specPR)
		if err != nil || status.Stage != NeedsAttention || status.FailureCode != "no_progress" {
			t.Fatal(status, err)
		}
	})
	t.Run("healthy ready", func(t *testing.T) {
		c, _, store := boundFixture(t)
		if err := store.Update(func(tx *Store) error {
			tx.Data.Control.InitialAccepted = true
			tx.Data.Control.InitialEventID = "initial-event"
			tx.Data.Control.Ready = true
			tx.Data.Control.Stage = Running
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		decision, err := c.StopDecision(RuntimeEvent{Name: "Stop", SessionID: specParent})
		if err != nil || decision != nil {
			t.Fatal("healthy watch kept parent alive", decision, err)
		}
		status, err := c.Status(specParent, specPR)
		if err != nil || status.Activity != "idle" {
			t.Fatal("historical running stage implied polling", status, err)
		}
		state, err := ReadState(store.Path)
		if err != nil || state.Control.ParentNoProgressStops != 0 {
			t.Fatal(state.Control, err)
		}
	})
}

func TestParentFaultInstructionUsesStatusCommandUntilReported(t *testing.T) {
	c, _, store := boundFixture(t)
	launcher := installTestLauncher(t)
	if err := store.Update(func(tx *Store) error {
		tx.Data.Control.Stage = NeedsAttention
		tx.Data.Control.FailureCode = "poll_failed"
		tx.Data.Control.FailureDetail = "saved diagnostic"
		tx.Data.Control.FaultSeen = false
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	decision, err := c.StopDecision(RuntimeEvent{Name: "Stop", SessionID: specParent})
	if err != nil || decision == nil || decision.Decision != "block" ||
		!strings.Contains(decision.Reason, shellQuote(launcher)) ||
		!strings.Contains(decision.Reason, shellQuote(specPR)) ||
		!strings.Contains(decision.Reason, "status") {
		t.Fatal("parent fault did not provide a status/report instruction", decision, err)
	}
	if _, err := c.Status(specParent, specPR); err != nil {
		t.Fatal(err)
	}
	decision, err = c.StopDecision(RuntimeEvent{Name: "Stop", SessionID: specParent})
	if err != nil || decision != nil {
		t.Fatal("reported fault kept blocking parent", decision, err)
	}
}

func TestStatusUsesWorkerLockAndReconcilesEndedExecution(t *testing.T) {
	c, _, store := boundFixture(t)
	executionID := "00000000-0000-4000-8000-000000000004"
	if err := store.Update(func(tx *Store) error {
		tx.Data.Control.Worker = Execution{ID: executionID, HeartbeatAt: specTime}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	release, err := store.Lock(false)
	if err != nil {
		t.Fatal(err)
	}
	status, err := c.Status(specParent, specPR)
	if err != nil || status.Activity != "polling" || status.CleanupConfirmed {
		t.Fatal(status, err)
	}
	c.Now = func() time.Time { return specTime.Add(91 * time.Second) }
	status, err = c.Status(specParent, specPR)
	if err != nil || status.Activity != "unknown" || status.Worker.Fresh {
		t.Fatal(status, err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	status, err = c.Status(specParent, specPR)
	if err != nil || status.Activity != "idle" || !status.CleanupConfirmed {
		t.Fatal(status, err)
	}
	state, err := ReadState(store.Path)
	if err != nil || !state.Control.Worker.Ended {
		t.Fatal("free worker lock did not reconcile saved execution", err)
	}
}

func TestStatusReportsPendingEventAndExpiresUnboundStartup(t *testing.T) {
	t.Run("pending", func(t *testing.T) {
		c, _, store := boundFixture(t)
		if err := store.Update(func(tx *Store) error {
			_, err := tx.Stage("initial", managedSnapshot(), Changes{}, nil, nil, specTime)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		status, err := c.Status(specParent, specPR)
		if err != nil || status.Activity != "delivery_pending" || status.PendingEventID == "" {
			t.Fatal(status, err)
		}
	})
	t.Run("expired startup", func(t *testing.T) {
		c, _ := startedFixture(t)
		c.Now = func() time.Time { return specTime.Add(bindLifetime + time.Nanosecond) }
		status, err := c.Status(specParent, specPR)
		if err != nil || status.Stage != NeedsAttention || status.FailureCode != "startup_expired" || status.Ready {
			t.Fatal(status, err)
		}
		store, err := c.Open(specParent, specPR)
		if err != nil {
			t.Fatal(err)
		}
		state, err := ReadState(store.Path)
		if err != nil || !state.Control.FaultSeen || state.Control.Progress != 0 {
			t.Fatal("status did not persist only the diagnostic", state.Control, err)
		}
		decision, err := c.StopDecision(RuntimeEvent{Name: "Stop", SessionID: specParent})
		if err != nil || decision != nil {
			t.Fatal("already reported startup fault blocked parent", decision, err)
		}
	})
}

func TestStopPreservesPendingAndPersistsMarker(t *testing.T) {
	c, _, store, _ := deliveryFixture(t)
	before, err := ReadState(store.Path)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Stop(specParent, specPR); err != nil {
		t.Fatal(err)
	}
	after, err := ReadState(store.Path)
	if err != nil {
		t.Fatal(err)
	}
	if after.Control.Stage != Stopping || !bytes.Equal(before.Pending, after.Pending) || after.Control.Outbox == nil {
		t.Fatal("stop discarded pending evidence", after.Control)
	}
	info, err := os.Stat(store.StopPath)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("stop marker missing or has wrong mode", err)
	}
}

func TestStopMarkerScopesCurrentGenerationAndResumePreservesIt(t *testing.T) {
	c, _, store := boundFixture(t)
	state, err := ReadState(store.Path)
	if err != nil {
		t.Fatal(err)
	}
	watchID := state.Control.WatchID
	if err := c.Stop(specParent, specPR); err != nil {
		t.Fatal(err)
	}
	markerWatchID, legacy, present, _, err := readStopMarker(store.StopPath)
	if err != nil || !present || legacy || markerWatchID != watchID {
		t.Fatalf("stop marker did not record its generation: watch_id=%q legacy=%v present=%v err=%v", markerWatchID, legacy, present, err)
	}
	if _, err := c.Resume(specParent, specPR, false); err != nil {
		t.Fatal(err)
	}
	state, err = ReadState(store.Path)
	if err != nil {
		t.Fatal(err)
	}
	markerWatchID, legacy, present, _, err = readStopMarker(store.StopPath)
	if err != nil || !present || legacy || markerWatchID != watchID || state.Control.WatchID != watchID || state.Control.Stage != Stopping {
		t.Fatalf("same-generation resume changed stop intent: state=%#v marker=%q legacy=%v present=%v err=%v", state.Control, markerWatchID, legacy, present, err)
	}
}

func TestStopRejectsSymlinkedMarkerWithoutTouchingTarget(t *testing.T) {
	c, _, store := boundFixture(t)
	target := filepath.Join(t.TempDir(), "outside-stop")
	want := []byte("outside sentinel")
	if err := os.WriteFile(target, want, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, store.StopPath); err != nil {
		t.Fatal(err)
	}
	if err := c.Stop(specParent, specPR); err == nil {
		t.Fatal("symlinked stop marker accepted")
	}
	got, err := os.ReadFile(target)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatal("symlink target changed", err)
	}
	state, err := ReadState(store.Path)
	if err != nil || state.Control.Stage == Stopping {
		t.Fatal("rejected stop changed lifecycle state", state.Control, err)
	}
}

func TestResumeRetainsVerifiedChildAndHistoricalEvidence(t *testing.T) {
	c, result, store := boundFixture(t)
	if err := store.Update(func(tx *Store) error {
		tx.Data.Control.Stage = NeedsAttention
		tx.Data.Control.Ready = true
		tx.Data.Control.InitialAccepted = true
		tx.Data.Control.InitialEventID = "initial-event"
		tx.Data.Control.FailureCode = "poll_failed"
		tx.Data.Control.FailureDetail = "saved failure"
		tx.Data.Control.FaultSeen = true
		tx.Data.Control.NoProgressStops = 2
		tx.Data.Control.ParentNoProgressStops = 2
		tx.Data.Control.History["history-event"] = Delivery{
			EventID: "history-event", Kind: "initial",
			Parts: []DeliveryPart{{SHA256: strings.Repeat("a", 64), Offers: 1}},
		}
		tx.Data.Collecting = &Batch{
			Snapshot: managedSnapshot(), Kind: "update",
			Observations: []Observation{{
				Type: "update", ObservedAt: specTime.Format(time.RFC3339Nano),
				HeadSHA: "abc123", Changes: Changes{},
			}},
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	ticketBefore, err := os.ReadFile(result.TicketFile)
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := c.Resume(specParent, specPR, false)
	if err != nil {
		t.Fatal(err)
	}
	ticketAfter, err := os.ReadFile(result.TicketFile)
	if err != nil {
		t.Fatal(err)
	}
	state, err := ReadState(store.Path)
	if err != nil {
		t.Fatal(err)
	}
	control := state.Control
	if resumed.Spawn || resumed.AgentID != specChild || !bytes.Equal(ticketBefore, ticketAfter) ||
		control.Stage != Initializing || control.Ready || !control.InitialAccepted ||
		control.NoProgressStops != 0 || control.ParentNoProgressStops != 0 ||
		control.FailureCode != "" || control.FailureDetail != "" || control.FaultSeen ||
		len(control.History) != 1 || state.Collecting == nil {
		t.Fatal("resume did not retain the verified generation", resumed, control)
	}
	terminal := managedSnapshot()
	terminal["state"] = "MERGED"
	if _, err := c.Advance(context.Background(), result.TicketFile, specChild, Dependencies{
		Read: func(context.Context, PR, func() bool) (Snapshot, error) { return terminal, nil },
		Now:  c.Now,
		Wait: func(ctx context.Context, duration time.Duration) error {
			if duration != 5*time.Second {
				return errors.New("unexpected poll wait")
			}
			<-ctx.Done()
			return ctx.Err()
		},
	}); err != nil {
		t.Fatal(err)
	}
	state, err = ReadState(store.Path)
	if err != nil || !state.Control.Ready || state.Control.Stage != Running {
		t.Fatal("real poll did not restore readiness", state.Control, err)
	}
}

func TestResumeReplacementRotatesTicketAndPreservesPending(t *testing.T) {
	c, result, store, _ := deliveryFixture(t)
	before, err := ReadState(store.Path)
	if err != nil {
		t.Fatal(err)
	}
	ticketBefore, err := os.ReadFile(result.TicketFile)
	if err != nil {
		t.Fatal(err)
	}
	now := specTime.Add(time.Hour)
	c.Now = func() time.Time { return now }
	resumed, err := c.Resume(specParent, specPR, true)
	if err != nil {
		t.Fatal(err)
	}
	ticketAfter, err := os.ReadFile(result.TicketFile)
	if err != nil {
		t.Fatal(err)
	}
	after, err := ReadState(store.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !resumed.Spawn || resumed.AgentID != "" || bytes.Equal(ticketBefore, ticketAfter) ||
		after.Control.Stage != Starting || after.Control.AgentID != "" || !after.Control.BindAfter.Equal(now) ||
		!bytes.Equal(before.Pending, after.Pending) || after.Control.Outbox == nil {
		t.Fatal("replacement lost pending state or reused the old binding", resumed, after.Control)
	}
}

func TestResumeRejectsLiveWorker(t *testing.T) {
	t.Run("live worker", func(t *testing.T) {
		c, result, store := boundFixture(t)
		release, err := store.Lock(false)
		if err != nil {
			t.Fatal(err)
		}
		defer release()
		before, err := os.ReadFile(store.Path)
		if err != nil {
			t.Fatal(err)
		}
		ticket, err := os.ReadFile(result.TicketFile)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.Resume(specParent, specPR, true); !errors.Is(err, ErrLocked) {
			t.Fatal("live worker resume was not rejected", err)
		}
		after, _ := os.ReadFile(store.Path)
		afterTicket, _ := os.ReadFile(result.TicketFile)
		if !bytes.Equal(before, after) || !bytes.Equal(ticket, afterTicket) {
			t.Fatal("rejected resume changed state")
		}
	})
}

func TestResumeUserStopAlwaysContinuesStopping(t *testing.T) {
	for _, tc := range []struct {
		name    string
		bound   bool
		replace bool
	}{
		{name: "bound keep child", bound: true},
		{name: "bound replace child", bound: true, replace: true},
		{name: "unbound keep ticket"},
		{name: "unbound replace ticket", replace: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var c *Controller
			var result StartResult
			var store *Store
			if tc.bound {
				c, result, store = boundFixture(t)
			} else {
				c, result = startedFixture(t)
				var err error
				store, err = c.Open(specParent, specPR)
				if err != nil {
					t.Fatal(err)
				}
			}
			ticketBefore, err := os.ReadFile(result.TicketFile)
			if err != nil {
				t.Fatal(err)
			}
			if !tc.bound {
				c.Now = func() time.Time { return specTime.Add(bindLifetime + time.Minute) }
			}
			if err := c.Stop(specParent, specPR); err != nil {
				t.Fatal(err)
			}
			resumed, err := c.Resume(specParent, specPR, tc.replace)
			if err != nil {
				t.Fatal(err)
			}
			wantSpawn := tc.replace || !tc.bound
			if resumed.Spawn != wantSpawn {
				t.Fatal("wrong stop-completion spawn decision", resumed)
			}
			ticketAfter, err := os.ReadFile(result.TicketFile)
			if err != nil || bytes.Equal(ticketBefore, ticketAfter) == tc.replace {
				t.Fatal("wrong stop-completion ticket decision", err)
			}
			state, err := ReadState(store.Path)
			if err != nil || state.Control.Stage != Stopping {
				t.Fatal("resume left stopping state", state.Control, err)
			}
			if _, err := os.Stat(store.StopPath); err != nil {
				t.Fatal("resume cleared the user stop", err)
			}

			agentID := state.Control.AgentID
			if agentID == "" {
				agentID = "00000000-0000-4000-8000-000000000009"
				if err := c.ObserveStart(specParent, agentID, specWatch); err != nil {
					t.Fatal(err)
				}
				if _, err := c.Bind(resumed.TicketFile, agentID); err != nil {
					t.Fatal(err)
				}
				state, err = ReadState(store.Path)
				if err != nil || state.Control.Stage != Stopping {
					t.Fatal("binding restarted a stopped watch", state.Control, err)
				}
			}
			reads := 0
			action, err := c.Advance(context.Background(), resumed.TicketFile, agentID, Dependencies{
				Read: func(context.Context, PR, func() bool) (Snapshot, error) {
					reads++
					return nil, errors.New("GitHub read after user stop")
				},
				Now: c.Now,
				Wait: func(context.Context, time.Duration) error {
					return errors.New("wait after user stop")
				},
			})
			if err != nil || action.Action != "send" || reads != 0 {
				t.Fatal("resume restarted polling instead of finishing stop", action, reads, err)
			}
		})
	}
}

func TestResumeUnboundFailureRotatesWithoutReplaceFlag(t *testing.T) {
	c, result := startedFixture(t)
	store, err := c.Open(specParent, specPR)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(func(tx *Store) error {
		tx.Data.Control.Stage = NeedsAttention
		tx.Data.Control.FailureCode = "startup_expired"
		tx.Data.Control.FailureDetail = "saved failure"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(result.TicketFile)
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := c.Resume(specParent, specPR, false)
	if err != nil || !resumed.Spawn || resumed.AgentID != "" {
		t.Fatal(resumed, err)
	}
	after, err := os.ReadFile(result.TicketFile)
	if err != nil || bytes.Equal(before, after) {
		t.Fatal("unbound failed startup reused its nonce", err)
	}
}

func TestResumeLiveUnboundStartupPreservesExistingIntent(t *testing.T) {
	c, result := startedFixture(t)
	ticketBefore, err := os.ReadFile(result.TicketFile)
	if err != nil {
		t.Fatal(err)
	}
	stateBefore, err := os.ReadFile(result.StateFile)
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := c.Resume(specParent, specPR, false)
	if err != nil {
		t.Fatal(err)
	}
	ticketAfter, err := os.ReadFile(result.TicketFile)
	if err != nil {
		t.Fatal(err)
	}
	stateAfter, err := os.ReadFile(result.StateFile)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Spawn || resumed.AgentID != "" || !bytes.Equal(ticketBefore, ticketAfter) || !bytes.Equal(stateBefore, stateAfter) {
		t.Fatal("ordinary resume replaced a live unbound startup", resumed)
	}
}

func TestTerminalAcceptedWithHeldLockRequiresCleanupAdvance(t *testing.T) {
	c, _, store := boundFixture(t)
	if err := store.Update(func(tx *Store) error {
		tx.Data.Finished = true
		tx.Data.Control.InitialAccepted = true
		tx.Data.Control.InitialEventID = "terminal-event"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	release, err := store.Lock(false)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	decision, err := c.StopDecision(RuntimeEvent{Name: "SubagentStop", SessionID: specParent, AgentID: specChild})
	if err != nil || decision == nil || decision.Decision != "block" || !strings.Contains(decision.Reason, "advance") {
		t.Fatal(decision, err)
	}
	status, err := c.Status(specParent, specPR)
	if err != nil || status.CleanupConfirmed {
		t.Fatal(status, err)
	}
}

func TestCorruptManagedStateWarnsParentButDoesNotBlockExactChild(t *testing.T) {
	c, _, store := boundFixture(t)
	if err := store.Update(func(tx *Store) error {
		tx.Data.Control.InitialAccepted = true
		tx.Data.Control.InitialEventID = "initial-event"
		tx.Data.Control.Ready = true
		tx.Data.Control.Stage = Running
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	otherPR := "https://github.com/example/project/pull/8"
	other, err := c.Start(specParent, t.TempDir(), otherPR, StartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	corrupt := []byte("{corrupt")
	if err := os.WriteFile(other.StateFile, corrupt, 0600); err != nil {
		t.Fatal(err)
	}
	child, err := c.StopDecision(RuntimeEvent{Name: "SubagentStop", SessionID: specParent, AgentID: specChild})
	if err != nil || child == nil || child.Decision != "block" {
		t.Fatal("corruption hid exact child state", child, err)
	}
	parent, err := c.StopDecision(RuntimeEvent{Name: "Stop", SessionID: specParent})
	if err != nil || parent == nil || parent.Decision == "block" || !strings.Contains(parent.SystemMessage, other.StateFile) {
		t.Fatal("corrupt matching state did not warn", parent, err)
	}
	after, err := os.ReadFile(other.StateFile)
	if err != nil || !bytes.Equal(after, corrupt) {
		t.Fatal("corrupt state was rewritten", err)
	}
}

func TestParentBlockIncludesCorruptSiblingWarning(t *testing.T) {
	c, _ := startedFixture(t)
	corruptWatch, err := c.Start(specParent, t.TempDir(), "https://github.com/example/project/pull/8", StartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	corrupt := []byte("{corrupt")
	if err := os.WriteFile(corruptWatch.StateFile, corrupt, 0600); err != nil {
		t.Fatal(err)
	}
	decision, err := c.StopDecision(RuntimeEvent{Name: "Stop", SessionID: specParent})
	if err != nil || decision == nil || decision.Decision != "block" ||
		!strings.Contains(decision.SystemMessage, corruptWatch.StateFile) {
		t.Fatal("blocking watch discarded corrupt sibling warning", decision, err)
	}
	after, err := os.ReadFile(corruptWatch.StateFile)
	if err != nil || !bytes.Equal(after, corrupt) {
		t.Fatal("corrupt sibling was rewritten", err)
	}
}

func TestRecoveryCommandsQuoteResolvedLauncherAndTicket(t *testing.T) {
	c, result, store := boundFixture(t)
	launcher := installTestLauncher(t)
	if err := store.Update(func(tx *Store) error {
		_, err := tx.Stage("initial", managedSnapshot(), Changes{}, nil, nil, specTime)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	decision, err := c.StopDecision(RuntimeEvent{Name: "SubagentStop", SessionID: specParent, AgentID: specChild})
	if err != nil || decision == nil || decision.Decision != "block" {
		t.Fatal(decision, err)
	}
	if !strings.Contains(decision.Reason, shellQuote(launcher)) || !strings.Contains(decision.Reason, shellQuote(result.TicketFile)) {
		t.Fatal("recovery command did not quote resolved paths", decision.Reason)
	}
}

func TestChildRecoveryInstructionUsesSavedWorkerFacts(t *testing.T) {
	t.Run("saved handle", func(t *testing.T) {
		c, _, store := boundFixture(t)
		if err := store.Update(func(tx *Store) error {
			tx.Data.Control.Worker = Execution{
				ID: "00000000-0000-4000-8000-000000000004", HostHandle: "exec:42",
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		decision, err := c.StopDecision(RuntimeEvent{Name: "SubagentStop", SessionID: specParent, AgentID: specChild})
		if err != nil || decision == nil || decision.Decision != "block" || !strings.Contains(decision.Reason, "exec:42") {
			t.Fatal(decision, err)
		}
	})
	t.Run("lost handle", func(t *testing.T) {
		c, result, store := boundFixture(t)
		launcher := installTestLauncher(t)
		if err := store.Update(func(tx *Store) error {
			tx.Data.Control.Worker = Execution{ID: "00000000-0000-4000-8000-000000000004"}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		decision, err := c.StopDecision(RuntimeEvent{Name: "SubagentStop", SessionID: specParent, AgentID: specChild})
		if err != nil || decision == nil || decision.Decision != "block" ||
			!strings.Contains(decision.Reason, shellQuote(launcher)) ||
			!strings.Contains(decision.Reason, shellQuote(result.TicketFile)) ||
			!strings.Contains(decision.Reason, "yield") || !strings.Contains(decision.Reason, "lock") {
			t.Fatal(decision, err)
		}
	})
}

func installTestLauncher(t *testing.T) string {
	t.Helper()
	pluginRoot := filepath.Join(t.TempDir(), "plugin ' root")
	launcher := filepath.Join(pluginRoot, "scripts", "goldilocks.sh")
	if err := os.MkdirAll(filepath.Dir(launcher), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(launcher, []byte("#!/bin/sh\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PLUGIN_ROOT", pluginRoot)
	resolved, err := filepath.EvalSymlinks(launcher)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func TestResumeDoesNotReopenFinishedBusinessState(t *testing.T) {
	c, _, store := boundFixture(t)
	if err := store.Update(func(tx *Store) error {
		tx.Data.Finished = true
		tx.Data.Control.InitialAccepted = true
		tx.Data.Control.InitialEventID = "terminal-event"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	resumed, err := c.Resume(specParent, specPR, false)
	if err != nil || resumed.Spawn {
		t.Fatal(resumed, err)
	}
	state, err := ReadState(store.Path)
	if err != nil || !state.Finished || !state.Control.InitialAccepted {
		t.Fatal("resume reopened terminal business state", state.Control, err)
	}
}
