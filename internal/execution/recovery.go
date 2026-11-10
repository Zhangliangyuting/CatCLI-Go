package execution

import (
	"context"
	"encoding/json"
	"fmt"
)

// cloneRun separates restored progress from both the ancestor and caller-owned
// slices, task definitions and tool references. No runtime objects are copied.
func cloneRun(state RunState) (RunState, error) {
	data, err := json.Marshal(state)
	if err != nil {
		return RunState{}, err
	}
	var copy RunState
	if err := json.Unmarshal(data, &copy); err != nil {
		return RunState{}, err
	}
	return copy, nil
}

func inheritRun(source RunState, start Event) (RunState, error) {
	if source.Plan == nil {
		return RunState{}, fmt.Errorf("workflow recovery requires a confirmed source plan")
	}
	if start.Run.Input != source.Input {
		return RunState{}, fmt.Errorf("workflow recovery must preserve original input")
	}
	state, err := cloneRun(source)
	if err != nil {
		return RunState{}, err
	}
	state.RunID, state.ResumedFrom = start.RunID, source.RunID
	if start.Run.Kind != source.Kind {
		return RunState{}, fmt.Errorf("recovery cannot change execution kind")
	}
	state.Status, state.LastSequence = Interrupted, start.Sequence
	state.Answer, state.Error = "", ""
	return state, nil
}

// StartRecoveredRun records a handoff using the authoritative state already
// loaded and replayed by Resume. It does not read or replay history. The caller
// must provide a current state and ensure the original executor has stopped.
func (r *Recorder) StartRecoveredRun(ctx context.Context, source RunState) (*RunRecorder, RunState, error) {
	if r == nil {
		return nil, RunState{}, fmt.Errorf("execution recorder is not configured")
	}
	r.startMu.Lock()
	defer r.startMu.Unlock()
	if source.ConversationID != r.conversationID || !validID(source.RunID) || source.Status != Interrupted {
		return nil, RunState{}, fmt.Errorf("source run is not interrupted")
	}
	if _, claimed := r.resumed[source.RunID]; claimed {
		return nil, RunState{}, fmt.Errorf("source run already resumed")
	}
	id, err := NewID()
	if err != nil {
		return nil, RunState{}, err
	}
	start := Event{EventHeader: EventHeader{
		ConversationID: r.conversationID, RunID: id, Type: RunStarted,
	}, Run: &RunEventData{Kind: source.Kind, Input: source.Input, ResumedFrom: source.RunID}}
	restored, err := inheritRun(source, start)
	if err != nil {
		return nil, RunState{}, err
	}
	internal, err := cloneRun(restored)
	if err != nil {
		return nil, RunState{}, err
	}
	run := &RunRecorder{recorder: r, runID: id, pending: make(map[string]ToolCallEventData), state: internal}
	if err := run.appendLocked(ctx, start); err != nil {
		return nil, RunState{}, err
	}
	r.resumed[source.RunID] = struct{}{}
	restored.LastSequence = run.state.LastSequence
	return run, restored, nil
}

// State returns an independent copy of this handle's current recording state.
// It performs no persistence or replay.
func (r *RunRecorder) State() (RunState, error) {
	if r == nil {
		return RunState{}, fmt.Errorf("execution run is not configured")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return cloneRun(r.state)
}
