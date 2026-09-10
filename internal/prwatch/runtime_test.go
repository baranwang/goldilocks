package prwatch

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func appSendEvent() RuntimeEvent {
	return RuntimeEvent{
		Name:      "PostToolUse",
		AgentID:   specChild,
		ToolName:  "mcp__codex_app__send_message_to_thread",
		ToolUseID: "exec-receipt-1",
		ToolInput: json.RawMessage(`{"threadId":"` + specParent + `","prompt":"test marker 界"}`),
		ToolResponse: json.RawMessage(`{"content":[{"type":"text","text":"{\"threadId\":\"` +
			specParent + `\"}"}],"isError":false}`),
	}
}

func TestAppReceiptDecodesObservedDesktopShape(t *testing.T) {
	e := appSendEvent()
	send, recognized, err := DecodeSend(e, specTime)
	if err != nil || !recognized || !send.Accepted || send.ParentID != specParent ||
		send.AgentID != specChild || send.CallID != e.ToolUseID || send.Prompt != "test marker 界" ||
		send.ObservedAt != specTime {
		t.Fatalf("%+v %v %v", send, recognized, err)
	}

	e.ToolResponse = json.RawMessage(`{"isError":false,"content":[]}`)
	send, recognized, err = DecodeSend(e, specTime)
	if err != nil || !recognized || send.Accepted {
		t.Fatalf("empty result counted as success: %+v %v %v", send, recognized, err)
	}
}

func TestAppReceiptRequiresExactUnambiguousResult(t *testing.T) {
	tests := []struct {
		name     string
		response string
		want     bool
		wantErr  bool
	}{
		{"failed MCP result", `{"isError":true,"content":[{"type":"text","text":"{\"threadId\":\"` + specParent + `\"}"}]}`, false, false},
		{"omitted isError", `{"content":[{"type":"text","text":"{\"threadId\":\"` + specParent + `\"}"}]}`, true, false},
		{"invalid isError type", `{"isError":"false","content":[]}`, false, true},
		{"wrong returned thread", `{"isError":false,"content":[{"type":"text","text":"{\"threadId\":\"` + specChild + `\"}"}]}`, false, true},
		{"conflicting returned threads", `{"isError":false,"content":[{"type":"text","text":"{\"threadId\":\"` + specParent + `\"}"},{"type":"text","text":"{\"threadId\":\"` + specChild + `\"}"}]}`, false, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := appSendEvent()
			e.ToolResponse = json.RawMessage(tc.response)
			send, recognized, err := DecodeSend(e, specTime)
			if !recognized || (err != nil) != tc.wantErr || send.Accepted != tc.want {
				t.Fatalf("%+v recognized=%v err=%v", send, recognized, err)
			}
		})
	}
}

func TestAppReceiptRejectsIncompleteManagedInput(t *testing.T) {
	for _, mutate := range []func(*RuntimeEvent){
		func(e *RuntimeEvent) { e.ToolUseID = "" },
		func(e *RuntimeEvent) { e.AgentID = "not-a-uuid" },
		func(e *RuntimeEvent) {
			e.ToolInput = json.RawMessage(`{"threadId":"` + specParent + `","prompt":"test marker","hostId":"remote"}`)
		},
	} {
		e := appSendEvent()
		mutate(&e)
		if _, recognized, err := DecodeSend(e, specTime); !recognized || err == nil {
			t.Fatalf("accepted incomplete managed input: recognized=%v err=%v", recognized, err)
		}
	}
}

func TestAppReceiptAcceptsExplicitLocalHost(t *testing.T) {
	e := appSendEvent()
	e.ToolInput = json.RawMessage(`{"threadId":"` + specParent + `","prompt":"test marker 界","hostId":"local"}`)
	send, recognized, err := DecodeSend(e, specTime)
	if err != nil || !recognized || !send.Accepted {
		t.Fatalf("local receipt rejected: %+v %v %v", send, recognized, err)
	}
}

func TestAppReceiptIgnoresUnrelatedToolsBeforeParsing(t *testing.T) {
	e := RuntimeEvent{ToolName: "mcp__codex_app__read_thread", ToolInput: json.RawMessage(`{`), ToolResponse: json.RawMessage(`{`)}
	if send, recognized, err := DecodeSend(e, specTime); err != nil || recognized || send != (ObservedSend{}) {
		t.Fatalf("unrelated tool parsed: %+v %v %v", send, recognized, err)
	}
}

func TestRuntimeRejectsTrailingAndOversizedInput(t *testing.T) {
	for _, raw := range []string{
		`{"hook_event_name":"Stop"}{}`,
		strings.Repeat(" ", (1<<20)+1),
	} {
		if _, err := DecodeRuntime(strings.NewReader(raw)); err == nil {
			t.Fatal("accepted bad payload")
		}
	}
}

func TestRuntimePermitsHostEvolutionFields(t *testing.T) {
	e, err := DecodeRuntime(strings.NewReader(`{"hook_event_name":"SubagentStart","future_field":{"nested":true}}`))
	if err != nil || e.Name != "SubagentStart" {
		t.Fatalf("%+v %v", e, err)
	}
}

func TestRuntimeObservationStopsRetryingLockedMembershipAfterOneSecond(t *testing.T) {
	c, _ := startedFixture(t)
	lock, err := flock(filepath.Join(c.parentDir(specParent), "bindings.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer lock()
	started := time.Now()
	err = c.ObserveRuntime(RuntimeEvent{
		Name: "SubagentStart", SessionID: specParent, AgentID: specChild, TurnID: specWatch,
	})
	if !errors.Is(err, ErrLocked) {
		t.Fatalf("locked observation returned %v", err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("runtime observation exceeded retry budget: %v", elapsed)
	}
}

func TestRuntimeObservationPersistsExactAcceptedReceipt(t *testing.T) {
	c, _, store, action := deliveryFixture(t)
	e := appSendEvent()
	e.ToolInput = json.RawMessage(`{"threadId":"` + specParent + `","prompt":` + quoteJSON(action.Prompt) + `}`)
	if err := c.ObserveRuntime(e); err != nil {
		t.Fatal(err)
	}
	state, err := ReadState(store.Path)
	if err != nil {
		t.Fatal(err)
	}
	receipt := state.Control.Outbox.Parts[0].Receipt
	if receipt == nil || receipt.CallID != e.ToolUseID || receipt.ParentID != specParent || receipt.AgentID != specChild {
		t.Fatalf("accepted App result was not persisted: %+v", receipt)
	}
}

func TestRuntimeObservationStopsRetryingLockedReceiptAfterOneSecond(t *testing.T) {
	c, _, store, action := deliveryFixture(t)
	lock, err := flock(stateLockPath(store.Path))
	if err != nil {
		t.Fatal(err)
	}
	defer lock()
	e := appSendEvent()
	e.ToolInput = json.RawMessage(`{"threadId":"` + specParent + `","prompt":` + quoteJSON(action.Prompt) + `}`)
	started := time.Now()
	err = c.ObserveRuntime(e)
	if !errors.Is(err, ErrLocked) {
		t.Fatalf("locked receipt returned %v", err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("receipt observation exceeded retry budget: %v", elapsed)
	}
}

func TestRuntimeObservationDoesNotCreateUnknownParent(t *testing.T) {
	c, err := NewController(t.TempDir(), func() time.Time { return specTime })
	if err != nil {
		t.Skipf("managed controller unsupported: %v", err)
	}
	unknown := "00000000-0000-4000-8000-000000000004"
	e := appSendEvent()
	e.ToolInput = json.RawMessage(`{"threadId":"` + unknown + `","prompt":"test marker 界"}`)
	e.ToolResponse = json.RawMessage(`{"content":[{"type":"text","text":"{\"threadId\":\"` + unknown + `\"}"}],"isError":false}`)
	if err := c.ObserveRuntime(e); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(c.parentDir(unknown)); !os.IsNotExist(err) {
		t.Fatalf("unrelated App send created parent state: %v", err)
	}
}

func quoteJSON(value string) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}
