package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// OpenAIEmbeddingClient calls an OpenAI-compatible embeddings endpoint.
type OpenAIEmbeddingClient struct {
	APIKey     string
	BaseURL    string
	Model      string
	HTTPClient *http.Client
}

func (c *OpenAIEmbeddingClient) Embed(ctx context.Context, text string) ([]float32, error) {
	if c == nil || c.APIKey == "" || c.BaseURL == "" || c.Model == "" {
		return nil, fmt.Errorf("embedding client is not configured")
	}
	body, err := json.Marshal(struct {
		Model string `json:"model"`
		Input string `json:"input"`
	}{c.Model, text})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.BaseURL, "/")+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	client := c.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("embedding request failed (%d): %s", resp.StatusCode, string(payload))
	}
	var result struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.Unmarshal(payload, &result); err != nil {
		return nil, fmt.Errorf("decode embedding response: %w", err)
	}
	if len(result.Data) != 1 || len(result.Data[0].Embedding) == 0 {
		return nil, fmt.Errorf("embedding response has no vector")
	}
	return result.Data[0].Embedding, nil
}
