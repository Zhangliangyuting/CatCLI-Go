package llm

import "context"

// Embedder converts text into a vector representation. Callers must use the
// same embedding model for indexed documents and search queries.
type Embedder interface {
	Embed(ctx context.Context, text string) ([]float32, error)
	EmbedBatch(ctx context.Context, texts []string) ([][]float32, error)
}
