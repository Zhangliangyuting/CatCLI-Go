package plan

import (
	"AgentCLI/internal/llm"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const PLANNING_PROMPT = `
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
	1. 每个任务必须有唯一的id（如 task_1, task_2）
	2. dependencies列出依赖的任务id
	3. 任务应该按执行顺序排列
	4. 任务描述要具体明确
	5. read_resources 和 write_resources 使用工作区相对路径；无法提前确定时使用空数组
	6. 会修改同一资源，或一方读取而另一方修改同一资源的任务，不应并行执行
	任务拆分规则：
		1. 简单任务拆分为 1-3 个子任务。
		2. 中等任务拆分为 3-5 个子任务。
		3. 只有复杂任务才拆分为 5-10 个子任务。
		4. 不要为检查目录、创建目录、验证简单输出等操作单独创建任务，
		除非它们具有独立价值。
		5. 可以在一次工具调用或同一执行上下文完成的操作，应尽量合并。
		6. 避免生成重复的检查和验证任务。
`

type PlanGenerator interface {
	Generate(ctx context.Context, userInput string) (*Plan, error)
	// 用户在执行前主动修改计划
	Revise(ctx context.Context, currentPlan *Plan, feedback string) (*Plan, error)
	// 执行失败后的自动重新规划
	Replan(ctx context.Context, failedPlan *Plan, failureReason string) (*Plan, error)
}

type LLMPlanGenerator struct {
	client *llm.OpenAICompatibleClient
}

func NewLLMPlanGenerator(
	client *llm.OpenAICompatibleClient,
) *LLMPlanGenerator {
	return &LLMPlanGenerator{
		client: client,
	}
}

func (g *LLMPlanGenerator) Generate(
	ctx context.Context,
	userInput string,
) (*Plan, error) {
	messages := []llm.Message{
		llm.SystemMessage(PLANNING_PROMPT),
		llm.UserMessage(userInput),
	}

	result, err := g.client.ChatContext(ctx, messages, nil)
	if err != nil {
		return nil, fmt.Errorf("generate plan: %w", err)
	}

	p, err := parsePlan(result.Message.Content)
	if err != nil {
		return nil, fmt.Errorf("parse plan: %w", err)
	}

	return p, nil
}

func parsePlan(planJSON string) (*Plan, error) {
	planJSON = cleanJSON(planJSON)

	var raw rawPlan
	if err := json.Unmarshal([]byte(planJSON), &raw); err != nil {
		return nil, err
	}

	if len(raw.Tasks) == 0 {
		return nil, fmt.Errorf("plan has no tasks")
	}

	plan := NewPlan(generatePlanID(), raw.Goal, raw.Summary)

	idMapping := make(map[string]string)

	for i, rt := range raw.Tasks {
		if rt.ID == "" {
			return nil, fmt.Errorf("task id is empty at index %d", i)
		}
		if _, exists := idMapping[rt.ID]; exists {
			return nil, fmt.Errorf("duplicate raw task id: %s", rt.ID)
		}
		if rt.Description == "" {
			return nil, fmt.Errorf("task description is empty for raw task %q", rt.ID)
		}
		if !isValidTaskType(rt.Type) {
			return nil, fmt.Errorf("invalid task type %q for raw task %q", rt.Type, rt.ID)
		}

		newID := fmt.Sprintf("task_%d", i+1)
		idMapping[rt.ID] = newID

		task := NewTask(
			newID,
			rt.Name,
			rt.Description,
			rt.Type,
			[]string{},
		)
		task.SetResources(rt.ReadResources, rt.WriteResources)

		if err := plan.AddTask(task); err != nil {
			return nil, err
		}
	}

	for i, rt := range raw.Tasks {
		taskID := fmt.Sprintf("task_%d", i+1)
		task, exists := plan.TaskByID(taskID)
		if !exists {
			return nil, fmt.Errorf("task not found: %s", taskID)
		}

		for _, rawDepID := range rt.Dependencies {
			depID, ok := idMapping[rawDepID]
			if !ok {
				return nil, fmt.Errorf("unknown dependency %q for task %q", rawDepID, rt.ID)
			}
			if depID == taskID {
				return nil, fmt.Errorf("task %q depends on itself", rt.ID)
			}

			task.SetDependencies(append(task.Dependencies(), depID))

			depTask, exists := plan.TaskByID(depID)
			if !exists {
				return nil, fmt.Errorf("dependency task not found: %s", depID)
			}
			depTask.SetDependents(append(depTask.Dependents(), taskID))
		}
	}

	if err := plan.computeExecutionOrder(); err != nil {
		return nil, err
	}

	return plan, nil
}

func cleanJSON(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	return strings.TrimSpace(s)
}

func isValidTaskType(t TaskType) bool {
	switch t {
	case PLANNING, FILE_READ, FILE_WRITE, COMMAND, ANALYSIS, VERIFICATION:
		return true
	default:
		return false
	}
}

func generatePlanID() string {
	return fmt.Sprintf("plan_%d", time.Now().UnixNano())
}

func (g *LLMPlanGenerator) Replan(
	ctx context.Context,
	failedPlan *Plan,
	failureReason string,
) (*Plan, error) {
	replanContext := buildReplanContext(failedPlan, failureReason)
	return g.Generate(ctx, replanContext)
}

func buildReplanContext(failedPlan *Plan, failureReason string) string {
	var context strings.Builder

	fmt.Fprintf(&context, "原始目标：\n%s\n\n", failedPlan.Goal())
	fmt.Fprintf(&context, "原计划摘要：\n%s\n\n", failedPlan.Summary())

	context.WriteString("已完成任务：\n")

	for _, taskID := range failedPlan.ExecutionOrder() {
		task, exists := failedPlan.TaskByID(taskID)

		if !exists {
			continue
		}

		if task.Status() != COMPLETED {
			continue
		}

		fmt.Fprintf(
			&context,
			"- %s\n  执行结果：%s\n",
			task.Description(),
			task.Result(),
		)
	}

	context.WriteString("\n失败任务：\n")

	for _, taskID := range failedPlan.ExecutionOrder() {
		task, exists := failedPlan.TaskByID(taskID)
		if !exists {
			continue
		}

		if task.Status() != FAILED {
			continue
		}

		fmt.Fprintf(
			&context,
			"- %s\n",
			task.Description(),
		)

		if task.Error() != nil {
			fmt.Fprintf(&context, "  任务错误：%s\n", task.Error())
		}
	}

	fmt.Fprintf(
		&context,
		"\n失败原因：\n%s\n",
		failureReason,
	)

	context.WriteString(`
请根据以上上下文重新生成执行计划。

要求：
1. 保持原始目标不变。
2. 不要重复已经完成的任务。
3. 利用已完成任务的执行结果。
4. 针对失败原因调整后续任务。
5. 只规划尚未完成的工作。
`)

	return context.String()
}

func (g *LLMPlanGenerator) Revise(
	ctx context.Context,
	currentPlan *Plan,
	feedback string,
) (*Plan, error) {
	revisionContext := buildRevisionContext(currentPlan, feedback)
	return g.Generate(ctx, revisionContext)
}

func buildRevisionContext(
	currentPlan *Plan,
	feedback string,
) string {
	var context strings.Builder

	fmt.Fprintf(&context, "原始目标：\n%s\n\n", currentPlan.Goal())
	fmt.Fprintf(&context, "当前计划摘要：\n%s\n\n", currentPlan.Summary())
	context.WriteString("当前计划任务：\n")

	for _, taskID := range currentPlan.ExecutionOrder() {
		task, ok := currentPlan.TaskByID(taskID)
		if !ok {
			continue
		}

		fmt.Fprintf(
			&context,
			"- %s：%s\n  类型：%s\n  依赖：%s\n  读取资源：%s\n  写入资源：%s\n",
			task.Name(),
			task.Description(),
			task.Type(),
			strings.Join(task.Dependencies(), ", "),
			strings.Join(task.ReadResources(), ", "),
			strings.Join(task.WriteResources(), ", "),
		)
	}

	fmt.Fprintf(&context, "\n用户修改意见：\n%s\n", feedback)

	context.WriteString(`
请根据用户意见生成一份完整的新计划。

要求：
1. 保持原始目标不变。
2. 根据用户意见调整任务。
3. 此计划尚未开始执行，不要描述任务执行结果。
4. 输出完整计划，而不是只输出变化部分。
`)

	return context.String()
}
