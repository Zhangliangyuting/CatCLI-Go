package plan

import (
	"AgentCLI/internal/llm"
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
				"dependencies": []
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
	5. 复杂任务拆分为5-10个子任务
`

type PlanGenerator interface {
	Generate(userInput string) (*Plan, error)
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
	userInput string,
) (*Plan, error) {
	messages := []llm.Message{
		llm.SystemMessage(PLANNING_PROMPT),
		llm.UserMessage(userInput),
	}

	result, err := g.client.Chat(messages, nil)
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

	plan := NewPlan(generatePlanID(), raw.Goal)
	plan.WriteSummary(raw.Summary)

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

func (g *LLMPlanGenerator) Replan(failedPlan *Plan, failureReason string) (*Plan, error) {
	context := buildReplanContext(failedPlan, failureReason)
	return g.Generate(context)
}

func buildReplanContext(failedPlan *Plan, failureReason string) string {
	var context strings.Builder

	fmt.Fprintf(&context, "原始目标：\n%s\n\n", failedPlan.Goal())

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
