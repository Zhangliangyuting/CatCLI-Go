package agent

import (
	"AgentCLI/internal/memory"
	"context"
	"fmt"
)

// FactAwareAgent extracts explicit memory mutations before delegating the user
// request. It can wrap both ReAct and Plan agents without teaching either one
// about persistence or fact extraction prompts.
type FactAwareAgent struct {
	delegate  Agent
	manager   *memory.Manager
	extractor memory.FactExtractor
}

var _ Agent = (*FactAwareAgent)(nil)

func NewFactAwareAgent(
	delegate Agent,
	manager *memory.Manager,
	extractor memory.FactExtractor,
) (*FactAwareAgent, error) {
	if delegate == nil {
		return nil, fmt.Errorf("delegate agent is nil")
	}
	if manager == nil {
		return nil, fmt.Errorf("memory manager is nil")
	}
	if extractor == nil {
		return nil, fmt.Errorf("fact extractor is nil")
	}
	return &FactAwareAgent{delegate: delegate, manager: manager, extractor: extractor}, nil
}

func (agent *FactAwareAgent) Run(ctx context.Context, userInput string) (string, error) {
	operations, err := agent.extractor.Extract(ctx, userInput, agent.manager.Facts())
	if err != nil {
		return "", fmt.Errorf("extract facts: %w", err)
	}
	if err := memory.ApplyFactOperations(agent.manager, operations); err != nil {
		return "", fmt.Errorf("apply facts: %w", err)
	}
	for _, operation := range operations {
		Emit(ctx, Event{
			Type:    EventMemoryFact,
			Title:   string(operation.Action),
			Content: fmt.Sprintf("scope=%s key=%s", operation.Scope, operation.Key),
		})
	}
	return agent.delegate.Run(ctx, userInput)
}
