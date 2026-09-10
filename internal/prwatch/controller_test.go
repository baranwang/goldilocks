package prwatch

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"runtime"
	"strings"
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
