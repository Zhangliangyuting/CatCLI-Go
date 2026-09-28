package routing

import (
	"AgentCLI/internal/llm"
	"context"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

type ExecutionMode string

const (
	ModeReact ExecutionMode = "REACT"
	ModePlan  ExecutionMode = "PLAN"
)

type DecisionSource string

const (
	SourceExplicit DecisionSource = "explicit"
	SourceRule     DecisionSource = "rule"
	SourceLLM      DecisionSource = "llm"
	SourceFallback DecisionSource = "fallback"
)

type Decision struct {
	Mode   ExecutionMode
	Input  string
	Source DecisionSource
	Reason string
}

type ModeRouter interface {
	Route(ctx context.Context, input string) (Decision, error)
}

type HybridModeRouter struct {
	client *llm.OpenAICompatibleClient
}

func NewHybridModeRouter(
	client *llm.OpenAICompatibleClient,
) *HybridModeRouter {
	return &HybridModeRouter{client: client}
}

func (r *HybridModeRouter) Route(
	ctx context.Context,
	input string,
) (Decision, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return Decision{}, fmt.Errorf("route mode: input is empty")
	}

	if decision, matched, err := explicitDecision(input); matched || err != nil {
		return decision, err
	}
	if decision, matched := ruleDecision(input); matched {
		return decision, nil
	}
	if r.client == nil {
		return Decision{}, fmt.Errorf("route mode: LLM client is nil")
	}

	result, err := r.client.Chat(
		ctx,
		[]llm.Message{
			llm.SystemMessage(modeClassifierPrompt),
			llm.UserMessage(input),
		},
		nil,
	)
	if err != nil {
		return Decision{}, fmt.Errorf("route mode with LLM: %w", err)
	}

	mode, ok := parseMode(result.Message.Content)
	if !ok {
		return Decision{
			Mode:   ModeReact,
			Input:  input,
			Source: SourceFallback,
			Reason: "分类结果无法识别，回退到 ReAct",
		}, nil
	}

	return Decision{
		Mode:   mode,
		Input:  input,
		Source: SourceLLM,
		Reason: "由 LLM 判断任务复杂度",
	}, nil
}

func explicitDecision(input string) (Decision, bool, error) {
	lower := strings.ToLower(input)
	for _, item := range []struct {
		command string
		mode    ExecutionMode
	}{
		{command: "/react", mode: ModeReact},
		{command: "/plan", mode: ModePlan},
	} {
		if lower != item.command && !strings.HasPrefix(lower, item.command+" ") {
			continue
		}

		taskInput := strings.TrimSpace(input[len(item.command):])
		if taskInput == "" {
			return Decision{}, true, fmt.Errorf(
				"%s 后面必须提供任务内容",
				item.command,
			)
		}

		return Decision{
			Mode:   item.mode,
			Input:  taskInput,
			Source: SourceExplicit,
			Reason: "用户显式指定执行模式",
		}, true, nil
	}

	return Decision{}, false, nil
}

func ruleDecision(input string) (Decision, bool) {
	complexKeywords := []string{
		"多个文件", "多个模块", "整个项目", "完整项目",
		"从零创建", "前后端", "重构", "迁移", "架构设计",
		"分步骤", "拆分任务", "并行执行", "批量修改",
	}
	for _, keyword := range complexKeywords {
		if strings.Contains(input, keyword) {
			return Decision{
				Mode:   ModePlan,
				Input:  input,
				Source: SourceRule,
				Reason: "命中复杂任务特征：" + keyword,
			}, true
		}
	}

	if strings.Contains(input, "先") && strings.Contains(input, "再") {
		return Decision{
			Mode:   ModePlan,
			Input:  input,
			Source: SourceRule,
			Reason: "任务包含明确的先后步骤",
		}, true
	}
	if countNumberedSteps(input) >= 2 {
		return Decision{
			Mode:   ModePlan,
			Input:  input,
			Source: SourceRule,
			Reason: "任务包含多个编号步骤",
		}, true
	}

	simpleKeywords := []string{
		"为什么", "是什么", "什么意思", "解释一下", "说明一下",
	}
	for _, keyword := range simpleKeywords {
		if strings.Contains(input, keyword) {
			return Decision{
				Mode:   ModeReact,
				Input:  input,
				Source: SourceRule,
				Reason: "命中单轮解释型任务特征：" + keyword,
			}, true
		}
	}

	if utf8.RuneCountInString(input) <= 120 {
		for _, prefix := range []string{"读取", "查看", "列出", "检查", "运行测试"} {
			if strings.HasPrefix(input, prefix) {
				return Decision{
					Mode:   ModeReact,
					Input:  input,
					Source: SourceRule,
					Reason: "命中单一操作特征：" + prefix,
				}, true
			}
		}
	}

	return Decision{}, false
}

func countNumberedSteps(input string) int {
	count := 0
	for _, line := range strings.Split(input, "\n") {
		line = strings.TrimSpace(line)
		if len(line) < 2 {
			continue
		}
		first, size := utf8.DecodeRuneInString(line)
		if !unicode.IsDigit(first) || len(line) == size {
			continue
		}
		next, _ := utf8.DecodeRuneInString(line[size:])
		if next == '.' || next == '、' || next == ')' || next == '）' {
			count++
		}
	}
	return count
}

func parseMode(content string) (ExecutionMode, bool) {
	content = strings.TrimSpace(strings.Trim(content, "`"))
	content = strings.TrimSpace(strings.TrimPrefix(
		strings.ToUpper(content),
		"MODE:",
	))
	fields := strings.Fields(content)
	if len(fields) == 0 {
		return "", false
	}

	first := strings.Trim(fields[0], "`.,:;，。；：")
	switch ExecutionMode(first) {
	case ModeReact:
		return ModeReact, true
	case ModePlan:
		return ModePlan, true
	default:
		return "", false
	}
}

const modeClassifierPrompt = `你是编程 Agent 的执行模式分类器。

判断用户任务应该使用哪种模式：
- REACT：单一、明确、局部的问答、检查、读取、测试或代码修改。
- PLAN：包含多个步骤、依赖关系、多个文件或模块、项目创建、重构、迁移，或者适合并行执行的复杂任务。

不要执行任务，不要解释原因，只能返回 REACT 或 PLAN。`
