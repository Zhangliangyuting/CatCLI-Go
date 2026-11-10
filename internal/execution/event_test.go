package execution

import (
	"encoding/json"
	"testing"
	"time"
)

func TestEventRequiresMatchingPayload(t *testing.T) {
	for _, tc := range []struct {
		name string
		typ  EventType
		run  *RunEventData
		call *ToolCallEventData
	}{
		{"missing run", RunStarted, nil, nil},
		{"tool data on run", RunStarted, nil, &ToolCallEventData{}},
		{"both payloads", RunStarted, &RunEventData{}, &ToolCallEventData{}},
		{"missing call", ToolCallStarted, nil, nil},
		{"run data on call", ToolCallCompleted, &RunEventData{}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := Event{EventHeader: EventHeader{Sequence: 1, ConversationID: "conversation", RunID: "run", Type: tc.typ, Timestamp: time.Now()}, Run: tc.run, ToolCall: tc.call}
			if err := validateEvent(e); err == nil {
				t.Fatal("accepted missing or mismatched payload")
			}
		})
	}
}

func TestEventJSONContainsOnlySelectedPayload(t *testing.T) {
	e := Event{EventHeader: EventHeader{Type: RunStarted}, Run: &RunEventData{Input: "read file"}}
	data, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	if fields["run"] == nil || fields["type"] == nil || fields["tool_call"] != nil || fields["input"] != nil {
		t.Fatalf("unexpected event layout: %s", data)
	}
}
