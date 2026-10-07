package multiagent

import (
	baseagent "AgentCLI/internal/agent"
	"AgentCLI/internal/llm"
	"AgentCLI/internal/memory"
	"AgentCLI/internal/plan"
	"AgentCLI/internal/tool"
	"context"
	"fmt"
)

type TaskExecution struct {
	TaskID          string
	TaskName        string
	TaskDescription string
	TaskType        plan.TaskType
	PlanGoal        string
	Prompt          string
}

// TaskExecutor creates a Worker, executes one task, and asks the Reviewer to
// validate the result. Scheduling and timeouts remain TaskScheduler concerns.
type TaskExecutor struct {
	client        *llm.OpenAICompatibleClient
	tools         *tool.ToolRegistry
	memoryRuntime *memory.ConversationRuntime
	reviewer      *ReviewerSubAgent
}

func newTaskExecutor(
	client *llm.OpenAICompatibleClient,
	tools *tool.ToolRegistry,
	memoryRuntime *memory.ConversationRuntime,
	reviewer *ReviewerSubAgent,
) *TaskExecutor {
	return &TaskExecutor{
		client:        client,
		tools:         tools,
		memoryRuntime: memoryRuntime,
		reviewer:      reviewer,
	}
}

func (executor *TaskExecutor) Execute(
	ctx context.Context,
	execution TaskExecution,
) (string, error) {
	taskCtx := baseagent.WithEventTaskID(ctx, execution.TaskID)
	result, err := executor.executeWorker(taskCtx, execution)
	if err != nil || executor.reviewer == nil {
		return result, err
	}

	review, err := executor.reviewer.ReviewResult(taskCtx, execution, result)
	if err != nil {
		return result, fmt.Errorf("review task result: %w", err)
	}
	if !review.Approved {
		baseagent.Emit(taskCtx, baseagent.Event{
			Type:    baseagent.EventResultReview,
			TaskID:  execution.TaskID,
			Title:   "[REVIEWER] 驳回",
			Content: review.Feedback,
		})
		return result, &ResultRejectedError{
			TaskID:   execution.TaskID,
			Feedback: review.Feedback,
		}
	}
	baseagent.Emit(taskCtx, baseagent.Event{
		Type:    baseagent.EventResultReview,
		TaskID:  execution.TaskID,
		Title:   "[REVIEWER] 通过",
		Content: review.Feedback,
	})
	return result, nil
}

func (executor *TaskExecutor) executeWorker(
	ctx context.Context,
	execution TaskExecution,
) (string, error) {
	if executor == nil {
		return "", fmt.Errorf("task executor is nil")
	}

	taskAgent, err := executor.newWorker(execution.TaskID)
	if err != nil {
		return "", fmt.Errorf("create task agent: %w", err)
	}
	if taskAgent == nil {
		return "", fmt.Errorf("create task agent: factory returned nil")
	}

	return taskAgent.Run(ctx, execution.Prompt)
}

func (executor *TaskExecutor) newWorker(taskID string) (baseagent.Agent, error) {
	if executor.client == nil || executor.tools == nil || executor.memoryRuntime == nil {
		return nil, fmt.Errorf("task executor dependency is nil")
	}
	taskRuntime, err := executor.memoryRuntime.NewTaskRuntime(taskID)
	if err != nil {
		return nil, fmt.Errorf("create task memory runtime: %w", err)
	}
	worker, err := NewWorkerSubAgent(executor.client, executor.tools)
	if err != nil {
		return nil, err
	}
	if err := worker.ConfigureMemory(taskRuntime.AgentMemoryContext); err != nil {
		return nil, err
	}
	return worker, nil
}
