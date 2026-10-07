package multiagent

import (
	baseagent "AgentCLI/internal/agent"
	"AgentCLI/internal/llm"
	"AgentCLI/internal/plan"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const PlannerSystemPrompt = `
你是一个任务规划专家，请帮用户把复杂任务拆解为可执行的子任务，并生成一个合理的执行计划。请按照以下要求进行规划：
    请按以下JSON格式输出执行计划：
	{
		"goal": "任务目标",
		"tasks": [
			{
				"id": "task_1",
				"name": "子任务名称",
				"description": "子任务描述",
				"type": "子任务类型",
				"dependencies": [],
				"read_resources": ["需要读取的文件路径"],
				"write_resources": ["需要修改的文件路径"]
			}
		],
		"summary": "任务摘要"
	}
	只输出 JSON，不要输出解释、Markdown 或代码块。

	子任务类型可以是以下几种：
	- PLANNING: 继续细化计划或整理执行策略
	- FILE_READ: 读取文件内容，用于获取信息
	- FILE_WRITE: 写入文件内容，用于输出结果
	- COMMAND: 执行Shell命令，用于编译运行等
	- ANALYSIS: 分析结果，用于中间决策
	- VERIFICATION: 验证结果，用于检查正确性

	规则：
	1. 每个任务必须有唯一的id（如 task_1、task_2）
	2. dependencies列出依赖的任务id
	3. 任务应该按执行顺序排列
	4. 任务描述要具体明确
	5. read_resources 和 write_resources 使用工作区相对路径；无法提前确定时使用空数组
	6. 会修改同一资源，或一方读取而另一方修改同一资源的任务，不应并行执行
	任务拆分规则：
		1. 简单任务拆分为 1-3 个子任务。
		2. 中等任务拆分为 3-5 个子任务。
		3. 只有复杂任务才拆分为 5-10 个子任务。
		4. 不要为检查目录、创建目录、验证简单输出等操作单独创建任务，除非它们具有独立价值。
		5. 可以在一次工具调用或同一执行上下文完成的操作，应尽量合并。
		6. 避免生成重复的检查和验证任务。
`

// PlannerSubAgent is both a SubAgent and a typed plan generator.
type PlannerSubAgent struct {
	*SubAgent
}

var _ baseagent.Agent = (*PlannerSubAgent)(nil)

func NewPlannerSubAgent(
	client *llm.OpenAICompatibleClient,
) (*PlannerSubAgent, error) {
	agent, err := NewSubAgent(RolePlanner, PlannerSystemPrompt, client, nil)
	if err != nil {
		return nil, err
	}
	return &PlannerSubAgent{SubAgent: agent}, nil
}

func (agent *PlannerSubAgent) Generate(
	ctx context.Context,
	contextMessages []llm.Message,
) (*plan.Plan, error) {
	content, err := agent.RunMessages(ctx, contextMessages)
	if err != nil {
		return nil, fmt.Errorf("generate plan: %w", err)
	}
	p, err := parsePlan(content)
	if err != nil {
		return nil, fmt.Errorf("parse plan: %w", err)
	}
	return p, nil
}

func (agent *PlannerSubAgent) Revise(
	ctx context.Context,
	currentPlan *plan.Plan,
	feedback string,
	contextMessages []llm.Message,
) (*plan.Plan, error) {
	messages := append([]llm.Message(nil), contextMessages...)
	messages = append(messages, llm.UserMessage(buildRevisionContext(currentPlan, feedback)))
	return agent.Generate(ctx, messages)
}

func (agent *PlannerSubAgent) Replan(
	ctx context.Context,
	failedPlan *plan.Plan,
	failureReason string,
	contextMessages []llm.Message,
) (*plan.Plan, error) {
	messages := append([]llm.Message(nil), contextMessages...)
	messages = append(messages, llm.UserMessage(buildReplanContext(failedPlan, failureReason)))
	return agent.Generate(ctx, messages)
}

type rawPlan struct {
	Goal    string    `json:"goal"`
	Summary string    `json:"summary"`
	Tasks   []rawTask `json:"tasks"`
}

type rawTask struct {
	ID             string        `json:"id"`
	Name           string        `json:"name"`
	Description    string        `json:"description"`
	Type           plan.TaskType `json:"type"`
	Dependencies   []string      `json:"dependencies"`
	ReadResources  []string      `json:"read_resources"`
	WriteResources []string      `json:"write_resources"`
}

func parsePlan(planJSON string) (*plan.Plan, error) {
	planJSON = cleanJSON(planJSON)

	var raw rawPlan
	if err := json.Unmarshal([]byte(planJSON), &raw); err != nil {
		return nil, err
	}
	if len(raw.Tasks) == 0 {
		return nil, fmt.Errorf("plan has no tasks")
	}

	p := plan.NewPlan(fmt.Sprintf("plan_%d", time.Now().UnixNano()), raw.Goal, raw.Summary)
	idMapping := make(map[string]string, len(raw.Tasks))

	for index, rawTask := range raw.Tasks {
		if rawTask.ID == "" {
			return nil, fmt.Errorf("task id is empty at index %d", index)
		}
		if _, exists := idMapping[rawTask.ID]; exists {
			return nil, fmt.Errorf("duplicate raw task id: %s", rawTask.ID)
		}
		if rawTask.Description == "" {
			return nil, fmt.Errorf("task description is empty for raw task %q", rawTask.ID)
		}
		if !validTaskType(rawTask.Type) {
			return nil, fmt.Errorf("invalid task type %q for raw task %q", rawTask.Type, rawTask.ID)
		}

		taskID := fmt.Sprintf("task_%d", index+1)
		idMapping[rawTask.ID] = taskID
		task := plan.NewTask(taskID, rawTask.Name, rawTask.Description, rawTask.Type, nil)
		task.SetResources(rawTask.ReadResources, rawTask.WriteResources)
		if err := p.AddTask(task); err != nil {
			return nil, err
		}
	}

	for index, rawTask := range raw.Tasks {
		taskID := fmt.Sprintf("task_%d", index+1)
		task, exists := p.TaskByID(taskID)
		if !exists {
			return nil, fmt.Errorf("task not found: %s", taskID)
		}
		for _, rawDependencyID := range rawTask.Dependencies {
			dependencyID, exists := idMapping[rawDependencyID]
			if !exists {
				return nil, fmt.Errorf("unknown dependency %q for task %q", rawDependencyID, rawTask.ID)
			}
			if dependencyID == taskID {
				return nil, fmt.Errorf("task %q depends on itself", rawTask.ID)
			}
			task.SetDependencies(append(task.Dependencies(), dependencyID))
			dependency, exists := p.TaskByID(dependencyID)
			if !exists {
				return nil, fmt.Errorf("dependency task not found: %s", dependencyID)
			}
			dependency.SetDependents(append(dependency.Dependents(), taskID))
		}
	}

	if err := p.TopologicalSort(); err != nil {
		return nil, err
	}
	return p, nil
}

func cleanJSON(content string) string {
	content = strings.TrimSpace(content)
	content = strings.TrimPrefix(content, "```json")
	content = strings.TrimPrefix(content, "```")
	content = strings.TrimSuffix(content, "```")
	return strings.TrimSpace(content)
}

func validTaskType(taskType plan.TaskType) bool {
	switch taskType {
	case plan.PLANNING, plan.FILE_READ, plan.FILE_WRITE, plan.COMMAND, plan.ANALYSIS, plan.VERIFICATION:
		return true
	default:
		return false
	}
}

func buildReplanContext(failedPlan *plan.Plan, failureReason string) string {
	var prompt strings.Builder
	fmt.Fprintf(&prompt, "原始目标：\n%s\n\n", failedPlan.Goal())
	fmt.Fprintf(&prompt, "原计划摘要：\n%s\n\n", failedPlan.Summary())
	prompt.WriteString("已完成任务：\n")

	for _, taskID := range failedPlan.ExecutionOrder() {
		task, exists := failedPlan.TaskByID(taskID)
		if !exists || task.Status() != plan.COMPLETED {
			continue
		}
		fmt.Fprintf(&prompt, "- %s\n  执行结果：%s\n", task.Description(), task.Result())
	}

	prompt.WriteString("\n失败任务：\n")
	for _, taskID := range failedPlan.ExecutionOrder() {
		task, exists := failedPlan.TaskByID(taskID)
		if !exists || task.Status() != plan.FAILED {
			continue
		}
		fmt.Fprintf(&prompt, "- %s\n", task.Description())
		if task.Error() != nil {
			fmt.Fprintf(&prompt, "  任务错误：%s\n", task.Error())
		}
	}

	fmt.Fprintf(&prompt, "\n失败原因：\n%s\n", failureReason)
	prompt.WriteString(`
请根据以上上下文重新生成执行计划。

要求：
1. 保持原始目标不变。
2. 不要重复已经完成的任务。
3. 利用已完成任务的执行结果。
4. 针对失败原因调整后续任务。
5. 只规划尚未完成的工作。
`)
	return prompt.String()
}

func buildRevisionContext(currentPlan *plan.Plan, feedback string) string {
	var prompt strings.Builder
	fmt.Fprintf(&prompt, "原始目标：\n%s\n\n", currentPlan.Goal())
	fmt.Fprintf(&prompt, "当前计划摘要：\n%s\n\n", currentPlan.Summary())
	prompt.WriteString("当前计划任务：\n")

	for _, taskID := range currentPlan.ExecutionOrder() {
		task, exists := currentPlan.TaskByID(taskID)
		if !exists {
			continue
		}
		fmt.Fprintf(
			&prompt,
			"- %s：%s\n  类型：%s\n  依赖：%s\n  读取资源：%s\n  写入资源：%s\n",
			task.Name(),
			task.Description(),
			task.Type(),
			strings.Join(task.Dependencies(), ", "),
			strings.Join(task.ReadResources(), ", "),
			strings.Join(task.WriteResources(), ", "),
		)
	}

	fmt.Fprintf(&prompt, "\n用户修改意见：\n%s\n", feedback)
	prompt.WriteString(`
请根据用户意见生成一份完整的新计划。

要求：
1. 保持原始目标不变。
2. 根据用户意见调整任务。
3. 此计划尚未开始执行，不要描述任务执行结果。
4. 输出完整计划，而不是只输出变化部分。
`)
	return prompt.String()
}
