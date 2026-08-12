package llm

type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function FunctionCall `json:"function"`
}

type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

func SystemMessage(content string) Message {
	return Message{
		Role:    "system",
		Content: content,
	}
}

func UserMessage(content string) Message {
	return Message{
		Role:    "user",
		Content: content,
	}
}

func AssistantMessage(content string) Message {
	return Message{
		Role:    "assistant",
		Content: content,
	}
}

func ToolMessage(toolCallID string, content string) Message {
	if content == "" {
		content = "(empty result)"
	}

	return Message{
		Role:       "tool",
		Content:    content,
		ToolCallID: toolCallID,
	}
}
