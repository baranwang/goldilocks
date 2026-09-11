package prwatch

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestImportKeepsPreparedLegacyMessage(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("managed import")
	}
	source := filepath.Join(t.TempDir(), "legacy.json")
	raw, err := os.ReadFile(filepath.Join("testdata", "v2-pending.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, raw, 0600); err != nil {
		t.Fatal(err)
	}
	old, err := ReadState(source)
	if err != nil {
		t.Fatal(err)
	}
	c, err := NewController(t.TempDir(), func() time.Time { return specTime })
	if err != nil {
		t.Fatal(err)
	}
	r, err := c.Import(specParent, t.TempDir(), old.PRURL, source)
	if err != nil {
		t.Fatal(err)
	}
	managed, err := ReadState(r.StateFile)
	if err != nil {
		t.Fatal(err)
	}
	var oldPending, newPending any
	if err := decodeJSON(old.Pending, &oldPending); err != nil {
		t.Fatal(err)
	}
	if err := decodeJSON(managed.Pending, &newPending); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(oldPending, newPending) {
		t.Fatal("event or prepared strings changed")
	}
	after, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, after) {
		t.Fatal("import rewrote source")
	}
	if managed.Control == nil || managed.Control.ImportPath != source || managed.Control.ImportSHA256 == "" {
		t.Fatalf("import audit metadata missing: %#v", managed.Control)
	}
}

func TestApplyPollRefreshesImportedBaseline(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("managed import")
	}
	s := managedFixture(t)
	s.Data.Snapshot = managedSnapshot()
	s.Data.Control.RefreshRequired = true
	snapshot := managedSnapshot()
	snapshot["head_sha"] = "new-head"
	out, err := s.ApplyPoll(snapshot, nil, specTime, specTime, Options{time.Minute, defaultQuiet}, PollCycle{})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Event) == 0 {
		t.Fatal("refresh did not stage an initial event")
	}
	var event Event
	if err := decodeJSON(out.Event, &event); err != nil {
		t.Fatal(err)
	}
	if event.Type != "initial" || s.Data.Control.RefreshRequired || s.Data.Control.InitialEventID != event.EventID {
		t.Fatalf("unexpected refresh state: event=%#v control=%#v", event, s.Data.Control)
	}
}

func TestImportPreservesV3CollectingAndLargeIDs(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("managed import")
	}
	snapshot := managedSnapshot()
	snapshot["comments"] = map[string]any{"9007199254740993": map[string]any{
		"id": json.Number("9007199254740993"), "body": "legacy", "author": "reviewer",
	}}
	legacy := State{
		Version: 3, PRURL: specPR, Snapshot: snapshot,
		Pending: mustLegacyPending(t), Collecting: &Batch{
			Snapshot: snapshot, Kind: "update", Observations: []Observation{{
				Type: "update", ObservedAt: specTime.Format(time.RFC3339Nano), HeadSHA: "abc123",
				Changes: Changes{"comments": map[string]any{"9007199254740993": snapshot["comments"].(map[string]any)["9007199254740993"]}},
			}},
		},
	}
	raw, err := encodeJSON(legacy)
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "legacy-v3.json")
	if err := os.WriteFile(source, raw, 0600); err != nil {
		t.Fatal(err)
	}
	c, err := NewController(t.TempDir(), func() time.Time { return specTime })
	if err != nil {
		t.Fatal(err)
	}
	result, err := c.Import(specParent, t.TempDir(), specPR, source)
	if err != nil {
		t.Fatal(err)
	}
	managed, err := ReadState(result.StateFile)
	if err != nil {
		t.Fatal(err)
	}
	if managed.Collecting == nil || managed.Pending == nil || managed.Snapshot["comments"].(map[string]any)["9007199254740993"] == nil {
		t.Fatalf("legacy evidence was not preserved: %#v", managed)
	}
	if _, ok := managed.Snapshot["comments"].(map[string]any)["9007199254740993"].(map[string]any)["id"].(json.Number); !ok {
		t.Fatal("large evidence id lost exact representation")
	}
}

func mustLegacyPending(t *testing.T) json.RawMessage {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "v2-pending.json"))
	if err != nil {
		t.Fatal(err)
	}
	state, err := decodeState(raw)
	if err != nil {
		t.Fatal(err)
	}
	return state.Pending
}

func TestImportRejectsHeldOrMismatchedSource(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("managed import")
	}
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
	if _, err := c.Import(specParent, t.TempDir(), "https://github.com/example/other/pull/7", source); err == nil {
		t.Fatal("mismatched source was imported")
	}
	release, err := flock(strings.TrimSuffix(source, filepath.Ext(source)) + ".lock")
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Import(specParent, t.TempDir(), specPR, source)
	if !errors.Is(err, ErrLocked) {
		t.Fatalf("held source lock was ignored: %v", err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
}

func TestStartReopenedRequiresFinishedCleanGeneration(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("managed import")
	}
	c, err := NewController(t.TempDir(), func() time.Time { return specTime })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Start(specParent, t.TempDir(), specPR, StartOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Start(specParent, t.TempDir(), specPR, StartOptions{Reopened: true}); err == nil {
		t.Fatal("reopened start accepted an unfinished generation")
	}
}

func TestReopenRejectsConflictingArchive(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("managed import")
	}
	c, err := NewController(t.TempDir(), func() time.Time { return specTime })
	if err != nil {
		t.Fatal(err)
	}
	start, err := c.Start(specParent, t.TempDir(), specPR, StartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	store, err := c.Open(specParent, specPR)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(func(tx *Store) error {
		tx.Data.Finished = true
		tx.Data.Control.Stage = Finished
		tx.Data.Control.Worker = Execution{Ended: true}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	history := filepath.Join(filepath.Dir(store.Path), "history")
	if err := os.MkdirAll(history, 0700); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(history, start.WatchID+".json")
	if err := os.WriteFile(archive, []byte("conflict\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Start(specParent, t.TempDir(), specPR, StartOptions{Reopened: true}); err == nil {
		t.Fatal("conflicting archive was overwritten")
	}
	state, err := ReadState(store.Path)
	if err != nil {
		t.Fatal(err)
	}
	if state.Control.WatchID != start.WatchID || !state.Finished {
		t.Fatalf("failed reopen changed active state: %#v", state.Control)
	}
}

func TestReceiveFindsLatePartInArchivedGeneration(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("managed import")
	}
	c, err := NewController(t.TempDir(), func() time.Time { return specTime })
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.Start(specParent, t.TempDir(), specPR, StartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	store, err := c.Open(specParent, specPR)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(func(tx *Store) error {
		tx.Data.Finished = true
		tx.Data.Control.Stage = Finished
		tx.Data.Control.Worker = Execution{Ended: true}
		tx.Data.Control.History["old-event"] = Delivery{EventID: "old-event", Kind: "initial", Parts: []DeliveryPart{{SHA256: strings.Repeat("a", 64)}}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Start(specParent, t.TempDir(), specPR, StartOptions{Reopened: true}); err != nil {
		t.Fatal(err)
	}
	result, err := c.Receive(specParent, specPR, "old-event", 1)
	if err != nil || result != "event_complete" {
		t.Fatalf("archived part was not received: result=%q err=%v", result, err)
	}
	result, err = c.Receive(specParent, specPR, "old-event", 1)
	if err != nil || result != "duplicate" {
		t.Fatalf("archived duplicate was not recognized: result=%q err=%v", result, err)
	}
}

func TestReceiveRejectsActiveAndArchivedDuplicateEvent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("managed import")
	}
	c, err := NewController(t.TempDir(), func() time.Time { return specTime })
	if err != nil {
		t.Fatal(err)
	}
	start, err := c.Start(specParent, t.TempDir(), specPR, StartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	store, err := c.Open(specParent, specPR)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(func(tx *Store) error {
		tx.Data.Finished = true
		tx.Data.Control.Stage = Finished
		tx.Data.Control.Worker = Execution{Ended: true}
		tx.Data.Control.History["duplicate-event"] = Delivery{EventID: "duplicate-event", Kind: "initial", Parts: []DeliveryPart{{SHA256: strings.Repeat("a", 64)}}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	reopened, err := c.Start(specParent, t.TempDir(), specPR, StartOptions{Reopened: true})
	if err != nil {
		t.Fatal(err)
	}
	current, err := c.Open(specParent, specPR)
	if err != nil {
		t.Fatal(err)
	}
	if err := current.Update(func(tx *Store) error {
		tx.Data.Control.History["duplicate-event"] = Delivery{EventID: "duplicate-event", Kind: "initial", Parts: []DeliveryPart{{SHA256: strings.Repeat("a", 64)}}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Receive(specParent, specPR, "duplicate-event", 1); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("duplicate active/archive event was attributed: %v", err)
	}
	_ = start
	_ = reopened
}

func TestReopenRecoversCandidateTicketAfterArchive(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("managed import")
	}
	c, err := NewController(t.TempDir(), func() time.Time { return specTime })
	if err != nil {
		t.Fatal(err)
	}
	start, err := c.Start(specParent, t.TempDir(), specPR, StartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	store, err := c.Open(specParent, specPR)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Update(func(tx *Store) error {
		tx.Data.Finished = true
		tx.Data.Control.Stage = Finished
		tx.Data.Control.Worker = Execution{Ended: true}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	oldState, err := encodeJSON(store.Data)
	if err != nil {
		t.Fatal(err)
	}
	oldTicket, err := os.ReadFile(start.TicketFile)
	if err != nil {
		t.Fatal(err)
	}
	history := filepath.Join(filepath.Dir(store.Path), "history")
	if err := os.MkdirAll(history, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(history, start.WatchID+".json"), oldState, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(history, start.WatchID+".ticket"), oldTicket, 0600); err != nil {
		t.Fatal(err)
	}
	candidateID, err := newUUID()
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := newTicket(candidateID, specParent, specPR)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeJSON0600(start.TicketFile, candidate); err != nil {
		t.Fatal(err)
	}
	reopened, err := c.Start(specParent, t.TempDir(), specPR, StartOptions{Reopened: true})
	if err != nil {
		t.Fatal(err)
	}
	if reopened.WatchID != candidateID {
		t.Fatalf("candidate ticket was not recovered: got %s want %s", reopened.WatchID, candidateID)
	}
	state, err := ReadState(store.Path)
	if err != nil {
		t.Fatal(err)
	}
	if state.Control.WatchID != candidateID || state.Control.Stage != Starting {
		t.Fatalf("candidate state was not published: %#v", state.Control)
	}
}

func TestImportAcknowledgedBaselineRequestsRefresh(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("managed import")
	}
	legacy := State{Version: 2, PRURL: specPR, Snapshot: managedSnapshot(), LastAck: cloneStringPtr("old-event")}
	raw, err := encodeJSON(legacy)
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "acknowledged.json")
	if err := os.WriteFile(source, raw, 0600); err != nil {
		t.Fatal(err)
	}
	c, err := NewController(t.TempDir(), func() time.Time { return specTime })
	if err != nil {
		t.Fatal(err)
	}
	result, err := c.Import(specParent, t.TempDir(), specPR, source)
	if err != nil {
		t.Fatal(err)
	}
	state, err := ReadState(result.StateFile)
	if err != nil {
		t.Fatal(err)
	}
	if state.Snapshot == nil || state.Control == nil || !state.Control.RefreshRequired || state.LastAck == nil || *state.LastAck != "old-event" {
		t.Fatalf("acknowledged baseline was not preserved for refresh: %#v", state)
	}
}

func cloneStringPtr(value string) *string { return &value }

func TestImportRejectsTerminalLegacyState(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("managed import")
	}
	legacy := State{Version: 2, PRURL: specPR, Snapshot: managedSnapshot(), Finished: true}
	raw, err := encodeJSON(legacy)
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "finished.json")
	if err := os.WriteFile(source, raw, 0600); err != nil {
		t.Fatal(err)
	}
	c, err := NewController(t.TempDir(), func() time.Time { return specTime })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Import(specParent, t.TempDir(), specPR, source); err == nil || !strings.Contains(err.Error(), "start --reopened") {
		t.Fatalf("terminal legacy state accepted: %v", err)
	}
}
