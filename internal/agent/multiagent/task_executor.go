package multiagent

import (
	baseagent "AgentCLI/internal/agent"
	executionlog "AgentCLI/internal/execution"
	"AgentCLI/internal/llm"
	"AgentCLI/internal/memory"
	"AgentCLI/internal/plan"
	"AgentCLI/internal/rag"
	"AgentCLI/internal/tool"
	"context"
	"errors"
	"fmt"
)

type TaskExecution struct {
	Recorder        *executionlog.RunRecorder
	Reference       executionlog.TaskReference
	ReviewOnly      bool
	SavedResult     string
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
	ragContext    *rag.ContextProvider
}

func newTaskExecutor(
	client *llm.OpenAICompatibleClient,
	tools *tool.ToolRegistry,
	memoryRuntime *memory.ConversationRuntime,
	reviewer *ReviewerSubAgent,
	ragContext *rag.ContextProvider,
) *TaskExecutor {
	if reviewer != nil {
		reviewer.ConfigureRAGContext(ragContext)
	}
	return &TaskExecutor{
		client:        client,
		tools:         tools,
		memoryRuntime: memoryRuntime,
		reviewer:      reviewer,
		ragContext:    ragContext,
	}
}

func (executor *TaskExecutor) Execute(
	ctx context.Context,
	execution TaskExecution,
) (result string, runErr error) {
	taskCtx := baseagent.WithEventTaskID(ctx, execution.TaskID)
	if !execution.ReviewOnly {
		if err := recordTask(taskCtx, execution.Recorder, executionlog.TaskStarted, execution.Reference, "", nil); err != nil {
			return "", err
		}
	}
	defer func() {
		if runErr == nil {
			return
		}
		typ := executionlog.TaskFailed
		if errors.Is(runErr, context.Canceled) {
			typ = executionlog.TaskCancelled
		}
		if err := recordTask(taskCtx, execution.Recorder, typ, execution.Reference, result, runErr); err != nil {
			runErr = errors.Join(runErr, err)
		}
	}()
	ref := execution.Reference
	ref.Phase = "worker"
	freshTools := map[string]bool{}
	if executor.reviewer != nil {
		for _, definition := range executor.reviewer.tools.ToolDefinitions() {
			freshTools[definition.FunctionDefinition.Name] = true
		}
	}
	taskCtx = context.WithValue(taskCtx, toolRecordingKey{}, taskToolRecording{run: execution.Recorder, ref: ref, freshTools: freshTools})
	result = execution.SavedResult
	if !execution.ReviewOnly {
		var err error
		result, err = executor.executeWorker(taskCtx, execution)
		if err != nil {
			return result, err
		}
		if err := recordTask(taskCtx, execution.Recorder, executionlog.TaskWorkCompleted, execution.Reference, result, nil); err != nil {
			return result, err
		}
	}
	if executor.reviewer == nil {
		return result, recordTask(taskCtx, execution.Recorder, executionlog.TaskCompleted, execution.Reference, result, nil)
	}
	ref.Phase = "reviewer"
	taskCtx = context.WithValue(taskCtx, toolRecordingKey{}, taskToolRecording{run: execution.Recorder, ref: ref})

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
	return result, recordTask(taskCtx, execution.Recorder, executionlog.TaskCompleted, execution.Reference, result, nil)
}

func (executor *TaskExecutor) executeWorker(
	ctx context.Context,
	execution TaskExecution,
) (string, error) {
	if executor == nil {
		return "", fmt.Errorf("task executor is nil")
	}

	memoryID := execution.TaskID
	if execution.Recorder != nil {
		memoryID = fmt.Sprintf("%s-%d-%s-%d", execution.Recorder.ID(), execution.Reference.Revision, execution.TaskID, execution.Reference.Attempt)
	}
	taskAgent, err := executor.newWorker(memoryID)
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
	worker.ConfigureRAGContext(executor.ragContext)
	return worker, nil
}
