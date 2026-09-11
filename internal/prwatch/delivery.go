package prwatch

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const deliveryRetryDelay = 5 * time.Second

var errDeliveryNotFound = errors.New("delivery event was not found")

type ObservedSend struct {
	AgentID    string
	ParentID   string
	CallID     string
	Prompt     string
	Accepted   bool
	ObservedAt time.Time
}

func (s *Store) Offer(now time.Time) (Action, error) {
	if !s.deferSave {
		return Action{}, errors.New("offer requires a state transaction")
	}
	control := s.Data.Control
	if control == nil {
		return Action{}, errors.New("managed state is missing control")
	}
	if control.Outbox == nil {
		count, err := s.Prepare("")
		if err != nil {
			return Action{}, err
		}
		raw, err := s.PendingEvent()
		if err != nil {
			return Action{}, err
		}
		var event Event
		if err := decodeJSON(raw, &event); err != nil {
			return Action{}, err
		}
		parts := make([]DeliveryPart, count)
		for i := range parts {
			message, err := s.Message(i + 1)
			if err != nil {
				return Action{}, err
			}
			parts[i] = DeliveryPart{Prompt: message.Prompt, SHA256: digest(message.Prompt)}
		}
		control.Outbox = &Delivery{EventID: event.EventID, Kind: event.Type, Parts: parts}
		if control.InitialEventID == "" && event.Type == "initial" && !control.RefreshRequired {
			control.InitialEventID = event.EventID
		}
	}

	outbox := control.Outbox
	for i := range outbox.Parts {
		part := &outbox.Parts[i]
		if part.Receipt != nil {
			continue
		}
		if !part.OfferedAt.IsZero() {
			remaining := part.OfferedAt.Add(deliveryRetryDelay).Sub(now)
			if remaining > 0 {
				return deliveryAction(control, outbox, i, "wait", int((remaining+time.Second-1)/time.Second)), nil
			}
		}
		if part.Offers >= 3 {
			control.Stage = NeedsAttention
			control.FailureCode = "delivery_receipt_missing"
			control.FailureDetail = fmt.Sprintf("event %s part %d has no accepted send receipt", outbox.EventID, i+1)
			return deliveryAction(control, outbox, i, "attention", 0), nil
		}
		part.Offers++
		part.OfferedAt = now.UTC()
		return deliveryAction(control, outbox, i, "send", 0), nil
	}
	return deliveryAction(control, outbox, len(outbox.Parts)-1, "wait", 1), nil
}

func deliveryAction(control *Control, delivery *Delivery, index int, action string, retry int) Action {
	result := Action{
		SchemaVersion: 1, Action: action, WatchID: control.WatchID,
		EventID: delivery.EventID, Part: index + 1, Parts: len(delivery.Parts),
		RetryAfterSeconds: retry,
	}
	if action == "send" {
		result.ThreadID = control.ParentID
		result.Prompt = delivery.Parts[index].Prompt
	}
	if action == "wait" {
		result.Reason = "delivery_receipt_pending"
	}
	if action == "attention" {
		result.Reason = control.FailureCode
	}
	return result
}

func (c *Controller) AcceptSend(send ObservedSend) error {
	if !send.Accepted || send.CallID == "" || send.Prompt == "" || send.ObservedAt.IsZero() ||
		!validUUID(send.AgentID) || !validUUID(send.ParentID) {
		return nil
	}
	parentDir := c.parentDir(send.ParentID)
	if err := c.validateManagedDir(parentDir); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	deadline := time.Now().Add(time.Second)
	return c.withParentLock(send.ParentID, func() error {
		states, err := c.parentStates(send.ParentID)
		if err != nil {
			return err
		}
		for _, state := range states {
			if state.Control == nil || state.Control.AgentID != send.AgentID || state.Control.ParentID != send.ParentID {
				continue
			}
			store, err := c.Open(send.ParentID, state.PRURL)
			if err != nil {
				return err
			}
			matched := false
			for {
				err = store.Update(func(tx *Store) error {
					var err error
					matched, err = tx.acceptSend(send)
					return err
				})
				if !errors.Is(err, ErrLocked) || !time.Now().Before(deadline) {
					break
				}
				time.Sleep(time.Millisecond)
			}
			if err != nil || matched {
				return err
			}
		}
		return nil
	})
}

func (s *Store) acceptSend(send ObservedSend) (bool, error) {
	control := s.Data.Control
	if control.AgentID != send.AgentID || control.ParentID != send.ParentID {
		return false, nil
	}
	hash := digest(send.Prompt)
	for _, delivery := range appendDeliveries(control.History, control.Outbox) {
		for _, part := range delivery.Parts {
			if part.Receipt == nil || part.Receipt.CallID != send.CallID {
				continue
			}
			receipt := part.Receipt
			if receipt.AgentID != send.AgentID || receipt.ParentID != send.ParentID || part.SHA256 != hash ||
				part.Prompt != "" && part.Prompt != send.Prompt {
				return false, errors.New("send call_id conflicts with an accepted receipt")
			}
			return true, nil
		}
	}
	if control.Outbox == nil {
		return false, nil
	}
	for _, part := range control.Outbox.Parts {
		if part.Receipt != nil && part.Prompt == send.Prompt && part.SHA256 == hash {
			return true, nil
		}
	}
	for i := range control.Outbox.Parts {
		part := &control.Outbox.Parts[i]
		if part.Receipt != nil {
			continue
		}
		if part.Prompt != send.Prompt || part.SHA256 != hash {
			return false, nil
		}
		part.Receipt = &Receipt{
			CallID: send.CallID, AgentID: send.AgentID, ParentID: send.ParentID,
			EventID: control.Outbox.EventID, Part: i + 1, SHA256: hash,
			AcceptedAt: send.ObservedAt.UTC(),
		}
		control.Progress++
		return true, nil
	}
	return false, nil
}

func appendDeliveries(history map[string]Delivery, outbox *Delivery) []Delivery {
	deliveries := make([]Delivery, 0, len(history)+1)
	for _, delivery := range history {
		deliveries = append(deliveries, delivery)
	}
	if outbox != nil {
		deliveries = append(deliveries, *outbox)
	}
	return deliveries
}

func (s *Store) CommitAccepted() error {
	if !s.deferSave {
		return errors.New("acceptance commit requires a state transaction")
	}
	control := s.Data.Control
	if control == nil || control.Outbox == nil {
		return nil
	}
	current := control.Outbox
	for _, part := range current.Parts {
		if part.Receipt == nil {
			return nil
		}
	}
	history := *current
	history.Parts = append([]DeliveryPart(nil), current.Parts...)
	for i := range history.Parts {
		history.Parts[i].Prompt = ""
	}
	if err := s.Ack(current.EventID); err != nil {
		return err
	}
	control.History[current.EventID] = history
	if current.EventID == control.InitialEventID {
		control.InitialAccepted = true
	}
	control.Outbox = nil
	control.Progress++
	return nil
}

func (c *Controller) Receive(parentID, prURL, eventID string, part int) (string, error) {
	if !validUUID(parentID) || eventID == "" || part < 1 {
		return "", errors.New("parent, event, and positive part are required")
	}
	s, err := c.Open(parentID, prURL)
	if err != nil {
		return "", err
	}
	result := ""
	err = c.withParentLock(parentID, func() error {
		archived, err := c.archivedDeliveries(s.PR, parentID, eventID)
		if err != nil {
			return err
		}
		if len(archived) > 1 {
			return errors.New("delivery event is ambiguous")
		}
		managedErr := managedUpdate(s, func(tx *Store) error {
			control := tx.Data.Control
			if control.ParentID != parentID {
				return errors.New("watch does not belong to parent")
			}
			var delivery Delivery
			outbox := control.Outbox != nil && control.Outbox.EventID == eventID
			if outbox {
				if len(archived) != 0 {
					return errors.New("delivery event is ambiguous")
				}
				delivery = *control.Outbox
			} else {
				var ok bool
				delivery, ok = control.History[eventID]
				if !ok {
					return errDeliveryNotFound
				}
				if len(archived) != 0 {
					return errors.New("delivery event is ambiguous")
				}
			}
			if part > len(delivery.Parts) {
				return errors.New("delivery part is out of range")
			}
			if delivery.Parts[part-1].Received {
				result = "duplicate"
				return nil
			}
			delivery.Parts[part-1].Received = true
			result = "event_complete"
			for _, current := range delivery.Parts {
				if !current.Received {
					result = "new_part"
					break
				}
			}
			if outbox {
				control.Outbox = &delivery
			} else {
				control.History[eventID] = delivery
			}
			return nil
		})
		if errors.Is(managedErr, errDeliveryNotFound) && len(archived) == 1 {
			var archiveErr error
			result, archiveErr = c.receiveArchivedCandidate(archived[0], eventID, part)
			return archiveErr
		}
		if errors.Is(managedErr, errDeliveryNotFound) && len(archived) > 1 {
			return errors.New("delivery event is ambiguous")
		}
		return managedErr
	})
	return result, err
}

type archiveCandidate struct {
	pr       PR
	path     string
	delivery Delivery
}

func (c *Controller) archivedDeliveries(pr PR, parentID, eventID string) ([]archiveCandidate, error) {
	historyDir := filepath.Join(c.parentDir(parentID), "history")
	if err := c.validateManagedDir(historyDir); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	entries, err := os.ReadDir(historyDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var found []archiveCandidate
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		path := filepath.Join(historyDir, entry.Name())
		if err := c.rejectSymlinkedFile(path); err != nil {
			return nil, err
		}
		state, err := ReadState(path)
		if err != nil {
			return nil, err
		}
		if state.PRURL != pr.URL || state.Control == nil || state.Control.ParentID != parentID {
			continue
		}
		if delivery, ok := state.Control.History[eventID]; ok {
			found = append(found, archiveCandidate{pr: pr, path: path, delivery: delivery})
		}
	}
	return found, nil
}

func (c *Controller) receiveArchived(pr PR, parentID, eventID string, part int) (string, error) {
	found, err := c.archivedDeliveries(pr, parentID, eventID)
	if err != nil {
		return "", err
	}
	if len(found) == 0 {
		return "", errDeliveryNotFound
	}
	if len(found) != 1 {
		return "", errors.New("delivery event is ambiguous")
	}
	return c.receiveArchivedCandidate(found[0], eventID, part)
}

func (c *Controller) receiveArchivedCandidate(candidate archiveCandidate, eventID string, part int) (string, error) {
	if part > len(candidate.delivery.Parts) {
		return "", errors.New("delivery part is out of range")
	}
	archived := &Store{PR: candidate.pr, Path: candidate.path, StopPath: filepath.Join(filepath.Dir(candidate.path), entryStem(candidate.path)+".stop")}
	result := ""
	err := managedUpdate(archived, func(tx *Store) error {
		delivery, ok := tx.Data.Control.History[eventID]
		if !ok {
			return errDeliveryNotFound
		}
		if part > len(delivery.Parts) {
			return errors.New("delivery part is out of range")
		}
		if delivery.Parts[part-1].Received {
			result = "duplicate"
			return nil
		}
		delivery.Parts[part-1].Received = true
		result = "event_complete"
		for _, current := range delivery.Parts {
			if !current.Received {
				result = "new_part"
				break
			}
		}
		tx.Data.Control.History[eventID] = delivery
		return nil
	})
	return result, err
}

func entryStem(path string) string {
	return strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
}
