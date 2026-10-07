package agent

import (
	"AgentCLI/internal/memory"
	"context"
	"errors"
	"testing"
)

type stubAgent struct {
	calls int
}

func (agent *stubAgent) Run(context.Context, string) (string, error) {
	agent.calls++
	return "done", nil
}

type stubFactExtractor struct {
	operations []memory.FactOperation
	err        error
}

func (extractor stubFactExtractor) Extract(
	context.Context,
	string,
	[]memory.Entry,
) ([]memory.FactOperation, error) {
	return extractor.operations, extractor.err
}

func TestFactAwareAgentAppliesFactsBeforeDelegating(t *testing.T) {
	manager := memory.NewManager(nil)
	delegate := &stubAgent{}
	agent, err := NewFactAwareAgent(delegate, manager, stubFactExtractor{operations: []memory.FactOperation{{
		Action: memory.FactActionUpsert, Scope: memory.FactScopeUser,
		Key: "programming_language", Content: "Prefer Go.",
	}}})
	if err != nil {
		t.Fatalf("NewFactAwareAgent() error = %v", err)
	}
	var events []Event
	ctx := WithObserver(context.Background(), func(event Event) {
		events = append(events, event)
	})
	answer, err := agent.Run(ctx, "以后用 Go")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if answer != "done" || delegate.calls != 1 {
		t.Fatalf("result = (%q, calls=%d), want (done, 1)", answer, delegate.calls)
	}
	fact, exists := manager.GetFact(memory.FactScopeUser, "programming_language")
	if !exists || fact.Content() != "Prefer Go." {
		t.Fatalf("stored fact = (%#v, %t)", fact, exists)
	}
	if len(events) != 1 || events[0].Type != EventMemoryFact {
		t.Fatalf("events = %#v, want one memory fact event", events)
	}
}

func TestFactAwareAgentDoesNotDelegateWhenExtractionFails(t *testing.T) {
	manager := memory.NewManager(nil)
	delegate := &stubAgent{}
	extractionError := errors.New("unavailable")
	agent, err := NewFactAwareAgent(delegate, manager, stubFactExtractor{err: extractionError})
	if err != nil {
		t.Fatalf("NewFactAwareAgent() error = %v", err)
	}
	_, err = agent.Run(context.Background(), "记住这个")
	if !errors.Is(err, extractionError) {
		t.Fatalf("Run() error = %v, want extraction error", err)
	}
	if delegate.calls != 0 {
		t.Fatalf("delegate calls = %d, want 0", delegate.calls)
	}
}
