package prwatch_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strconv"
	"testing"
	"time"

	pw "github.com/baranwang/goldilocks/internal/prwatch"
)

type fakeClock struct{ start, now time.Time }

func newClock() *fakeClock {
	at := time.Unix(0, 0)
	return &fakeClock{start: at, now: at}
}

func (c *fakeClock) Now() time.Time { return c.now }

func (c *fakeClock) Wait(ctx context.Context, d time.Duration) error {
	c.now = c.now.Add(d)
	return ctx.Err()
}

func (c *fakeClock) Seconds() int { return int(c.now.Sub(c.start) / time.Second) }

func changedSnapshot(body string) pw.Snapshot {
	snapshot := emptySnapshot()
	snapshot["comments"] = map[string]any{"1": map[string]any{"body": body, "url": testPR}}
	return snapshot
}

func eventFrom(t *testing.T, raw json.RawMessage) pw.Event {
	t.Helper()
	var event pw.Event
	check(t, json.Unmarshal(raw, &event))
	return event
}

func ackInitial(t *testing.T, s *pw.Store, c *fakeClock) {
	t.Helper()
	kind, err := s.Observe(emptySnapshot(), c.Now())
	check(t, err)
	if kind != "initial" {
		t.Fatalf("initial observation returned %q", kind)
	}
	raw, err := s.Freeze("", c.Now())
	check(t, err)
	check(t, s.Ack(eventFrom(t, raw).EventID))
}

func observedCommentBodies(t *testing.T, observations []pw.Observation) []string {
	t.Helper()
	var bodies []string
	for _, observation := range observations {
		comments, ok := observation.Changes["comments"].(map[string]any)
		if !ok {
			continue
		}
		comment, ok := comments["1"].(map[string]any)
		if !ok {
			t.Fatalf("bad comment delta: %#v", comments)
		}
		body, ok := comment["body"].(string)
		if !ok {
			t.Fatalf("bad comment body: %#v", comment)
		}
		bodies = append(bodies, body)
	}
	return bodies
}

func TestQuietResetsAtZeroSixTwelve(t *testing.T) {
	s := mustStore(t, t.TempDir())
	release, err := s.Lock(true)
	check(t, err)
	defer release()
	c := newClock()
	ackInitial(t, s, c)

	var starts []int
	read := func(ctx context.Context, pr pw.PR, cancelled func() bool) (pw.Snapshot, error) {
		second := c.Seconds()
		starts = append(starts, second)
		return changedSnapshot(strconv.Itoa(min(second/6, 2))), nil
	}
	raw, err := pw.Watch(context.Background(), s, pw.Options{Interval: 6 * time.Second, Quiet: 10 * time.Second}, pw.Dependencies{Read: read, Now: c.Now, Wait: c.Wait})
	check(t, err)
	event := eventFrom(t, raw)
	if !reflect.DeepEqual(starts, []int{0, 6, 12, 18, 22}) || len(event.Observations) != 3 ||
		!reflect.DeepEqual(observedCommentBodies(t, event.Observations), []string{"0", "1", "2"}) ||
		event.ObservedAt != c.start.Add(12*time.Second).UTC().Format(time.RFC3339Nano) ||
		event.Changes["comments"].(map[string]any)["1"].(map[string]any)["body"] != "2" {
		t.Fatal(starts, event)
	}
}

func TestQuietCadence(t *testing.T) {
	for _, tc := range []struct {
		interval int
		starts   []int
	}{{60, []int{0, 10}}, {6, []int{0, 6, 10}}, {5, []int{0, 5, 10}}} {
		t.Run(strconv.Itoa(tc.interval), func(t *testing.T) {
			s := mustStore(t, t.TempDir())
			release, err := s.Lock(true)
			check(t, err)
			defer release()
			c := newClock()
			ackInitial(t, s, c)
			var starts []int
			read := func(ctx context.Context, pr pw.PR, cancelled func() bool) (pw.Snapshot, error) {
				starts = append(starts, c.Seconds())
				return changedSnapshot("changed"), nil
			}
			raw, err := pw.Watch(context.Background(), s, pw.Options{Interval: time.Duration(tc.interval) * time.Second, Quiet: 10 * time.Second}, pw.Dependencies{Read: read, Now: c.Now, Wait: c.Wait})
			check(t, err)
			event := eventFrom(t, raw)
			if !reflect.DeepEqual(starts, tc.starts) || len(event.Observations) != 1 {
				t.Fatal(tc, starts, event)
			}
		})
	}
}

func TestReadCrossingDeadlineRequiresAnotherObservation(t *testing.T) {
	s := mustStore(t, t.TempDir())
	release, err := s.Lock(true)
	check(t, err)
	defer release()
	c := newClock()
	ackInitial(t, s, c)
	var starts []int
	read := func(ctx context.Context, pr pw.PR, cancelled func() bool) (pw.Snapshot, error) {
		starts = append(starts, c.Seconds())
		if c.Seconds() == 6 {
			check(t, c.Wait(ctx, 15*time.Second))
		}
		return changedSnapshot("changed"), nil
	}
	raw, err := pw.Watch(context.Background(), s, pw.Options{Interval: 6 * time.Second, Quiet: 10 * time.Second}, pw.Dependencies{Read: read, Now: c.Now, Wait: c.Wait})
	check(t, err)
	if event := eventFrom(t, raw); !reflect.DeepEqual(starts, []int{0, 6, 21}) || len(event.Observations) != 1 {
		t.Fatal(starts, event)
	}
}

func TestNoChangeWaitsUntilStop(t *testing.T) {
	s := mustStore(t, t.TempDir())
	release, err := s.Lock(true)
	check(t, err)
	defer release()
	c := newClock()
	ackInitial(t, s, c)
	var starts []int
	read := func(ctx context.Context, pr pw.PR, cancelled func() bool) (pw.Snapshot, error) {
		starts = append(starts, c.Seconds())
		return emptySnapshot(), nil
	}
	wait := func(ctx context.Context, d time.Duration) error {
		if err := c.Wait(ctx, d); err != nil {
			return err
		}
		if c.Seconds() == 30 {
			return os.WriteFile(s.StopPath, []byte("stop\n"), 0600)
		}
		return nil
	}
	raw, err := pw.Watch(context.Background(), s, pw.Options{Interval: 6 * time.Second, Quiet: 10 * time.Second}, pw.Dependencies{Read: read, Now: c.Now, Wait: wait})
	check(t, err)
	event := eventFrom(t, raw)
	observedAt, err := time.Parse(time.RFC3339Nano, event.ObservedAt)
	check(t, err)
	if event.Type != "stopped" || observedAt.Before(c.start.Add(30*time.Second)) || !reflect.DeepEqual(starts, []int{0, 6, 12, 18, 24}) {
		t.Fatal(starts, event)
	}
}

func TestTerminalKeepsEveryIntermediateDelta(t *testing.T) {
	for _, terminal := range []string{"MERGED", "CLOSED"} {
		t.Run(terminal, func(t *testing.T) {
			s := mustStore(t, t.TempDir())
			release, err := s.Lock(true)
			check(t, err)
			defer release()
			c := newClock()
			ackInitial(t, s, c)
			var starts []int
			read := func(ctx context.Context, pr pw.PR, cancelled func() bool) (pw.Snapshot, error) {
				second := c.Seconds()
				starts = append(starts, second)
				if second == 15 {
					snapshot := emptySnapshot()
					snapshot["state"] = terminal
					snapshot["mergeable"] = "UNKNOWN"
					snapshot["merge_state"] = "UNKNOWN"
					return snapshot, nil
				}
				return changedSnapshot(strconv.Itoa(min(second/6, 2))), nil
			}
			raw, err := pw.Watch(context.Background(), s, pw.Options{Interval: 3 * time.Second, Quiet: 10 * time.Second}, pw.Dependencies{Read: read, Now: c.Now, Wait: c.Wait})
			check(t, err)
			event := eventFrom(t, raw)
			if event.Type != string(bytes.ToLower([]byte(terminal))) ||
				!reflect.DeepEqual(starts, []int{0, 3, 6, 9, 12, 15}) || len(event.Observations) != 4 ||
				!reflect.DeepEqual(observedCommentBodies(t, event.Observations), []string{"0", "1", "2"}) || event.Changes["comments_removed"] != nil {
				t.Fatal(starts, event)
			}
		})
	}
}

func TestGraceStopFreezesCollectedEvidence(t *testing.T) {
	s := mustStore(t, t.TempDir())
	release, err := s.Lock(true)
	check(t, err)
	defer release()
	c := newClock()
	ackInitial(t, s, c)
	var starts []int
	read := func(ctx context.Context, pr pw.PR, cancelled func() bool) (pw.Snapshot, error) {
		starts = append(starts, c.Seconds())
		return changedSnapshot("changed"), nil
	}
	wait := func(ctx context.Context, d time.Duration) error {
		if err := c.Wait(ctx, d); err != nil {
			return err
		}
		if c.Seconds() == 5 {
			return os.WriteFile(s.StopPath, []byte("stop\n"), 0600)
		}
		return nil
	}
	raw, err := pw.Watch(context.Background(), s, pw.Options{Interval: 6 * time.Second, Quiet: 10 * time.Second}, pw.Dependencies{Read: read, Now: c.Now, Wait: wait})
	check(t, err)
	event := eventFrom(t, raw)
	if event.Type != "stopped" || c.Seconds() != 5 || !reflect.DeepEqual(starts, []int{0}) ||
		len(event.Observations) != 1 || !reflect.DeepEqual(observedCommentBodies(t, event.Observations), []string{"changed"}) {
		t.Fatal(starts, event)
	}
}

func TestRestartWaitsAFullQuietWindowWithoutDuplicatingEvidence(t *testing.T) {
	dir := t.TempDir()
	s := mustStore(t, dir)
	release, err := s.Lock(true)
	check(t, err)
	firstClock := newClock()
	ackInitial(t, s, firstClock)
	_, err = s.Observe(changedSnapshot("persisted"), firstClock.Now())
	check(t, err)
	check(t, release())

	s = mustStore(t, dir)
	release, err = s.Lock(true)
	check(t, err)
	defer release()
	c := newClock()
	var starts []int
	read := func(ctx context.Context, pr pw.PR, cancelled func() bool) (pw.Snapshot, error) {
		starts = append(starts, c.Seconds())
		return changedSnapshot("persisted"), nil
	}
	raw, err := pw.Watch(context.Background(), s, pw.Options{Interval: 6 * time.Second, Quiet: 10 * time.Second}, pw.Dependencies{Read: read, Now: c.Now, Wait: c.Wait})
	check(t, err)
	event := eventFrom(t, raw)
	if !reflect.DeepEqual(starts, []int{0, 6, 10}) || len(event.Observations) != 1 {
		t.Fatal(starts, event)
	}
}

func TestErrorDoesNotAdvanceBaselineAndRecoveryResetsQuiet(t *testing.T) {
	s := mustStore(t, t.TempDir())
	release, err := s.Lock(true)
	check(t, err)
	defer release()
	c := newClock()
	ackInitial(t, s, c)
	var starts []int
	failing := true
	read := func(ctx context.Context, pr pw.PR, cancelled func() bool) (pw.Snapshot, error) {
		second := c.Seconds()
		starts = append(starts, second)
		if failing && second > 0 {
			return nil, &pw.ReadError{Err: errors.New("outage")}
		}
		return changedSnapshot("changed"), nil
	}
	options := pw.Options{Interval: time.Second, Quiet: 10 * time.Second}
	deps := pw.Dependencies{Read: read, Now: c.Now, Wait: c.Wait}
	raw, err := pw.Watch(context.Background(), s, options, deps)
	check(t, err)
	errorEvent := eventFrom(t, raw)
	if errorEvent.Type != "error" || c.Seconds() != 7 || s.Data.Snapshot["comments"].(map[string]any)["1"] != nil ||
		s.Data.Collecting == nil || len(s.Data.Collecting.Observations) != 1 {
		t.Fatal(starts, errorEvent, s.Data)
	}
	check(t, s.Ack(errorEvent.EventID))

	failing = false
	raw, err = pw.Watch(context.Background(), s, options, deps)
	check(t, err)
	recovered := eventFrom(t, raw)
	wantStarts := []int{0, 1, 3, 7, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17}
	recoveredCount := 0
	for _, observation := range recovered.Observations {
		if observation.Type == "recovered" {
			recoveredCount++
		}
	}
	if !reflect.DeepEqual(starts, wantStarts) || recovered.Type != "recovered" || recovered.Error != nil ||
		len(recovered.Observations) != 2 || recoveredCount != 1 {
		t.Fatal(starts, recovered)
	}
}

func TestTransientReadFailureResetsQuietWindow(t *testing.T) {
	s := mustStore(t, t.TempDir())
	release, err := s.Lock(true)
	check(t, err)
	defer release()
	c := newClock()
	ackInitial(t, s, c)
	failed := false
	var starts []int
	read := func(ctx context.Context, pr pw.PR, cancelled func() bool) (pw.Snapshot, error) {
		starts = append(starts, c.Seconds())
		if c.Seconds() == 6 && !failed {
			failed = true
			return nil, &pw.ReadError{Err: errors.New("transient")}
		}
		return changedSnapshot("changed"), nil
	}
	raw, err := pw.Watch(context.Background(), s, pw.Options{Interval: 6 * time.Second, Quiet: 10 * time.Second}, pw.Dependencies{Read: read, Now: c.Now, Wait: c.Wait})
	check(t, err)
	event := eventFrom(t, raw)
	if !reflect.DeepEqual(starts, []int{0, 6, 18, 24, 28}) || len(event.Observations) != 1 || event.Type != "update" {
		t.Fatal(starts, event)
	}
}

func TestCancellationKeepsPersistedCollecting(t *testing.T) {
	dir := t.TempDir()
	s := mustStore(t, dir)
	release, err := s.Lock(true)
	check(t, err)
	c := newClock()
	ackInitial(t, s, c)
	ctx, cancel := context.WithCancel(context.Background())
	wait := func(ctx context.Context, d time.Duration) error {
		cancel()
		return c.Wait(ctx, d)
	}
	raw, err := pw.Watch(ctx, s, pw.Options{Interval: 6 * time.Second, Quiet: 10 * time.Second}, pw.Dependencies{
		Read: func(context.Context, pw.PR, func() bool) (pw.Snapshot, error) {
			return changedSnapshot("persisted"), nil
		},
		Now: c.Now, Wait: wait,
	})
	if raw != nil || !errors.Is(err, context.Canceled) || s.Data.Collecting == nil || len(s.Data.Collecting.Observations) != 1 {
		t.Fatal(string(raw), err, s.Data)
	}
	check(t, release())
	reopened := mustStore(t, dir)
	_, pendingErr := reopened.PendingEvent()
	if reopened.Data.Collecting == nil || len(reopened.Data.Collecting.Observations) != 1 || pendingErr == nil {
		t.Fatal(reopened.Data)
	}
}

func TestPendingAndFinishedGuardsAvoidReadsAndMutation(t *testing.T) {
	s := mustStore(t, t.TempDir())
	release, err := s.Lock(true)
	check(t, err)
	defer release()
	c := newClock()
	_, err = s.Observe(emptySnapshot(), c.Now())
	check(t, err)
	pending, err := s.Freeze("", c.Now())
	check(t, err)
	check(t, os.WriteFile(s.StopPath, []byte("stop\n"), 0600))
	before, err := os.ReadFile(s.Path)
	check(t, err)
	kind, err := s.Observe(changedSnapshot("ignored"), c.Now())
	check(t, err)
	stopped, err := s.Stop(c.Now())
	check(t, err)
	reads := 0
	watchRaw, err := pw.Watch(context.Background(), s, pw.Options{Interval: time.Second, Quiet: time.Second}, pw.Dependencies{
		Read: func(context.Context, pw.PR, func() bool) (pw.Snapshot, error) {
			reads++
			return nil, errors.New("unexpected read")
		},
		Now: c.Now, Wait: c.Wait,
	})
	check(t, err)
	after, err := os.ReadFile(s.Path)
	check(t, err)
	if kind != "" || reads != 0 || !bytes.Equal(pending, stopped) || !bytes.Equal(pending, watchRaw) || !bytes.Equal(before, after) {
		t.Fatal(kind, reads, string(pending), string(stopped), string(watchRaw))
	}

	check(t, s.Ack(eventFrom(t, pending).EventID))
	stopRaw, err := s.Stop(c.Now())
	check(t, err)
	check(t, s.Ack(eventFrom(t, stopRaw).EventID))
	check(t, os.WriteFile(s.StopPath, []byte("stop\n"), 0600))
	kind, err = s.Observe(changedSnapshot("ignored"), c.Now())
	check(t, err)
	finished, err := pw.Watch(context.Background(), s, pw.Options{Interval: time.Second, Quiet: time.Second}, pw.Dependencies{
		Read: func(context.Context, pw.PR, func() bool) (pw.Snapshot, error) {
			reads++
			return nil, errors.New("unexpected read")
		},
		Now: c.Now, Wait: c.Wait,
	})
	check(t, err)
	var envelope map[string]any
	check(t, json.Unmarshal(finished, &envelope))
	if kind != "" || reads != 0 || envelope["source"] != "pr-watch" || envelope["type"] != "finished" || envelope["watcher_id"] != s.PR.Key {
		t.Fatal(kind, reads, envelope)
	}
}

func TestFailureSuppressesOnlyAcknowledgedMatchingMessage(t *testing.T) {
	s := mustStore(t, t.TempDir())
	release, err := s.Lock(true)
	check(t, err)
	defer release()
	c := newClock()
	ackInitial(t, s, c)
	raw, err := s.Failure("outage", c.Now())
	check(t, err)
	check(t, s.Ack(eventFrom(t, raw).EventID))
	before, err := os.ReadFile(s.Path)
	check(t, err)
	repeated, err := s.Failure("outage", c.Now())
	check(t, err)
	after, err := os.ReadFile(s.Path)
	check(t, err)
	different, err := s.Failure("different outage", c.Now())
	check(t, err)
	if repeated != nil || !bytes.Equal(before, after) || eventFrom(t, different).Type != "error" {
		t.Fatal(string(repeated), bytes.Equal(before, after), string(different))
	}
}

func TestWatchReturnsLocalErrorsWithoutRetrying(t *testing.T) {
	s := mustStore(t, t.TempDir())
	release, err := s.Lock(true)
	check(t, err)
	defer release()
	c := newClock()
	ackInitial(t, s, c)
	before, err := os.ReadFile(s.Path)
	check(t, err)
	kind, err := s.Observe(emptySnapshot(), c.Now().Add(time.Second))
	check(t, err)
	after, err := os.ReadFile(s.Path)
	check(t, err)
	if kind != "" || !bytes.Equal(before, after) {
		t.Fatal("unchanged observation rewrote state")
	}
	want := errors.New("local failure")
	reads, waits := 0, 0
	_, err = pw.Watch(context.Background(), s, pw.Options{Interval: time.Second, Quiet: time.Second}, pw.Dependencies{
		Read: func(context.Context, pw.PR, func() bool) (pw.Snapshot, error) { reads++; return nil, want },
		Now:  c.Now,
		Wait: func(ctx context.Context, d time.Duration) error { waits++; return c.Wait(ctx, d) },
	})
	if !errors.Is(err, want) || reads != 1 || waits != 0 || s.Data.Pending != nil {
		t.Fatal(err, reads, waits, s.Data)
	}
}
