package prwatch

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"
)

const appSendTool = "mcp__codex_app__send_message_to_thread"

type RuntimeEvent struct {
	Name         string          `json:"hook_event_name"`
	Cwd          string          `json:"cwd"`
	SessionID    string          `json:"session_id"`
	AgentID      string          `json:"agent_id"`
	AgentType    string          `json:"agent_type"`
	TurnID       string          `json:"turn_id"`
	StopActive   bool            `json:"stop_hook_active"`
	ToolName     string          `json:"tool_name"`
	ToolUseID    string          `json:"tool_use_id"`
	ToolInput    json.RawMessage `json:"tool_input"`
	ToolResponse json.RawMessage `json:"tool_response"`
}

func DecodeRuntime(input io.Reader) (RuntimeEvent, error) {
	raw, err := io.ReadAll(io.LimitReader(input, (1<<20)+1))
	if err != nil {
		return RuntimeEvent{}, err
	}
	if len(raw) > 1<<20 {
		return RuntimeEvent{}, errors.New("hook input exceeds 1 MiB")
	}
	var event RuntimeEvent
	if err := decodeJSON(raw, &event); err != nil {
		return RuntimeEvent{}, err
	}
	return event, nil
}

func DecodeSend(event RuntimeEvent, observedAt time.Time) (ObservedSend, bool, error) {
	if event.ToolName != appSendTool {
		return ObservedSend{}, false, nil
	}
	send := ObservedSend{
		AgentID: event.AgentID, CallID: event.ToolUseID, ObservedAt: observedAt,
	}
	if !validUUID(event.AgentID) {
		return send, true, errors.New("send agent_id must be a UUID")
	}
	if event.ToolUseID == "" {
		return send, true, errors.New("send tool_use_id is required")
	}
	var input struct {
		ThreadID string  `json:"threadId"`
		Prompt   string  `json:"prompt"`
		HostID   *string `json:"hostId"`
	}
	if err := decodeJSON(event.ToolInput, &input); err != nil {
		return send, true, fmt.Errorf("decode send input: %w", err)
	}
	send.ParentID, send.Prompt = input.ThreadID, input.Prompt
	if !validUUID(input.ThreadID) || input.Prompt == "" {
		return send, true, errors.New("send threadId UUID and prompt are required")
	}
	if input.HostID != nil && *input.HostID != "" && *input.HostID != "local" {
		return send, true, errors.New("send receipt for a remote host is unsupported")
	}

	var response struct {
		IsError *bool `json:"isError"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := decodeJSON(event.ToolResponse, &response); err != nil {
		return send, true, fmt.Errorf("decode send result: %w", err)
	}
	if response.IsError != nil && *response.IsError {
		return send, true, nil
	}
	returned := make([]string, 0, 1)
	for _, block := range response.Content {
		if block.Type != "text" {
			continue
		}
		var receipt struct {
			ThreadID string `json:"threadId"`
		}
		if decodeJSON([]byte(block.Text), &receipt) == nil && receipt.ThreadID != "" {
			returned = append(returned, receipt.ThreadID)
		}
	}
	if len(returned) == 0 {
		return send, true, nil
	}
	if len(returned) != 1 || returned[0] != input.ThreadID {
		return send, true, errors.New("send result does not unambiguously match destination thread")
	}
	send.Accepted = true
	return send, true, nil
}

func (c *Controller) ObserveRuntime(event RuntimeEvent) error {
	deadline := time.Now().Add(time.Second)
	for {
		err := c.observeRuntime(event)
		if !errors.Is(err, ErrLocked) || !time.Now().Before(deadline) {
			return err
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (c *Controller) observeRuntime(event RuntimeEvent) error {
	switch event.Name {
	case "SubagentStart":
		return c.ObserveStart(event.SessionID, event.AgentID, event.TurnID)
	case "PostToolUse":
		send, recognized, err := DecodeSend(event, c.Now())
		if err != nil || !recognized {
			return err
		}
		return c.AcceptSend(send)
	default:
		return nil
	}
}
