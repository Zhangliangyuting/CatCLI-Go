package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"AgentCLI/internal/config"
	"AgentCLI/internal/rag"
	"AgentCLI/internal/workspace"
)

type blockedIndexEmbedder struct {
	started chan struct{}
	release chan struct{}
	err     error
}

func (embedder *blockedIndexEmbedder) Embed(context.Context, string) ([]float32, error) {
	return []float32{1, 0}, nil
}

func (embedder *blockedIndexEmbedder) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	select {
	case embedder.started <- struct{}{}:
	default:
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-embedder.release:
	}
	if embedder.err != nil {
		return nil, embedder.err
	}
	vectors := make([][]float32, len(texts))
	for index := range vectors {
		vectors[index] = []float32{1, 0}
	}
	return vectors, nil
}

func TestWorkspaceRAGIndexesWithoutBlockingRequests(t *testing.T) {
	for _, test := range []struct {
		name      string
		oldModel  string
		indexErr  error
		available bool
		oldRules  bool
	}{
		{name: "first index"},
		{name: "compatible index", oldModel: "test-embed", available: true},
		{name: "model changed", oldModel: "old-embed"},
		{name: "chunk rules changed", oldModel: "test-embed", oldRules: true},
		{name: "index failure", indexErr: errors.New("embedding unavailable")},
		{name: "update failure preserves old index", oldModel: "test-embed", available: true, indexErr: errors.New("embedding unavailable")},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			work := newRAGTestWorkspace(t)
			if test.oldModel != "" {
				store, err := rag.NewSQLiteStore(filepath.Join(work.MetadataDir(), "rag.db"))
				if err != nil {
					t.Fatal(err)
				}
				version := rag.NewDefaultChunkerSet(rag.ChunkConfig{}).Version()
				if test.oldRules {
					version = "old-rules"
				}
				err = store.ResetIndex(ctx, rag.IndexMetadata{
					EmbeddingModel: test.oldModel, EmbeddingDimensions: 2,
					ChunkerVersion: version,
				})
				if err == nil {
					err = store.ReplaceFile(ctx, rag.IndexedFile{Path: "evidence.go", ContentHash: "old-hash"}, []rag.EmbeddedChunk{{
						Chunk: rag.Chunk{Path: "evidence.go", Symbol: "WorkspaceAnswer", Content: "old-value"},
						ID:    "old-chunk", ContentHash: "old-chunk-hash", Embedding: []float32{1, 0},
					}})
				}
				store.Close()
				if err != nil {
					t.Fatal(err)
				}
			}
			embedder := &blockedIndexEmbedder{started: make(chan struct{}, 1), release: make(chan struct{}), err: test.indexErr}
			provider, runtime, err := buildWorkspaceRAG(ctx, work, embedder, ragTestConfig())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { runtime.Close() })
			select {
			case <-embedder.started:
			case <-ctx.Done():
				t.Fatal("background indexing did not reach embedding")
			}
			if ctx.Err() != nil {
				t.Fatal("startup waited for embedding instead of returning immediately")
			}
			var progress rag.RetrievalReport
			messages, err := runtime.BuildWithReport(ctx, "WorkspaceAnswer", func(report rag.RetrievalReport) {
				progress = report
			})
			if err != nil {
				t.Fatal(err)
			}
			if test.available {
				if len(progress.Included) == 0 || progress.SkippedReason != "" {
					t.Fatalf("compatible index report = %+v", progress)
				}
				if len(messages) == 0 || !strings.Contains(messages[0].Content, "old-value") {
					t.Fatal("compatible old index must remain searchable during indexing")
				}
			} else if len(messages) != 0 {
				t.Fatal("missing or incompatible index must not provide references")
			} else if progress.SkippedReason != "index_not_ready" {
				t.Fatalf("unavailable index report = %+v", progress)
			}
			close(embedder.release)
			select {
			case <-runtime.done:
			case <-ctx.Done():
				t.Fatal("indexing did not finish")
			}
			messages, err = provider.Build(ctx, "WorkspaceAnswer")
			if err != nil {
				t.Fatal(err)
			}
			if test.indexErr != nil {
				if test.available {
					if len(messages) == 0 || !strings.Contains(messages[0].Content, "old-value") {
						t.Fatal("failed update must preserve the compatible old index")
					}
				} else if len(messages) != 0 {
					t.Fatal("failed initial indexing must leave references unavailable")
				}
			} else if len(messages) == 0 || !strings.Contains(messages[0].Content, "new-value") {
				t.Fatal("completed index must supply updated references")
			}
		})
	}
}

func TestWorkspaceRAGCloseCancelsIndexing(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	embedder := &blockedIndexEmbedder{started: make(chan struct{}, 1), release: make(chan struct{})}
	_, runtime, err := buildWorkspaceRAG(ctx, newRAGTestWorkspace(t), embedder, ragTestConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { runtime.Close() })
	select {
	case <-embedder.started:
	case <-ctx.Done():
		t.Fatal("indexing did not start")
	}
	closed := make(chan error, 1)
	go func() { closed <- runtime.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("shutdown did not cancel and join indexing")
	}
	if _, err := runtime.store.ListFiles(context.Background()); err == nil {
		t.Fatal("database must be closed after indexing stops")
	}
}

func newRAGTestWorkspace(t *testing.T) *workspace.Workspace {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "evidence.go"), []byte("package evidence\nfunc WorkspaceAnswer() string { return \"new-value\" }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	work, err := workspace.New(root)
	if err != nil {
		t.Fatal(err)
	}
	return work
}

func ragTestConfig() config.Config {
	return config.Config{
		Embedding:        config.EmbeddingConfig{Model: "test-embed"},
		OpenAICompatible: config.OpenAICompatibleConfig{ContextWindowTokens: 10000},
		RAG:              config.RAGConfig{Enabled: true, MaxContextTokens: 1000},
	}
}
