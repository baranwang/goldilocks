package prwatch

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

const specParent = "00000000-0000-4000-8000-000000000001"
const specChild = "00000000-0000-4000-8000-000000000002"
const specWatch = "00000000-0000-4000-8000-000000000003"
const specPR = "https://github.com/example/project/pull/7"

var specTime = time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)

func managedFixture(t *testing.T) *Store {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("managed POSIX state")
	}
	s, err := NewStore(t.TempDir(), specPR)
	if err != nil {
		t.Fatal(err)
	}
	s.Data.Version = 4
	s.Data.Control = newControl(specParent, specWatch, t.TempDir(),
		strings.Repeat("a", 64), specTime, Options{time.Minute, 30 * time.Second})
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestManagedTransactionRollsBack(t *testing.T) {
	s := managedFixture(t)
	before, err := os.ReadFile(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	sentinel := errors.New("abort")
	err = s.Update(func(tx *Store) error {
		tx.Data.Control.Progress++
		if err := tx.Save(); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	after, err := os.ReadFile(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("aborted transaction persisted")
	}
}

func TestManagedTransactionDoesNotNeedWorkerLock(t *testing.T) {
	s := managedFixture(t)
	release, err := s.Lock(false)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if err := s.Update(func(tx *Store) error {
		tx.Data.Control.Progress++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if s.Data.Control.Progress != 1 {
		t.Fatal(s.Data.Control.Progress)
	}
}

func TestNewControlStartsAtTheRequestedGeneration(t *testing.T) {
	local := specTime.In(time.FixedZone("offset", 8*60*60))
	control := newControl(specParent, specWatch, "/tmp/work", strings.Repeat("a", 64),
		local, Options{time.Minute, 30 * time.Second})
	if control.ParentID != specParent || control.WatchID != specWatch || control.Cwd != "/tmp/work" ||
		control.Stage != Starting || control.CreatedAt != specTime || control.BindAfter != specTime ||
		control.Interval != time.Minute || control.Quiet != 30*time.Second || control.History == nil ||
		len(control.History) != 0 || control.Progress != 0 {
		t.Fatalf("unexpected initial control: %#v", control)
	}
}

func TestManagedStateRejectsInvalidControlWithoutWriting(t *testing.T) {
	otherEvent := "00000000-0000-4000-8000-000000000004"
	prompt := "prepared prompt"
	digest := sha256.Sum256([]byte(prompt))
	hash := hex.EncodeToString(digest[:])

	tests := map[string]func(*State){
		"missing control": func(state *State) { state.Control = nil },
		"invalid stage":   func(state *State) { state.Control.Stage = Stage("unknown") },
		"mismatched receipt": func(state *State) {
			state.Control.History[otherEvent] = Delivery{
				EventID: otherEvent,
				Kind:    "initial",
				Parts: []DeliveryPart{{
					Prompt: prompt,
					SHA256: hash,
					Receipt: &Receipt{
						CallID: "call-1", AgentID: specChild, ParentID: specParent,
						EventID: specWatch, Part: 1, SHA256: hash, AcceptedAt: specTime,
					},
				}},
			}
		},
		"unknown version": func(state *State) { state.Version = 5 },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			s := managedFixture(t)
			before, err := os.ReadFile(s.Path)
			if err != nil {
				t.Fatal(err)
			}
			mutate(&s.Data)
			if err := s.Save(); err == nil {
				t.Fatal("invalid managed state saved")
			}
			after, err := os.ReadFile(s.Path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Fatal("failed save changed state file")
			}
		})
	}
}

func TestManagedTransactionSaveFailureRollsBack(t *testing.T) {
	s := managedFixture(t)
	before, err := os.ReadFile(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	err = s.Update(func(tx *Store) error {
		tx.Data.Pending = json.RawMessage(`{`)
		return nil
	})
	if err == nil {
		t.Fatal("invalid transaction committed")
	}
	after, readErr := os.ReadFile(s.Path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !bytes.Equal(before, after) || s.Data.Control.Progress != 0 {
		t.Fatal("failed transaction changed state")
	}
}

func startedFixture(t *testing.T) (*Controller, StartResult) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("managed POSIX state")
	}
	c, err := NewController(t.TempDir(), func() time.Time { return specTime })
	if err != nil {
		t.Fatal(err)
	}
	r, err := c.Start(specParent, t.TempDir(), specPR,
		StartOptions{Interval: time.Minute, Quiet: 30 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return c, r
}

func TestTicketRequiresObservedChildAndIsSingleOwner(t *testing.T) {
	c, r := startedFixture(t)
	if _, err := c.Bind(r.TicketFile, specChild); err == nil {
		t.Fatal("unobserved bind")
	}
	if err := c.ObserveStart(specParent, specChild, specWatch); err != nil {
		t.Fatal(err)
	}
	s, err := c.Bind(r.TicketFile, specChild)
	if err != nil {
		t.Fatal(err)
	}
	if s.Data.Control.Stage != Initializing || s.Data.Control.Ready {
		t.Fatal("binding fabricated readiness")
	}
	if s.Data.Control.Progress != 1 {
		t.Fatal("first binding did not advance progress once")
	}
	if err := os.RemoveAll(filepath.Join(c.Root, "parents", specParent, "starts")); err != nil {
		t.Fatal(err)
	}
	again, err := c.Bind(r.TicketFile, specChild)
	if err != nil {
		t.Fatal(err)
	}
	if again.Data.Control.Progress != 1 {
		t.Fatal("duplicate binding advanced progress")
	}
	other := "00000000-0000-4000-8000-000000000004"
	if _, err := c.Bind(r.TicketFile, other); err == nil {
		t.Fatal("different owner")
	}
}

func TestDuplicateStartKeepsIntent(t *testing.T) {
	c, first := startedFixture(t)
	ticket, err := os.ReadFile(first.TicketFile)
	if err != nil {
		t.Fatal(err)
	}
	next, err := c.Start(specParent, t.TempDir(), specPR, StartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(next.TicketFile)
	if err != nil {
		t.Fatal(err)
	}
	if next.WatchID != first.WatchID || next.TicketFile != first.TicketFile || !bytes.Equal(ticket, after) {
		t.Fatal("duplicate intent")
	}
}

func TestTicketsBindToTheirObservedParentAndChild(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("managed POSIX state")
	}
	c, err := NewController(t.TempDir(), func() time.Time { return specTime })
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		parent string
		pr     string
		child  string
	}{
		{specParent, specPR, specChild},
		{"00000000-0000-4000-8000-000000000005", "https://github.com/example/project/pull/8", "00000000-0000-4000-8000-000000000004"},
	}
	results := make([]StartResult, len(cases))
	for i, tc := range cases {
		results[i], err = c.Start(tc.parent, t.TempDir(), tc.pr, StartOptions{})
		if err != nil {
			t.Fatal(err)
		}
	}
	for i := len(cases) - 1; i >= 0; i-- {
		if err := c.ObserveStart(cases[i].parent, cases[i].child, results[i].WatchID); err != nil {
			t.Fatal(err)
		}
	}
	for i, tc := range cases {
		s, err := c.Bind(results[i].TicketFile, tc.child)
		if err != nil {
			t.Fatal(err)
		}
		control := s.Data.Control
		if control.ParentID != tc.parent || control.AgentID != tc.child || control.WatchID != results[i].WatchID {
			t.Fatalf("wrong binding triple: %#v", control)
		}
	}
}

func TestDuplicateStartConcurrentReturnsOneIntent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("managed POSIX state")
	}
	c, err := NewController(t.TempDir(), func() time.Time { return specTime })
	if err != nil {
		t.Fatal(err)
	}
	cwd := t.TempDir()
	const callers = 12
	results := make(chan StartResult, callers)
	errs := make(chan error, callers)
	var ready, start sync.WaitGroup
	ready.Add(callers)
	start.Add(1)
	for range callers {
		go func() {
			ready.Done()
			start.Wait()
			r, err := c.Start(specParent, cwd, specPR, StartOptions{})
			results <- r
			errs <- err
		}()
	}
	ready.Wait()
	start.Done()
	var watchID string
	for range callers {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
		r := <-results
		if watchID == "" {
			watchID = r.WatchID
		} else if r.WatchID != watchID {
			t.Fatalf("multiple intents: %s and %s", watchID, r.WatchID)
		}
	}
}

func TestTicketConcurrentTwoIntentsRejectSameChild(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("managed POSIX state")
	}
	c, err := NewController(t.TempDir(), func() time.Time { return specTime })
	if err != nil {
		t.Fatal(err)
	}
	first, err := c.Start(specParent, t.TempDir(), specPR, StartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	second, err := c.Start(specParent, t.TempDir(), "https://github.com/example/project/pull/8", StartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.ObserveStart(specParent, specChild, specWatch); err != nil {
		t.Fatal(err)
	}
	tickets := []string{first.TicketFile, second.TicketFile}
	errs := make(chan error, len(tickets))
	var start sync.WaitGroup
	start.Add(1)
	for _, ticket := range tickets {
		go func() {
			start.Wait()
			_, err := c.Bind(ticket, specChild)
			errs <- err
		}()
	}
	start.Done()
	succeeded := 0
	for range tickets {
		if <-errs == nil {
			succeeded++
		}
	}
	if succeeded != 1 {
		t.Fatalf("wanted one owner, got %d", succeeded)
	}
}

func TestTicketValidationRejectsExpiredAndUntrustedPaths(t *testing.T) {
	t.Run("expired", func(t *testing.T) {
		c, r := startedFixture(t)
		if err := c.ObserveStart(specParent, specChild, specWatch); err != nil {
			t.Fatal(err)
		}
		c.Now = func() time.Time { return specTime.Add(10*time.Minute + time.Nanosecond) }
		if _, err := c.Bind(r.TicketFile, specChild); err == nil {
			t.Fatal("expired ticket accepted")
		}
	})
	t.Run("outside root", func(t *testing.T) {
		c, r := startedFixture(t)
		raw, err := os.ReadFile(r.TicketFile)
		if err != nil {
			t.Fatal(err)
		}
		outside := filepath.Join(t.TempDir(), "ticket.json")
		if err := os.WriteFile(outside, raw, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := c.Bind(outside, specChild); err == nil {
			t.Fatal("outside ticket accepted")
		}
	})
	t.Run("relative", func(t *testing.T) {
		c, r := startedFixture(t)
		rel, err := filepath.Rel(c.Root, r.TicketFile)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.Bind(rel, specChild); err == nil {
			t.Fatal("relative ticket accepted")
		}
	})
	t.Run("symlinked state", func(t *testing.T) {
		c, r := startedFixture(t)
		raw, err := os.ReadFile(r.StateFile)
		if err != nil {
			t.Fatal(err)
		}
		outside := filepath.Join(t.TempDir(), "state.json")
		if err := os.WriteFile(outside, raw, 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(r.StateFile); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(outside, r.StateFile); err != nil {
			t.Fatal(err)
		}
		if _, err := c.Bind(r.TicketFile, specChild); err == nil {
			t.Fatal("symlinked state accepted")
		}
	})
}

func TestTicketValidationRejectsMalformedOrMismatchedTicket(t *testing.T) {
	tests := map[string]func(*Ticket) string{
		"malformed": func(_ *Ticket) string { return "{" },
		"wrong nonce": func(ticket *Ticket) string {
			ticket.Nonce = strings.Repeat("0", 64)
			return marshalTicket(t, *ticket)
		},
		"wrong pr": func(ticket *Ticket) string {
			ticket.PRURL = "https://github.com/example/project/pull/8"
			return marshalTicket(t, *ticket)
		},
		"wrong watch": func(ticket *Ticket) string {
			ticket.WatchID = "00000000-0000-4000-8000-000000000004"
			return marshalTicket(t, *ticket)
		},
		"wrong parent": func(ticket *Ticket) string {
			ticket.ParentID = "00000000-0000-4000-8000-000000000005"
			return marshalTicket(t, *ticket)
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			c, r := startedFixture(t)
			raw, err := os.ReadFile(r.TicketFile)
			if err != nil {
				t.Fatal(err)
			}
			var ticket Ticket
			if err := json.Unmarshal(raw, &ticket); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(r.TicketFile, []byte(mutate(&ticket)), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := c.Bind(r.TicketFile, specChild); err == nil {
				t.Fatal("invalid ticket accepted")
			}
		})
	}
}

func marshalTicket(t *testing.T, ticket Ticket) string {
	t.Helper()
	raw, err := json.Marshal(ticket)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw) + "\n"
}

func TestStartDefaultsRejectsNegativeAndConflictingOptions(t *testing.T) {
	c, first := startedFixture(t)
	if first.Spawn != true {
		t.Fatal("new intent did not request spawn")
	}
	for _, options := range []StartOptions{{Interval: -1}, {Quiet: -1}} {
		if _, err := c.Start(specParent, t.TempDir(), "https://github.com/example/project/pull/8", options); err == nil {
			t.Fatal("negative option accepted")
		}
	}
	if _, err := c.Start(specParent, t.TempDir(), specPR, StartOptions{Interval: 2 * time.Minute}); err == nil {
		t.Fatal("conflicting duplicate options accepted")
	}
	if err := c.ObserveStart(specParent, specChild, specWatch); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Bind(first.TicketFile, specChild); err != nil {
		t.Fatal(err)
	}
	duplicate, err := c.Start(specParent, t.TempDir(), specPR, StartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if duplicate.Spawn || duplicate.AgentID != specChild {
		t.Fatalf("bound duplicate requested spawn: %#v", duplicate)
	}
}

func TestStartResumesMissingTicketOnlyWhenExplicit(t *testing.T) {
	c, first := startedFixture(t)
	if err := os.Remove(first.TicketFile); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Start(specParent, t.TempDir(), specPR, StartOptions{}); err == nil {
		t.Fatal("missing ticket resumed implicitly")
	}
	resumed, err := c.Start(specParent, t.TempDir(), specPR, StartOptions{Reopened: true})
	if err != nil {
		t.Fatal(err)
	}
	if resumed.WatchID != first.WatchID || resumed.TicketFile != first.TicketFile || !resumed.Spawn {
		t.Fatalf("wrong resumed intent: %#v", resumed)
	}
}

func TestStartReplacesOrphanTicketOnlyWithoutState(t *testing.T) {
	c, first := startedFixture(t)
	if err := os.Remove(first.StateFile); err != nil {
		t.Fatal(err)
	}
	next, err := c.Start(specParent, t.TempDir(), specPR, StartOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if next.WatchID == first.WatchID || next.TicketFile != first.TicketFile {
		t.Fatalf("orphan ticket was not replaced: %#v", next)
	}
}

func TestStartRemovesUnusedOldMemberships(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("managed POSIX state")
	}
	now := specTime
	c, err := NewController(t.TempDir(), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Start(specParent, t.TempDir(), specPR, StartOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := c.ObserveStart(specParent, specChild, specWatch); err != nil {
		t.Fatal(err)
	}
	membership := filepath.Join(c.Root, "parents", specParent, "starts", specChild+".json")
	now = now.Add(25 * time.Hour)
	if _, err := c.Start(specParent, t.TempDir(), "https://github.com/example/project/pull/8", StartOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(membership); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old membership was not removed: %v", err)
	}
}

func TestObserveStartIgnoresUnknownParent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("managed POSIX state")
	}
	c, err := NewController(t.TempDir(), func() time.Time { return specTime })
	if err != nil {
		t.Fatal(err)
	}
	if err := c.ObserveStart(specParent, specChild, specWatch); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(c.Root, "parents", specParent)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unknown parent created state: %v", err)
	}
}

func TestDefaultControllerRootUsesCodexHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	root, err := DefaultControllerRoot()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, "goldilocks", "pr-watch")
	if root != want {
		t.Fatalf("want %s, got %s", want, root)
	}
}
