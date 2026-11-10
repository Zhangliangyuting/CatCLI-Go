package multiagent

import (
	executionlog "AgentCLI/internal/execution"
	"AgentCLI/internal/llm"
	"AgentCLI/internal/plan"
	"AgentCLI/internal/tool"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const executionTestPlan = `{"goal":"update code","tasks":[{"id":"task_1","name":"update","description":"update file","type":"FILE_WRITE","dependencies":[],"write_resources":["main.go"]}],"summary":"update"}`

type workflowTestFixture struct {
	planJSON                               string
	writesStarted                          chan struct{}
	releaseWrites                          chan struct{}
	agent                                  *PlanAndExecuteAgent
	recorder                               *executionlog.Recorder
	store                                  *executionlog.JSONLEventStore
	plannerCalls, workerCalls, reviewCalls atomic.Int32
	writes, reads                          atomic.Int32
	rejectReviews                          atomic.Int32
}

func newExecutionFixture(t *testing.T) *workflowTestFixture {
	t.Helper()
	f := &workflowTestFixture{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var request llm.ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			http.Error(w, "bad request", 400)
			return
		}
		var response llm.Message
		system := strings.TrimSpace(request.Messages[0].Content)
		last := request.Messages[len(request.Messages)-1]
		switch system {
		case strings.TrimSpace(PlannerSystemPrompt):
			f.plannerCalls.Add(1)
			content := f.planJSON
			if content == "" {
				content = executionTestPlan
			}
			response = llm.AssistantMessage(content)
		case WorkerSystemPrompt:
			f.workerCalls.Add(1)
			if last.Role == "tool" {
				response = llm.AssistantMessage("worker complete")
			} else {
				response = llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "worker-call", Type: "function", Function: llm.FunctionCall{Name: "mcp__filesystem__write_file", Arguments: `{"path":"main.go","content":"updated"}`}}}}
			}
		case ReviewerSystemPrompt:
			f.reviewCalls.Add(1)
			if last.Role == "tool" {
				if f.rejectReviews.Load() > 0 {
					f.rejectReviews.Add(-1)
					response = llm.AssistantMessage(`{"approved":false,"feedback":"fix result"}`)
				} else {
					response = llm.AssistantMessage(`{"approved":true,"feedback":"verified"}`)
				}
			} else {
				response = llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "review-call", Type: "function", Function: llm.FunctionCall{Name: "mcp__filesystem__read_file", Arguments: `{"path":"main.go"}`}}}}
			}
		default:
			t.Errorf("unexpected model role %q", system)
			response = llm.AssistantMessage("unexpected role")
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(llm.ChatResponse{Choices: []llm.Choice{{Message: response, FinishReason: "stop"}}})
	}))
	t.Cleanup(server.Close)
	client := &llm.OpenAICompatibleClient{APIKey: "test", BaseURL: server.URL, Model: "test", HTTPClient: server.Client()}
	runtime := newTestMemoryRuntime(t, client)
	store, err := executionlog.NewJSONLEventStore(filepath.Join(t.TempDir(), "execution"))
	if err != nil {
		t.Fatal(err)
	}
	f.store = store
	f.recorder = executionlog.NewRecorder(store, runtime.RootContext().ConversationID)
	registry := tool.NewToolRegistry()
	register := func(name string, handler tool.Handler) {
		registry.RegisterTool(tool.Definition{Type: "function", FunctionDefinition: tool.FunctionDefinition{Name: name, Parameters: map[string]any{"type": "object"}}}, handler)
	}
	register("mcp__filesystem__write_file", func(ctx context.Context, _ map[string]any) (string, error) {
		f.writes.Add(1)
		if f.writesStarted != nil {
			select {
			case f.writesStarted <- struct{}{}:
			case <-ctx.Done():
				return "", ctx.Err()
			}
			select {
			case <-f.releaseWrites:
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}
		return "updated", nil
	})
	register("mcp__filesystem__read_file", func(context.Context, map[string]any) (string, error) { f.reads.Add(1); return "updated", nil })
	planner, err := NewPlannerSubAgent(client)
	if err != nil {
		t.Fatal(err)
	}
	reviewer, err := NewReviewerSubAgent(client, registry.Subset("mcp__filesystem__read_file"))
	if err != nil {
		t.Fatal(err)
	}
	f.agent, err = NewPlanAndExecuteAgent(planner, client, registry, reviewer, runtime, &workflowDecisionProvider{actions: []PlanAction{PlanExecute}}, 1, 2, 0, 0, WithRecorder(f.recorder))
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *workflowTestFixture) states(t *testing.T) ([]executionlog.Event, []executionlog.RunState) {
	t.Helper()
	events, err := f.recorder.LoadEvents(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	runs, err := executionlog.Replay(events)
	if err != nil {
		t.Fatal(err)
	}
	return events, runs
}

func TestPlanExecutionRecordsWorkerAndReviewerMCPCalls(t *testing.T) {
	f := newExecutionFixture(t)
	if _, err := f.agent.Run(t.Context(), "update code"); err != nil {
		t.Fatal(err)
	}
	events, runs := f.states(t)
	state := runs[0]
	if state.Kind != executionlog.PlanAndExecuteRun || state.Status != executionlog.Completed || state.Plan == nil || len(state.Calls) != 2 || state.Tasks[0].Status != executionlog.TaskDone {
		t.Fatalf("incomplete execution log: %+v", state)
	}
	if f.writes.Load() != 1 || f.reads.Load() != 1 || state.Calls[0].Task.Phase != "worker" || state.Calls[1].Task.Phase != "reviewer" {
		t.Fatalf("wrong role tools: %+v", state.Calls)
	}
	positions := map[executionlog.EventType]int{}
	for i, event := range events {
		if _, ok := positions[event.Type]; !ok {
			positions[event.Type] = i
		}
	}
	if !(positions[executionlog.PlanSaved] < positions[executionlog.TaskStarted] && positions[executionlog.TaskStarted] < positions[executionlog.ToolCallStarted] && positions[executionlog.ToolCallCompleted] < positions[executionlog.TaskWorkCompleted] && positions[executionlog.TaskWorkCompleted] < positions[executionlog.TaskCompleted]) {
		t.Fatal("execution facts persisted in wrong order")
	}
}

func TestExecutionFailureSavesReplannedVersion(t *testing.T) {
	f := newExecutionFixture(t)
	f.rejectReviews.Store(1)
	if _, err := f.agent.Run(t.Context(), "update code"); err != nil {
		t.Fatal(err)
	}
	events, runs := f.states(t)
	state := runs[0]
	if state.Plan == nil || state.Plan.Revision != 2 || len(state.Tasks) != 1 || state.Tasks[0].Revision != 2 || state.Tasks[0].Status != executionlog.TaskDone {
		t.Fatalf("wrong replan history: %+v", state)
	}
	var history []*executionlog.PlanEventData
	failedOldTask := false
	for _, event := range events {
		if event.Type == executionlog.PlanSaved {
			history = append(history, event.Plan)
		}
		if event.Type == executionlog.TaskFailed && event.Task.Revision == 1 {
			failedOldTask = true
		}
	}
	if len(history) != 2 || history[0].PlanID != history[1].PlanID || history[0].Revision != 1 || history[1].Revision != 2 || !failedOldTask {
		t.Fatal("replacing current state lost persisted execution history")
	}
}

func (f *workflowTestFixture) interruptedPlan(t *testing.T, stage string) executionlog.RunState {
	t.Helper()
	run, err := f.recorder.StartRun(t.Context(), "update code", "", executionlog.PlanAndExecuteRun)
	if err != nil {
		t.Fatal(err)
	}
	p, err := parsePlan(executionTestPlan)
	if err != nil {
		t.Fatal(err)
	}
	saved := executionlog.PlanEventData{PlanID: "saved-plan", Revision: 1, Goal: p.Goal(), Summary: p.Summary()}
	for _, id := range p.ExecutionOrder() {
		task, _ := p.TaskByID(id)
		saved.Tasks = append(saved.Tasks, task.Definition())
	}
	if stage == "mixed" {
		saved.Tasks = []plan.TaskDefinition{
			{ID: "a", Name: "done", Description: "completed task"},
			{ID: "b", Name: "review", Description: "review saved output", Dependencies: []string{"a"}},
			{ID: "c", Name: "pending", Description: "remaining work", Dependencies: []string{"b"}},
		}
	}
	if err := run.SavePlan(t.Context(), saved); err != nil {
		t.Fatal(err)
	}
	record := func(typ executionlog.EventType, id, result string) {
		t.Helper()
		if err := run.RecordTask(t.Context(), typ, executionlog.TaskEventData{Revision: 1, TaskID: id, Attempt: 1, Result: result}); err != nil {
			t.Fatal(err)
		}
	}
	if stage == "mixed" {
		record(executionlog.TaskStarted, "a", "")
		record(executionlog.TaskWorkCompleted, "a", "saved A")
		record(executionlog.TaskCompleted, "a", "")
		record(executionlog.TaskStarted, "b", "")
		record(executionlog.TaskWorkCompleted, "b", "saved B")
	} else if stage == "unknown" {
		record(executionlog.TaskStarted, "task_1", "")
		_, err := run.StartToolCall(t.Context(), executionlog.ToolCallEventData{ToolCallID: "old-call", ToolName: "mcp__filesystem__write_file", Arguments: `{"content":"updated","path":"main.go"}`, Task: &executionlog.TaskReference{Revision: 1, TaskID: "task_1", Attempt: 1, Phase: "worker"}})
		if err != nil {
			t.Fatal(err)
		}
	}
	_, states := f.states(t)
	return states[len(states)-1]
}

func TestPlanResumeSkipsCompletedAndReviewsSavedWorkerOutput(t *testing.T) {
	f := newExecutionFixture(t)
	source := f.interruptedPlan(t, "mixed")
	answer, err := f.agent.Resume(t.Context(), source)
	if err != nil {
		t.Fatal(err)
	}
	if f.plannerCalls.Load() != 0 || f.writes.Load() != 1 || f.reads.Load() != 2 || !strings.Contains(answer, "saved A") {
		t.Fatalf("unexpected resumed work: planner=%d writes=%d reads=%d answer=%s", f.plannerCalls.Load(), f.writes.Load(), f.reads.Load(), answer)
	}
	_, runs := f.states(t)
	state := runs[1]
	if runs[0].Status != executionlog.Resumed || state.Status != executionlog.Completed || state.Plan == nil || state.Tasks[0].Result != "saved A" || state.Tasks[1].Result != "saved B" {
		t.Fatalf("lost restored state: %+v", runs)
	}
	for _, call := range state.Calls {
		if call.Task.TaskID == "a" || call.Task.TaskID == "b" && call.Task.Phase == "worker" {
			t.Fatal("re-executed completed work")
		}
	}
	if _, err := f.agent.Resume(t.Context(), source); err == nil {
		t.Fatal("resumed consumed source twice")
	}
}

// Build a historical reconciliation event to test compatibility with logs that
// already contain a checked outcome. Resume itself performs no verification.
func (f *workflowTestFixture) withRecordedReconciliation(t *testing.T, source executionlog.RunState, outcome executionlog.ReconciliationOutcome, result string) executionlog.RunState {
	t.Helper()
	_, err := f.store.Append(t.Context(), executionlog.Event{
		EventHeader: executionlog.EventHeader{ConversationID: source.ConversationID, RunID: source.RunID, Type: executionlog.ToolCallReconciled},
		Reconciliation: &executionlog.ToolCallReconciliation{
			SourceRunID: source.RunID, CallID: source.Calls[0].CallID,
			Outcome: outcome, Evidence: "pre-existing checked operation record", Result: result,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, runs := f.states(t)
	return runs[len(runs)-1]
}

func TestUnknownWriteMustBeCheckedAndVerifiedResultIsReused(t *testing.T) {
	f := newExecutionFixture(t)
	source := f.interruptedPlan(t, "unknown")
	before, _ := f.states(t)
	if _, err := f.agent.Resume(t.Context(), source); !errors.Is(err, ErrRecoveryNeedsVerification) {
		t.Fatalf("expected verification gate, got %v", err)
	}
	after, runs := f.states(t)
	if len(before) != len(after) || runs[0].Status != executionlog.Interrupted || f.workerCalls.Load() != 0 {
		t.Fatal("unknown call dispatched before verification")
	}
	source = f.withRecordedReconciliation(t, source, executionlog.ConfirmedCompleted, "updated")
	if _, err := f.agent.Resume(t.Context(), source); err != nil {
		t.Fatal(err)
	}
	_, runs = f.states(t)
	if f.writes.Load() != 0 || f.reads.Load() != 1 || runs[1].Tasks[0].Attempt != 2 || runs[1].Status != executionlog.Completed {
		t.Fatalf("repeated write or lost progress: writes=%d reads=%d runs=%+v", f.writes.Load(), f.reads.Load(), runs)
	}
	found := false
	for _, call := range runs[1].Calls {
		if call.OriginRunID == runs[1].RunID && call.Task.Phase == "worker" {
			found = call.ReusedFrom != ""
		}
	}
	if !found {
		t.Fatal("reused tool result has no provenance")
	}
}

type failedPlanStore struct{ executionlog.EventStore }

func (s failedPlanStore) Append(ctx context.Context, event executionlog.Event) (executionlog.Event, error) {
	if event.Type == executionlog.PlanSaved {
		return executionlog.Event{}, errors.New("disk failure")
	}
	return s.EventStore.Append(ctx, event)
}

func TestPlanSaveFailureStopsWorkerDispatch(t *testing.T) {
	f := newExecutionFixture(t)
	f.agent.recorder = executionlog.NewRecorder(failedPlanStore{f.store}, f.recorder.ConversationID())
	if _, err := f.agent.Run(t.Context(), "update code"); err == nil {
		t.Fatal("ignored failed confirmed plan save")
	}
	if f.workerCalls.Load() != 0 || f.reviewCalls.Load() != 0 || f.writes.Load() != 0 {
		t.Fatal("dispatched work after persistence failure")
	}
	_, runs := f.states(t)
	if runs[0].Status != executionlog.Interrupted {
		t.Fatal("reported terminal success after failed write")
	}
}

type reviseOnceDecision struct{ calls int }

func (d *reviseOnceDecision) Decide(*plan.Plan) (PlanAction, string, error) {
	d.calls++
	if d.calls == 1 {
		return PlanRevise, "clarify the task", nil
	}
	return PlanExecute, "", nil
}

func TestOnlyConfirmedDraftIsSaved(t *testing.T) {
	f := newExecutionFixture(t)
	f.agent.decisionProvider = &reviseOnceDecision{}
	if _, err := f.agent.Run(t.Context(), "update code"); err != nil {
		t.Fatal(err)
	}
	_, runs := f.states(t)
	if f.plannerCalls.Load() != 2 || runs[0].Plan == nil || runs[0].Plan.Revision != 1 {
		t.Fatalf("draft revisions were saved as confirmed plans: %+v", runs)
	}
}

func TestResumePlanningDraftUsesPlanEntryAndConfirmationGate(t *testing.T) {
	f := newExecutionFixture(t)
	if _, err := f.recorder.StartRun(t.Context(), "update code", "", executionlog.PlanAndExecuteRun); err != nil {
		t.Fatal(err)
	}
	_, runs := f.states(t)
	if _, err := f.agent.Resume(t.Context(), runs[0]); err != nil {
		t.Fatal(err)
	}
	_, runs = f.states(t)
	if len(runs) != 2 || runs[0].Status != executionlog.Resumed || runs[1].Kind != executionlog.PlanAndExecuteRun || runs[1].Status != executionlog.Completed || f.plannerCalls.Load() != 1 {
		t.Fatalf("wrong draft recovery: %+v", runs)
	}
}

func TestConfirmedNotExecutedAllowsFreshWrite(t *testing.T) {
	f := newExecutionFixture(t)
	source := f.interruptedPlan(t, "unknown")
	source = f.withRecordedReconciliation(t, source, executionlog.ConfirmedNotExecuted, "")
	if _, err := f.agent.Resume(t.Context(), source); err != nil {
		t.Fatal(err)
	}
	_, runs := f.states(t)
	if f.writes.Load() != 1 || runs[1].Calls[0].Status != executionlog.NotExecuted {
		t.Fatalf("not-executed call was incorrectly reused: %+v", runs)
	}
}

func TestRecoveredPlanReplansUsingRecordedRevision(t *testing.T) {
	f := newExecutionFixture(t)
	source := f.interruptedPlan(t, "pending")
	f.rejectReviews.Store(1)
	if _, err := f.agent.Resume(t.Context(), source); err != nil {
		t.Fatal(err)
	}
	events, runs := f.states(t)
	if len(runs) != 2 || runs[1].Status != executionlog.Completed || runs[1].Plan == nil {
		t.Fatalf("incomplete recovery: %+v", runs)
	}
	recovered := runs[1]
	if recovered.Plan.PlanID != source.Plan.PlanID || recovered.Plan.Revision != 2 || f.plannerCalls.Load() != 1 {
		t.Fatalf("wrong recovered plan revision: %+v, planner=%d", recovered.Plan, f.plannerCalls.Load())
	}
	saves := 0
	for _, e := range events {
		if e.RunID == recovered.RunID && e.Type == executionlog.PlanSaved {
			saves++
			if e.Plan.Revision != 2 {
				t.Fatalf("resaved inherited plan: %+v", e.Plan)
			}
		}
	}
	if saves != 1 {
		t.Fatalf("recovery saved %d plans, want only the replanned revision", saves)
	}
}

type failedToolResultStore struct {
	executionlog.EventStore
	phase string
}

func (s failedToolResultStore) Append(ctx context.Context, event executionlog.Event) (executionlog.Event, error) {
	if event.Type == executionlog.ToolCallCompleted && event.ToolCall.Task.Phase == s.phase {
		return executionlog.Event{}, errors.New("tool result disk failure")
	}
	return s.EventStore.Append(ctx, event)
}

func TestWorkerAndReviewerPersistenceFailureStopsReplanning(t *testing.T) {
	for _, phase := range []string{"worker", "reviewer"} {
		t.Run(phase, func(t *testing.T) {
			f := newExecutionFixture(t)
			f.agent.recorder = executionlog.NewRecorder(failedToolResultStore{f.store, phase}, f.recorder.ConversationID())
			if _, err := f.agent.Run(t.Context(), "update code"); err == nil {
				t.Fatal("ignored lost tool result")
			}
			if f.plannerCalls.Load() != 1 || f.writes.Load() != 1 {
				t.Fatal("dispatched replanning or repeated external operation after recording failure")
			}
			_, runs := f.states(t)
			last := runs[0].Calls[len(runs[0].Calls)-1]
			if runs[0].Status != executionlog.Interrupted || last.Status != executionlog.Unknown || last.Task.Phase != phase {
				t.Fatalf("lost uncertain execution facts: %+v", runs)
			}
			if phase == "reviewer" && f.reviewCalls.Load() != 1 {
				t.Fatal("requested reviewer verdict after losing tool result")
			}
		})
	}
}

func TestParallelWorkersKeepTaskAndReviewerRecordingIsolated(t *testing.T) {
	f := newExecutionFixture(t)
	f.planJSON = `{"goal":"parallel work","tasks":[{"id":"task_1","name":"one","description":"first task","type":"ANALYSIS","dependencies":[]},{"id":"task_2","name":"two","description":"second task","type":"ANALYSIS","dependencies":[]}],"summary":"parallel"}`
	f.writesStarted = make(chan struct{}, 2)
	f.releaseWrites = make(chan struct{})
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := f.agent.Run(ctx, "parallel work"); done <- err }()
	for range 2 {
		select {
		case <-f.writesStarted:
		case <-ctx.Done():
			t.Fatal("workers did not execute concurrently")
		}
	}
	close(f.releaseWrites)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	_, runs := f.states(t)
	state := runs[0]
	if state.Status != executionlog.Completed || len(state.Calls) != 4 || f.writes.Load() != 2 || f.reads.Load() != 2 {
		t.Fatalf("missing parallel execution facts: %+v", state)
	}
	counts := map[string]int{}
	for _, call := range state.Calls {
		counts[call.Task.TaskID+"/"+call.Task.Phase]++
	}
	for _, id := range []string{"task_1", "task_2"} {
		if counts[id+"/worker"] != 1 || counts[id+"/reviewer"] != 1 {
			t.Fatalf("mixed task identities: %+v", counts)
		}
	}
}

type countedRecoveryLoads struct {
	executionlog.EventStore
	loads atomic.Int32
}

func (s *countedRecoveryLoads) Load(ctx context.Context, id string, after uint64) ([]executionlog.Event, error) {
	s.loads.Add(1)
	return nil, errors.New("Resume must use the supplied state without loading history")
}

func TestPlanResumeUsesSuppliedStateWithoutLoadingHistory(t *testing.T) {
	for _, stage := range []string{"mixed", "unknown", "planning"} {
		t.Run(stage, func(t *testing.T) {
			f := newExecutionFixture(t)
			var source executionlog.RunState
			if stage == "planning" {
				if _, err := f.recorder.StartRun(t.Context(), "update code", "", executionlog.PlanAndExecuteRun); err != nil {
					t.Fatal(err)
				}
				_, runs := f.states(t)
				source = runs[len(runs)-1]
			} else {
				source = f.interruptedPlan(t, stage)
			}
			counted := &countedRecoveryLoads{EventStore: f.store}
			f.agent.recorder = executionlog.NewRecorder(counted, f.recorder.ConversationID())
			if stage == "unknown" {
				if _, err := f.agent.Resume(t.Context(), source); !errors.Is(err, ErrRecoveryNeedsVerification) {
					t.Fatalf("expected unknown-call gate, got %v", err)
				}
				if counted.loads.Load() != 0 || f.workerCalls.Load() != 0 || f.reviewCalls.Load() != 0 {
					t.Fatal("unknown recovery loaded history or dispatched work")
				}
				return
			}
			if _, err := f.agent.Resume(t.Context(), source); err != nil {
				t.Fatal(err)
			}
			if count := counted.loads.Load(); count != 0 {
				t.Fatalf("Resume loaded execution history %d times, want 0", count)
			}
			plannerCalls, workerCalls, reviewCalls := f.plannerCalls.Load(), f.workerCalls.Load(), f.reviewCalls.Load()
			if _, err := f.agent.Resume(t.Context(), source); err == nil {
				t.Fatal("accepted duplicate recovery of supplied state")
			}
			if counted.loads.Load() != 0 || f.plannerCalls.Load() != plannerCalls || f.workerCalls.Load() != workerCalls || f.reviewCalls.Load() != reviewCalls {
				t.Fatal("duplicate recovery loaded history or dispatched a model")
			}
		})
	}
}

func TestPlanResumeRejectsInvalidSuppliedState(t *testing.T) {
	for _, mode := range []string{"completed", "wrong conversation", "wrong kind"} {
		t.Run(mode, func(t *testing.T) {
			f := newExecutionFixture(t)
			source := f.interruptedPlan(t, "pending")
			switch mode {
			case "completed":
				source.Status = executionlog.Completed
			case "wrong conversation":
				source.ConversationID = "other"
			case "wrong kind":
				source.Kind = executionlog.ReActRun
			}
			counted := &countedRecoveryLoads{EventStore: f.store}
			f.agent.recorder = executionlog.NewRecorder(counted, f.recorder.ConversationID())
			if _, err := f.agent.Resume(t.Context(), source); err == nil {
				t.Fatal("accepted invalid recovery state")
			}
			if counted.loads.Load() != 0 || f.plannerCalls.Load() != 0 || f.workerCalls.Load() != 0 || f.reviewCalls.Load() != 0 {
				t.Fatal("invalid recovery loaded history or dispatched a model")
			}
		})
	}
}
