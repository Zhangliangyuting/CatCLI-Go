package agent

import (
	"AgentCLI/internal/llm"
	"AgentCLI/internal/memory"
	"AgentCLI/internal/plan"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// MemoryAwarePlanAgent connects a Plan agent to the root conversation. It
// gives the planner the same facts, summaries, and dialogue that ReAct sees,
// and records the plan result back into that conversation.
type MemoryAwarePlanAgent struct {
	delegate            ObservableAgent
	manager             *memory.Manager
	compactionScheduler ContextCompactionScheduler
	requestEstimator    memory.RequestTokenEstimator
	transcript          memory.TranscriptStore
	conversationID      string
}

var _ ObservableAgent = (*MemoryAwarePlanAgent)(nil)

func NewMemoryAwarePlanAgent(
	delegate ObservableAgent,
	manager *memory.Manager,
	scheduler ContextCompactionScheduler,
	estimator memory.RequestTokenEstimator,
	transcript memory.TranscriptStore,
	conversationID string,
) (*MemoryAwarePlanAgent, error) {
	if delegate == nil {
		return nil, fmt.Errorf("delegate agent is nil")
	}
	if manager == nil {
		return nil, fmt.Errorf("memory manager is nil")
	}
	if scheduler == nil {
		return nil, fmt.Errorf("compaction scheduler is nil")
	}
	if estimator == nil {
		return nil, fmt.Errorf("request token estimator is nil")
	}
	if transcript == nil {
		return nil, fmt.Errorf("transcript store is nil")
	}
	if strings.TrimSpace(conversationID) == "" {
		return nil, fmt.Errorf("conversation ID is empty")
	}
	return &MemoryAwarePlanAgent{
		delegate:            delegate,
		manager:             manager,
		compactionScheduler: scheduler,
		requestEstimator:    estimator,
		transcript:          transcript,
		conversationID:      conversationID,
	}, nil
}

func (agent *MemoryAwarePlanAgent) Run(ctx context.Context, userInput string) (string, error) {
	return agent.RunWithObserver(ctx, userInput, nil)
}

func (agent *MemoryAwarePlanAgent) RunWithObserver(
	ctx context.Context,
	userInput string,
	observer Observer,
) (string, error) {
	if _, err := agent.storeMessage(llm.UserMessage(userInput)); err != nil {
		return "", fmt.Errorf("store plan user message: %w", err)
	}

	measure := func() (int, error) {
		input, err := agent.plannerInput()
		if err != nil {
			return 0, err
		}
		return agent.requestEstimator.Estimate([]llm.Message{
			llm.SystemMessage(plan.PLANNING_PROMPT),
			llm.UserMessage(input),
		}, nil)
	}
	decisions, err := agent.compactionScheduler.CompactToFitMeasured(ctx, agent.manager, measure)
	emitCompactionDecisions(observer, decisions)
	if err != nil {
		return "", fmt.Errorf("compact plan context: %w", err)
	}

	input, err := agent.plannerInput()
	if err != nil {
		return "", err
	}
	result, runErr := agent.delegate.RunWithObserver(ctx, input, observer)
	if strings.TrimSpace(result) != "" {
		if _, err := agent.storeMessage(llm.AssistantMessage(result)); err != nil {
			if runErr != nil {
				return result, errors.Join(runErr, fmt.Errorf("store plan result: %w", err))
			}
			return result, fmt.Errorf("store plan result: %w", err)
		}
	}
	return result, runErr
}

func (agent *MemoryAwarePlanAgent) plannerInput() (string, error) {
	messages, err := agent.manager.ContextMessages()
	if err != nil {
		return "", fmt.Errorf("build plan conversation context: %w", err)
	}
	encoded, err := json.Marshal(messages)
	if err != nil {
		return "", fmt.Errorf("encode plan conversation context: %w", err)
	}
	return "Use the following conversation context when creating the plan. " +
		"Facts and previous summaries are context, and the final user message is the current request.\n\n" +
		"CONVERSATION_CONTEXT_JSON:\n" + string(encoded), nil
}

func (agent *MemoryAwarePlanAgent) storeMessage(message llm.Message) (memory.Entry, error) {
	entry, err := agent.manager.AddMessage(message, "")
	if err != nil {
		return memory.Entry{}, err
	}
	if err := agent.transcript.Append(agent.conversationID, entry); err != nil {
		return entry, fmt.Errorf("append message to transcript: %w", err)
	}
	return entry, nil
}

func emitCompactionDecisions(observer Observer, decisions []memory.CompactionDecision) {
	for _, decision := range decisions {
		emit(observer, Event{
			Type:  EventMemoryCompaction,
			Title: string(decision.Action),
			Content: fmt.Sprintf(
				"tokens=%d->%d usage=%.1f%%->%.1f%% emergency=%t reason=%s",
				decision.BeforeTokens,
				decision.AfterTokens,
				decision.UsageBefore*100,
				decision.UsageAfter*100,
				decision.Emergency,
				decision.Reason,
			),
		})
	}
}
