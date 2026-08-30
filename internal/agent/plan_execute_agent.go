package agent

import (
	"AgentCLI/internal/plan"
	"context"
	"fmt"
	"strings"
	"time"
)

type PlanAndExecuteAgent struct {
	planner           plan.PlanGenerator
	reviewer          PlanReviewer
	scheduler         *planScheduler
	currentPlan       *plan.Plan
	maxReplanAttempts int
	planTimeout       time.Duration
}

var _ Agent = (*PlanAndExecuteAgent)(nil)
var _ ObservableAgent = (*PlanAndExecuteAgent)(nil)

func NewPlanAndExecuteAgent(
	planner plan.PlanGenerator,
	executor func() Agent,
	reviewer PlanReviewer,
	maxReplanAttempts int,
	maxWorkers int,
	taskTimeout time.Duration,
	planTimeout time.Duration,
) *PlanAndExecuteAgent {
	return &PlanAndExecuteAgent{
		planner:           planner,
		reviewer:          reviewer,
		scheduler:         newPlanScheduler(maxWorkers, taskTimeout, executor),
		maxReplanAttempts: maxReplanAttempts,
		planTimeout:       planTimeout,
	}
}

func (a *PlanAndExecuteAgent) Run(
	ctx context.Context,
	input string,
) (string, error) {
	return a.RunWithObserver(ctx, input, nil)
}

func (a *PlanAndExecuteAgent) RunWithObserver(
	ctx context.Context,
	input string,
	observer Observer,
) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}

	p, err := a.planner.Generate(ctx, input)
	if err != nil {
		return "", fmt.Errorf("generate plan: %w", err)
	}
	emit(observer, Event{
		Type:    EventPlanGenerated,
		Title:   "计划已生成",
		Content: p.Visualize(),
	})

	if a.reviewer == nil {
		return "", fmt.Errorf("review plan: reviewer is nil")
	}

	for {
		if err := ctx.Err(); err != nil {
			p.MarkCancelled()
			return "", err
		}

		a.currentPlan = p

		action, feedback, err := a.reviewer.Review(p)
		if err != nil {
			return "", fmt.Errorf("review plan: %w", err)
		}
		if err := ctx.Err(); err != nil {
			p.MarkCancelled()
			return "", err
		}

		switch action {
		case PlanExecute:
			return a.executeWithReplanObserver(ctx, p, observer)
		case PlanRevise:
			if strings.TrimSpace(feedback) == "" {
				return "", fmt.Errorf("revise plan: feedback is empty")
			}

			p, err = a.planner.Revise(ctx, p, feedback)
			if err != nil {
				return "", fmt.Errorf("revise plan: %w", err)
			}
			emit(observer, Event{
				Type:    EventPlanRevised,
				Title:   "计划已修改",
				Content: p.Visualize(),
			})
		case PlanCancel:
			p.MarkCancelled()
			emit(observer, Event{
				Type:    EventPlanCancelled,
				Title:   "计划已取消",
				Content: p.Visualize(),
			})
			return "计划已取消，已返回 ReAct 模式", nil
		default:
			return "", fmt.Errorf("review plan: unknown action %q", action)
		}
	}
}

func (a *PlanAndExecuteAgent) executeWithReplan(
	ctx context.Context,
	p *plan.Plan,
) (string, error) {
	return a.executeWithReplanObserver(ctx, p, nil)
}

func (a *PlanAndExecuteAgent) executeWithReplanObserver(
	ctx context.Context,
	p *plan.Plan,
	observer Observer,
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
			emit(observer, Event{
				Type:    EventPlanReplanning,
				Title:   "已根据执行错误重新规划",
				Content: p.Visualize(),
			})
		}

		result, executeErr := a.scheduler.ExecuteWithObserver(
			planCtx,
			p,
			observer,
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

		emit(observer, Event{
			Type:    EventPlanReplanning,
			Title:   "执行失败，准备重新规划",
			Content: executeErr.Error(),
		})

		replanned, err := a.planner.Replan(
			planCtx,
			p,
			executeErr.Error(),
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

func (a *PlanAndExecuteAgent) CurrentPlan() *plan.Plan {
	return a.currentPlan
}
