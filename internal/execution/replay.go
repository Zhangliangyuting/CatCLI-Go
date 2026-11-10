package execution

import (
	"encoding/json"
	"fmt"
)

// Replay consumes the full conversation log in order. It is a pure reducer:
// no I/O, model calls or tool execution. A started call without a returned result
// is unknown, not safe to retry. Returned errors do not guarantee no side effects.
func Replay(events []Event) ([]RunState, error) {
	return replayEvents(nil, "", 0, events)
}

// ReplayFrom continues a snapshot with its immediately following events.
// It never mutates the snapshot, including when a later event is rejected.
func ReplayFrom(checkpoint Checkpoint, events []Event) ([]RunState, error) {
	if err := checkpoint.validateState(); err != nil {
		return nil, err
	}
	data, err := json.Marshal(checkpoint.Runs)
	if err != nil {
		return nil, err
	}
	var runs []RunState
	if err := json.Unmarshal(data, &runs); err != nil {
		return nil, err
	}
	return replayEvents(runs, checkpoint.ConversationID, checkpoint.LastSequence, events)
}

func replayEvents(runs []RunState, conversationID string, afterSequence uint64, events []Event) ([]RunState, error) {
	indices := make(map[string]int)
	for i, run := range runs {
		indices[run.RunID] = i
	}
	for i, e := range events {
		if err := validateEvent(e); err != nil {
			return nil, err
		}
		if conversationID == "" {
			conversationID = e.ConversationID
		}
		if e.Sequence != afterSequence+uint64(i)+1 || e.Sequence <= afterSequence || e.ConversationID != conversationID {
			return nil, fmt.Errorf("replay requires a complete ordered conversation log")
		}
		index, exists := indices[e.RunID]
		if e.Type == RunStarted {
			if exists {
				return nil, fmt.Errorf("duplicate start for run %s", e.RunID)
			}
			restored := RunState{Kind: e.Run.Kind, RunID: e.RunID, ConversationID: e.ConversationID, Input: e.Run.Input, ResumedFrom: e.Run.ResumedFrom, Status: Interrupted, LastSequence: e.Sequence}
			if source := e.Run.ResumedFrom; source != "" {
				sourceIndex, ok := indices[source]
				if !ok || source == e.RunID || runs[sourceIndex].Status != Interrupted {
					return nil, fmt.Errorf("invalid interrupted source run %s", source)
				}
				if runs[sourceIndex].Plan != nil {
					var err error
					restored, err = inheritRun(runs[sourceIndex], e)
					if err != nil {
						return nil, err
					}
				}
				runs[sourceIndex].Status = Resumed
			}
			indices[e.RunID] = len(runs)
			runs = append(runs, restored)
			continue
		}
		if !exists || runs[index].Status != Interrupted {
			return nil, fmt.Errorf("event outside an open run %s", e.RunID)
		}
		run := &runs[index]
		run.LastSequence = e.Sequence
		callIndex := -1
		for j := range run.Calls {
			if e.ToolCall != nil && run.Calls[j].CallID == e.ToolCall.CallID {
				callIndex = j
				break
			}
		}
		switch e.Type {
		case PlanSaved, TaskStarted, TaskWorkCompleted, TaskCompleted, TaskFailed, TaskCancelled:
			if err := updatePlanAndTaskState(run, e); err != nil {
				return nil, err
			}
		case ToolCallReconciled:
			if err := applyReconciliation(runs, indices, index, e); err != nil {
				return nil, err
			}
		case ToolCallStarted:
			if err := validateToolTask(*run, e.ToolCall.Task); err != nil {
				return nil, err
			}
			if callIndex >= 0 {
				return nil, fmt.Errorf("duplicate tool call start %s", e.ToolCall.CallID)
			}
			if _, err := reusedCallResult(*run, *e.ToolCall); err != nil {
				return nil, err
			}
			run.Calls = append(run.Calls, CallState{ReusedFrom: e.ToolCall.ReusedFrom, OriginRunID: e.RunID, Task: e.ToolCall.Task, CallID: e.ToolCall.CallID, ToolCallID: e.ToolCall.ToolCallID, ToolName: e.ToolCall.ToolName, Arguments: e.ToolCall.Arguments, Status: Unknown})
		case ToolCallCompleted:
			if callIndex < 0 {
				return nil, fmt.Errorf("result without tool call start %s", e.ToolCall.CallID)
			}
			call := &run.Calls[callIndex]
			if call.OriginRunID != e.RunID || call.Status != Unknown || call.ToolCallID != e.ToolCall.ToolCallID || call.ToolName != e.ToolCall.ToolName || call.ReusedFrom != e.ToolCall.ReusedFrom || !sameTaskReference(call.Task, e.ToolCall.Task) {
				return nil, fmt.Errorf("duplicate or mismatched tool result %s", e.ToolCall.CallID)
			}
			if call.ReusedFrom != "" {
				result, err := reusedCallResult(*run, *e.ToolCall)
				if err != nil || result != e.ToolCall.Result || e.ToolCall.Error != "" {
					return nil, fmt.Errorf("reused result differs from verified source")
				}
			}
			call.Status, call.Result, call.Error = Returned, e.ToolCall.Result, e.ToolCall.Error
		case RunCompleted:
			if err := checkPlanTasksCompleted(*run); err != nil {
				return nil, err
			}
			for _, call := range run.Calls {
				if call.Status == Unknown {
					return nil, fmt.Errorf("completed run has unresolved tool calls")
				}
			}
			run.Status, run.Answer = Completed, e.Run.Answer
		case RunFailed:
			run.Status, run.Error = Failed, e.Run.Error
		case RunCancelled:
			run.Status, run.Error = Cancelled, e.Run.Error
		case RunAbandoned:
			run.Status, run.Error = Abandoned, e.Run.Error
		}
	}
	return runs, nil
}
