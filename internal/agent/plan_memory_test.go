package agent

import (
	"AgentCLI/internal/llm"
	"AgentCLI/internal/memory"
	"AgentCLI/internal/plan"
	"context"
	"strings"
	"testing"
)

type capturingPlanGenerator struct {
	messages []llm.Message
	plan     *plan.Plan
}

func (g *capturingPlanGenerator) Generate(_ context.Context, messages []llm.Message) (*plan.Plan, error) {
	g.messages = append([]llm.Message(nil), messages...)
	return g.plan, nil
}

func (g *capturingPlanGenerator) Revise(context.Context, *plan.Plan, string, []llm.Message) (*plan.Plan, error) {
	return g.plan, nil
}

func (g *capturingPlanGenerator) Replan(context.Context, *plan.Plan, string, []llm.Message) (*plan.Plan, error) {
	return g.plan, nil
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
		"This project uses Go.",
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

	generator := &capturingPlanGenerator{plan: plan.NewPlan("plan_1", "Test plan", "Test summary")}
	reviewer := &workflowReviewer{actions: []PlanAction{PlanCancel}}
	scheduler := &recordingContextCompactionScheduler{}
	estimator := &recordingRequestTokenEstimator{next: 321}
	transcript := &recordingAgentTranscript{}
	a := NewPlanAndExecuteAgent(generator, func() Agent { return &workflowExecutor{} }, reviewer, 0, 1, 0, 0)
	if err := a.ConfigureMemory(manager, scheduler, estimator, transcript, "conversation_1"); err != nil {
		t.Fatalf("ConfigureMemory() error = %v", err)
	}

	result, err := a.Run(context.Background(), "Current plan request")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result != "计划已取消，已返回 ReAct 模式" {
		t.Fatalf("Run() = %q", result)
	}
	if len(generator.messages) != 4 || generator.messages[0].Role != "system" || generator.messages[3].Role != "user" {
		t.Fatalf("planner message roles = %#v", generator.messages)
	}
	for _, want := range []string{
		"This project uses Go.",
		"Earlier request",
		"Earlier answer",
		"Current plan request",
	} {
		found := false
		for _, message := range generator.messages {
			found = found || strings.Contains(message.Content, want)
		}
		if !found {
			t.Fatalf("planner messages do not contain %q: %#v", want, generator.messages)
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
