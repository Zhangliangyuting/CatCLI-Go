package multiagent

import (
	baseagent "AgentCLI/internal/agent"
	"AgentCLI/internal/llm"
	"AgentCLI/internal/memory"
	"AgentCLI/internal/plan"
	"AgentCLI/internal/tool"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

type PlanAndExecuteAgent struct {
	planner           *PlannerSubAgent
	executor          *TaskExecutor
	scheduler         *TaskScheduler
	memoryRuntime     *memory.ConversationRuntime
	decisionProvider  PlanDecisionProvider
	currentPlan       *plan.Plan
	maxReplanAttempts int
	planTimeout       time.Duration
	rootMemory        memory.AgentMemoryContext
	currentQuery      string
}

var _ baseagent.Agent = (*PlanAndExecuteAgent)(nil)

// NewPlanAndExecuteAgent assembles the Plan-and-Execute workflow
// from three role-specialized SubAgents while preserving their typed contracts.
func NewPlanAndExecuteAgent(
	planner *PlannerSubAgent,
	client *llm.OpenAICompatibleClient,
	tools *tool.ToolRegistry,
	reviewer *ReviewerSubAgent,
	memoryRuntime *memory.ConversationRuntime,
	decisionProvider PlanDecisionProvider,
	maxReplanAttempts int,
	maxWorkers int,
	taskTimeout time.Duration,
	planTimeout time.Duration,
) (*PlanAndExecuteAgent, error) {
	if planner == nil {
		return nil, fmt.Errorf("configure planner: SubAgent is nil")
	}
	if client == nil || tools == nil {
		return nil, fmt.Errorf("configure worker: dependency is nil")
	}
	if reviewer == nil {
		return nil, fmt.Errorf("configure reviewer: SubAgent is nil")
	}
	if memoryRuntime == nil {
		return nil, fmt.Errorf("configure memory runtime: nil")
	}
	rootMemory := memoryRuntime.RootContext()
	return &PlanAndExecuteAgent{
		planner:           planner,
		executor:          newTaskExecutor(client, tools, memoryRuntime, reviewer),
		scheduler:         newTaskScheduler(maxWorkers, taskTimeout),
		memoryRuntime:     memoryRuntime,
		decisionProvider:  decisionProvider,
		maxReplanAttempts: maxReplanAttempts,
		planTimeout:       planTimeout,
		rootMemory:        rootMemory,
	}, nil
}

func (a *PlanAndExecuteAgent) Run(
	ctx context.Context,
	input string,
) (string, error) {
	a.currentQuery = input
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if _, err := a.storePlanMessage(llm.UserMessage(input)); err != nil {
		return "", fmt.Errorf("store plan user message: %w", err)
	}
	if err := a.compactPlanContext(ctx); err != nil {
		return "", err
	}
	messages, err := a.planningMessages(ctx)
	if err != nil {
		return "", err
	}

	result, runErr := a.runPlan(ctx, messages)
	if strings.TrimSpace(result) != "" {
		if _, err := a.storePlanMessage(llm.AssistantMessage(result)); err != nil {
			if runErr != nil {
				return result, errors.Join(runErr, fmt.Errorf("store plan result: %w", err))
			}
			return result, fmt.Errorf("store plan result: %w", err)
		}
	}
	return result, runErr
}

func (a *PlanAndExecuteAgent) runPlan(
	ctx context.Context,
	messages []llm.Message,
) (string, error) {
	p, err := a.planner.Generate(ctx, messages)
	if err != nil {
		return "", fmt.Errorf("generate plan: %w", err)
	}
	baseagent.Emit(ctx, baseagent.Event{
		Type:    baseagent.EventPlanGenerated,
		Title:   "[PLANNER] 计划已生成",
		Content: p.Visualize(),
	})

	if a.decisionProvider == nil {
		return "", fmt.Errorf("choose plan action: decision provider is nil")
	}

	for {
		if err := ctx.Err(); err != nil {
			p.MarkCancelled()
			return "", err
		}

		a.currentPlan = p

		action, feedback, err := a.decisionProvider.Decide(p)
		if err != nil {
			return "", fmt.Errorf("choose plan action: %w", err)
		}
		if err := ctx.Err(); err != nil {
			p.MarkCancelled()
			return "", err
		}

		switch action {
		case PlanExecute:
			return a.executeWithReplan(ctx, p)
		case PlanRevise:
			if strings.TrimSpace(feedback) == "" {
				return "", fmt.Errorf("revise plan: feedback is empty")
			}

			messages, contextErr := a.planningMessages(ctx)
			if contextErr != nil {
				return "", contextErr
			}
			p, err = a.planner.Revise(ctx, p, feedback, messages)
			if err != nil {
				return "", fmt.Errorf("revise plan: %w", err)
			}
			baseagent.Emit(ctx, baseagent.Event{
				Type:    baseagent.EventPlanRevised,
				Title:   "[PLANNER] 计划已修改",
				Content: p.Visualize(),
			})
		case PlanCancel:
			p.MarkCancelled()
			baseagent.Emit(ctx, baseagent.Event{
				Type:    baseagent.EventPlanCancelled,
				Title:   "计划已取消",
				Content: p.Visualize(),
			})
			return "计划已取消，已返回 ReAct 模式", nil
		default:
			return "", fmt.Errorf("choose plan action: unknown action %q", action)
		}
	}
}

func (a *PlanAndExecuteAgent) executeWithReplan(
	ctx context.Context,
	p *plan.Plan,
) (string, error) {
	var allResults strings.Builder
	planCtx := ctx
	cancel := func() {}
	if a.planTimeout > 0 {
		planCtx, cancel = context.WithTimeout(ctx, a.planTimeout)
	}
	defer cancel()

	for replanCount := 0; ; replanCount++ {
		a.currentPlan = p

		if replanCount > 0 {
			baseagent.Emit(ctx, baseagent.Event{
				Type:    baseagent.EventPlanReplanning,
				Title:   "已根据执行错误重新规划",
				Content: p.Visualize(),
			})
		}

		result, executeErr := a.scheduler.Execute(
			planCtx,
			p,
			a.executor,
		)
		allResults.WriteString(result)

		if executeErr == nil {
			return allResults.String(), nil
		}
		if planCtx.Err() != nil {
			return allResults.String(), planCtx.Err()
		}

		if replanCount >= a.maxReplanAttempts {
			return allResults.String(), executeErr
		}

		baseagent.Emit(ctx, baseagent.Event{
			Type:    baseagent.EventPlanReplanning,
			Title:   "执行失败，准备重新规划",
			Content: executeErr.Error(),
		})

		messages, contextErr := a.planningMessages(planCtx)
		if contextErr != nil {
			return allResults.String(), contextErr
		}
		replanned, err := a.planner.Replan(
			planCtx,
			p,
			executeErr.Error(),
			messages,
		)
		if err != nil {
			return allResults.String(), fmt.Errorf(
				"replan after execution failure: %w",
				err,
			)
		}
		p = replanned
	}
}

func (a *PlanAndExecuteAgent) compactPlanContext(
	ctx context.Context,
) error {
	if a.rootMemory.Scheduler == nil {
		return nil
	}
	measure := func() (int, error) {
		contextMessages, err := a.planningMessages(ctx)
		if err != nil {
			return 0, err
		}
		messages := make([]llm.Message, 0, len(contextMessages)+1)
		messages = append(messages, llm.SystemMessage(PlannerSystemPrompt))
		messages = append(messages, contextMessages...)
		return a.rootMemory.Estimator.Estimate(messages, nil)
	}
	decisions, err := a.rootMemory.Scheduler.CompactToFit(ctx, a.rootMemory.Manager, measure)
	baseagent.EmitCompactionDecisions(ctx, decisions)
	if err != nil {
		return fmt.Errorf("compact plan context: %w", err)
	}
	return nil
}

func (a *PlanAndExecuteAgent) planningMessages(
	ctx context.Context,
) ([]llm.Message, error) {
	if a.rootMemory.ContextBuilder == nil {
		return nil, fmt.Errorf("context builder is nil")
	}
	messages, err := a.rootMemory.ContextBuilder.BuildWithReport(
		ctx,
		a.currentQuery,
		func(report memory.RetrievalReport) {
			baseagent.Emit(ctx, baseagent.Event{
				Type:      baseagent.EventMemoryRetrieval,
				Retrieval: &report,
			})
		},
	)
	if err != nil {
		return nil, fmt.Errorf("build plan conversation context: %w", err)
	}
	return messages, nil
}

func (a *PlanAndExecuteAgent) storePlanMessage(
	message llm.Message,
) (memory.Entry, error) {
	entry, err := a.rootMemory.Manager.AddMessage(message, "")
	if err != nil {
		return memory.Entry{}, err
	}
	if a.rootMemory.Transcript != nil {
		if err := a.rootMemory.Transcript.Append(a.rootMemory.ConversationID, entry); err != nil {
			return entry, fmt.Errorf("append message to transcript: %w", err)
		}
	}
	return entry, nil
}

func (a *PlanAndExecuteAgent) CurrentPlan() *plan.Plan {
	return a.currentPlan
}
