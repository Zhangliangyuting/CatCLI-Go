package execution

import "testing"

func TestToolArgumentIdentityPreservesLargeIntegers(t *testing.T) {
	if !sameToolArguments(`{"a":1,"b":{"x":2}}`, `{ "b": {"x":2}, "a":1 }`) {
		t.Fatal("equivalent object order should match")
	}
	if sameToolArguments(`{"id":9007199254740992}`, `{"id":9007199254740993}`) {
		t.Fatal("large integers collapsed into the same invocation identity")
	}
	if sameToolArguments(`{"path":"a"}`, `{"path":"b"}`) {
		t.Fatal("different tool arguments matched")
	}
}

func TestRetryDoesNotReuseOlderSuccessfulMatchingCall(t *testing.T) {
	ref := &TaskReference{Revision: 1, TaskID: "task", Attempt: 2}
	old := CallState{OriginRunID: "old", CallID: "success", Task: ref, ToolName: "write_file", Arguments: `{}`, Status: Returned, Result: "old success"}
	for _, status := range []CallStatus{RetryAllowed, NotExecuted, Unknown, Returned} {
		latest := old
		latest.CallID, latest.Status, latest.Result, latest.Error = "latest", status, "", "failed"
		state := RunState{RunID: "new", Calls: []CallState{old, latest}}
		if reusableFor(state, ref, old, "write_file", `{}`) {
			t.Fatalf("reused stale success after %s", status)
		}
	}
}

func TestRecoveredQueriesAlwaysUseFreshObservation(t *testing.T) {
	ref := &TaskReference{Revision: 1, TaskID: "task", Attempt: 2}
	for _, name := range []string{"read_file", "list_dir", "mcp__filesystem__read_text_file", "mcp__filesystem__list_directory", "mcp__filesystem__get_file_info", "mcp__brave__brave_web_search", "mcp__fetch__fetch"} {
		old := CallState{OriginRunID: "old", CallID: "read", Task: ref, ToolName: name, Arguments: `{"path":"note.txt"}`, Status: Returned, Result: "stale"}
		recorder := &RunRecorder{state: RunState{RunID: "new", Calls: []CallState{old}}}
		if _, _, ok := recorder.ReusableToolResult(*ref, name, old.Arguments); ok {
			t.Errorf("reused stale %s result", name)
		}
		if _, err := reusedCallResult(recorder.state, ToolCallEventData{ReusedFrom: old.CallID, Task: ref, ToolName: name, Arguments: old.Arguments}); err != nil {
			t.Errorf("could not replay old %s result: %v", name, err)
		}
	}
	write := CallState{OriginRunID: "old", CallID: "write", Task: ref, ToolName: "write_file", Arguments: `{}`, Status: Returned, Result: "done"}
	if !reusableFor(RunState{RunID: "new", Calls: []CallState{write}}, ref, write, "write_file", `{}`) {
		t.Fatal("lost successful write result reuse")
	}
}
