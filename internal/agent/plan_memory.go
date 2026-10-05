package agent

import (
	"AgentCLI/internal/llm"
	"AgentCLI/internal/memory"
	"AgentCLI/internal/plan"
	"context"
	"fmt"
	"strings"
)

// ConfigureMemory connects planning to the root conversation. Each task still
// receives its own memory manager through the task factory.
func (a *PlanAndExecuteAgent) ConfigureMemory(
	manager *memory.Manager,
	builder *memory.ContextBuilder,
	scheduler ContextCompactionScheduler,
	estimator memory.RequestTokenEstimator,
	transcript memory.TranscriptStore,
	conversationID string,
) error {
	if a == nil {
		return fmt.Errorf("plan agent is nil")
	}
	if manager == nil {
		return fmt.Errorf("memory manager is nil")
	}
	if builder == nil {
		return fmt.Errorf("context builder is nil")
	}
	if scheduler == nil {
		return fmt.Errorf("compaction scheduler is nil")
	}
	if estimator == nil {
		return fmt.Errorf("request token estimator is nil")
	}
	if transcript == nil {
		return fmt.Errorf("transcript store is nil")
	}
	if strings.TrimSpace(conversationID) == "" {
		return fmt.Errorf("conversation ID is empty")
	}
	a.memoryManager = manager
	a.contextBuilder = builder
	a.compactionScheduler = scheduler
	a.requestEstimator = estimator
	a.transcript = transcript
	a.conversationID = conversationID
	return nil
}

func (a *PlanAndExecuteAgent) compactPlanContext(ctx context.Context, observer Observer) error {
	if a.compactionScheduler == nil {
		return nil
	}
	measure := func() (int, error) {
		contextMessages, err := a.planningMessagesFor(ctx)
		if err != nil {
			return 0, err
		}
		messages := make([]llm.Message, 0, len(contextMessages)+1)
		messages = append(messages, llm.SystemMessage(plan.PLANNING_PROMPT))
		messages = append(messages, contextMessages...)
		return a.requestEstimator.Estimate(messages, nil)
	}
	decisions, err := a.compactionScheduler.CompactToFit(ctx, a.memoryManager, measure)
	emitCompactionDecisions(observer, decisions)
	if err != nil {
		return fmt.Errorf("compact plan context: %w", err)
	}
	return nil
}

func (a *PlanAndExecuteAgent) planningMessages() ([]llm.Message, error) {
	return a.planningMessagesFor(context.Background())
}

func (a *PlanAndExecuteAgent) planningMessagesFor(ctx context.Context) ([]llm.Message, error) {
	return a.planningMessagesForObserved(ctx, nil)
}

func (a *PlanAndExecuteAgent) planningMessagesForObserved(ctx context.Context, observer Observer) ([]llm.Message, error) {
	if a.contextBuilder == nil {
		return nil, fmt.Errorf("context builder is nil")
	}
	messages, err := a.contextBuilder.BuildWithReport(ctx, a.currentQuery, func(report memory.RetrievalReport) {
		emit(observer, Event{Type: EventMemoryRetrieval, Retrieval: &report})
	})
	if err != nil {
		return nil, fmt.Errorf("build plan conversation context: %w", err)
	}
	return messages, nil
}

func (a *PlanAndExecuteAgent) storePlanMessage(message llm.Message) (memory.Entry, error) {
	entry, err := a.memoryManager.AddMessage(message, "")
	if err != nil {
		return memory.Entry{}, err
	}
	if a.transcript != nil {
		if err := a.transcript.Append(a.conversationID, entry); err != nil {
			return entry, fmt.Errorf("append message to transcript: %w", err)
		}
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
