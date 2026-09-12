package prwatch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func deliveryFixture(t *testing.T) (*Controller, StartResult, *Store, Action) {
	t.Helper()
	c, r, s := boundFixture(t)
	err := s.Update(func(tx *Store) error {
		_, err := tx.Stage("initial", managedSnapshot(), Changes{}, nil, nil, specTime)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	a, err := c.Advance(context.Background(), r.TicketFile, specChild, Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	if a.Action != "send" {
		t.Fatal(a)
	}
	return c, r, s, a
}

func TestSendAcceptanceRequiresExactDestinationAndBody(t *testing.T) {
	c, _, s, a := deliveryFixture(t)
	for _, bad := range []ObservedSend{
		{specChild, specWatch, "bad-target", a.Prompt, true, specTime},
		{specChild, specParent, "bad-body", a.Prompt + "changed", true, specTime},
		{specWatch, specParent, "bad-agent", a.Prompt, true, specTime},
		{specChild, specParent, "failed", a.Prompt, false, specTime},
	} {
		_ = c.AcceptSend(bad)
	}
	state, err := ReadState(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	if state.Control.Outbox.Parts[0].Receipt != nil {
		t.Fatal("false receipt")
	}
	good := ObservedSend{specChild, specParent, "call-1", a.Prompt, true, specTime}
	if err := c.AcceptSend(good); err != nil {
		t.Fatal(err)
	}
	if err := c.AcceptSend(good); err != nil {
		t.Fatal(err)
	}
	state, err = ReadState(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	if state.LastAck != nil {
		t.Fatal("receipt alone acknowledged business event")
	}
}

func TestAcceptedEventCommitsAtomicallyAfterRestart(t *testing.T) {
	c, _, s, a := deliveryFixture(t)
	err := c.AcceptSend(ObservedSend{specChild, specParent, "call-1", a.Prompt, true, specTime})
	if err != nil {
		t.Fatal(err)
	}
	again, err := c.Open(specParent, specPR)
	if err != nil {
		t.Fatal(err)
	}
	if err := again.Update(func(tx *Store) error { return tx.CommitAccepted() }); err != nil {
		t.Fatal(err)
	}
	state, err := ReadState(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	history := state.Control.History[a.EventID]
	if state.LastAck == nil || *state.LastAck != a.EventID || state.Control.Outbox != nil ||
		!state.Control.InitialAccepted || len(history.Parts) != 1 || history.Parts[0].Prompt != "" {
		t.Fatal("acceptance did not commit baseline and history together")
	}
}

func multipartDeliveryFixture(t *testing.T) (*Controller, StartResult, *Store, Action) {
	t.Helper()
	c, r, s := boundFixture(t)
	changes := Changes{"comments": map[string]any{"1": map[string]any{
		"body": strings.Repeat("界", 12500), "author": "reviewer", "url": specPR,
	}}}
	if err := s.Update(func(tx *Store) error {
		_, err := tx.Stage("update", managedSnapshot(), changes, nil, nil, specTime)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	a, err := c.Advance(context.Background(), r.TicketFile, specChild, Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	if a.Action != "send" || a.Part != 1 || a.Parts != 3 {
		t.Fatalf("first multipart offer: %#v", a)
	}
	return c, r, s, a
}

func TestMultipartReceiptResumesExactNextPartAfterRestart(t *testing.T) {
	c, r, s, first := multipartDeliveryFixture(t)
	if !strings.Contains(first.Prompt, " · Run: "+s.Data.Control.WatchID) {
		t.Fatal("managed run identity missing from frozen prompt")
	}
	if err := c.AcceptSend(ObservedSend{specChild, specParent, "call-1", first.Prompt, true, specTime}); err != nil {
		t.Fatal(err)
	}
	state, err := ReadState(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	want := state.Control.Outbox.Parts[1].Prompt
	restarted, err := NewController(c.Root, func() time.Time { return specTime })
	if err != nil {
		t.Fatal(err)
	}
	next, err := restarted.Advance(context.Background(), r.TicketFile, specChild, Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	if next.Action != "send" || next.Part != 2 || next.Prompt != want {
		t.Fatalf("resumed wrong part: %#v", next)
	}
}

func TestReceiptRejectsFuturePartAndConflictingCallID(t *testing.T) {
	c, _, s, first := multipartDeliveryFixture(t)
	state, err := ReadState(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	second := state.Control.Outbox.Parts[1].Prompt
	if err := c.AcceptSend(ObservedSend{specChild, specParent, "future", second, true, specTime}); err != nil {
		t.Fatal(err)
	}
	if err := c.AcceptSend(ObservedSend{specChild, specParent, "call-1", first.Prompt, true, specTime}); err != nil {
		t.Fatal(err)
	}
	state, err = ReadState(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	progress := state.Control.Progress
	if state.Control.Outbox.Parts[1].Receipt != nil {
		t.Fatal("future part accepted")
	}
	if err := c.AcceptSend(ObservedSend{specChild, specParent, "call-1", second, true, specTime}); err == nil {
		t.Fatal("conflicting call id accepted")
	}
	if err := c.AcceptSend(ObservedSend{specChild, specParent, "call-2", first.Prompt, true, specTime}); err != nil {
		t.Fatal(err)
	}
	state, err = ReadState(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	if state.Control.Progress != progress {
		t.Fatal("repeated accepted content counted as new progress")
	}
}

func TestOfferRetriesAreDelayedAndBounded(t *testing.T) {
	c, r, s, _ := deliveryFixture(t)
	now := specTime.Add(4 * time.Second)
	c.Now = func() time.Time { return now }
	wait, err := c.Advance(context.Background(), r.TicketFile, specChild, Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	if wait.Action != "wait" || wait.RetryAfterSeconds != 1 {
		t.Fatalf("retry was not delayed: %#v", wait)
	}
	state, err := ReadState(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	progress := state.Control.Progress
	pending := append([]byte(nil), state.Pending...)
	for offer := 2; offer <= 3; offer++ {
		now = specTime.Add(time.Duration(offer-1) * 5 * time.Second)
		a, err := c.Advance(context.Background(), r.TicketFile, specChild, Dependencies{})
		if err != nil || a.Action != "send" {
			t.Fatalf("offer %d: %#v, %v", offer, a, err)
		}
	}
	now = specTime.Add(15 * time.Second)
	attention, err := c.Advance(context.Background(), r.TicketFile, specChild, Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	state, err = ReadState(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	if attention.Action != "attention" || attention.Reason != "delivery_receipt_missing" ||
		state.Control.Outbox == nil || state.Control.Progress != progress || !bytes.Equal(state.Pending, pending) ||
		state.Finished || state.Control.Ready {
		t.Fatalf("unbounded or destructive retries: %#v %#v", attention, state.Control)
	}
}

func TestReceiveDeduplicatesParentPartsWithoutTransportReceipt(t *testing.T) {
	c, _, s, first := multipartDeliveryFixture(t)
	for _, step := range []struct {
		part int
		want string
	}{{3, "new_part"}, {1, "new_part"}, {2, "event_complete"}, {2, "duplicate"}} {
		got, err := c.Receive(specParent, specPR, first.EventID, step.part)
		if err != nil || got != step.want {
			t.Fatalf("receive part %d: %q, %v", step.part, got, err)
		}
	}
	state, err := ReadState(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	for _, part := range state.Control.Outbox.Parts {
		if part.Receipt != nil || !part.Received {
			t.Fatal("reception fabricated a receipt or lost a part")
		}
	}
	if state.LastAck != nil || state.Control.Ready {
		t.Fatal("reception advanced business state")
	}
}

func TestReceiveAloneLeavesTransportReceiptUnset(t *testing.T) {
	c, _, s, first := deliveryFixture(t)
	if _, err := c.Receive(specParent, specPR, first.EventID, first.Part); err != nil {
		t.Fatal(err)
	}
	state, err := ReadState(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	if receipt := state.Control.Outbox.Parts[first.Part-1].Receipt; receipt != nil {
		t.Fatalf("reception fabricated transport receipt: %+v", receipt)
	}
}

func TestReceiptAndParentReceiveRemainValidInEitherOrder(t *testing.T) {
	for _, receiptFirst := range []bool{true, false} {
		name := "parent_received_first"
		if receiptFirst {
			name = "post_tool_receipt_first"
		}
		t.Run(name, func(t *testing.T) {
			c, _, s, first := deliveryFixture(t)
			receipt := func() error {
				return c.AcceptSend(ObservedSend{specChild, specParent, "ordered-call", first.Prompt, true, specTime})
			}
			parent := func() error {
				_, err := c.Receive(specParent, specPR, first.EventID, first.Part)
				return err
			}
			if receiptFirst {
				if err := receipt(); err != nil {
					t.Fatal(err)
				}
				if err := parent(); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := parent(); err != nil {
					t.Fatal(err)
				}
				if err := receipt(); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.Update(func(tx *Store) error { return tx.CommitAccepted() }); err != nil {
				t.Fatal(err)
			}
			state, err := ReadState(s.Path)
			if err != nil {
				t.Fatal(err)
			}
			part := state.Control.History[first.EventID].Parts[first.Part-1]
			if !part.Received || part.Receipt == nil || part.Receipt.CallID != "ordered-call" {
				t.Fatalf("delivery evidence lost: %+v", part)
			}
		})
	}
}

func TestStatusFailsClosedWhenParentReceivedWithoutPostToolReceipt(t *testing.T) {
	c, _, s, first := deliveryFixture(t)
	if _, err := c.Receive(specParent, specPR, first.EventID, first.Part); err != nil {
		t.Fatal(err)
	}
	if err := s.Update(func(tx *Store) error {
		part := &tx.Data.Control.Outbox.Parts[first.Part-1]
		part.Offers = 3
		tx.Data.Control.Worker = Execution{ID: specWatch, Ended: true}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	status, err := c.Status(specParent, specPR)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	if fields["ready"] != false || fields["activity"] != "delivery_pending" ||
		fields["parent_received"] != true || fields["post_tool_receipt"] != false ||
		fields["hook_not_observed"] != true || fields["failure_code"] != "delivery_receipt_missing" ||
		!strings.Contains(status.FailureDetail, "PostToolUse was not observed") || status.Worker.Locked || status.Worker.Fresh {
		t.Fatalf("missing receipt was not diagnosed: %s", raw)
	}
}

func readyMultipartMissingReceiptFixture(t *testing.T) (*Controller, *Store) {
	t.Helper()
	c, _, s, first := multipartDeliveryFixture(t)
	if err := s.Update(func(tx *Store) error {
		const initialEvent = "accepted-initial-event"
		hash := strings.Repeat("a", 64)
		tx.Data.Control.InitialAccepted = true
		tx.Data.Control.InitialEventID = initialEvent
		tx.Data.Control.History[initialEvent] = Delivery{
			EventID: initialEvent, Kind: "initial", Parts: []DeliveryPart{{
				SHA256: hash,
				Receipt: &Receipt{
					CallID: "initial-call", AgentID: specChild, ParentID: specParent,
					EventID: initialEvent, Part: 1, SHA256: hash, AcceptedAt: specTime,
				},
			}},
		}
		tx.Data.Control.Ready = true
		tx.Data.Control.Stage = Running
		part := &tx.Data.Control.Outbox.Parts[first.Part-1]
		part.Offers = 3
		part.Received = true
		tx.Data.Control.Worker = Execution{ID: specWatch, Ended: true}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return c, s
}

func TestMissingLaterReceiptPreservesHistoricalReadiness(t *testing.T) {
	c, _ := readyMultipartMissingReceiptFixture(t)
	status, err := c.Status(specParent, specPR)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Ready || status.Stage != NeedsAttention || status.Activity != "delivery_pending" ||
		status.FailureCode != "delivery_receipt_missing" {
		t.Fatalf("later delivery failure erased startup readiness: %+v", status)
	}
}

func TestMultipartStatusReportsEvidenceForMissingReceiptPart(t *testing.T) {
	c, _ := readyMultipartMissingReceiptFixture(t)
	status, err := c.Status(specParent, specPR)
	if err != nil {
		t.Fatal(err)
	}
	if !status.ParentReceived || status.PostToolReceipt || !status.HookNotObserved ||
		status.FailureCode != "delivery_receipt_missing" || !strings.Contains(status.FailureDetail, "PostToolUse was not observed") {
		t.Fatalf("later unoffered parts hid missing receipt evidence: %+v", status)
	}
}

func TestCommitAcceptedRollsBackWithOuterTransaction(t *testing.T) {
	c, _, s, a := deliveryFixture(t)
	if err := c.AcceptSend(ObservedSend{specChild, specParent, "call-1", a.Prompt, true, specTime}); err != nil {
		t.Fatal(err)
	}
	before, err := ReadState(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	beforePending := append([]byte(nil), before.Pending...)
	rollback := errors.New("stop before outer save")
	if err := s.Update(func(tx *Store) error {
		if err := tx.CommitAccepted(); err != nil {
			return err
		}
		return rollback
	}); !errors.Is(err, rollback) {
		t.Fatal(err)
	}
	after, err := ReadState(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after.Pending, beforePending) || after.LastAck != nil || after.Control.Outbox == nil || len(after.Control.History) != 0 {
		t.Fatal("failed outer transaction partially committed acceptance")
	}
}

func TestTerminalCommitAcceptedKeepsStopMarkerOnSaveFailure(t *testing.T) {
	c, r, s := boundFixture(t)
	terminal := managedSnapshot()
	terminal["state"] = "MERGED"
	if err := s.Update(func(tx *Store) error {
		_, err := tx.Stage("merged", terminal, Changes{}, nil, nil, specTime)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	offer, err := c.Advance(context.Background(), r.TicketFile, specChild, Dependencies{})
	if err != nil || offer.Action != "send" {
		t.Fatal(offer, err)
	}
	if err := c.AcceptSend(ObservedSend{specChild, specParent, "terminal-save-failure", offer.Prompt, true, specTime}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.StopPath, []byte("stop\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.Update(func(tx *Store) error {
		if err := tx.CommitAccepted(); err != nil {
			return err
		}
		tx.Path = filepath.Join(t.TempDir(), "missing", "state.json")
		return nil
	}); err == nil {
		t.Fatal("outer save failure was not reported")
	}
	if _, err := os.Stat(s.StopPath); err != nil {
		t.Fatalf("stop marker was removed before outer save committed: %v", err)
	}
	state, err := ReadState(s.Path)
	if err != nil || state.Pending == nil || state.Control.Outbox == nil || state.Finished {
		t.Fatalf("failed commit changed durable state: %#v", state)
	}
}

func TestAdvancePollsThenOffersPendingEvent(t *testing.T) {
	c, r, s := boundFixture(t)
	a, err := c.Advance(context.Background(), r.TicketFile, specChild, Dependencies{
		Read: func(context.Context, PR, func() bool) (Snapshot, error) { return managedSnapshot(), nil },
		Now:  func() time.Time { return specTime },
		Wait: func(ctx context.Context, _ time.Duration) error { <-ctx.Done(); return ctx.Err() },
	})
	if err != nil {
		t.Fatal(err)
	}
	if a.Action != "send" || a.Part != 1 || !strings.Contains(a.Prompt, " · Run: "+s.Data.Control.WatchID) {
		t.Fatalf("poll result was not offered: %#v", a)
	}
}

func TestAdvancePersistsPollingFaultWithoutErasingPending(t *testing.T) {
	c, r, s := boundFixture(t)
	readErr := errors.New("credential helper failed")
	a, err := c.Advance(context.Background(), r.TicketFile, specChild, Dependencies{
		Read: func(context.Context, PR, func() bool) (Snapshot, error) { return nil, readErr },
		Now:  func() time.Time { return specTime },
		Wait: func(ctx context.Context, _ time.Duration) error { <-ctx.Done(); return ctx.Err() },
	})
	if err != nil {
		t.Fatal(err)
	}
	state, err := ReadState(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	if a.Action != "attention" || state.Control.Stage != NeedsAttention ||
		state.Control.FailureCode == "" || !strings.Contains(state.Control.FailureDetail, readErr.Error()) || hasPendingState(state) {
		t.Fatalf("polling fault was not persisted: %#v %#v", a, state.Control)
	}
}

func TestTerminalAcceptanceWaitsForWorkerLockBeforeFinishedStage(t *testing.T) {
	c, r, s := boundFixture(t)
	terminal := managedSnapshot()
	terminal["state"] = "MERGED"
	if err := s.Update(func(tx *Store) error {
		_, err := tx.Stage("merged", terminal, Changes{}, nil, nil, specTime)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	offer, err := c.Advance(context.Background(), r.TicketFile, specChild, Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.AcceptSend(ObservedSend{specChild, specParent, "terminal", offer.Prompt, true, specTime}); err != nil {
		t.Fatal(err)
	}
	release, err := s.Lock(false)
	if err != nil {
		t.Fatal(err)
	}
	wait, err := c.Advance(context.Background(), r.TicketFile, specChild, Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	state, err := ReadState(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	if wait.Action != "wait" || state.Control.Stage == Finished || state.Control.Ready {
		t.Fatalf("terminal lock fabricated completion: %#v %#v", wait, state.Control)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	finished, err := c.Advance(context.Background(), r.TicketFile, specChild, Dependencies{})
	if err != nil {
		t.Fatal(err)
	}
	state, err = ReadState(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	if finished.Action != "finished" || state.Control.Stage != Finished {
		t.Fatalf("terminal state did not finish after lock release: %#v %#v", finished, state.Control)
	}
}
