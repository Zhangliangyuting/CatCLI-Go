package llm

import (
	"AgentCLI/internal/tool"
	"bytes"
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
}

type ChatResponse struct {
	Choices []Choice `json:"choices"`
	Usage   Usage    `json:"usage"`
}

type Choice struct {
	Message Message `json:"message"`
}

type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type ChatResult struct {
	Message Message
	Usage   Usage
}

func (c *OpenAICompatibleClient) Chat(messages []Message, toolDefinitions []tool.Definition) (ChatResult, error) {
	if c.APIKey == "" {
		return ChatResult{}, fmt.Errorf("api key is empty")
	}

	chatRequest := ChatRequest{
		Model:           c.Model,
		Messages:        messages,
		ToolDefinitions: toolDefinitions,
	}

	requestBody, err := json.Marshal(chatRequest)
	if err != nil {
		return ChatResult{}, fmt.Errorf("failed to marshal request body: %w", err)
	}

	url := strings.TrimRight(c.BaseURL, "/") + "/chat/completions"
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(requestBody))
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
		Message: chatResponse.Choices[0].Message,
		Usage:   chatResponse.Usage,
	}, nil

}
