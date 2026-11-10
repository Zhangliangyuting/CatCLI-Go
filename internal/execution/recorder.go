package execution

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Recorder is shared across runs. It records supplied execution facts without
// replaying history or deciding which tools an Agent should execute.
type Recorder struct {
	startMu        sync.Mutex
	store          EventStore
	conversationID string
	resumed        map[string]struct{}
}

func NewRecorder(store EventStore, conversationID string) *Recorder {
	if store == nil {
		return nil
	}
	return &Recorder{store: store, conversationID: conversationID, resumed: make(map[string]struct{})}
}

func (r *Recorder) ConversationID() string {
	if r == nil {
		return ""
	}
	return r.conversationID
}

func (r *Recorder) LoadEvents(ctx context.Context) ([]Event, error) {
	if r == nil {
		return nil, fmt.Errorf("execution recorder is not configured")
	}
	return r.store.Load(ctx, r.conversationID, 0)
}

// RunRecorder belongs to one run. Its mutex protects recording state while tool
// handlers execute outside the lock, so parallel Workers can share a run handle.
type RunRecorder struct {
	mu       sync.Mutex
	recorder *Recorder
	runID    string
	writeErr error
	finished bool
	pending  map[string]ToolCallEventData
	state    RunState
}

func (r *RunRecorder) ID() string {
	if r == nil {
		return ""
	}
	return r.runID
}

func (r *Recorder) StartRun(ctx context.Context, input, resumedFrom string, kind RunKind) (*RunRecorder, error) {
	if r == nil {
		return nil, nil
	}
	r.startMu.Lock()
	defer r.startMu.Unlock()
	if _, claimed := r.resumed[resumedFrom]; resumedFrom != "" && claimed {
		return nil, fmt.Errorf("source run already resumed")
	}
	id, err := NewID()
	if err != nil {
		return nil, err
	}
	run := &RunRecorder{recorder: r, runID: id, pending: make(map[string]ToolCallEventData)}
	run.state = RunState{Kind: kind, RunID: id, ConversationID: r.conversationID, Input: input, ResumedFrom: resumedFrom, Status: Interrupted}
	if err := run.appendLocked(ctx, Event{EventHeader: EventHeader{Type: RunStarted}, Run: &RunEventData{Kind: kind, Input: input, ResumedFrom: resumedFrom}}); err != nil {
		return nil, err
	}
	if resumedFrom != "" {
		r.resumed[resumedFrom] = struct{}{}
	}
	return run, nil
}

// appendLocked must be called with the run mutex held (or before publication).
func (r *RunRecorder) appendLocked(ctx context.Context, e Event) error {
	if r.writeErr != nil {
		return r.writeErr
	}
	if r.finished {
		return fmt.Errorf("execution run already finished")
	}
	e.ConversationID, e.RunID = r.recorder.conversationID, r.runID
	saved, err := r.recorder.store.Append(ctx, e)
	if err != nil {
		r.writeErr = fmt.Errorf("persist execution event %s: %w", e.Type, err)
		return r.writeErr
	}
	r.state.LastSequence = saved.Sequence
	return nil
}

func (r *RunRecorder) appendResultLocked(ctx context.Context, e Event) error {
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return r.appendLocked(writeCtx, e)
}

func (r *RunRecorder) StartToolCall(ctx context.Context, call ToolCallEventData) (ToolCallEventData, error) {
	if r == nil {
		return call, nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := validateToolTask(r.state, call.Task); err != nil {
		return ToolCallEventData{}, err
	}
	if _, err := reusedCallResult(r.state, call); err != nil {
		return ToolCallEventData{}, err
	}
	if call.Task != nil {
		ref := *call.Task
		call.Task = &ref
	}
	id, err := NewID()
	if err != nil {
		return ToolCallEventData{}, err
	}
	call.CallID, call.Result, call.Error = id, "", ""
	if err := r.appendLocked(ctx, Event{EventHeader: EventHeader{Type: ToolCallStarted}, ToolCall: &call}); err != nil {
		return ToolCallEventData{}, err
	}
	r.pending[id] = call
	r.state.Calls = append(r.state.Calls, CallState{OriginRunID: r.runID, ReusedFrom: call.ReusedFrom, CallID: id, ToolCallID: call.ToolCallID, ToolName: call.ToolName, Arguments: call.Arguments, VerificationData: call.VerificationData, Task: call.Task, Status: Unknown})
	if call.Task != nil {
		ref := *call.Task
		call.Task = &ref
	}
	return call, nil
}

// CompleteToolCall records the returned result even if the task was cancelled.
// A returned error is not proof that the tool produced no side effects.
func (r *RunRecorder) CompleteToolCall(ctx context.Context, callID, result string, toolErr error) error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	call, ok := r.pending[callID]
	if !ok {
		return fmt.Errorf("tool call has no pending start: %s", callID)
	}
	call.Result = result
	if toolErr != nil {
		call.Error = toolErr.Error()
	}
	if call.ReusedFrom != "" {
		expected, err := reusedCallResult(r.state, call)
		if err != nil || expected != result || toolErr != nil {
			return fmt.Errorf("reused result differs from verified source")
		}
	}
	if err := r.appendResultLocked(ctx, Event{EventHeader: EventHeader{Type: ToolCallCompleted}, ToolCall: &call}); err != nil {
		return err
	}
	delete(r.pending, callID)
	for i := range r.state.Calls {
		if r.state.Calls[i].CallID == callID {
			r.state.Calls[i].Status = Returned
			r.state.Calls[i].Result, r.state.Calls[i].Error = call.Result, call.Error
			break
		}
	}
	return nil
}

func (r *RunRecorder) FinishRun(ctx context.Context, answer string, runErr error) error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.writeErr != nil {
		return r.writeErr // Do not append again after an ambiguous write.
	}
	if runErr == nil {
		for _, call := range r.state.Calls {
			if call.Status == Unknown {
				return fmt.Errorf("cannot complete run with unknown inherited or pending tool calls")
			}
		}
	}
	if len(r.pending) > 0 && runErr == nil {
		return fmt.Errorf("cannot complete run with pending tool calls")
	}
	if runErr == nil {
		if err := checkPlanTasksCompleted(r.state); err != nil {
			return err
		}
	}
	e := Event{EventHeader: EventHeader{Type: RunCompleted}, Run: &RunEventData{Answer: answer}}
	if runErr != nil {
		e.Type, e.Run.Error = RunFailed, runErr.Error()
		if errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded) {
			e.Type = RunCancelled
		}
	}
	if err := r.appendResultLocked(ctx, e); err != nil {
		return err
	}
	r.finished = true
	r.state.Status, r.state.Answer, r.state.Error = Completed, answer, ""
	if runErr != nil {
		r.state.Status, r.state.Error = Failed, runErr.Error()
		if e.Type == RunCancelled {
			r.state.Status = Cancelled
		}
	}
	return nil
}
