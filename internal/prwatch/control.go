package prwatch

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"time"
)

type Stage string

const (
	Starting       Stage = "starting"
	Initializing   Stage = "initializing"
	Running        Stage = "running"
	Stopping       Stage = "stopping"
	NeedsAttention Stage = "needs_attention"
	Finished       Stage = "finished"
)

type PollCycle struct {
	QuietDeadline time.Time `json:"quiet_deadline"`
	Failures      int       `json:"failures"`
	ReadFailed    bool      `json:"read_failed"`
}

type Execution struct {
	ID          string    `json:"id"`
	HeartbeatAt time.Time `json:"heartbeat_at"`
	HostHandle  string    `json:"host_handle,omitempty"`
	Ended       bool      `json:"ended"`
}

type Receipt struct {
	CallID     string    `json:"call_id"`
	AgentID    string    `json:"agent_id"`
	ParentID   string    `json:"parent_id"`
	EventID    string    `json:"event_id"`
	Part       int       `json:"part"`
	SHA256     string    `json:"sha256"`
	AcceptedAt time.Time `json:"accepted_at"`
}

type DeliveryPart struct {
	Prompt    string    `json:"prompt,omitempty"`
	SHA256    string    `json:"sha256"`
	Offers    int       `json:"offers"`
	OfferedAt time.Time `json:"offered_at"`
	Receipt   *Receipt  `json:"receipt,omitempty"`
	Received  bool      `json:"received"`
}

type Delivery struct {
	EventID string         `json:"event_id"`
	Kind    string         `json:"kind"`
	Parts   []DeliveryPart `json:"parts"`
}

type Control struct {
	WatchID               string              `json:"watch_id"`
	ParentID              string              `json:"parent_id"`
	AgentID               string              `json:"agent_id"`
	Cwd                   string              `json:"cwd"`
	TicketSHA256          string              `json:"ticket_sha256"`
	CreatedAt             time.Time           `json:"created_at"`
	BindAfter             time.Time           `json:"bind_after"`
	Stage                 Stage               `json:"stage"`
	Interval              time.Duration       `json:"interval"`
	Quiet                 time.Duration       `json:"quiet"`
	InitialEventID        string              `json:"initial_event_id"`
	InitialAccepted       bool                `json:"initial_accepted"`
	RefreshRequired       bool                `json:"refresh_required"`
	Ready                 bool                `json:"ready"`
	Worker                Execution           `json:"worker"`
	Cycle                 PollCycle           `json:"cycle"`
	Outbox                *Delivery           `json:"outbox,omitempty"`
	History               map[string]Delivery `json:"history"`
	Progress              uint64              `json:"progress"`
	LastStopProgress      uint64              `json:"last_stop_progress"`
	NoProgressStops       int                 `json:"no_progress_stops"`
	ParentStopProgress    uint64              `json:"parent_stop_progress"`
	ParentNoProgressStops int                 `json:"parent_no_progress_stops"`
	FailureCode           string              `json:"failure_code"`
	FailureDetail         string              `json:"failure_detail"`
	FaultSeen             bool                `json:"fault_seen"`
	ImportPath            string              `json:"import_path,omitempty"`
	ImportSHA256          string              `json:"import_sha256,omitempty"`
}

type Action struct {
	SchemaVersion     int    `json:"schema_version"`
	Action            string `json:"action"`
	WatchID           string `json:"watch_id"`
	EventID           string `json:"event_id,omitempty"`
	Part              int    `json:"part,omitempty"`
	Parts             int    `json:"parts,omitempty"`
	ThreadID          string `json:"thread_id,omitempty"`
	Prompt            string `json:"prompt,omitempty"`
	RetryAfterSeconds int    `json:"retry_after_seconds"`
	Reason            string `json:"reason"`
}

func newControl(parent, watchID, cwd, ticketHash string, now time.Time, options Options) *Control {
	now = now.UTC()
	return &Control{
		WatchID:      watchID,
		ParentID:     parent,
		Cwd:          cwd,
		TicketSHA256: ticketHash,
		CreatedAt:    now,
		BindAfter:    now,
		Stage:        Starting,
		Interval:     options.Interval,
		Quiet:        options.Quiet,
		History:      map[string]Delivery{},
	}
}

func validateControl(state State) error {
	switch state.Version {
	case 2, 3:
		if state.Control != nil {
			return errors.New("control requires state version 4")
		}
		return nil
	case 4:
		if state.Control == nil {
			return errors.New("state version 4 requires control")
		}
	default:
		return fmt.Errorf("unsupported state version %d", state.Version)
	}

	pr, err := ParsePR(state.PRURL)
	if err != nil || pr.URL != state.PRURL {
		return errors.New("state pr_url is not canonical")
	}
	c := state.Control
	if !validUUID(c.WatchID) || !validUUID(c.ParentID) ||
		(c.AgentID != "" && !validUUID(c.AgentID)) ||
		(c.Worker.ID != "" && !validUUID(c.Worker.ID)) {
		return errors.New("control identity contains an invalid UUID")
	}
	if !filepath.IsAbs(c.Cwd) {
		return errors.New("control cwd must be absolute")
	}
	if !validSHA256(c.TicketSHA256) {
		return errors.New("control ticket_sha256 must be a 64-character hex digest")
	}
	if c.CreatedAt.IsZero() || c.BindAfter.IsZero() {
		return errors.New("control timestamps are required")
	}
	if c.Interval <= 0 || c.Quiet <= 0 {
		return errors.New("control durations must be positive")
	}
	if !knownStage(c.Stage) {
		return errors.New("control stage is invalid")
	}
	if c.Cycle.Failures < 0 || c.NoProgressStops < 0 || c.ParentNoProgressStops < 0 ||
		c.LastStopProgress > c.Progress || c.ParentStopProgress > c.Progress {
		return errors.New("control counters are invalid")
	}
	if c.History == nil {
		return errors.New("control history must be an object")
	}
	if c.Ready && !c.InitialAccepted {
		return errors.New("ready control requires initial acceptance")
	}
	if c.InitialAccepted && c.InitialEventID == "" {
		return errors.New("initial acceptance requires an event")
	}
	if (c.ImportPath == "") != (c.ImportSHA256 == "") || c.ImportSHA256 != "" && !validSHA256(c.ImportSHA256) {
		return errors.New("control import path and digest are invalid")
	}
	if c.Stage == Finished && (!state.Finished || hasPendingState(state) || state.Collecting != nil || c.Outbox != nil) {
		return errors.New("finished control has unfinished business state")
	}

	calls := map[string]string{}
	for eventID, delivery := range c.History {
		if eventID != delivery.EventID {
			return errors.New("control history key does not match event")
		}
		if err := validateDelivery(c, delivery, calls); err != nil {
			return err
		}
	}
	if c.Outbox != nil {
		if err := validateDelivery(c, *c.Outbox, calls); err != nil {
			return err
		}
		if err := validateOutbox(state, *c.Outbox); err != nil {
			return err
		}
	}
	return nil
}

func knownStage(stage Stage) bool {
	switch stage {
	case Starting, Initializing, Running, Stopping, NeedsAttention, Finished:
		return true
	default:
		return false
	}
}

func validUUID(value string) bool {
	if len(value) != 36 || value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return false
	}
	decoded, err := hex.DecodeString(value[:8] + value[9:13] + value[14:18] + value[19:23] + value[24:])
	if err != nil || decoded[6]>>4 == 0 || decoded[8]>>6 != 2 {
		return false
	}
	for _, b := range decoded {
		if b != 0 {
			return true
		}
	}
	return false
}

func validSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validateDelivery(control *Control, delivery Delivery, calls map[string]string) error {
	if delivery.EventID == "" || delivery.Kind == "" || len(delivery.Parts) == 0 {
		return errors.New("delivery identity and parts are required")
	}
	for i, part := range delivery.Parts {
		if !validSHA256(part.SHA256) || part.Offers < 0 {
			return errors.New("delivery part hash or offers is invalid")
		}
		if part.Prompt != "" && digest(part.Prompt) != part.SHA256 {
			return errors.New("delivery part prompt does not match its hash")
		}
		if receipt := part.Receipt; receipt != nil {
			if receipt.CallID == "" || !validUUID(receipt.AgentID) || receipt.ParentID != control.ParentID ||
				receipt.EventID != delivery.EventID || receipt.Part != i+1 || receipt.SHA256 != part.SHA256 ||
				receipt.AcceptedAt.IsZero() {
				return errors.New("delivery receipt identity is invalid")
			}
			identity := fmt.Sprintf("%s/%d/%s/%s/%s", receipt.EventID, receipt.Part, receipt.SHA256, receipt.AgentID, receipt.ParentID)
			if previous, exists := calls[receipt.CallID]; exists && previous != identity {
				return errors.New("receipt call_id has conflicting identities")
			}
			calls[receipt.CallID] = identity
		}
	}
	return nil
}

func validateOutbox(state State, outbox Delivery) error {
	if !hasPendingState(state) {
		return errors.New("outbox requires a pending event")
	}
	fields, err := objectFields(state.Pending)
	if err != nil {
		return fmt.Errorf("outbox pending: %w", err)
	}
	var event Event
	if raw, ok := fields["event"]; !ok || decodeJSON(raw, &event) != nil {
		return errors.New("outbox pending event is invalid")
	}
	var messages []Message
	if raw, ok := fields["messages"]; !ok || decodeJSON(raw, &messages) != nil || len(messages) != len(outbox.Parts) {
		return errors.New("outbox does not match pending messages")
	}
	if outbox.EventID != event.EventID || outbox.Kind != event.Type {
		return errors.New("outbox does not match pending event")
	}
	for i, message := range messages {
		if outbox.Parts[i].Prompt != message.Prompt || outbox.Parts[i].SHA256 != digest(message.Prompt) {
			return errors.New("outbox does not match pending prompt")
		}
	}
	return nil
}

func hasPendingState(state State) bool {
	return len(state.Pending) != 0 && !isNull(state.Pending)
}

func digest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
