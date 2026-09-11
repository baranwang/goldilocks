package prwatch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func managedSnapshot() Snapshot {
	return Snapshot{
		"head_sha": "abc123", "state": "OPEN", "draft": false,
		"mergeable": "MERGEABLE", "merge_state": "CLEAN", "checks": []any{},
		"comments": map[string]any{}, "reviews": map[string]any{}, "threads": map[string]any{},
	}
}

func TestManagedQuietRequiresSuccessfulObservation(t *testing.T) {
	s := managedFixture(t)
	s.Data.Snapshot = managedSnapshot()
	changed := managedSnapshot()
	changed["head_sha"] = "def456"
	opts := Options{time.Minute, 30 * time.Second}
	first, err := s.ApplyPoll(changed, nil, specTime, specTime, opts, PollCycle{})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Event) != 0 {
		t.Fatal("froze before quiet")
	}
	early, err := s.ApplyPoll(changed, nil, specTime.Add(20*time.Second),
		specTime.Add(35*time.Second), opts, first.Cycle)
	if err != nil {
		t.Fatal(err)
	}
	if len(early.Event) != 0 {
		t.Fatal("read began before quiet deadline")
	}
	last, err := s.ApplyPoll(changed, nil, specTime.Add(36*time.Second),
		specTime.Add(37*time.Second), opts, early.Cycle)
	if err != nil {
		t.Fatal(err)
	}
	if len(last.Event) == 0 {
		t.Fatal("never froze observed quiet")
	}
}

func boundFixture(t *testing.T) (*Controller, StartResult, *Store) {
	t.Helper()
	c, r := startedFixture(t)
	if err := c.ObserveStart(specParent, specChild, specWatch); err != nil {
		t.Fatal(err)
	}
	s, err := c.Bind(r.TicketFile, specChild)
	if err != nil {
		t.Fatal(err)
	}
	return c, r, s
}

func TestManagedWorkerExcludesDuplicateButAllowsStateWrites(t *testing.T) {
	c, _, s := boundFixture(t)
	release, err := s.Lock(false)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	_, err = c.PollManaged(context.Background(), s, Dependencies{})
	if !errors.Is(err, ErrBusy) {
		t.Fatal(err)
	}
	if err := s.Update(func(tx *Store) error {
		tx.Data.Control.FaultSeen = true
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

type managedClock struct {
	mu  sync.Mutex
	now time.Time
}

func newManagedClock() *managedClock { return &managedClock{now: specTime} }

func (c *managedClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *managedClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func (c *managedClock) pollingWait(ctx context.Context, d time.Duration) error {
	if d == 5*time.Second {
		<-ctx.Done()
		return ctx.Err()
	}
	c.Advance(d)
	return ctx.Err()
}

func blockManagedRead(started, release <-chan struct{}, entered chan<- struct{}) func(context.Context, PR, func() bool) (Snapshot, error) {
	return func(ctx context.Context, _ PR, _ func() bool) (Snapshot, error) {
		close(entered)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-started:
		case <-release:
		}
		return managedSnapshot(), nil
	}
}

func TestManagedStopDuringReadStagesStop(t *testing.T) {
	c, _, s := boundFixture(t)
	clock := newManagedClock()
	entered, releaseRead := make(chan struct{}), make(chan struct{})
	done := make(chan struct {
		raw json.RawMessage
		err error
	}, 1)
	go func() {
		raw, err := c.PollManaged(context.Background(), s, Dependencies{
			Read: func(context.Context, PR, func() bool) (Snapshot, error) {
				close(entered)
				<-releaseRead
				return managedSnapshot(), nil
			},
			Now: clock.Now, Wait: clock.pollingWait,
		})
		done <- struct {
			raw json.RawMessage
			err error
		}{raw, err}
	}()
	<-entered
	if err := os.WriteFile(s.StopPath, []byte("stop\n"), 0600); err != nil {
		t.Fatal(err)
	}
	close(releaseRead)
	result := <-done
	if result.err != nil {
		t.Fatal(result.err)
	}
	var event Event
	if err := json.Unmarshal(result.raw, &event); err != nil {
		t.Fatal(err)
	}
	if event.Type != "stopped" {
		t.Fatal(event)
	}
}

func TestManagedHeartbeatContinuesDuringRead(t *testing.T) {
	c, _, s := boundFixture(t)
	clock := newManagedClock()
	entered := make(chan struct{})
	heartbeat := make(chan struct{})
	var once sync.Once
	wait := func(ctx context.Context, d time.Duration) error {
		if d != 5*time.Second {
			<-ctx.Done()
			return ctx.Err()
		}
		once.Do(func() {
			clock.Advance(5 * time.Second)
			close(heartbeat)
		})
		if ctx.Err() == nil {
			return nil
		}
		return ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := c.PollManaged(ctx, s, Dependencies{
			Read: func(ctx context.Context, _ PR, _ func() bool) (Snapshot, error) {
				close(entered)
				<-ctx.Done()
				return nil, ctx.Err()
			},
			Now: clock.Now, Wait: wait,
		})
		done <- err
	}()
	<-entered
	<-heartbeat
	deadline := time.After(2 * time.Second)
	for {
		state, err := ReadState(s.Path)
		if err != nil {
			t.Fatal(err)
		}
		if state.Control.Worker.HeartbeatAt.Equal(clock.Now()) {
			break
		}
		select {
		case <-deadline:
			t.Fatal("heartbeat was not persisted during read")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestManagedYieldDiscardsReadAndPreservesCollection(t *testing.T) {
	c, r, s := boundFixture(t)
	if err := s.Update(func(tx *Store) error {
		tx.Data.Snapshot = managedSnapshot()
		_, err := tx.Observe(Snapshot{
			"head_sha": "abc123", "state": "OPEN", "draft": false,
			"mergeable": "MERGEABLE", "merge_state": "CLEAN", "checks": []any{},
			"comments": map[string]any{"1": map[string]any{"body": "saved", "url": specPR}},
			"reviews":  map[string]any{}, "threads": map[string]any{},
		}, specTime)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	before, err := json.Marshal(s.Data.Collecting)
	if err != nil {
		t.Fatal(err)
	}
	clock := newManagedClock()
	entered, releaseRead := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := c.PollManaged(context.Background(), s, Dependencies{
			Read: func(context.Context, PR, func() bool) (Snapshot, error) {
				close(entered)
				<-releaseRead
				changed := managedSnapshot()
				changed["head_sha"] = "discarded"
				return changed, nil
			},
			Now: clock.Now, Wait: clock.pollingWait,
		})
		done <- err
	}()
	<-entered
	if err := c.Yield(r.TicketFile, specChild); err != nil {
		t.Fatal(err)
	}
	close(releaseRead)
	if err := <-done; !errors.Is(err, ErrYielded) {
		t.Fatal(err)
	}
	state, err := ReadState(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	after, err := json.Marshal(state.Collecting)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("collection changed across yield:\n%s\n%s", before, after)
	}
}

func TestManagedYieldStopsCollectionBeforeNextGitHubCall(t *testing.T) {
	c, r, s := boundFixture(t)
	clock := newManagedClock()
	firstRead, releaseFirst := make(chan struct{}), make(chan struct{})
	secondRead := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := c.PollManaged(context.Background(), s, Dependencies{
			Read: func(ctx context.Context, pr PR, cancelled func() bool) (Snapshot, error) {
				return Collect(ctx, pr, func(context.Context, ...string) (any, error) {
					select {
					case <-firstRead:
						close(secondRead)
						return nil, errors.New("second GitHub call ran after yield")
					default:
						close(firstRead)
						<-releaseFirst
						return map[string]any{
							"headRefOid": "abc123", "state": "OPEN", "isDraft": false,
							"mergeable": "MERGEABLE", "mergeStateStatus": "CLEAN",
						}, nil
					}
				}, cancelled)
			},
			Now: clock.Now, Wait: clock.pollingWait,
		})
		done <- err
	}()
	<-firstRead
	if err := c.Yield(r.TicketFile, specChild); err != nil {
		t.Fatal(err)
	}
	close(releaseFirst)
	if err := <-done; !errors.Is(err, ErrYielded) {
		t.Fatal(err)
	}
	select {
	case <-secondRead:
		t.Fatal("collection made another GitHub call after yield")
	default:
	}
	release, err := s.Lock(false)
	if err != nil {
		t.Fatal(err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
}

func TestManagedYieldKeepsExactPendingBytes(t *testing.T) {
	c, r, s := boundFixture(t)
	clock := newManagedClock()
	entered, releaseRead := make(chan struct{}), make(chan struct{})
	done := make(chan struct {
		raw json.RawMessage
		err error
	}, 1)
	go func() {
		raw, err := c.PollManaged(context.Background(), s, Dependencies{
			Read: func(context.Context, PR, func() bool) (Snapshot, error) {
				close(entered)
				<-releaseRead
				return managedSnapshot(), nil
			},
			Now: clock.Now, Wait: clock.pollingWait,
		})
		done <- struct {
			raw json.RawMessage
			err error
		}{raw, err}
	}()
	<-entered
	var pending json.RawMessage
	if err := s.Update(func(tx *Store) error {
		_, err := tx.Stage("update", nil, Changes{"draft": map[string]any{"before": false, "after": true}}, nil, nil, specTime)
		pending = append(pending, tx.Data.Pending...)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := c.Yield(r.TicketFile, specChild); err != nil {
		t.Fatal(err)
	}
	close(releaseRead)
	result := <-done
	if result.err != nil || len(result.raw) == 0 {
		t.Fatal(result.err, string(result.raw))
	}
	state, err := ReadState(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(pending, state.Pending) {
		t.Fatalf("pending changed across yield:\n%s\n%s", pending, state.Pending)
	}
}

func TestManagedCancellationReleasesWorkerLock(t *testing.T) {
	c, _, s := boundFixture(t)
	clock := newManagedClock()
	ctx, cancel := context.WithCancel(context.Background())
	entered := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := c.PollManaged(ctx, s, Dependencies{
			Read: func(ctx context.Context, _ PR, _ func() bool) (Snapshot, error) {
				close(entered)
				<-ctx.Done()
				return nil, ctx.Err()
			},
			Now: clock.Now, Wait: clock.pollingWait,
		})
		done <- err
	}()
	<-entered
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	release, err := s.Lock(false)
	if err != nil {
		t.Fatal(err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	state, err := ReadState(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !state.Control.Worker.Ended {
		t.Fatal("execution was not marked ended")
	}
}

func TestManagedHeartbeatPersistenceFailureIsReturned(t *testing.T) {
	c, _, s := boundFixture(t)
	clock := newManagedClock()
	entered := make(chan struct{})
	tick := make(chan struct{})
	var once sync.Once
	wait := func(ctx context.Context, d time.Duration) error {
		if d == 5*time.Second {
			once.Do(func() { <-tick })
			return nil
		}
		<-ctx.Done()
		return ctx.Err()
	}
	done := make(chan error, 1)
	go func() {
		_, err := c.PollManaged(context.Background(), s, Dependencies{
			Read: func(ctx context.Context, _ PR, _ func() bool) (Snapshot, error) {
				close(entered)
				<-ctx.Done()
				return nil, ctx.Err()
			},
			Now: clock.Now, Wait: wait,
		})
		done <- err
	}()
	<-entered
	if err := os.WriteFile(s.Path, []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	close(tick)
	if err := <-done; err == nil || errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "JSON") {
		t.Fatal(err)
	}
}

func TestManagedRestartCollectingGetsFullQuietWindow(t *testing.T) {
	c, _, s := boundFixture(t)
	if err := s.Update(func(tx *Store) error {
		tx.Data.Snapshot = managedSnapshot()
		changed := managedSnapshot()
		changed["head_sha"] = "persisted"
		_, err := tx.Observe(changed, specTime.Add(-time.Hour))
		tx.Data.Control.Cycle.QuietDeadline = specTime.Add(-time.Minute)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	clock := newManagedClock()
	var starts []int
	raw, err := c.PollManaged(context.Background(), s, Dependencies{
		Read: func(context.Context, PR, func() bool) (Snapshot, error) {
			starts = append(starts, int(clock.Now().Sub(specTime)/time.Second))
			changed := managedSnapshot()
			changed["head_sha"] = "persisted"
			return changed, nil
		},
		Now: clock.Now, Wait: clock.pollingWait,
	})
	if err != nil {
		t.Fatal(err)
	}
	var event Event
	if err := json.Unmarshal(raw, &event); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(starts, []int{0, int(s.Data.Control.Quiet / time.Second)}) || len(event.Observations) != 1 {
		t.Fatal(starts, event)
	}
}

func TestManagedNewExecutionClearsOldQuietDeadline(t *testing.T) {
	c, _, s := boundFixture(t)
	if err := s.Update(func(tx *Store) error {
		tx.Data.Snapshot = managedSnapshot()
		tx.Data.Control.Cycle.QuietDeadline = specTime.Add(-time.Minute)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	clock := newManagedClock()
	want := errors.New("wait reached")
	reads := 0
	_, err := c.PollManaged(context.Background(), s, Dependencies{
		Read: func(context.Context, PR, func() bool) (Snapshot, error) {
			reads++
			return managedSnapshot(), nil
		},
		Now: clock.Now,
		Wait: func(ctx context.Context, d time.Duration) error {
			if d == 5*time.Second {
				<-ctx.Done()
				return ctx.Err()
			}
			return want
		},
	})
	if !errors.Is(err, want) || reads != 1 {
		t.Fatal(err, reads)
	}
	state, readErr := ReadState(s.Path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !state.Control.Cycle.QuietDeadline.IsZero() {
		t.Fatal(state.Control.Cycle.QuietDeadline)
	}
}

func TestWorkerStatusRequiresLockAndFreshHeartbeat(t *testing.T) {
	c, _, s := boundFixture(t)
	now := specTime
	c.Now = func() time.Time { return now }
	executionID := "00000000-0000-4000-8000-000000000004"
	if err := s.Update(func(tx *Store) error {
		tx.Data.Control.Worker = Execution{ID: executionID, HeartbeatAt: specTime}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	release, err := s.Lock(false)
	if err != nil {
		t.Fatal(err)
	}
	status, err := c.WorkerStatus(s)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Locked || !status.Fresh || status.ExecutionID != executionID {
		t.Fatal(status)
	}
	now = specTime.Add(91 * time.Second)
	status, err = c.WorkerStatus(s)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Locked || status.Fresh {
		t.Fatal(status)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	status, err = c.WorkerStatus(s)
	if err != nil {
		t.Fatal(err)
	}
	if status.Locked || status.Fresh {
		t.Fatal(status)
	}
}
