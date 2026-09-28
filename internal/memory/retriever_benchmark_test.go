package memory

import (
	"AgentCLI/internal/llm"
	"context"
	"fmt"
	"hash/fnv"
	"os"
	"testing"
)

type benchmarkEmbedder struct{}

func (benchmarkEmbedder) Embed(_ context.Context, text string) ([]float32, error) {
	hash := fnv.New64a()
	_, _ = hash.Write([]byte(text))
	seed := hash.Sum64()
	vector := make([]float32, 1024)
	for i := range vector {
		seed = seed*6364136223846793005 + 1
		vector[i] = float32(seed%1000) / 1000
	}
	return vector, nil
}

func BenchmarkMemoryRetrieverSearch(b *testing.B) {
	for _, count := range []int{10, 100, 1000} {
		docs := make([]MemoryDocument, count)
		for i := range docs {
			docs[i] = MemoryDocument{
				ID:   fmt.Sprintf("fact-%d", i),
				Text: fmt.Sprintf("项目记忆检索使用 Go 和 bge-m3，归档对话编号 %d", i),
				Kind: Fact,
			}
		}
		for _, mode := range []string{"BM25", "HybridCached"} {
			b.Run(fmt.Sprintf("%s/%d", mode, count), func(b *testing.B) {
				var embedder EmbeddingProvider
				if mode == "HybridCached" {
					embedder = benchmarkEmbedder{}
				}
				retriever := NewMemoryRetriever(embedder)
				ctx := context.Background()
				if _, err := retriever.Search(ctx, "项目记忆检索 bge-m3", docs, 12); err != nil {
					b.Fatal(err)
				}
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					if _, err := retriever.Search(ctx, "项目记忆检索 bge-m3", docs, 12); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

// Run with CATCLI_BENCH_EMBEDDING_URL=http://localhost:11435/v1 and
// -benchtime=3x to include first-time vectorization of each document.
func BenchmarkMemoryRetrieverFirstSearchLive(b *testing.B) {
	baseURL := os.Getenv("CATCLI_BENCH_EMBEDDING_URL")
	if baseURL == "" {
		b.Skip("set CATCLI_BENCH_EMBEDDING_URL to run the live benchmark")
	}
	client := &llm.OpenAIEmbeddingClient{APIKey: "ollama", BaseURL: baseURL, Model: "bge-m3"}
	docs := make([]MemoryDocument, 10)
	for i := range docs {
		docs[i] = MemoryDocument{ID: fmt.Sprintf("fact-%d", i), Text: fmt.Sprintf("项目事实 %d：使用 BM25 与 BGE-M3 检索记忆", i), Kind: Fact}
	}
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		retriever := NewMemoryRetriever(client)
		if _, err := retriever.Search(ctx, "项目记忆检索", docs, 10); err != nil {
			b.Fatal(err)
		}
	}
}
