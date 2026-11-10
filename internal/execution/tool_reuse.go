package execution

import (
	"bytes"
	"encoding/json"
	"fmt"
)

func sameToolArguments(a, b string) bool {
	canonical := func(raw string) ([]byte, error) {
		if !json.Valid([]byte(raw)) {
			return nil, fmt.Errorf("invalid arguments")
		}
		decoder := json.NewDecoder(bytes.NewBufferString(raw))
		decoder.UseNumber()
		var value any
		if err := decoder.Decode(&value); err != nil {
			return nil, err
		}
		return json.Marshal(value)
	}
	left, err := canonical(a)
	if err != nil {
		return false
	}
	right, err := canonical(b)
	return err == nil && bytes.Equal(left, right)
}

func reusableFor(run RunState, ref *TaskReference, source CallState, name, args string) bool {
	if ref == nil || ref.Phase == "reviewer" || source.Task == nil || source.Task.Phase == "reviewer" {
		return false
	}
	known := source.Status == Reconciled || source.Status == Returned && source.Error == ""
	return known && source.OriginRunID != run.RunID && source.Task.TaskID == ref.TaskID && source.Task.Revision == ref.Revision && source.ToolName == name && sameToolArguments(source.Arguments, args)
}

// ReusableToolResult reuses successful inherited Worker calls within the same
// task and plan version. Reviewers always inspect the current state.
func (r *RunRecorder) ReusableToolResult(ref TaskReference, name, args string) (string, string, bool) {
	if r == nil {
		return "", "", false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := len(r.state.Calls) - 1; i >= 0; i-- {
		call := r.state.Calls[i]
		if reusableFor(r.state, &ref, call, name, args) {
			return call.CallID, call.Result, true
		}
	}
	return "", "", false
}

func reusedCallResult(run RunState, call ToolCallEventData) (string, error) {
	if call.ReusedFrom == "" {
		return "", nil
	}
	for _, source := range run.Calls {
		if source.CallID == call.ReusedFrom && reusableFor(run, call.Task, source, call.ToolName, call.Arguments) {
			return source.Result, nil
		}
	}
	return "", fmt.Errorf("tool reuse requires a successful inherited call with matching task, tool and arguments")
}
