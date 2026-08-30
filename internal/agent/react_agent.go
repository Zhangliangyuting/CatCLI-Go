package agent

import (
	"AgentCLI/internal/llm"
	"AgentCLI/internal/tool"
	"context"
	"encoding/json"
	"fmt"
)

type ReActAgent struct {
	llmClient           *llm.OpenAICompatibleClient
	tools               *tool.ToolRegistry
	conversationHistory []llm.Message
}

var _ ObservableAgent = (*ReActAgent)(nil)

func NewReActAgent(client *llm.OpenAICompatibleClient, tools *tool.ToolRegistry) *ReActAgent {
	return &ReActAgent{
		llmClient: client,
		tools:     tools,
		conversationHistory: []llm.Message{
			llm.SystemMessage("You are a helpful assistant."),
		},
	}
}

func (a *ReActAgent) Run(
	ctx context.Context,
	userInput string,
) (string, error) {
	return a.RunWithObserver(ctx, userInput, nil)
}

func (a *ReActAgent) RunWithObserver(
	ctx context.Context,
	userInput string,
	observer Observer,
) (string, error) {
	a.conversationHistory = append(a.conversationHistory, llm.UserMessage(userInput))

	tools := a.tools.ToolDefinitions()

	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}

		result, err := a.llmClient.ChatContext(
			ctx,
			a.conversationHistory,
			tools,
		)
		if err != nil {
			return "", err
		}

		a.conversationHistory = append(a.conversationHistory, result.Message)

		emit(observer, Event{
			Type:  EventTokenUsage,
			Title: "Token usage",
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
			emit(observer, Event{
				Type:    EventToolCall,
				Title:   toolCall.Function.Name,
				Content: toolCall.Function.Arguments,
			})

			var args map[string]interface{}
			if err := json.Unmarshal([]byte(toolCall.Function.Arguments), &args); err != nil {
				toolResult := "ERROR: invalid tool arguments: " + err.Error()
				emit(observer, Event{
					Type:    EventToolResult,
					Title:   toolCall.Function.Name,
					Content: toolResult,
				})
				a.conversationHistory = append(a.conversationHistory, llm.ToolMessage(toolCall.ID, toolResult))
				continue
			}

			toolResult, err := a.tools.Execute(
				ctx,
				toolCall.Function.Name,
				args,
			)
			if err != nil {
				toolResult = "ERROR: " + err.Error()
			}

			emit(observer, Event{
				Type:    EventToolResult,
				Title:   toolCall.Function.Name,
				Content: toolResult,
			})

			a.conversationHistory = append(a.conversationHistory, llm.ToolMessage(toolCall.ID, toolResult))
		}
	}
}

func (a *ReActAgent) ClearHistory() {
	a.conversationHistory = []llm.Message{
		llm.SystemMessage("You are a helpful assistant."),
	}
}
