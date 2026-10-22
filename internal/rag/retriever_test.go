package rag

import (
	"AgentCLI/internal/llm"
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"testing"
	"time"
)

type staticChunkReader struct {
	chunks []EmbeddedChunk
	err    error
}

func (reader staticChunkReader) LoadChunks(context.Context) ([]EmbeddedChunk, error) {
	return append([]EmbeddedChunk(nil), reader.chunks...), reader.err
}

type queryEmbedder struct {
	vector []float32
	err    error
	query  *string
	delay  time.Duration
}

func (embedder queryEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	if embedder.delay > 0 {
		timer := time.NewTimer(embedder.delay)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if embedder.query != nil {
		*embedder.query = text
	}
	return embedder.vector, embedder.err
}

func (embedder queryEmbedder) EmbedBatch(context.Context, []string) ([][]float32, error) {
	return nil, errors.New("EmbedBatch should not be called during retrieval")
}

func TestRetrieverUsesCodeAwareLexicalSearchWithoutEmbedder(t *testing.T) {
	reader := staticChunkReader{chunks: []EmbeddedChunk{
		retrievalChunk("manager", "internal/memory/manager.go", "Manager.AddMessage", "method", "func (manager *Manager) AddMessage() {}", []float32{1, 0}),
		retrievalChunk("router", "internal/routing/router.go", "Router.Route", "method", "func (router *Router) Route() {}", []float32{0, 1}),
	}}
	retriever, err := NewRetriever(reader, nil)
	if err != nil {
		t.Fatal(err)
	}
	results, err := retriever.Search(context.Background(), "where is manager add message", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Symbol != "Manager.AddMessage" || results[0].LexicalScore <= 0 {
		t.Fatalf("Search() = %#v", results)
	}
}

func TestRetrieverSearchesMarkdownHeadingsAndPaths(t *testing.T) {
	reader := staticChunkReader{chunks: []EmbeddedChunk{
		{
			Chunk: Chunk{
				Path: "docs/deployment.md", Language: "markdown", Kind: "section",
				Heading: "Operations > Production Deployment", Content: "Configure the service.",
			},
			ID: "deployment", ContentHash: "deployment-hash", Embedding: []float32{1, 0},
		},
		{
			Chunk: Chunk{
				Path: "docs/testing.md", Language: "markdown", Kind: "section",
				Heading: "Testing", Content: "Run unit tests.",
			},
			ID: "testing", ContentHash: "testing-hash", Embedding: []float32{0, 1},
		},
	}}
	retriever, err := NewRetriever(reader, nil)
	if err != nil {
		t.Fatal(err)
	}
	results, err := retriever.Search(context.Background(), "production deployment", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].ID != "deployment" {
		t.Fatalf("Search() = %#v", results)
	}
}

func TestRetrieverFusesLexicalAndSemanticRanks(t *testing.T) {
	reader := staticChunkReader{chunks: []EmbeddedChunk{
		retrievalChunk("hybrid", "manager.go", "Manager.AddMessage", "method", "append one conversation message", []float32{0.9, float32(math.Sqrt(0.19))}),
		retrievalChunk("semantic", "runtime.go", "Runtime.Save", "method", "persist current state", []float32{1, 0}),
		retrievalChunk("weak", "unrelated.go", "Unrelated", "function", "unrelated implementation", []float32{0.5, float32(math.Sqrt(0.75))}),
	}}
	retriever, err := NewRetriever(reader, queryEmbedder{vector: []float32{1, 0}})
	if err != nil {
		t.Fatal(err)
	}
	results, err := retriever.Search(context.Background(), "Manager.AddMessage", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("result count = %d, want 2: %#v", len(results), results)
	}
	if results[0].ID != "hybrid" || results[0].LexicalScore <= 0 || results[0].SemanticScore < 0.89 {
		t.Fatalf("first result = %#v", results[0])
	}
	if results[1].ID != "semantic" || results[1].SemanticScore < 0.99 {
		t.Fatalf("second result = %#v", results[1])
	}
	for _, result := range results {
		if result.ID == "weak" {
			t.Fatalf("weak semantic result was not filtered: %#v", results)
		}
	}
}

func TestRetrieverHonorsLimit(t *testing.T) {
	reader := staticChunkReader{chunks: []EmbeddedChunk{
		retrievalChunk("one", "one.go", "One", "function", "shared search term", []float32{1, 0}),
		retrievalChunk("two", "two.go", "Two", "function", "shared search term", []float32{1, 0}),
	}}
	retriever, err := NewRetriever(reader, nil)
	if err != nil {
		t.Fatal(err)
	}
	results, err := retriever.Search(context.Background(), "shared search term", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("result count = %d, want 1", len(results))
	}
}

func TestRetrieverRejectsEmbeddingDimensionMismatch(t *testing.T) {
	reader := staticChunkReader{chunks: []EmbeddedChunk{
		retrievalChunk("bad", "bad.go", "Bad", "function", "bad vector", []float32{1, 0, 0}),
	}}
	retriever, err := NewRetriever(reader, queryEmbedder{vector: []float32{1, 0}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = retriever.Search(context.Background(), "query", 10)
	if err == nil || !strings.Contains(err.Error(), "dimension") {
		t.Fatalf("Search() error = %v", err)
	}
}

func TestRetrieverPropagatesStoreAndEmbeddingErrors(t *testing.T) {
	storeErr := errors.New("store unavailable")
	retriever, err := NewRetriever(staticChunkReader{err: storeErr}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := retriever.Search(context.Background(), "query", 10); !errors.Is(err, storeErr) {
		t.Fatalf("store error = %v", err)
	}

	embedErr := errors.New("embedding unavailable")
	retriever, err = NewRetriever(staticChunkReader{chunks: []EmbeddedChunk{
		retrievalChunk("one", "one.go", "One", "function", "content", []float32{1, 0}),
	}}, queryEmbedder{err: embedErr})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := retriever.Search(context.Background(), "query", 10); !errors.Is(err, embedErr) {
		t.Fatalf("embedding error = %v", err)
	}
}

func TestCodeTermsSplitIdentifiersAndChineseBigrams(t *testing.T) {
	terms := codeTerms("HTTPClient Manager.AddMessage content_hash 上下文压缩")
	for _, want := range []string{
		"httpclient", "http", "client", "manager", "addmessage", "add", "message",
		"content_hash", "content", "hash", "上下", "下文", "文压", "压缩",
	} {
		if !slices.Contains(terms, want) {
			t.Fatalf("codeTerms() = %v, missing %q", terms, want)
		}
	}
}

func TestQueryCodeTermsRemoveChineseBoilerplate(t *testing.T) {
	// Keep the full topic out of indexed test source so it is not evidence for itself.
	const topic = "aes" + "pa"
	for _, test := range []struct {
		query string
		want  []string
	}{
		{"你知道什么是 " + topic + " 吗", []string{topic}},
		{"你知道什么是" + topic + "吗", []string{topic}},
		{"请问一下，我想知道 " + topic + " 是什么？", []string{topic}},
		{"能不能帮我解释一下 Workspace.Resolve", []string{"workspace", "resolve"}},
		{"请问文件如何读取", []string{"文件", "读取"}},
		{"为什么有序集合不能读取", []string{"有序", "序集", "集合", "合不", "不能", "能读", "读取"}},
		{"请问什么是", nil},
	} {
		t.Run(test.query, func(t *testing.T) {
			if got := queryCodeTerms(test.query); !slices.Equal(got, test.want) {
				t.Fatalf("queryCodeTerms() = %v, want %v", got, test.want)
			}
		})
	}
	query := "请帮我看一下 internal/rag/retriever.go 的 SearchResult 和 content_hash"
	for _, term := range []string{"internal", "rag", "retriever", "go", "searchresult", "search", "result", "content_hash", "content", "hash"} {
		if !slices.Contains(queryCodeTerms(query), term) {
			t.Fatalf("code term %q was lost", term)
		}
	}
}

func TestRetrieverIgnoresQuestionBoilerplateButKeepsOriginalEmbeddingQuery(t *testing.T) {
	const topic = "aes" + "pa"
	reader := staticChunkReader{chunks: []EmbeddedChunk{
		retrievalChunk("guide", "guide.md", "", "section", "你知道什么是 Embedding 吗？我想知道如何构建索引。", []float32{0, 1}),
		retrievalChunk("code", "workspace.go", "Workspace.Resolve", "method", "func (w *Workspace) Resolve() {}", []float32{0, 1}),
	}}
	query := "你知道什么是 " + topic + " 吗"
	for _, semantic := range []bool{false, true} {
		t.Run(fmt.Sprint("semantic=", semantic), func(t *testing.T) {
			var embeddedQuery string
			var embedder llm.Embedder
			if semantic {
				embedder = queryEmbedder{vector: []float32{1, 0}, query: &embeddedQuery}
			}
			retriever, err := NewRetriever(reader, embedder)
			if err != nil {
				t.Fatal(err)
			}
			results, err := retriever.Search(context.Background(), query, 10)
			if err != nil {
				t.Fatal(err)
			}
			if len(results) != 0 {
				t.Fatalf("unrelated question retrieved chunks: %+v", results)
			}
			if semantic && embeddedQuery != query {
				t.Fatalf("embedding received %q, want original %q", embeddedQuery, query)
			}
			results, err = retriever.Search(context.Background(), "能不能帮我解释一下 Workspace.Resolve", 10)
			if err != nil {
				t.Fatal(err)
			}
			if len(results) != 1 || results[0].ID != "code" {
				t.Fatalf("symbol search = %+v", results)
			}
		})
	}
	// Cleaning the query must not discard matching words from indexed content.
	reader.chunks = append(reader.chunks, retrievalChunk("topic", "notes.md", "", "section", topic, []float32{0, 1}))
	retriever, err := NewRetriever(reader, nil)
	if err != nil {
		t.Fatal(err)
	}
	results, err := retriever.Search(context.Background(), query, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].ID != "topic" {
		t.Fatalf("actual topic match = %+v", results)
	}
}

func TestRetrieverEmptyInputsDoNotLoadStore(t *testing.T) {
	retriever, err := NewRetriever(staticChunkReader{err: errors.New("must not load")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range []struct {
		query string
		limit int
	}{{"", 10}, {"query", 0}} {
		results, err := retriever.Search(context.Background(), input.query, input.limit)
		if err != nil || results != nil {
			t.Fatalf("Search(%q, %d) = %#v, %v", input.query, input.limit, results, err)
		}
	}
}

func retrievalChunk(id, path, symbol, kind, content string, embedding []float32) EmbeddedChunk {
	return EmbeddedChunk{
		Chunk: Chunk{
			Path:      path,
			Language:  "go",
			Kind:      kind,
			Symbol:    symbol,
			StartLine: 1,
			EndLine:   2,
			Content:   content,
		},
		ID:          id,
		ContentHash: id + "-hash",
		Embedding:   embedding,
	}
}
