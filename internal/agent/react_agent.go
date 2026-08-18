package agent

import (
	"AgentCLI/internal/llm"
	"AgentCLI/internal/tool"
	"encoding/json"
	"fmt"
)

type ReActAgent struct {
	llmClient           *llm.OpenAICompatibleClient
	tools               *tool.ToolRegistry
	conversationHistory []llm.Message
}

func NewReActAgent(client *llm.OpenAICompatibleClient, tools *tool.ToolRegistry) *ReActAgent {
	return &ReActAgent{
		llmClient: client,
		tools:     tools,
		conversationHistory: []llm.Message{
			llm.SystemMessage("You are a helpful assistant."),
		},
	}
}

func (a *ReActAgent) Run(userInput string) (string, error) {
	a.conversationHistory = append(a.conversationHistory, llm.UserMessage(userInput))

	tools := a.tools.ToolDefinitions()

	for {
		result, err := a.llmClient.Chat(a.conversationHistory, tools)
		if err != nil {
			return "", err
		}

		a.conversationHistory = append(a.conversationHistory, result.Message)

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
				a.conversationHistory = append(a.conversationHistory, llm.ToolMessage(toolCall.ID, toolResult))
				continue
			}

			toolResult, err := a.tools.Execute(toolCall.Function.Name, args)
			if err != nil {
				toolResult = "ERROR: " + err.Error()
			}

			fmt.Println("[agent] tool result:", toolResult)

			a.conversationHistory = append(a.conversationHistory, llm.ToolMessage(toolCall.ID, toolResult))
		}
	}

}

func (a *ReActAgent) ClearHistory() {
	a.conversationHistory = []llm.Message{
		llm.SystemMessage("You are a helpful assistant."),
	}
}
