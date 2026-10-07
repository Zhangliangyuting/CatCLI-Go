package agent

import (
	"AgentCLI/internal/memory"
	"context"
	"fmt"
	"sync"
)

type EventType string

const (
	EventTokenUsage       EventType = "token_usage"
	EventMemoryCompaction EventType = "memory_compaction"
	EventMemoryFact       EventType = "memory_fact"
	EventMemoryRetrieval  EventType = "memory_retrieval"
	EventToolCall         EventType = "tool_call"
	EventToolResult       EventType = "tool_result"
	EventResultReview     EventType = "result_review"
	EventTaskStarted      EventType = "task_started"
	EventTaskCompleted    EventType = "task_completed"
	EventTaskFailed       EventType = "task_failed"
	EventTaskCancelled    EventType = "task_cancelled"
	EventTaskTimeout      EventType = "task_timeout"
	EventPlanGenerated    EventType = "plan_generated"
	EventPlanRevised      EventType = "plan_revised"
	EventPlanCancelled    EventType = "plan_cancelled"
	EventPlanReplanning   EventType = "plan_replanning"
	EventPlanCompleted    EventType = "plan_completed"
	EventPlanFailed       EventType = "plan_failed"
	EventPlanTimeout      EventType = "plan_timeout"
)

type Event struct {
	Type      EventType
	TaskID    string
	Title     string
	Content   string
	Retrieval *memory.RetrievalReport
}

type Observer func(Event)

type observerContextKey struct{}

// WithObserver binds a request-scoped event observer to ctx.
func WithObserver(ctx context.Context, observer Observer) context.Context {
	if observer == nil {
		return ctx
	}
	return context.WithValue(ctx, observerContextKey{}, synchronizedObserver(observer))
}

func observerFromContext(ctx context.Context) Observer {
	if ctx == nil {
		return nil
	}
	observer, _ := ctx.Value(observerContextKey{}).(Observer)
	return observer
}

// WithEventTaskID fills an empty TaskID on events emitted by work belonging to
// one scheduled task.
func WithEventTaskID(ctx context.Context, taskID string) context.Context {
	observer := observerFromContext(ctx)
	if observer == nil || taskID == "" {
		return ctx
	}
	return context.WithValue(ctx, observerContextKey{}, Observer(func(event Event) {
		if event.TaskID == "" {
			event.TaskID = taskID
		}
		observer(event)
	}))
}

// Emit sends an event to the observer bound to this request context.
func Emit(ctx context.Context, event Event) {
	observer := observerFromContext(ctx)
	if observer != nil {
		observer(event)
	}
}

// EmitCompactionDecisions adapts memory-layer compaction results to Agent
// events without making the memory package depend on Agent event types.
func EmitCompactionDecisions(
	ctx context.Context,
	decisions []memory.CompactionDecision,
) {
	for _, decision := range decisions {
		Emit(ctx, Event{
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

// synchronizedObserver prevents concurrent Workers from invoking the event
// receiver at the same time.
func synchronizedObserver(observer Observer) Observer {
	if observer == nil {
		return nil
	}

	var mu sync.Mutex
	return func(event Event) {
		mu.Lock()
		defer mu.Unlock()
		observer(event)
	}
}
