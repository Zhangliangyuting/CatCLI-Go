package execution

import (
	"AgentCLI/internal/plan"
	"context"
	"errors"
	"sync"
	"testing"
)

func recordRecoveryTask(t *testing.T, run *RunRecorder, typ EventType, id, result string) {
	t.Helper()
	if err := run.RecordTask(t.Context(), typ, TaskEventData{Revision: 1, TaskID: id, Attempt: 1, Result: result}); err != nil {
		t.Fatal(err)
	}
}

func recoveryStates(t *testing.T, recorder *Recorder) ([]Event, []RunState) {
	t.Helper()
	events, err := recorder.LoadEvents(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	states, err := Replay(events)
	if err != nil {
		t.Fatal(err)
	}
	return events, states
}

func recoverySource(t *testing.T, recorder *Recorder, id string) RunState {
	t.Helper()
	_, states := recoveryStates(t, recorder)
	for _, state := range states {
		if state.RunID == id {
			return state
		}
	}
	t.Fatalf("missing run %s", id)
	return RunState{}
}

func TestRecoveredRunInheritsProgressAfterReopeningLog(t *testing.T) {
	store := newTestStore(t)
	recorder := NewRecorder(store, "conversation")
	old, err := recorder.StartRun(t.Context(), "update code", "", PlanAndExecuteRun)
	if err != nil {
		t.Fatal(err)
	}
	p := testPlan(1)
	p.Tasks = append(p.Tasks, plan.TaskDefinition{ID: "test", Dependencies: []string{"edit"}})
	if err := old.SavePlan(t.Context(), p); err != nil {
		t.Fatal(err)
	}
	recordRecoveryTask(t, old, TaskStarted, "read", "")
	recordRecoveryTask(t, old, TaskWorkCompleted, "read", "source code")
	recordRecoveryTask(t, old, TaskCompleted, "read", "")
	recordRecoveryTask(t, old, TaskStarted, "edit", "")
	recordRecoveryTask(t, old, TaskWorkCompleted, "edit", "patch ready for review")
	before, original := recoveryStates(t, recorder)

	// Reopen storage instead of depending on the old recorder's in-memory state.
	reopened, err := NewJSONLEventStore(store.root)
	if err != nil {
		t.Fatal(err)
	}
	recorder = NewRecorder(reopened, "conversation")
	continued, inherited, err := recorder.StartRecoveredRun(t.Context(), original[0])
	if err != nil {
		t.Fatal(err)
	}
	if inherited.ResumedFrom != old.runID || inherited.Input != "update code" {
		t.Fatalf("wrong recovery origin: %+v", inherited)
	}
	if inherited.Tasks[0].Status != TaskDone || inherited.Tasks[0].Result != "source code" || inherited.Tasks[1].Status != TaskAwaitingReview || inherited.Tasks[1].Result != "patch ready for review" || inherited.Tasks[2].Status != TaskPending {
		t.Fatalf("lost task progress: %+v", inherited.Tasks)
	}
	after, _ := recoveryStates(t, recorder)
	if len(after) != len(before)+1 || after[len(before)].Type != RunStarted || after[len(before)].Run.ResumedFrom != old.runID {
		t.Fatal("handoff must be one start record, without synthetic plan/task events")
	}
	// Caller-owned restored data cannot modify the live recorder state.
	inherited.Tasks[1].Status = TaskPending
	inherited.Plan.Tasks[2].Dependencies[0] = "missing"
	recordRecoveryTask(t, continued, TaskCompleted, "edit", "review passed")
	recordRecoveryTask(t, continued, TaskStarted, "test", "")
	recordRecoveryTask(t, continued, TaskWorkCompleted, "test", "tests passed")
	recordRecoveryTask(t, continued, TaskCompleted, "test", "")
	if err := continued.FinishRun(t.Context(), "done", nil); err != nil {
		t.Fatal(err)
	}
	current, err := continued.State()
	if err != nil || current.Status != Completed || current.Answer != "done" || current.Tasks[2].Status != TaskDone {
		t.Fatalf("handle state disagrees with recorded completion: %+v %v", current, err)
	}
	_, states := recoveryStates(t, recorder)
	if states[0].Status != Resumed || states[0].Tasks[1].Status != TaskAwaitingReview || states[1].Status != Completed || states[1].Tasks[0].Result != "source code" {
		t.Fatalf("ancestor and continuation were not isolated: %+v", states)
	}
	count, _ := recoveryStates(t, recorder)
	if _, _, err := recorder.StartRecoveredRun(t.Context(), original[0]); err == nil {
		t.Fatal("recovered the same ancestor twice")
	}
	unchanged, _ := recoveryStates(t, recorder)
	if len(count) != len(unchanged) {
		t.Fatal("rejected recovery changed the log")
	}
}

func TestRepeatedRecoveryPreservesUnknownCallUntilReconciled(t *testing.T) {
	recorder := NewRecorder(newTestStore(t), "conversation")
	old, err := recorder.StartRun(t.Context(), "read source", "", PlanAndExecuteRun)
	if err != nil {
		t.Fatal(err)
	}
	if err := old.SavePlan(t.Context(), testPlan(1)); err != nil {
		t.Fatal(err)
	}
	recordRecoveryTask(t, old, TaskStarted, "read", "")
	call, err := old.StartToolCall(t.Context(), ToolCallEventData{ToolCallID: "model", ToolName: "read", Task: &TaskReference{Revision: 1, TaskID: "read", Attempt: 1}})
	if err != nil {
		t.Fatal(err)
	}
	first, _, err := recorder.StartRecoveredRun(t.Context(), recoverySource(t, recorder, old.runID))
	if err != nil {
		t.Fatal(err)
	}
	second, state, err := recorder.StartRecoveredRun(t.Context(), recoverySource(t, recorder, first.runID))
	if err != nil {
		t.Fatal(err)
	}
	if state.Calls[0].OriginRunID != old.runID || state.Calls[0].Status != Unknown || state.Tasks[0].Status != TaskInterrupted {
		t.Fatalf("lost original execution identity: %+v", state)
	}
	if err := second.CompleteToolCall(t.Context(), call.CallID, "invented return", nil); err == nil {
		t.Fatal("inherited calls must be reconciled, not completed as fresh calls")
	}
	check := ToolCallReconciliation{SourceRunID: old.runID, CallID: call.CallID, Outcome: StillUnknown, Evidence: "verification unavailable"}
	if err := second.ReconcileToolCall(t.Context(), check); err != nil {
		t.Fatal(err)
	}
	if err := second.RecordTask(t.Context(), TaskWorkCompleted, TaskEventData{Revision: 1, TaskID: "read", Attempt: 1}); err == nil {
		t.Fatal("worker completed with unresolved inherited call")
	}
	if err := second.FinishRun(t.Context(), "done", nil); err == nil {
		t.Fatal("completed with unresolved inherited call")
	}
	check.Outcome, check.Evidence, check.Result = ConfirmedCompleted, "checked original read result", "source code"
	if err := second.ReconcileToolCall(t.Context(), check); err != nil {
		t.Fatal(err)
	}
	recordRecoveryTask(t, second, TaskWorkCompleted, "read", "source code")
	recordRecoveryTask(t, second, TaskCompleted, "read", "")
	recordRecoveryTask(t, second, TaskStarted, "edit", "")
	recordRecoveryTask(t, second, TaskWorkCompleted, "edit", "patch")
	recordRecoveryTask(t, second, TaskCompleted, "edit", "")
	if err := second.FinishRun(t.Context(), "done", nil); err != nil {
		t.Fatal(err)
	}
	_, states := recoveryStates(t, recorder)
	for _, run := range states {
		if run.Calls[0].Status != Reconciled || run.Calls[0].OriginRunID != old.runID {
			t.Fatalf("reconciliation did not propagate across inheritance: %+v", run)
		}
	}
	if states[0].Tasks[0].Status != TaskInterrupted || states[2].Status != Completed {
		t.Fatal("continuation rewrote ancestor task progress")
	}
}

func TestReplayRejectsInvalidRecoverySources(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*RunEventData)
	}{
		{"wrong source", func(d *RunEventData) { d.ResumedFrom = "missing" }},
		{"changed goal", func(d *RunEventData) { d.Input = "different task" }},
		{"changed kind", func(d *RunEventData) { d.Kind = PlanAndExecuteRun }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := &RunEventData{ResumedFrom: "run"}
			tc.mutate(data)
			start := Event{EventHeader: EventHeader{RunID: "new", Type: RunStarted}, Run: data}
			if _, err := Replay(workflowLog(runStart(), planEvent(testPlan(1)), start)); err == nil {
				t.Fatal("accepted invalid recovery reference")
			}
		})
	}
}

type recoveryFailureStore struct{ EventStore }

func (s recoveryFailureStore) Append(ctx context.Context, e Event) (Event, error) {
	if e.Run != nil && e.Run.ResumedFrom != "" {
		return Event{}, errors.New("recovery start write failed")
	}
	return s.EventStore.Append(ctx, e)
}

func TestFailedRecoveryStartLeavesSourceAvailable(t *testing.T) {
	store := newTestStore(t)
	recorder := NewRecorder(store, "conversation")
	old, err := recorder.StartRun(t.Context(), "task", "", PlanAndExecuteRun)
	if err != nil {
		t.Fatal(err)
	}
	if err := old.SavePlan(t.Context(), testPlan(1)); err != nil {
		t.Fatal(err)
	}
	failing := NewRecorder(recoveryFailureStore{store}, "conversation")
	source := recoverySource(t, recorder, old.runID)
	if _, _, err := failing.StartRecoveredRun(t.Context(), source); err == nil {
		t.Fatal("ignored failed recovery handoff")
	}
	_, states := recoveryStates(t, recorder)
	if len(states) != 1 || states[0].Status != Interrupted {
		t.Fatalf("failed handoff consumed source: %+v", states)
	}
	if _, _, err := recorder.StartRecoveredRun(t.Context(), source); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentRecoveryClaimsSourceOnce(t *testing.T) {
	recorder := NewRecorder(newTestStore(t), "conversation")
	old, err := recorder.StartRun(t.Context(), "task", "", PlanAndExecuteRun)
	if err != nil {
		t.Fatal(err)
	}
	if err := old.SavePlan(t.Context(), testPlan(1)); err != nil {
		t.Fatal(err)
	}
	var wait sync.WaitGroup
	source := recoverySource(t, recorder, old.runID)
	results := make(chan error, 2)
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, _, err := recorder.StartRecoveredRun(t.Context(), source)
			results <- err
		}()
	}
	wait.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		}
	}
	if success != 1 {
		t.Fatalf("expected one handoff, got %d", success)
	}
	_, states := recoveryStates(t, recorder)
	if len(states) != 2 || states[0].Status != Resumed {
		t.Fatalf("invalid handoff history: %+v", states)
	}
}

type appendOnlyRecoveryStore struct{ EventStore }

func (s appendOnlyRecoveryStore) Load(context.Context, string, uint64) ([]Event, error) {
	return nil, errors.New("recording recovery must not load history")
}

func TestRecoveryHandoffAndReconciliationNeverReloadHistory(t *testing.T) {
	store := newTestStore(t)
	recorder := NewRecorder(store, "conversation")
	old, err := recorder.StartRun(t.Context(), "task", "", PlanAndExecuteRun)
	if err != nil {
		t.Fatal(err)
	}
	if err := old.SavePlan(t.Context(), testPlan(1)); err != nil {
		t.Fatal(err)
	}
	call, err := old.StartToolCall(t.Context(), ToolCallEventData{ToolCallID: "model", ToolName: "write", Arguments: `{}`})
	if err != nil {
		t.Fatal(err)
	}
	source := recoverySource(t, recorder, old.runID)
	appendOnly := NewRecorder(appendOnlyRecoveryStore{store}, "conversation")
	run, _, err := appendOnly.StartRecoveredRun(t.Context(), source)
	if err != nil {
		t.Fatal(err)
	}
	if err := run.ReconcileToolCall(t.Context(), ToolCallReconciliation{SourceRunID: source.RunID, CallID: call.CallID, Outcome: ConfirmedCompleted, Evidence: "checked external state", Result: "done"}); err != nil {
		t.Fatal(err)
	}
	state, err := run.State()
	if err != nil {
		t.Fatal(err)
	}
	if state.Calls[0].Status != Reconciled || state.Calls[0].Result != "done" {
		t.Fatalf("state not updated: %+v", state)
	}
	state.Calls[0].Status = Unknown
	_, states := recoveryStates(t, recorder)
	if states[1].Calls[0].Status != Reconciled {
		t.Fatal("returned state aliases authoritative state")
	}
}
