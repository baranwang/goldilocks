package prwatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"
)

type PollOutcome struct {
	Cycle PollCycle
	Event json.RawMessage
	Delay time.Duration
}

type WorkerStatus struct {
	Locked      bool
	Fresh       bool
	ExecutionID string
}

var ErrYielded = errors.New("current execution yielded; state preserved")
var ErrBusy = errors.New("watch execution already running")

func (s *Store) ApplyPoll(snapshot Snapshot, readErr error, started, observed time.Time,
	options Options, cycle PollCycle) (PollOutcome, error) {
	out := PollOutcome{Cycle: cycle}
	if errors.Is(readErr, ErrStopped) {
		out.Event, readErr = s.Stop(observed)
		return out, readErr
	}
	if readErr != nil {
		var failure *ReadError
		if !errors.As(readErr, &failure) {
			return out, readErr
		}
		out.Cycle.Failures++
		out.Cycle.ReadFailed = true
		if out.Cycle.Failures >= 3 {
			var err error
			out.Event, err = s.Failure(readErr.Error(), observed)
			if err != nil || out.Event != nil {
				return out, err
			}
		}
	} else {
		out.Cycle.Failures = 0
		kind, err := s.Observe(snapshot, observed)
		if err != nil {
			return out, err
		}
		if kind == "initial" || kind == "merged" || kind == "closed" ||
			kind == "recovered" && s.Data.Snapshot == nil {
			out.Event, err = s.Freeze("", observed)
			return out, err
		}
		if kind != "" || cycle.ReadFailed && s.Data.Collecting != nil {
			out.Cycle.QuietDeadline = observed.Add(options.Quiet)
		}
		if s.Data.Collecting != nil && kind == "" && !cycle.ReadFailed &&
			!out.Cycle.QuietDeadline.IsZero() && !started.Before(out.Cycle.QuietDeadline) {
			out.Event, err = s.Freeze("", observed)
			return out, err
		}
		out.Cycle.ReadFailed = false
	}
	out.Delay = min(options.Interval, 300*time.Second)
	for range min(out.Cycle.Failures, 3) {
		out.Delay = min(2*out.Delay, 300*time.Second)
	}
	if !out.Cycle.ReadFailed && !out.Cycle.QuietDeadline.IsZero() {
		out.Delay = min(out.Delay, max(time.Duration(0), out.Cycle.QuietDeadline.Sub(observed)))
	}
	return out, nil
}

func (c *Controller) PollManaged(ctx context.Context, s *Store, deps Dependencies) (event json.RawMessage, resultErr error) {
	release, err := s.Lock(false)
	if errors.Is(err, ErrLocked) {
		return nil, ErrBusy
	}
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := release(); resultErr == nil && err != nil {
			resultErr = err
		}
	}()
	if deps.Read == nil || deps.Now == nil || deps.Wait == nil {
		return nil, errors.New("managed polling dependencies are required")
	}

	executionID, err := newUUID()
	if err != nil {
		return nil, err
	}
	var initial json.RawMessage
	err = managedUpdate(s, func(tx *Store) error {
		if tx.Data.Control == nil {
			return errors.New("managed state is missing control")
		}
		if hasPending(tx) {
			var err error
			initial, err = tx.PendingEvent()
			return err
		}
		if tx.Data.Finished {
			var err error
			initial, err = finishedEnvelope(tx)
			return err
		}
		if stopped(tx) {
			var err error
			initial, err = tx.Stop(deps.Now())
			return err
		}
		control := tx.Data.Control
		control.Worker = Execution{ID: executionID, HeartbeatAt: deps.Now().UTC()}
		control.Cycle = PollCycle{}
		if tx.Data.Collecting != nil {
			control.Cycle.QuietDeadline = deps.Now().Add(control.Quiet)
		}
		if control.InitialAccepted && !control.FaultSeen {
			control.Ready = true
			control.Stage = Running
		}
		return nil
	})
	if err != nil || initial != nil {
		return initial, err
	}

	workerCtx, cancelWorker := context.WithCancel(ctx)
	heartbeatCtx, cancelHeartbeat := context.WithCancel(workerCtx)
	heartbeatDone := make(chan struct{})
	heartbeatErr := make(chan error, 1)
	heartbeatStore := *s
	go func() {
		defer close(heartbeatDone)
		for {
			if err := deps.Wait(heartbeatCtx, 5*time.Second); err != nil {
				if heartbeatCtx.Err() == nil {
					heartbeatErr <- err
					cancelWorker()
				}
				return
			}
			active := false
			err := heartbeatStore.Update(func(tx *Store) error {
				worker := &tx.Data.Control.Worker
				if worker.ID != executionID || worker.Ended {
					return nil
				}
				active = true
				worker.HeartbeatAt = deps.Now().UTC()
				return nil
			})
			if errors.Is(err, ErrLocked) {
				time.Sleep(time.Millisecond)
				continue
			}
			if err != nil {
				heartbeatErr <- fmt.Errorf("heartbeat persistence: invalid state JSON: %w", err)
				cancelWorker()
				return
			}
			if !active {
				return
			}
		}
	}()
	defer func() {
		cancelHeartbeat()
		<-heartbeatDone
		if err := managedUpdate(s, func(tx *Store) error {
			if tx.Data.Control != nil && tx.Data.Control.Worker.ID == executionID {
				tx.Data.Control.Worker.Ended = true
			}
			return nil
		}); resultErr == nil && err != nil {
			resultErr = err
		}
	}()

	for {
		if err := managedHeartbeatError(heartbeatErr); err != nil {
			return nil, err
		}
		var before json.RawMessage
		var yielded bool
		err := managedUpdate(s, func(tx *Store) error {
			if hasPending(tx) {
				var err error
				before, err = tx.PendingEvent()
				return err
			}
			if tx.Data.Finished {
				var err error
				before, err = finishedEnvelope(tx)
				return err
			}
			if stopped(tx) {
				var err error
				before, err = tx.Stop(deps.Now())
				return err
			}
			control := tx.Data.Control
			if control.Worker.ID != executionID || control.Worker.Ended {
				yielded = true
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		if yielded {
			return nil, ErrYielded
		}
		if before != nil {
			return before, nil
		}

		started := deps.Now()
		snapshot, readErr := deps.Read(workerCtx, s.PR, func() bool { return stopped(s) })
		if err := managedHeartbeatError(heartbeatErr); err != nil {
			return nil, err
		}
		var outcome PollOutcome
		var after json.RawMessage
		yielded = false
		err = managedUpdate(s, func(tx *Store) error {
			if hasPending(tx) {
				var err error
				after, err = tx.PendingEvent()
				return err
			}
			if tx.Data.Finished {
				var err error
				after, err = finishedEnvelope(tx)
				return err
			}
			if stopped(tx) {
				var err error
				after, err = tx.Stop(deps.Now())
				return err
			}
			control := tx.Data.Control
			if control.Worker.ID != executionID || control.Worker.Ended {
				yielded = true
				return nil
			}
			var err error
			outcome, err = tx.ApplyPoll(snapshot, readErr, started, deps.Now(), Options{Interval: control.Interval, Quiet: control.Quiet}, control.Cycle)
			if err == nil {
				control.Cycle = outcome.Cycle
				if readErr == nil {
					control.Progress++
				}
			}
			return err
		})
		if err != nil {
			return nil, err
		}
		if yielded {
			return nil, ErrYielded
		}
		if after != nil {
			return after, nil
		}
		if outcome.Event != nil {
			return outcome.Event, nil
		}

		next := deps.Now().Add(outcome.Delay)
		for deps.Now().Before(next) {
			if err := managedHeartbeatError(heartbeatErr); err != nil {
				return nil, err
			}
			if err := deps.Wait(workerCtx, min(time.Second, next.Sub(deps.Now()))); err != nil {
				if heartbeatErr := managedHeartbeatError(heartbeatErr); heartbeatErr != nil {
					return nil, heartbeatErr
				}
				return nil, err
			}
		}
	}
}

func managedHeartbeatError(errors <-chan error) error {
	select {
	case err := <-errors:
		return err
	default:
		return nil
	}
}

func managedUpdate(s *Store, fn func(*Store) error) error {
	for {
		err := s.Update(fn)
		if !errors.Is(err, ErrLocked) {
			return err
		}
		time.Sleep(time.Millisecond)
	}
}

func stopped(s *Store) bool {
	_, err := os.Stat(s.StopPath)
	return err == nil
}

func (c *Controller) Yield(ticketFile, agentID string) error {
	if !validUUID(agentID) {
		return errors.New("agent_id must be a UUID")
	}
	ticket, stateFile, err := c.readManagedTicket(ticketFile)
	if err != nil {
		return err
	}
	s, err := c.Open(ticket.ParentID, ticket.PRURL)
	if err != nil {
		return err
	}
	if s.Path != stateFile {
		return errors.New("ticket state path mismatch")
	}
	return s.Update(func(tx *Store) error {
		if !ticketMatches(tx.Data, ticket) || tx.Data.Control.AgentID != agentID {
			return errors.New("ticket is not authorized for this child")
		}
		tx.Data.Control.Worker.Ended = true
		return nil
	})
}

func (c *Controller) WorkerStatus(s *Store) (WorkerStatus, error) {
	status := WorkerStatus{}
	release, err := s.Lock(false)
	if errors.Is(err, ErrLocked) {
		status.Locked = true
	} else if err != nil {
		return status, err
	} else if err := release(); err != nil {
		return status, err
	}
	state, err := ReadState(s.Path)
	if err != nil {
		return status, err
	}
	if state.Control == nil {
		return status, fmt.Errorf("managed state is missing control")
	}
	status.ExecutionID = state.Control.Worker.ID
	if status.Locked && !state.Control.Worker.HeartbeatAt.IsZero() {
		now := time.Now()
		if c.Now != nil {
			now = c.Now()
		}
		status.Fresh = !now.After(state.Control.Worker.HeartbeatAt.Add(90 * time.Second))
	}
	return status, nil
}
