package multiagent

import (
	"AgentCLI/internal/llm"
	"AgentCLI/internal/memory"
	"AgentCLI/internal/plan"
	"AgentCLI/internal/tool"
	"context"
	"strings"
	"testing"
)

type recordingContextCompactionScheduler struct {
	calls       int
	managerLens []int
	measured    []int
	decision    memory.CompactionDecision
	err         error
}

func (scheduler *recordingContextCompactionScheduler) CompactToFit(
	_ context.Context,
	manager *memory.Manager,
	measure memory.ContextTokenMeasurer,
) ([]memory.CompactionDecision, error) {
	scheduler.calls++
	scheduler.managerLens = append(scheduler.managerLens, manager.Len())
	if measure != nil {
		tokens, err := measure()
		if err != nil {
			return nil, err
		}
		scheduler.measured = append(scheduler.measured, tokens)
	}
	if scheduler.decision.Action == memory.CompactionActionNone {
		return nil, scheduler.err
	}
	return []memory.CompactionDecision{scheduler.decision}, scheduler.err
}

type recordingRequestTokenEstimator struct {
	estimates []int
	next      int
	observed  [][2]int
}

func (estimator *recordingRequestTokenEstimator) Estimate(
	[]llm.Message,
	[]tool.Definition,
) (int, error) {
	estimator.estimates = append(estimator.estimates, estimator.next)
	return estimator.next, nil
}

func (estimator *recordingRequestTokenEstimator) Observe(estimated, actual int) {
	estimator.observed = append(estimator.observed, [2]int{estimated, actual})
}

type workflowDecisionProvider struct {
	actions []PlanAction
	calls   int
}

func (provider *workflowDecisionProvider) Decide(*plan.Plan) (PlanAction, string, error) {
	action := provider.actions[provider.calls]
	provider.calls++
	return action, "", nil
}

type recordingAgentTranscript struct {
	conversationIDs []string
	entries         []memory.Entry
}

func (store *recordingAgentTranscript) Append(conversationID string, entries ...memory.Entry) error {
	store.conversationIDs = append(store.conversationIDs, conversationID)
	store.entries = append(store.entries, entries...)
	return nil
}

func (store *recordingAgentTranscript) Load(string) ([]memory.Entry, error) {
	return append([]memory.Entry(nil), store.entries...), nil
}

func TestPlanAndExecuteAgentUsesRootContextAndWritesBackResult(t *testing.T) {
	manager := memory.NewManager(nil)
	if _, err := manager.UpsertFact(
		memory.FactScopeProject,
		"language",
		"Current plan request must use Go.",
		memory.Metadata{},
	); err != nil {
		t.Fatalf("UpsertFact() error = %v", err)
	}
	if _, err := manager.AddMessage(llm.UserMessage("Earlier request"), ""); err != nil {
		t.Fatalf("AddMessage(user) error = %v", err)
	}
	if _, err := manager.AddMessage(llm.AssistantMessage("Earlier answer"), ""); err != nil {
		t.Fatalf("AddMessage(assistant) error = %v", err)
	}

	plannerClient, plannerRecording := newRecordedChatClient(t, `{
			"goal":"Test plan",
			"summary":"Test summary",
			"tasks":[{
				"id":"task_1",
				"name":"Test task",
				"description":"Test task description",
				"type":"ANALYSIS",
				"dependencies":[],
				"read_resources":[],
				"write_resources":[]
			}]
		}`)
	planner, err := NewPlannerSubAgent(plannerClient)
	if err != nil {
		t.Fatalf("NewPlannerSubAgent() error = %v", err)
	}
	decisionProvider := &workflowDecisionProvider{actions: []PlanAction{PlanCancel}}
	scheduler := &recordingContextCompactionScheduler{}
	estimator := &recordingRequestTokenEstimator{next: 321}
	transcript := &recordingAgentTranscript{}
	a := &PlanAndExecuteAgent{
		planner:           planner,
		scheduler:         newTaskScheduler(1, 0),
		decisionProvider:  decisionProvider,
		maxReplanAttempts: 0,
	}
	builder := memory.NewContextBuilder(manager, memory.NewMemoryRetriever(nil), 1000)
	a.rootMemory = memory.AgentMemoryContext{
		ConversationID: "conversation_1",
		Manager:        manager,
		ContextBuilder: builder,
		Scheduler:      scheduler,
		Estimator:      estimator,
		Transcript:     transcript,
	}

	result, err := a.Run(context.Background(), "Current plan request")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result != "计划已取消，已返回 ReAct 模式" {
		t.Fatalf("Run() = %q", result)
	}
	plannerMessages := plannerRecording.lastMessages()
	if len(plannerMessages) != 5 || plannerMessages[0].Role != "system" || plannerMessages[4].Role != "user" {
		t.Fatalf("planner message roles = %#v", plannerMessages)
	}
	for _, want := range []string{
		"Current plan request must use Go.",
		"Earlier request",
		"Earlier answer",
		"Current plan request",
	} {
		found := false
		for _, message := range plannerMessages {
			found = found || strings.Contains(message.Content, want)
		}
		if !found {
			t.Fatalf("planner messages do not contain %q: %#v", want, plannerMessages)
		}
	}
	if scheduler.calls != 1 || len(estimator.estimates) == 0 {
		t.Fatalf("scheduler calls=%d estimates=%v", scheduler.calls, estimator.estimates)
	}
	entries := manager.Entries()
	if len(entries) != 4 || entries[2].Content() != "Current plan request" || entries[3].Content() != result {
		t.Fatalf("root entries after plan = %#v", entries)
	}
	if len(transcript.entries) != 2 || transcript.entries[0].Content() != "Current plan request" || transcript.entries[1].Content() != result {
		t.Fatalf("transcript entries = %#v", transcript.entries)
	}
}
