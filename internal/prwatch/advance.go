package prwatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

func (c *Controller) Advance(ctx context.Context, ticketFile, agentID string, deps Dependencies) (Action, error) {
	s, err := c.Bind(ticketFile, agentID)
	if err != nil {
		return Action{}, err
	}
	var action Action
	terminal := false
	err = managedUpdate(s, func(tx *Store) error {
		if err := tx.CommitAccepted(); err != nil {
			return err
		}
		control := tx.Data.Control
		if control.Stage == NeedsAttention {
			action = attentionAction(control, s.Path)
			return nil
		}
		if hasPending(tx) {
			action, err = tx.Offer(c.Now())
			if err == nil && action.Action == "attention" {
				action = attentionAction(tx.Data.Control, s.Path)
			}
			return err
		}
		terminal = tx.Data.Finished
		return nil
	})
	if err != nil || action.Action != "" {
		return action, err
	}
	if terminal {
		return c.finishAdvance(s)
	}

	event, err := c.PollManaged(ctx, s, deps)
	if err != nil {
		switch {
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			return Action{}, err
		case errors.Is(err, ErrBusy):
			return c.busyAdvance(s)
		case errors.Is(err, ErrYielded):
			return c.yieldedAdvance(s)
		default:
			return c.faultAdvance(s, "poll_failed", err)
		}
	}
	if len(event) == 0 {
		return c.faultAdvance(s, "poll_failed", errors.New("managed poll returned no event"))
	}
	err = managedUpdate(s, func(tx *Store) error {
		action, err = tx.Offer(c.Now())
		if err == nil && action.Action == "attention" {
			action = attentionAction(tx.Data.Control, s.Path)
		}
		return err
	})
	return action, err
}

// Inspect reports ticket-scoped worker state without polling, offering a
// delivery message, or advancing durable progress.
func (c *Controller) Inspect(ticketFile, agentID string) (Action, error) {
	if !validUUID(agentID) {
		return Action{}, errors.New("agent_id must be a UUID")
	}
	store, err := c.bindTicket(ticketFile, agentID, false)
	if err != nil {
		return Action{}, err
	}
	control := store.Data.Control
	if control.Stage == NeedsAttention {
		return attentionAction(control, store.Path), nil
	}
	worker, err := c.WorkerStatus(store)
	if err != nil {
		return Action{}, err
	}
	if worker.Locked {
		action := advanceAction(control, "wait", "worker_running", 1)
		action.ThreadID = control.Worker.HostHandle
		return action, nil
	}
	if store.Data.Finished {
		// Terminal business state may be durable before finishAdvance has
		// settled the stage and worker metadata. Keep the child alive to run
		// advance again until cleanup is durably confirmed.
		if control.Stage != Finished || hasPending(store) || store.Data.Collecting != nil || control.Outbox != nil || control.Worker.ID != "" && !control.Worker.Ended {
			return advanceAction(control, "wait", "worker_released", 1), nil
		}
		return advanceAction(control, "finished", "", 0), nil
	}
	if control.Stage == Finished {
		return advanceAction(control, "wait", "worker_released", 1), nil
	}
	return advanceAction(control, "wait", "worker_released", 1), nil
}

func advanceAction(control *Control, action, reason string, retry int) Action {
	return Action{
		SchemaVersion: 1, Action: action, WatchID: control.WatchID,
		RetryAfterSeconds: retry, Reason: reason,
	}
}

func (c *Controller) finishAdvance(s *Store) (Action, error) {
	release, err := s.Lock(false)
	if errors.Is(err, ErrLocked) {
		state, readErr := ReadState(s.Path)
		if readErr != nil {
			return Action{}, readErr
		}
		return advanceAction(state.Control, "wait", "worker_active", 1), nil
	}
	if err != nil {
		return Action{}, err
	}
	err = managedUpdate(s, func(tx *Store) error {
		if !tx.Data.Finished || hasPending(tx) || tx.Data.Collecting != nil || tx.Data.Control.Outbox != nil {
			return errors.New("terminal cleanup requires settled business state")
		}
		tx.Data.Control.Stage = Finished
		return nil
	})
	releaseErr := release()
	if err != nil {
		return Action{}, err
	}
	if releaseErr != nil {
		return Action{}, releaseErr
	}
	return advanceAction(s.Data.Control, "finished", "", 0), nil
}

func (c *Controller) busyAdvance(s *Store) (Action, error) {
	state, err := ReadState(s.Path)
	if err != nil {
		return Action{}, err
	}
	control := state.Control
	if control.Worker.HostHandle == "" {
		return Action{}, errors.New("active worker has no host handle")
	}
	action := advanceAction(control, "wait", "worker_active", 1)
	action.ThreadID = control.Worker.HostHandle
	return action, nil
}

func (c *Controller) yieldedAdvance(s *Store) (Action, error) {
	state, err := ReadState(s.Path)
	if err != nil {
		return Action{}, err
	}
	if state.Control.Stage == NeedsAttention {
		return attentionAction(state.Control, s.Path), nil
	}
	return advanceAction(state.Control, "wait", "worker_yielded", 1), nil
}

func (c *Controller) faultAdvance(s *Store, code string, cause error) (Action, error) {
	var action Action
	err := managedUpdate(s, func(tx *Store) error {
		control := tx.Data.Control
		control.Stage = NeedsAttention
		control.FailureCode = code
		control.FailureDetail = cause.Error()
		control.FaultSeen = true
		action = attentionAction(control, s.Path)
		return nil
	})
	return action, err
}

func attentionAction(control *Control, stateFile string) Action {
	action := advanceAction(control, "attention", control.FailureCode, 0)
	action.EventID = ""
	action.Part = 0
	action.Parts = 0
	action.ThreadID = control.ParentID
	action.Prompt = fmt.Sprintf("Goldilocks PR watcher needs attention for watch %s. Failure code: %s. State file: %s.", control.WatchID, control.FailureCode, stateFile)
	return action
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
		snapshot, readErr := deps.Read(workerCtx, s.PR, func() bool {
			return stopped(s) || executionEnded(s, executionID)
		})
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
	pending, err := stopMarkerPending(s)
	return err == nil && pending
}

func executionEnded(s *Store, executionID string) bool {
	state, err := ReadState(s.Path)
	return err != nil || state.Control == nil || state.Control.Worker.ID != executionID || state.Control.Worker.Ended
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
