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
	vectors, err := c.embed(ctx, text, 1)
	if err != nil {
		return nil, err
	}
	return vectors[0], nil
}

// EmbedBatch converts several texts in one embeddings request. Returned
// vectors preserve the order of texts, even if the endpoint returns data in a
// different order.
func (c *OpenAIEmbeddingClient) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return [][]float32{}, nil
	}
	return c.embed(ctx, texts, len(texts))
}

func (c *OpenAIEmbeddingClient) embed(ctx context.Context, input any, expected int) ([][]float32, error) {
	if c == nil || c.APIKey == "" || c.BaseURL == "" || c.Model == "" {
		return nil, fmt.Errorf("embedding client is not configured")
	}
	body, err := json.Marshal(struct {
		Model string `json:"model"`
		Input any    `json:"input"`
	}{c.Model, input})
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
			Index     int       `json:"index"`
		} `json:"data"`
	}
	if err := json.Unmarshal(payload, &result); err != nil {
		return nil, fmt.Errorf("decode embedding response: %w", err)
	}
	if len(result.Data) != expected {
		return nil, fmt.Errorf("embedding response returned %d vectors, want %d", len(result.Data), expected)
	}
	vectors := make([][]float32, expected)
	for _, item := range result.Data {
		if item.Index < 0 || item.Index >= expected {
			return nil, fmt.Errorf("embedding response index %d is out of range", item.Index)
		}
		if vectors[item.Index] != nil {
			return nil, fmt.Errorf("embedding response contains duplicate index %d", item.Index)
		}
		if len(item.Embedding) == 0 {
			return nil, fmt.Errorf("embedding response vector %d is empty", item.Index)
		}
		vectors[item.Index] = item.Embedding
	}
	return vectors, nil
}
