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

// Reads and remote queries describe the state at one moment. A resumed Worker
// must observe the current state even when an earlier call returned successfully.
func requiresFreshObservation(name string) bool {
	switch name {
	case "read_file", "list_dir", "mcp__filesystem__read_file", "mcp__filesystem__read_text_file", "mcp__filesystem__list_directory", "mcp__filesystem__get_file_info", "mcp__brave__brave_web_search", "mcp__fetch__fetch":
		return true
	default:
		return false
	}
}

func reusableFor(run RunState, ref *TaskReference, source CallState, name, args string) bool {
	if ref == nil || ref.Phase == "reviewer" || source.Task == nil || source.Task.Phase == "reviewer" {
		return false
	}
	known := source.Status == Reconciled || source.Status == Returned && source.Error == ""
	if !known || source.OriginRunID == run.RunID || source.Task.TaskID != ref.TaskID || source.Task.Revision != ref.Revision || source.ToolName != name || !sameToolArguments(source.Arguments, args) {
		return false
	}
	// Do not fall back to an older success after a later matching call failed
	// or was explicitly allowed to retry. Current-run calls are not inherited.
	for i := len(run.Calls) - 1; i >= 0; i-- {
		candidate := run.Calls[i]
		if candidate.OriginRunID == run.RunID || candidate.Task == nil || candidate.Task.Phase == "reviewer" {
			continue
		}
		if candidate.Task.TaskID == ref.TaskID && candidate.Task.Revision == ref.Revision && candidate.ToolName == name && sameToolArguments(candidate.Arguments, args) {
			return candidate.CallID == source.CallID
		}
	}
	return false
}

// ReusableToolResult reuses successful inherited Worker calls within the same
// task and plan version. Reviewers always inspect the current state.
func (r *RunRecorder) ReusableToolResult(ref TaskReference, name, args string) (string, string, bool) {
	if r == nil || requiresFreshObservation(name) {
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
