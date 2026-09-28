package llm

import (
	"AgentCLI/internal/tool"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type OpenAICompatibleClient struct {
	APIKey     string
	BaseURL    string
	Model      string
	HTTPClient *http.Client
}

func NewOpenAICompatibleClient(apiKey string, baseUrl string, model string) (*OpenAICompatibleClient, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("api key is empty")
	}

	if baseUrl == "" {
		return nil, fmt.Errorf("base URL is empty")
	}

	if model == "" {
		return nil, fmt.Errorf("model is empty")
	}

	return &OpenAICompatibleClient{
		APIKey:     apiKey,
		BaseURL:    baseUrl,
		Model:      model,
		HTTPClient: http.DefaultClient,
	}, nil
}

type ChatRequest struct {
	Model           string            `json:"model"`
	Messages        []Message         `json:"messages"`
	ToolDefinitions []tool.Definition `json:"tools,omitempty"`
	MaxTokens       int               `json:"max_tokens,omitempty"`
	ResponseFormat  *ResponseFormat   `json:"response_format,omitempty"`
	Thinking        *ThinkingConfig   `json:"thinking,omitempty"`
}

type ResponseFormat struct {
	Type string `json:"type"`
}

type ThinkingConfig struct {
	Type string `json:"type"`
}

type ChatOptions struct {
	MaxTokens      int
	ResponseFormat *ResponseFormat
	Thinking       *ThinkingConfig
}

// DisableThinkingIfSupported adds a provider-specific request parameter only
// for models whose non-thinking option this client knows how to express.
func (c *OpenAICompatibleClient) DisableThinkingIfSupported(options *ChatOptions) {
	if c == nil || options == nil {
		return
	}
	switch model := strings.ToLower(strings.TrimSpace(c.Model)); {
	case strings.HasPrefix(model, "deepseek-"):
		options.Thinking = &ThinkingConfig{Type: "disabled"}
	}
}

type ChatResponse struct {
	Choices []Choice `json:"choices"`
	Usage   Usage    `json:"usage"`
}

type Choice struct {
	Message      Message `json:"message"`
	FinishReason string  `json:"finish_reason"`
}

type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type ChatResult struct {
	Message      Message
	Usage        Usage
	FinishReason string
}

func (c *OpenAICompatibleClient) Chat(
	ctx context.Context,
	messages []Message,
	toolDefinitions []tool.Definition,
) (ChatResult, error) {
	return c.ChatWithOptions(ctx, messages, toolDefinitions, ChatOptions{})
}

func (c *OpenAICompatibleClient) ChatWithOptions(
	ctx context.Context,
	messages []Message,
	toolDefinitions []tool.Definition,
	options ChatOptions,
) (ChatResult, error) {
	if c.APIKey == "" {
		return ChatResult{}, fmt.Errorf("api key is empty")
	}
	chatRequest := ChatRequest{
		Model:           c.Model,
		Messages:        messages,
		ToolDefinitions: toolDefinitions,
		MaxTokens:       options.MaxTokens,
		ResponseFormat:  options.ResponseFormat,
		Thinking:        options.Thinking,
	}

	requestBody, err := json.Marshal(chatRequest)
	if err != nil {
		return ChatResult{}, fmt.Errorf("failed to marshal request body: %w", err)
	}

	url := strings.TrimRight(c.BaseURL, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		url,
		bytes.NewReader(requestBody),
	)
	if err != nil {
		return ChatResult{}, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.APIKey)

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return ChatResult{}, fmt.Errorf("failed to send request: %w", err)
	}
	defer resp.Body.Close()

	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return ChatResult{}, fmt.Errorf("failed to read response body: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ChatResult{}, fmt.Errorf("request failed with status code %d: %s", resp.StatusCode, string(responseBody))
	}

	var chatResponse ChatResponse
	err = json.Unmarshal(responseBody, &chatResponse)
	if err != nil {
		return ChatResult{}, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	if len(chatResponse.Choices) == 0 {
		return ChatResult{}, fmt.Errorf("response has no choices")
	}

	return ChatResult{
		Message:      chatResponse.Choices[0].Message,
		Usage:        chatResponse.Usage,
		FinishReason: chatResponse.Choices[0].FinishReason,
	}, nil

}
