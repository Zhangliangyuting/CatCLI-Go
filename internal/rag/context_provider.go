package rag

import (
	"AgentCLI/internal/llm"
	"context"
	"fmt"
	"strings"
	"time"
)

const DefaultSearchLimit = 20

// RetrievalReport describes one search and the results admitted to its budget.
type RetrievalReport struct {
	Matches       []SearchResult
	Included      []SearchResult
	Duration      time.Duration
	SkippedReason string
	Error         string
}

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
	return provider.BuildWithReport(ctx, query, nil)
}

func (provider *ContextProvider) BuildWithReport(
	ctx context.Context,
	query string,
	onReport func(RetrievalReport),
) ([]llm.Message, error) {
	started := time.Now()
	report := RetrievalReport{}
	defer func() {
		report.Duration = time.Since(started)
		if onReport != nil {
			onReport(report)
		}
	}()
	if provider == nil || strings.TrimSpace(query) == "" {
		report.SkippedReason = "empty_provider_or_query"
		return nil, nil
	}
	results, err := provider.retriever.Search(ctx, query, provider.limit)
	if err != nil {
		report.Error = err.Error()
		return nil, fmt.Errorf("retrieve workspace context: %w", err)
	}
	formatted := provider.formatter.Format(results, provider.maxTokens)
	report.Matches = results
	report.Included = formatted.Included
	if formatted.Text == "" {
		return nil, nil
	}
	return []llm.Message{llm.SystemMessage(formatted.Text)}, nil
}
