package prwatch

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"time"
)

type Options struct {
	Interval time.Duration
	Quiet    time.Duration
}

type Dependencies struct {
	Read func(context.Context, PR, func() bool) (Snapshot, error)
	Now  func() time.Time
	Wait func(context.Context, time.Duration) error
}

func (s *Store) Observe(snapshot Snapshot, at time.Time) (string, error) {
	if hasPending(s) || s.Data.Finished {
		return "", nil
	}

	current, err := CloneSnapshot(snapshot)
	if err != nil {
		return "", err
	}
	previous := s.Data.Snapshot
	if s.Data.Collecting != nil {
		previous = s.Data.Collecting.Snapshot
	}

	state := current["state"].(string)
	terminal := state == "MERGED" || state == "CLOSED"
	if terminal && previous != nil {
		current, err = CloneSnapshot(previous)
		if err != nil {
			return "", err
		}
		for _, field := range []string{"head_sha", "state", "draft", "mergeable", "merge_state"} {
			current[field] = snapshot[field]
		}
	}

	changes, err := ChangesBetween(previous, current)
	if err != nil {
		return "", err
	}
	kind := ""
	switch {
	case terminal:
		kind = strings.ToLower(state)
	case s.Data.Error != nil && !sameError(s.Data.Collecting, *s.Data.Error):
		kind = "recovered"
	case previous == nil:
		kind = "initial"
	case len(changes) != 0:
		kind = "update"
	default:
		return "", nil
	}

	if len(changes) == 0 && s.Data.Collecting != nil && s.Data.Collecting.Kind == kind && terminal {
		return kind, nil
	}
	observation := Observation{
		Type:       kind,
		ObservedAt: at.UTC().Format(time.RFC3339Nano),
		HeadSHA:    current["head_sha"].(string),
		Changes:    changes,
	}
	batch := &Batch{Snapshot: current, Kind: kind, Observations: []Observation{observation}}
	if old := s.Data.Collecting; old != nil {
		batch.Kind = old.Kind
		batch.Observations = append(append([]Observation(nil), old.Observations...), observation)
		batch.RecoveredError = cloneString(old.RecoveredError)
	}
	if kind == "initial" || kind == "recovered" || terminal {
		batch.Kind = kind
	}
	if kind == "recovered" {
		batch.RecoveredError = cloneString(s.Data.Error)
	}

	old := s.Data.Collecting
	s.Data.Collecting = batch
	if err := s.Save(); err != nil {
		s.Data.Collecting = old
		return "", err
	}
	return kind, nil
}

func sameError(batch *Batch, message string) bool {
	return batch != nil && batch.RecoveredError != nil && *batch.RecoveredError == message
}

func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
}

func (s *Store) Freeze(kind string, at time.Time) (json.RawMessage, error) {
	if hasPending(s) {
		return s.PendingEvent()
	}
	if s.Data.Finished {
		return finishedEnvelope(s)
	}
	if s.Data.Collecting == nil {
		if kind == "" {
			return nil, errors.New("there are no observations to freeze")
		}
		return s.Stage(kind, s.Data.Snapshot, Changes{}, nil, nil, at)
	}
	batch := s.Data.Collecting
	if kind == "" {
		kind = batch.Kind
	}
	changes := batch.Observations[len(batch.Observations)-1].Changes
	return s.Stage(kind, batch.Snapshot, changes, nil, batch.Observations, at)
}

func (s *Store) Failure(message string, at time.Time) (json.RawMessage, error) {
	if hasPending(s) {
		return s.PendingEvent()
	}
	if s.Data.Finished || s.Data.Error != nil && *s.Data.Error == message {
		return nil, nil
	}
	return s.Stage("error", s.Data.Snapshot, Changes{}, &message, nil, at)
}

func (s *Store) Stop(at time.Time) (json.RawMessage, error) {
	if hasPending(s) {
		return s.PendingEvent()
	}
	if s.Data.Finished {
		return finishedEnvelope(s)
	}
	return s.Freeze("stopped", at)
}

func Watch(ctx context.Context, s *Store, options Options, deps Dependencies) (json.RawMessage, error) {
	cycle := PollCycle{}
	if s.Data.Collecting != nil {
		cycle.QuietDeadline = deps.Now().Add(options.Quiet)
	}
	stopped := func() bool {
		_, err := os.Stat(s.StopPath)
		return err == nil
	}

	for {
		if hasPending(s) {
			return s.PendingEvent()
		}
		if s.Data.Finished {
			return finishedEnvelope(s)
		}
		if stopped() {
			return s.Stop(deps.Now())
		}

		started := deps.Now()
		snapshot, readErr := deps.Read(ctx, s.PR, stopped)
		outcome, err := s.ApplyPoll(snapshot, readErr, started, deps.Now(), options, cycle)
		if err != nil || outcome.Event != nil {
			return outcome.Event, err
		}
		cycle = outcome.Cycle
		next := deps.Now().Add(outcome.Delay)
		for deps.Now().Before(next) && !stopped() {
			if err := deps.Wait(ctx, min(time.Second, next.Sub(deps.Now()))); err != nil {
				return nil, err
			}
		}
	}
}

func hasPending(s *Store) bool {
	return len(s.Data.Pending) != 0 && !isNull(s.Data.Pending)
}

func finishedEnvelope(s *Store) (json.RawMessage, error) {
	return encodeJSON(struct {
		Source    string `json:"source"`
		Type      string `json:"type"`
		WatcherID string `json:"watcher_id"`
	}{"pr-watch", "finished", s.PR.Key})
}
