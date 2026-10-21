package rag

import (
	"AgentCLI/internal/llm"
	"context"
	"fmt"
	"strings"
)

const DefaultSearchLimit = 20

// ContextProvider retrieves and formats workspace excerpts for one model
// request. It satisfies agent.ReferenceContextProvider without depending on
// the agent package.
type ContextProvider struct {
	retriever *Retriever
	formatter *SearchResultFormatter
	maxTokens int
	limit     int
}

func NewContextProvider(
	retriever *Retriever,
	formatter *SearchResultFormatter,
	maxTokens int,
	limit int,
) (*ContextProvider, error) {
	if retriever == nil {
		return nil, fmt.Errorf("RAG retriever is nil")
	}
	if formatter == nil {
		return nil, fmt.Errorf("RAG result formatter is nil")
	}
	if maxTokens <= 0 {
		return nil, fmt.Errorf("RAG context token budget must be positive")
	}
	if limit <= 0 {
		limit = DefaultSearchLimit
	}
	return &ContextProvider{
		retriever: retriever,
		formatter: formatter,
		maxTokens: maxTokens,
		limit:     limit,
	}, nil
}

func (provider *ContextProvider) Build(ctx context.Context, query string) ([]llm.Message, error) {
	if provider == nil || strings.TrimSpace(query) == "" {
		return nil, nil
	}
	results, err := provider.retriever.Search(ctx, query, provider.limit)
	if err != nil {
		return nil, fmt.Errorf("retrieve workspace context: %w", err)
	}
	formatted := provider.formatter.Format(results, provider.maxTokens)
	if formatted.Text == "" {
		return nil, nil
	}
	return []llm.Message{llm.SystemMessage(formatted.Text)}, nil
}
