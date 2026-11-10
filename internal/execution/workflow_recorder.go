package execution

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// SavePlan records only a confirmed plan. The scheduler must wait for success
// before dispatching tasks. Payloads are copied so callers can reuse drafts.
func (r *RunRecorder) SavePlan(ctx context.Context, plan PlanEventData) error {
	return r.recordWorkflow(ctx, Event{EventHeader: EventHeader{Type: PlanSaved}, Plan: &plan})
}

// RecordTask records a task transition, including the plan revision and attempt.
func (r *RunRecorder) RecordTask(ctx context.Context, typ EventType, task TaskEventData) error {
	switch typ {
	case TaskStarted, TaskWorkCompleted, TaskCompleted, TaskFailed, TaskCancelled:
	default:
		return fmt.Errorf("not a task event: %s", typ)
	}
	return r.recordWorkflow(ctx, Event{EventHeader: EventHeader{Type: typ}, Task: &task})
}

func (r *RunRecorder) recordWorkflow(ctx context.Context, e Event) error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	// Keep independent data in the recorder; a caller may mutate its draft later.
	data, err := json.Marshal(e)
	if err != nil {
		return err
	}
	var copied Event
	if err := json.Unmarshal(data, &copied); err != nil {
		return err
	}
	e = copied
	e.Sequence, e.Timestamp, e.ConversationID, e.RunID = 1, time.Now().UTC(), r.recorder.conversationID, r.runID
	if err := validateEvent(e); err != nil {
		return err
	}
	next := r.state
	next.Tasks = append([]TaskState(nil), r.state.Tasks...)
	if err := updatePlanAndTaskState(&next, e); err != nil {
		return err
	}
	if e.Type == PlanSaved || e.Type == TaskStarted {
		err = r.appendLocked(ctx, e)
	} else {
		err = r.appendResultLocked(ctx, e)
	}
	if err != nil {
		return err
	}
	next.LastSequence = r.state.LastSequence
	r.state = next
	return nil
}

// ReconcileToolCall records a checked outcome or permission to retry an unknown call.
// It does not reload history; Resume supplies authoritative state at handoff.
func (r *RunRecorder) ReconcileToolCall(ctx context.Context, reconciliation ToolCallReconciliation) error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.writeErr != nil {
		return r.writeErr
	}
	if r.finished {
		return fmt.Errorf("execution run already finished")
	}
	e := Event{EventHeader: EventHeader{
		Sequence: 1, Timestamp: time.Now().UTC(),
		ConversationID: r.recorder.conversationID, RunID: r.runID, Type: ToolCallReconciled,
	}, Reconciliation: &reconciliation}
	if err := validateEvent(e); err != nil {
		return err
	}
	index := -1
	for i, call := range r.state.Calls {
		if call.CallID == reconciliation.CallID {
			index = i
			break
		}
	}
	if index < 0 {
		return fmt.Errorf("reconciliation call is not part of this run's state")
	}
	call := r.state.Calls[index]
	if reconciliation.SourceRunID != r.runID && reconciliation.SourceRunID != r.state.ResumedFrom && reconciliation.SourceRunID != call.OriginRunID {
		return fmt.Errorf("cannot reconcile unrelated run")
	}
	if call.Status != Unknown {
		return fmt.Errorf("cannot reconcile already resolved tool call")
	}
	if err := r.appendResultLocked(ctx, e); err != nil {
		return err
	}
	reconcileCall(&call, reconciliation)
	r.state.Calls[index] = call
	if reconciliation.Outcome != StillUnknown {
		delete(r.pending, reconciliation.CallID)
	}
	return nil
}
