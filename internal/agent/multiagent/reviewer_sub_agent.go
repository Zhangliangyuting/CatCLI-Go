package multiagent

import (
	baseagent "AgentCLI/internal/agent"
	"AgentCLI/internal/llm"
	"AgentCLI/internal/plan"
	"AgentCLI/internal/tool"
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

const ReviewerSystemPrompt = `你是独立的任务结果检查者。请根据整体目标、当前任务和执行结果判断任务是否完成。
你可以使用提供的只读工具检查文件和目录，不能修改任何内容，也不能执行命令。
只输出严格 JSON：{"approved":true或false,"feedback":"具体反馈"}。
通过时 feedback 可以为空；驳回时必须指出缺失内容或错误。`

// ReviewerSubAgent is both a SubAgent and a typed result reviewer.
type ReviewerSubAgent struct {
	*SubAgent
}

var _ baseagent.Agent = (*ReviewerSubAgent)(nil)

type ResultReview struct {
	Approved bool   `json:"approved"`
	Feedback string `json:"feedback"`
}

type ResultRejectedError struct {
	TaskID   string
	Feedback string
}

func (err *ResultRejectedError) Error() string {
	feedback := strings.TrimSpace(err.Feedback)
	if feedback == "" {
		feedback = "result did not satisfy the task requirements"
	}
	return fmt.Sprintf("task %s result rejected: %s", err.TaskID, feedback)
}

func NewReviewerSubAgent(
	client *llm.OpenAICompatibleClient,
	tools *tool.ToolRegistry,
) (*ReviewerSubAgent, error) {
	agent, err := NewSubAgent(RoleReviewer, ReviewerSystemPrompt, client, tools)
	if err != nil {
		return nil, err
	}
	return &ReviewerSubAgent{SubAgent: agent}, nil
}

func (agent *ReviewerSubAgent) ReviewResult(
	ctx context.Context,
	execution TaskExecution,
	result string,
) (ResultReview, error) {
	payload, err := json.Marshal(struct {
		Goal            string        `json:"goal"`
		TaskID          string        `json:"task_id"`
		TaskName        string        `json:"task_name"`
		TaskDescription string        `json:"task_description"`
		TaskType        plan.TaskType `json:"task_type"`
		TaskPrompt      string        `json:"task_prompt"`
		Result          string        `json:"result"`
	}{
		Goal:            execution.PlanGoal,
		TaskID:          execution.TaskID,
		TaskName:        execution.TaskName,
		TaskDescription: execution.TaskDescription,
		TaskType:        execution.TaskType,
		TaskPrompt:      execution.Prompt,
		Result:          result,
	})
	if err != nil {
		return ResultReview{}, fmt.Errorf("encode result review input: %w", err)
	}

	content, err := agent.Run(ctx, string(payload))
	if err != nil {
		return ResultReview{}, fmt.Errorf("review task result: %w", err)
	}

	var review ResultReview
	if err := json.Unmarshal([]byte(cleanSubAgentJSON(content)), &review); err != nil {
		return ResultReview{}, fmt.Errorf("parse result review: %w", err)
	}
	if !review.Approved && strings.TrimSpace(review.Feedback) == "" {
		return ResultReview{}, fmt.Errorf("parse result review: rejected result has empty feedback")
	}
	return review, nil
}

func cleanSubAgentJSON(content string) string {
	content = strings.TrimSpace(content)
	content = strings.TrimPrefix(content, "```json")
	content = strings.TrimPrefix(content, "```")
	content = strings.TrimSuffix(content, "```")
	return strings.TrimSpace(content)
}
