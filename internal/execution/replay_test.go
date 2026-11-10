package execution

import (
	"testing"
	"time"
)

func TestReplayRejectsInvalidTransitions(t *testing.T) {
	start := Event{EventHeader: EventHeader{Type: RunStarted}, Run: &RunEventData{}}
	call := Event{EventHeader: EventHeader{Type: ToolCallStarted}, ToolCall: &ToolCallEventData{CallID: "call", ToolCallID: "model-call", ToolName: "write", Arguments: "{}"}}
	result := call
	result.Type = ToolCallCompleted
	for _, tc := range []struct {
		name   string
		events []Event
	}{
		{"missing run", []Event{call}},
		{"duplicate run", []Event{start, start}},
		{"result without start", []Event{start, result}},
		{"duplicate call", []Event{start, call, call}},
		{"duplicate result", []Event{start, call, result, result}},
		{"finish with unknown call", []Event{start, call, {EventHeader: EventHeader{Type: RunCompleted}, Run: &RunEventData{}}}},
		{"event after terminal", []Event{start, {EventHeader: EventHeader{Type: RunCancelled}, Run: &RunEventData{}}, call}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for i := range tc.events {
				e := &tc.events[i]
				e.Sequence = uint64(i) + 1
				e.Timestamp = time.Now()
				e.ConversationID = "conversation"
				e.RunID = "run"
			}
			if _, err := Replay(tc.events); err == nil {
				t.Fatal("accepted invalid transition")
			}
		})
	}
}

func TestReplayPreservesToolErrorAndCancelledRun(t *testing.T) {
	s := newTestStore(t)
	appendEvent(t, s, Event{EventHeader: EventHeader{Type: RunStarted}, Run: &RunEventData{}})
	appendEvent(t, s, Event{EventHeader: EventHeader{Type: ToolCallStarted}, ToolCall: &ToolCallEventData{CallID: "call", ToolCallID: "model-call", ToolName: "write"}})
	appendEvent(t, s, Event{EventHeader: EventHeader{Type: ToolCallCompleted}, ToolCall: &ToolCallEventData{CallID: "call", ToolCallID: "model-call", ToolName: "write", Result: "ERROR: cancelled", Error: "cancelled"}})
	appendEvent(t, s, Event{EventHeader: EventHeader{Type: RunCancelled}, Run: &RunEventData{Error: "context canceled"}})
	events, _ := s.Load(t.Context(), "conversation", 0)
	runs, err := Replay(events)
	if err != nil {
		t.Fatal(err)
	}
	if runs[0].Status != Cancelled || runs[0].Calls[0].Status != Returned || runs[0].Calls[0].Error != "cancelled" {
		t.Fatalf("lost cancellation: %+v", runs)
	}
}

func TestReplayAbandonedRunIsTerminal(t *testing.T) {
	s := newTestStore(t)
	appendEvent(t, s, Event{EventHeader: EventHeader{Type: RunStarted}, Run: &RunEventData{}})
	appendEvent(t, s, Event{EventHeader: EventHeader{Type: RunAbandoned}, Run: &RunEventData{Error: "user declined"}})
	events, _ := s.Load(t.Context(), "conversation", 0)
	runs, err := Replay(events)
	if err != nil {
		t.Fatal(err)
	}
	if runs[0].Status != Abandoned {
		t.Fatal(runs[0].Status)
	}
	appendEvent(t, s, Event{EventHeader: EventHeader{Type: RunCompleted}, Run: &RunEventData{}})
	events, _ = s.Load(t.Context(), "conversation", 0)
	if _, err := Replay(events); err == nil {
		t.Fatal("allowed event after abandonment")
	}
}
