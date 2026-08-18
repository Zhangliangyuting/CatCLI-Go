package agent

import (
	"AgentCLI/internal/plan"
	"fmt"
	"strings"
)

type PlanAndExecuteAgent struct {
	planner     plan.PlanGenerator
	executor    func() Agent
	currentPlan *plan.Plan
}

func NewPlanAndExecuteAgent(planner plan.PlanGenerator, executor func() Agent) *PlanAndExecuteAgent {
	return &PlanAndExecuteAgent{
		planner:  planner,
		executor: executor,
	}
}

func (a *PlanAndExecuteAgent) Run(input string) (string, error) {
	p, err := a.planner.Generate(input)
	if err != nil {
		return "", fmt.Errorf("generate plan: %w", err)
	}

	p.MarkRunning()

	var results strings.Builder

	for _, taskID := range p.ExecutionOrder() {
		t, exists := p.TaskByID(taskID)
		if !exists {
			p.MarkFailed()
			return "", fmt.Errorf("task not found: %s", taskID)
		}

		t.MarkRunning()

		taskPrompt := buildTaskPrompt(p, t)

		executor := a.executor()
		result, err := executor.Run(taskPrompt)
		if err != nil {
			t.MarkFailed(err)
			p.MarkFailed()
			return "", fmt.Errorf("task %s failed: %w", taskID, err)
		}

		t.MarkCompleted(result)

		fmt.Fprintf(
			&results,
			"%s: %s\n",
			taskID,
			result,
		)
	}

	p.MarkCompleted()
	return results.String(), nil
}

func buildTaskPrompt(p *plan.Plan, task *plan.Task) string {
	var context strings.Builder

	fmt.Fprintf(&context, "整体目标：\n%s\n\n", p.Goal())
	fmt.Fprintf(&context, "当前任务：\n%s\n\n", task.Description())

	if len(task.Dependencies()) > 0 {
		context.WriteString("前置任务及其执行结果：\n")

		for _, dependencyID := range task.Dependencies() {
			dependency, ok := p.TaskByID(dependencyID)
			if !ok {
				continue
			}

			fmt.Fprintf(
				&context,
				"- %s\n  结果：%s\n",
				dependency.Description(),
				dependency.Result(),
			)
		}

		context.WriteString("\n")
	}

	context.WriteString(`执行要求：
1. 只执行当前任务，不要重新规划整个任务。
2. 可以使用提供的工具完成任务。
3. 使用前置任务结果作为上下文。
4. 完成后简洁说明执行结果。
5. 如果无法完成，明确说明原因。
`)

	return context.String()
}

func (a *PlanAndExecuteAgent) CurrentPlan() *plan.Plan {
	return a.currentPlan
}
