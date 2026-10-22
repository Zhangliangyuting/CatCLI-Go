package agent

import (
	"AgentCLI/internal/llm"
	"AgentCLI/internal/rag"
	"context"
)

type Agent interface {
	Run(ctx context.Context, userInput string) (string, error)
}

// ReferenceContextProvider supplies request-scoped, read-only context from an
// external source such as a workspace RAG index.
type ReferenceContextProvider interface {
	Build(ctx context.Context, query string) ([]llm.Message, error)
}

// ReportingReferenceContextProvider optionally exposes workspace retrieval details.
type ReportingReferenceContextProvider interface {
	ReferenceContextProvider
	BuildWithReport(ctx context.Context, query string, onReport func(rag.RetrievalReport)) ([]llm.Message, error)
}
