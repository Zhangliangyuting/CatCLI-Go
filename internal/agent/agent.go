package agent

import (
	"AgentCLI/internal/llm"
	"AgentCLI/internal/tool"
	"encoding/json"
	"fmt"
)

type Agent struct {
	Client              *llm.OpenAICompatibleClient
	Tools               *tool.ToolRegistry
	MaxSteps            int
	ConversationHistory []llm.Message
}

func NewAgent(client *llm.OpenAICompatibleClient, tools *tool.ToolRegistry, maxSteps int) *Agent {
	return &Agent{
		Client:   client,
		Tools:    tools,
		MaxSteps: maxSteps,
		ConversationHistory: []llm.Message{
			llm.SystemMessage("You are a helpful assistant."),
		},
	}
}

func (a *Agent) Run(userInput string) (string, error) {
	a.ConversationHistory = append(a.ConversationHistory, llm.UserMessage(userInput))

	tools := a.Tools.ToolDefinitions()

	for step := 0; step < a.MaxSteps; step++ {
		result, err := a.Client.Chat(a.ConversationHistory, tools)
		if err != nil {
			return "", err
		}

		a.ConversationHistory = append(a.ConversationHistory, result.Message)

		fmt.Printf("Token: input=%d output=%d total=%d\n",
			result.Usage.PromptTokens,
			result.Usage.CompletionTokens,
			result.Usage.TotalTokens,
		)

		if len(result.Message.ToolCalls) == 0 {
			return result.Message.Content, nil
		}

		for _, toolCall := range result.Message.ToolCalls {
			fmt.Println("[agent] tool call:", toolCall.Function.Name, toolCall.Function.Arguments)

			var args map[string]interface{}
			if err := json.Unmarshal([]byte(toolCall.Function.Arguments), &args); err != nil {
				toolResult := "ERROR: invalid tool arguments: " + err.Error()
				a.ConversationHistory = append(a.ConversationHistory, llm.ToolMessage(toolCall.ID, toolResult))
				continue
			}

			toolResult, err := a.Tools.Execute(toolCall.Function.Name, args)
			if err != nil {
				toolResult = "ERROR: " + err.Error()
			}

			fmt.Println("[agent] tool result:", toolResult)

			a.ConversationHistory = append(a.ConversationHistory, llm.ToolMessage(toolCall.ID, toolResult))
		}
	}

	return "", fmt.Errorf("max steps reached")
}

func (a *Agent) ClearHistory() {
	a.ConversationHistory = []llm.Message{
		llm.SystemMessage("You are a helpful assistant."),
	}
}
