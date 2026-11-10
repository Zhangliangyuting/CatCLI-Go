package multiagent

import (
	baseagent "AgentCLI/internal/agent"
	"AgentCLI/internal/llm"
	"AgentCLI/internal/tool"
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

type AgentRole string

const (
	RolePlanner  AgentRole = "PLANNER"
	RoleWorker   AgentRole = "WORKER"
	RoleReviewer AgentRole = "REVIEWER"
)

// SubAgent is the shared implementation embedded by every specialized role.
type SubAgent struct {
	role         AgentRole
	systemPrompt string
	llmClient    *llm.OpenAICompatibleClient
	tools        *tool.ToolRegistry
	chatOptions  llm.ChatOptions
}

var _ baseagent.Agent = (*SubAgent)(nil)

func NewSubAgent(
	role AgentRole,
	systemPrompt string,
	llmClient *llm.OpenAICompatibleClient,
	tools *tool.ToolRegistry,
) (*SubAgent, error) {
	switch role {
	case RolePlanner, RoleWorker, RoleReviewer:
	default:
		return nil, fmt.Errorf("invalid SubAgent role %q", role)
	}
	if llmClient == nil {
		return nil, fmt.Errorf("%s SubAgent LLM client is nil", role)
	}
	if tools == nil {
		tools = tool.NewToolRegistry()
	}
	chatOptions := llm.ChatOptions{}
	if role == RoleReviewer {
		chatOptions.ResponseFormat = &llm.ResponseFormat{Type: "json_object"}
		llmClient.DisableThinkingIfSupported(&chatOptions)
	}
	if systemPrompt = strings.TrimSpace(systemPrompt); systemPrompt == "" {
		return nil, fmt.Errorf("%s SubAgent system prompt is empty", role)
	}
	return &SubAgent{
		role:         role,
		systemPrompt: systemPrompt,
		llmClient:    llmClient,
		tools:        tools,
		chatOptions:  chatOptions,
	}, nil
}

func (agent *SubAgent) Role() AgentRole {
	return agent.role
}

func (agent *SubAgent) SystemPrompt() string {
	return agent.systemPrompt
}

func (agent *SubAgent) Run(ctx context.Context, input string) (string, error) {
	return agent.RunMessages(ctx, []llm.Message{llm.UserMessage(input)})
}

func (agent *SubAgent) RunMessages(
	ctx context.Context,
	inputMessages []llm.Message,
) (string, error) {
	if agent == nil || agent.llmClient == nil {
		return "", fmt.Errorf("SubAgent LLM client is nil")
	}

	messages := make([]llm.Message, 0, len(inputMessages)+1)
	messages = append(messages, llm.SystemMessage(agent.systemPrompt))
	messages = append(messages, inputMessages...)
	toolDefinitions := agent.tools.ToolDefinitions()
	rolePrefix := "[" + string(agent.role) + "] "

	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}

		result, err := agent.llmClient.ChatWithOptions(
			ctx,
			messages,
			toolDefinitions,
			agent.chatOptions,
		)
		if err != nil {
			return "", fmt.Errorf("run %s SubAgent: %w", agent.role, err)
		}
		messages = append(messages, result.Message)

		baseagent.Emit(ctx, baseagent.Event{
			Type:  baseagent.EventTokenUsage,
			Title: rolePrefix + "Token usage",
			Content: fmt.Sprintf(
				"input=%d output=%d total=%d",
				result.Usage.PromptTokens,
				result.Usage.CompletionTokens,
				result.Usage.TotalTokens,
			),
		})

		if len(result.Message.ToolCalls) == 0 {
			return result.Message.Content, nil
		}

		for _, toolCall := range result.Message.ToolCalls {
			baseagent.Emit(ctx, baseagent.Event{
				Type:    baseagent.EventToolCall,
				Title:   rolePrefix + toolCall.Function.Name,
				Content: toolCall.Function.Arguments,
			})

			toolResult, err := agent.executeTool(ctx, toolCall)
			if err != nil {
				return "", err
			}
			baseagent.Emit(ctx, baseagent.Event{
				Type:    baseagent.EventToolResult,
				Title:   rolePrefix + toolCall.Function.Name,
				Content: toolResult,
			})
			messages = append(messages, llm.ToolMessage(toolCall.ID, toolResult))
		}
	}
}

func (agent *SubAgent) executeTool(ctx context.Context, call llm.ToolCall) (string, error) {
	data, result, err := startToolCallRecording(ctx, call)
	if err != nil {
		return "", err
	}
	var args map[string]any
	var toolErr error
	if err := json.Unmarshal([]byte(data.Arguments), &args); err != nil {
		toolErr = fmt.Errorf("invalid tool arguments: %w", err)
	} else if data.ReusedFrom == "" {
		result, toolErr = agent.tools.Execute(ctx, data.ToolName, args)
	}
	if toolErr != nil {
		result = "ERROR: " + toolErr.Error()
	}
	if err := completeToolCallRecording(ctx, data.CallID, result, toolErr); err != nil {
		return "", err
	}
	return result, nil
}
