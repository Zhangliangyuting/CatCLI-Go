package execution

import (
	"AgentCLI/internal/plan"
	"context"
	"errors"
	"testing"
	"time"
)

func testPlan(revision uint64) PlanEventData {
	return PlanEventData{PlanID: "plan", Revision: revision, Goal: "update code", Tasks: []plan.TaskDefinition{
		{ID: "read", Type: "FILE_READ", ReadResources: []string{"main.go"}},
		{ID: "edit", Type: "FILE_WRITE", Dependencies: []string{"read"}, WriteResources: []string{"main.go"}},
	}}
}

func planEvent(p PlanEventData) Event {
	return Event{EventHeader: EventHeader{Type: PlanSaved}, Plan: &p}
}
func taskEvent(typ EventType, id string, revision, attempt uint64) Event {
	return Event{EventHeader: EventHeader{Type: typ}, Task: &TaskEventData{Revision: revision, TaskID: id, Attempt: attempt}}
}
func workflowLog(events ...Event) []Event {
	for i := range events {
		events[i].Sequence = uint64(i) + 1
		events[i].Timestamp = time.Now().UTC()
		events[i].ConversationID = "conversation"
		if events[i].RunID == "" {
			events[i].RunID = "run"
		}
	}
	return events
}
func runStart() Event { return Event{EventHeader: EventHeader{Type: RunStarted}, Run: &RunEventData{}} }

func TestWorkflowPersistsPlanAttemptsReviewAndToolOwnership(t *testing.T) {
	store := newTestStore(t)
	recorder := NewRecorder(store, "conversation")
	run, err := recorder.StartRun(t.Context(), "update code", "", PlanAndExecuteRun)
	if err != nil {
		t.Fatal(err)
	}
	p := testPlan(1)
	if err := run.SavePlan(t.Context(), p); err != nil {
		t.Fatal(err)
	}
	// The recorder must not retain aliases into a mutable planner draft.
	p.Tasks[1].Dependencies[0] = "wrong"
	record := func(typ EventType, id string, attempt uint64) {
		t.Helper()
		if err := run.RecordTask(t.Context(), typ, TaskEventData{Revision: 1, TaskID: id, Attempt: attempt, Result: "evidence"}); err != nil {
			t.Fatal(err)
		}
	}
	record(TaskStarted, "read", 1)
	if err := run.RecordTask(t.Context(), TaskStarted, TaskEventData{Revision: 1, TaskID: "edit", Attempt: 1}); err == nil {
		t.Fatal("started before dependency review")
	}
	call, err := run.StartToolCall(t.Context(), ToolCallEventData{ToolCallID: "model", ToolName: "read_file", Arguments: `{}`, Task: &TaskReference{Revision: 1, TaskID: "read", Attempt: 1}})
	if err != nil {
		t.Fatal(err)
	}
	if err := run.RecordTask(t.Context(), TaskWorkCompleted, TaskEventData{Revision: 1, TaskID: "read", Attempt: 1}); err == nil {
		t.Fatal("worker completed with unknown tool call")
	}
	if err := run.CompleteToolCall(t.Context(), call.CallID, "source code", nil); err != nil {
		t.Fatal(err)
	}
	record(TaskWorkCompleted, "read", 1)
	if err := run.FinishRun(t.Context(), "done", nil); err == nil {
		t.Fatal("completed before review")
	}
	record(TaskCompleted, "read", 1)
	record(TaskStarted, "edit", 1)
	record(TaskFailed, "edit", 1)
	record(TaskStarted, "edit", 2)
	record(TaskWorkCompleted, "edit", 2)
	record(TaskCompleted, "edit", 2)
	if err := run.FinishRun(t.Context(), "done", nil); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewJSONLEventStore(store.root)
	if err != nil {
		t.Fatal(err)
	}
	events, err := reopened.Load(t.Context(), "conversation", 0)
	if err != nil {
		t.Fatal(err)
	}
	runs, err := Replay(events)
	if err != nil {
		t.Fatal(err)
	}
	state := runs[0]
	if state.Status != Completed || state.Plan == nil || state.Tasks[1].Attempt != 2 || state.Tasks[1].Status != TaskDone || state.Calls[0].Task.TaskID != "read" {
		t.Fatalf("wrong recovered workflow: %+v", state)
	}
	if state.Plan.Tasks[1].Dependencies[0] != "read" {
		t.Fatal("draft mutation corrupted saved plan")
	}
}

func TestReplayRetainsOnlyCurrentPlanAndTasks(t *testing.T) {
	events := workflowLog(runStart(), planEvent(testPlan(1)), planEvent(testPlan(2)), taskEvent(TaskStarted, "read", 2, 1))
	runs, err := Replay(events)
	if err != nil {
		t.Fatal(err)
	}
	state := runs[0]
	if state.Plan == nil || state.Plan.Revision != 2 || len(state.Tasks) != 2 || state.Tasks[0].Revision != 2 || state.Tasks[0].Status != TaskInterrupted || state.Tasks[1].Status != TaskPending {
		t.Fatalf("wrong snapshot: %+v", state)
	}
	// Worker output is retained even if review never finishes.
	events = append(events, taskEvent(TaskWorkCompleted, "read", 2, 1))
	events = workflowLog(events...)
	runs, err = Replay(events)
	if err != nil || runs[0].Tasks[0].Status != TaskAwaitingReview {
		t.Fatalf("runs=%+v err=%v", runs, err)
	}
}

func TestReplayRejectsInvalidWorkflowTransitions(t *testing.T) {
	base := []Event{runStart(), planEvent(testPlan(1))}
	for _, tc := range []struct {
		name string
		tail []Event
	}{
		{"duplicate revision", []Event{planEvent(testPlan(1))}},
		{"skipped revision", []Event{planEvent(testPlan(3))}},
		{"unknown task", []Event{taskEvent(TaskStarted, "missing", 1, 1)}},
		{"wrong revision", []Event{taskEvent(TaskStarted, "read", 2, 1)}},
		{"late old result", []Event{planEvent(testPlan(2)), taskEvent(TaskWorkCompleted, "read", 1, 1)}},
		{"dependency not completed", []Event{taskEvent(TaskStarted, "edit", 1, 1)}},
		{"result before start", []Event{taskEvent(TaskWorkCompleted, "read", 1, 1)}},
		{"skip review", []Event{taskEvent(TaskStarted, "read", 1, 1), taskEvent(TaskCompleted, "read", 1, 1)}},
		{"duplicate start", []Event{taskEvent(TaskStarted, "read", 1, 1), taskEvent(TaskStarted, "read", 1, 1)}},
		{"wrong attempt", []Event{taskEvent(TaskStarted, "read", 1, 1), taskEvent(TaskWorkCompleted, "read", 1, 2)}},
		{"replace active plan", []Event{taskEvent(TaskStarted, "read", 1, 1), planEvent(testPlan(2))}},
		{"replace awaiting review", []Event{taskEvent(TaskStarted, "read", 1, 1), taskEvent(TaskWorkCompleted, "read", 1, 1), planEvent(testPlan(2))}},
		{"finish pending tasks", []Event{{EventHeader: EventHeader{Type: RunCompleted}, Run: &RunEventData{}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			events := append(append([]Event(nil), base...), tc.tail...)
			if _, err := Replay(workflowLog(events...)); err == nil {
				t.Fatal("accepted invalid workflow")
			}
		})
	}
}

func TestPlanRejectsBrokenDependencyGraphsAndMixedPayloads(t *testing.T) {
	for _, mutate := range []func(*PlanEventData){
		func(p *PlanEventData) { p.Tasks[1].ID = "read" },
		func(p *PlanEventData) { p.Tasks[1].Dependencies = []string{"missing"} },
		func(p *PlanEventData) { p.Tasks[0].Dependencies = []string{"edit"} },
		func(p *PlanEventData) { p.Tasks[1].Dependencies = []string{"read", "read"} },
		func(p *PlanEventData) { p.Revision = 0 },
	} {
		p := testPlan(1)
		mutate(&p)
		if _, err := Replay(workflowLog(runStart(), planEvent(p))); err == nil {
			t.Fatal("accepted malformed plan")
		}
	}
	e := planEvent(testPlan(1))
	e.Run = &RunEventData{}
	if _, err := Replay(workflowLog(runStart(), e)); err == nil {
		t.Fatal("accepted mixed payloads")
	}
}

func TestRecoveryReconcilesAncestorCallWithoutReexecutingIt(t *testing.T) {
	recorder := NewRecorder(newTestStore(t), "conversation")
	old, err := recorder.StartRun(t.Context(), "write config", "", PlanAndExecuteRun)
	if err != nil {
		t.Fatal(err)
	}
	if err := old.SavePlan(t.Context(), testPlan(1)); err != nil {
		t.Fatal(err)
	}
	call, err := old.StartToolCall(t.Context(), ToolCallEventData{ToolCallID: "model", ToolName: "write_file"})
	if err != nil {
		t.Fatal(err)
	}
	continued, _, err := recorder.StartRecoveredRun(t.Context(), recoverySource(t, recorder, old.runID))
	if err != nil {
		t.Fatal(err)
	}
	check := ToolCallReconciliation{SourceRunID: old.runID, CallID: call.CallID, Outcome: StillUnknown, Evidence: "read unavailable"}
	if err := continued.ReconcileToolCall(t.Context(), check); err != nil {
		t.Fatal(err)
	}
	events, _ := recorder.LoadEvents(t.Context())
	runs, err := Replay(events)
	if err != nil || runs[0].Calls[0].Status != Unknown {
		t.Fatalf("lost uncertainty: %+v %v", runs, err)
	}
	check.Outcome, check.Evidence, check.Result = ConfirmedCompleted, "read config: port is 8080", "port=8080"
	if err := continued.ReconcileToolCall(t.Context(), check); err != nil {
		t.Fatal(err)
	}
	if err := continued.ReconcileToolCall(t.Context(), check); err == nil {
		t.Fatal("overwrote resolved call")
	}
	for _, id := range []string{"read", "edit"} {
		recordRecoveryTask(t, continued, TaskStarted, id, "")
		recordRecoveryTask(t, continued, TaskWorkCompleted, id, "verified")
		recordRecoveryTask(t, continued, TaskCompleted, id, "")
	}
	if err := continued.FinishRun(t.Context(), "verified", nil); err != nil {
		t.Fatal(err)
	}
	events, _ = recorder.LoadEvents(t.Context())
	runs, err = Replay(events)
	if err != nil {
		t.Fatal(err)
	}
	c := runs[0].Calls[0]
	if runs[0].Status != Resumed || runs[1].Status != Completed || c.Status != Reconciled || c.Result != "port=8080" || c.Reconciliation.Evidence != check.Evidence {
		t.Fatalf("bad recovery: %+v", runs)
	}
}

func TestReconciliationRejectsUnrelatedMissingOrUnprovenCalls(t *testing.T) {
	rec := NewRecorder(newTestStore(t), "conversation")
	old, _ := rec.StartRun(t.Context(), "task", "", PlanAndExecuteRun)
	if err := old.SavePlan(t.Context(), testPlan(1)); err != nil {
		t.Fatal(err)
	}
	call, _ := old.StartToolCall(t.Context(), ToolCallEventData{ToolCallID: "model", ToolName: "write"})
	unrelated, _ := rec.StartRun(t.Context(), "other", "", ReActRun)
	data := ToolCallReconciliation{SourceRunID: old.runID, CallID: call.CallID, Outcome: ConfirmedNotExecuted, Evidence: "external audit"}
	if err := unrelated.ReconcileToolCall(t.Context(), data); err == nil {
		t.Fatal("allowed unrelated source")
	}
	recovery, _, err := rec.StartRecoveredRun(t.Context(), recoverySource(t, rec, old.runID))
	if err != nil {
		t.Fatal(err)
	}
	missing := data
	missing.CallID = "missing"
	if err := recovery.ReconcileToolCall(t.Context(), missing); err == nil {
		t.Fatal("allowed missing call")
	}
	missing = data
	missing.Evidence = ""
	if err := recovery.ReconcileToolCall(t.Context(), missing); err == nil {
		t.Fatal("allowed evidence-free resolution")
	}
	if err := recovery.ReconcileToolCall(t.Context(), data); err != nil {
		t.Fatal(err)
	}
	events, _ := rec.LoadEvents(t.Context())
	runs, err := Replay(events)
	if err != nil || runs[0].Calls[0].Status != NotExecuted {
		t.Fatalf("not-executed lost: %+v %v", runs, err)
	}
}

type planWriteFailureStore struct{ EventStore }

func (s planWriteFailureStore) Append(ctx context.Context, e Event) (Event, error) {
	if e.Type == PlanSaved {
		return Event{}, errors.New("disk failure")
	}
	return s.EventStore.Append(ctx, e)
}
func TestFailedPlanSavePreventsFurtherRecordingAndSuccess(t *testing.T) {
	rec := NewRecorder(planWriteFailureStore{newTestStore(t)}, "conversation")
	run, _ := rec.StartRun(t.Context(), "task", "", PlanAndExecuteRun)
	if err := run.SavePlan(t.Context(), testPlan(1)); err == nil {
		t.Fatal("ignored disk failure")
	}
	if _, err := run.StartToolCall(t.Context(), ToolCallEventData{ToolCallID: "model", ToolName: "write"}); err == nil {
		t.Fatal("dispatched after failed plan save")
	}
	if err := run.FinishRun(t.Context(), "done", nil); err == nil {
		t.Fatal("completed after failed save")
	}
}

func TestFailedPlanReplacementKeepsCurrentPlanAndTasks(t *testing.T) {
	store := newTestStore(t)
	recorder := NewRecorder(store, "conversation")
	run, err := recorder.StartRun(t.Context(), "task", "", PlanAndExecuteRun)
	if err != nil {
		t.Fatal(err)
	}
	if err := run.SavePlan(t.Context(), testPlan(1)); err != nil {
		t.Fatal(err)
	}
	recordRecoveryTask(t, run, TaskStarted, "read", "")
	recordRecoveryTask(t, run, TaskWorkCompleted, "read", "saved result")
	recordRecoveryTask(t, run, TaskCompleted, "read", "")
	replacement := testPlan(2)
	replacement.Tasks = []plan.TaskDefinition{{ID: "new-task", Type: plan.ANALYSIS}}
	recorder.store = planWriteFailureStore{store}
	if err := run.SavePlan(t.Context(), replacement); err == nil {
		t.Fatal("ignored failed plan replacement")
	}
	state, err := run.State()
	if err != nil {
		t.Fatal(err)
	}
	if state.Plan == nil || state.Plan.Revision != 1 || len(state.Tasks) != 2 || state.Tasks[0].Definition.ID != "read" || state.Tasks[0].Status != TaskDone || state.Tasks[0].Result != "saved result" || state.Tasks[1].Status != TaskPending {
		t.Fatalf("failed persistence replaced committed state: %+v", state)
	}
	events, states := recoveryStates(t, recorder)
	saved := 0
	for _, event := range events {
		if event.Type == PlanSaved {
			saved++
		}
	}
	if saved != 1 || states[0].Plan.Revision != 1 {
		t.Fatal("failed plan appeared in committed event history")
	}
}

func TestUnknownToolCallBlocksTaskRetryUntilChecked(t *testing.T) {
	rec := NewRecorder(newTestStore(t), "conversation")
	run, _ := rec.StartRun(t.Context(), "task", "", PlanAndExecuteRun)
	if err := run.SavePlan(t.Context(), testPlan(1)); err != nil {
		t.Fatal(err)
	}
	data := TaskEventData{Revision: 1, TaskID: "read", Attempt: 1}
	if err := run.RecordTask(t.Context(), TaskStarted, data); err != nil {
		t.Fatal(err)
	}
	call, err := run.StartToolCall(t.Context(), ToolCallEventData{ToolCallID: "model", ToolName: "read", Task: &TaskReference{Revision: 1, TaskID: "read", Attempt: 1}})
	if err != nil {
		t.Fatal(err)
	}
	// The returned reference must not be able to rewrite the stored ownership.
	call.Task.TaskID = "mutated"
	if err := run.RecordTask(t.Context(), TaskFailed, data); err != nil {
		t.Fatal(err)
	}
	data.Attempt = 2
	if err := run.RecordTask(t.Context(), TaskStarted, data); err == nil {
		t.Fatal("retried while earlier call remains unknown")
	}
	if err := run.ReconcileToolCall(t.Context(), ToolCallReconciliation{SourceRunID: run.runID, CallID: call.CallID, Outcome: ConfirmedNotExecuted, Evidence: "independent execution audit confirms no dispatch"}); err != nil {
		t.Fatal(err)
	}
	if err := run.RecordTask(t.Context(), TaskStarted, data); err != nil {
		t.Fatal(err)
	}
	stale := ToolCallEventData{ToolCallID: "stale", ToolName: "read", Task: &TaskReference{Revision: 1, TaskID: "read", Attempt: 1}}
	if _, err := run.StartToolCall(t.Context(), stale); err == nil {
		t.Fatal("accepted stale task attempt")
	}
	events, _ := rec.LoadEvents(t.Context())
	runs, err := Replay(events)
	if err != nil || runs[0].Tasks[0].Attempt != 2 || runs[0].Calls[0].Task.TaskID != "read" {
		t.Fatalf("runs=%+v err=%v", runs, err)
	}
}

func TestReconciliationCannotOverwriteReturnedError(t *testing.T) {
	rec := NewRecorder(newTestStore(t), "conversation")
	run, _ := rec.StartRun(t.Context(), "task", "", ReActRun)
	call, err := run.StartToolCall(t.Context(), ToolCallEventData{ToolCallID: "model", ToolName: "write"})
	if err != nil {
		t.Fatal(err)
	}
	if err := run.CompleteToolCall(t.Context(), call.CallID, "partial result", errors.New("external error")); err != nil {
		t.Fatal(err)
	}
	if err := run.ReconcileToolCall(t.Context(), ToolCallReconciliation{SourceRunID: run.runID, CallID: call.CallID, Outcome: ConfirmedCompleted, Evidence: "a guess"}); err == nil {
		t.Fatal("overwrote returned tool error")
	}
	events, _ := rec.LoadEvents(t.Context())
	runs, err := Replay(events)
	if err != nil || runs[0].Calls[0].Error != "external error" || runs[0].Calls[0].Status != Returned {
		t.Fatalf("runs=%+v err=%v", runs, err)
	}
}
