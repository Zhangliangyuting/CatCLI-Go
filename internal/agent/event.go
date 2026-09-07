package agent

import (
	"context"
	"sync"
)

type EventType string

const (
	EventTokenUsage       EventType = "token_usage"
	EventMemoryCompaction EventType = "memory_compaction"
	EventMemoryFact       EventType = "memory_fact"
	EventToolCall         EventType = "tool_call"
	EventToolResult       EventType = "tool_result"
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
	Type    EventType
	TaskID  string
	Title   string
	Content string
}

type Observer func(Event)

// ObservableAgent 是支持结构化执行事件的 Agent。
// Agent 接口保持精简，未实现事件能力的 Agent 仍可被调度器使用。
type ObservableAgent interface {
	Agent
	RunWithObserver(
		ctx context.Context,
		userInput string,
		observer Observer,
	) (string, error)
}

func emit(observer Observer, event Event) {
	if observer != nil {
		observer(event)
	}
}

// SynchronizedObserver 保证并发 Worker 不会同时调用底层 Observer。
func SynchronizedObserver(observer Observer) Observer {
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
