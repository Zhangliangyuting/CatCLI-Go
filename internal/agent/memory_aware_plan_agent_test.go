package agent

import (
	"AgentCLI/internal/llm"
	"AgentCLI/internal/memory"
	"context"
	"strings"
	"testing"
)

type capturingPlanAgent struct {
	input  string
	result string
	err    error
}

func (agent *capturingPlanAgent) Run(ctx context.Context, input string) (string, error) {
	return agent.RunWithObserver(ctx, input, nil)
}

func (agent *capturingPlanAgent) RunWithObserver(
	_ context.Context,
	input string,
	_ Observer,
) (string, error) {
	agent.input = input
	return agent.result, agent.err
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

func TestMemoryAwarePlanAgentUsesRootContextAndWritesBackResult(t *testing.T) {
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

	delegate := &capturingPlanAgent{result: "Plan completed"}
	scheduler := &recordingContextCompactionScheduler{}
	estimator := &recordingRequestTokenEstimator{next: 321}
	transcript := &recordingAgentTranscript{}
	agent, err := NewMemoryAwarePlanAgent(
		delegate,
		manager,
		scheduler,
		estimator,
		transcript,
		"conversation_1",
	)
	if err != nil {
		t.Fatalf("NewMemoryAwarePlanAgent() error = %v", err)
	}

	result, err := agent.Run(context.Background(), "Current plan request")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result != "Plan completed" {
		t.Fatalf("Run() = %q", result)
	}
	for _, want := range []string{
		"This project uses Go.",
		"Earlier request",
		"Earlier answer",
		"Current plan request",
	} {
		if !strings.Contains(delegate.input, want) {
			t.Fatalf("planner input does not contain %q:\n%s", want, delegate.input)
		}
	}
	if scheduler.calls != 1 || len(estimator.estimates) == 0 {
		t.Fatalf("scheduler calls=%d estimates=%v", scheduler.calls, estimator.estimates)
	}
	entries := manager.Entries()
	if len(entries) != 4 || entries[2].Content() != "Current plan request" || entries[3].Content() != "Plan completed" {
		t.Fatalf("root entries after plan = %#v", entries)
	}
	if len(transcript.entries) != 2 || transcript.entries[0].Content() != "Current plan request" || transcript.entries[1].Content() != "Plan completed" {
		t.Fatalf("transcript entries = %#v", transcript.entries)
	}
}
