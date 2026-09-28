package memory

import (
	"context"
	"fmt"
	"reflect"
	"testing"
)

type fixtureEmbedder struct{ calls map[string]int }

func (e *fixtureEmbedder) Embed(_ context.Context, text string) ([]float32, error) {
	e.calls[text]++
	switch text {
	case "semantic question", "meaningful answer":
		return []float32{1, 0}, nil
	default:
		return []float32{0, 1}, nil
	}
}

func TestMemoryRetrieverCombinesLexicalAndSemanticResults(t *testing.T) {
	embedder := &fixtureEmbedder{calls: map[string]int{}}
	retriever := NewMemoryRetriever(embedder)
	docs := []MemoryDocument{
		{ID: "lexical", Text: "semantic question", Kind: Fact},
		{ID: "semantic", Text: "meaningful answer", Kind: Fact},
		{ID: "other", Text: "unrelated", Kind: Fact},
	}
	got, err := retriever.Search(context.Background(), "semantic question", docs, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) < 2 || got[0].ID != "lexical" || got[1].ID != "semantic" {
		t.Fatalf("results = %#v", got)
	}
	_, err = retriever.Search(context.Background(), "semantic question", docs, 3)
	if err != nil {
		t.Fatal(err)
	}
	if embedder.calls["meaningful answer"] != 1 {
		t.Fatalf("document was re-embedded: %v", embedder.calls)
	}
}

func TestMemoryRetrieverChineseBigrams(t *testing.T) {
	retriever := NewMemoryRetriever(nil)
	docs := []MemoryDocument{{ID: "match", Text: "这个项目使用记忆检索", Kind: Fact}, {ID: "other", Text: "完全无关的内容", Kind: Fact}}
	got, err := retriever.Search(context.Background(), "记忆检索", docs, 1)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, docs[:1]) {
		t.Fatalf("results = %#v", got)
	}
}

// Weak positive cosine scores and incidental BM25 overlap must not fill the
// result limit when only a few memories actually match the question.
type scoredEmbedder struct{ vectors map[string][]float32 }

func (e scoredEmbedder) Embed(_ context.Context, text string) ([]float32, error) {
	return e.vectors[text], nil
}

func TestMemoryRetrieverDoesNotFillLimitWithWeakMatches(t *testing.T) {
	query := "本次测试号码是多少"
	docs := []MemoryDocument{
		{ID: "exact", Text: "本次测试号码是7319", Kind: Fact},
		{ID: "paraphrase", Text: "用户询问当前编号，编号为7319", Kind: Fact},
	}
	vectors := map[string][]float32{
		query:        {1, 0},
		docs[0].Text: {1, 0},
		docs[1].Text: {0.85, 0.527},
	}
	for i := 0; i < 18; i++ {
		doc := MemoryDocument{ID: fmt.Sprintf("weak-%d", i), Text: fmt.Sprintf("测试报告与天气话题 %d", i), Kind: Conversation}
		docs = append(docs, doc)
		vectors[doc.Text] = []float32{0.55, 0.835}
	}

	got, err := NewMemoryRetriever(scoredEmbedder{vectors: vectors}).Search(context.Background(), query, docs, 12)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, docs[:2]) {
		t.Fatalf("results = %#v, want only the two relevant memories", got)
	}

	lexicalOnly, err := NewMemoryRetriever(nil).Search(context.Background(), query, docs, 12)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(lexicalOnly, docs[:1]) {
		t.Fatalf("BM25 results = %#v, want only the strong lexical match", lexicalOnly)
	}
}

func TestMemoryRetrieverCanReturnNoResults(t *testing.T) {
	query := "unrelated question"
	doc := MemoryDocument{ID: "other", Text: "weather forecast", Kind: Fact}
	vectors := map[string][]float32{query: {1, 0}, doc.Text: {0.55, 0.835}}
	got, err := NewMemoryRetriever(scoredEmbedder{vectors: vectors}).Search(context.Background(), query, []MemoryDocument{doc}, 12)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("results = %#v, want none", got)
	}
}

func TestMemoryRetrieverVectorCacheUsesLRUOrder(t *testing.T) {
	embedder := &fixtureEmbedder{calls: map[string]int{}}
	retriever := NewMemoryRetrieverWithCacheCapacity(embedder, 2)
	ctx := context.Background()
	first := MemoryDocument{ID: "first", Text: "first text"}
	second := MemoryDocument{ID: "second", Text: "second text"}
	third := MemoryDocument{ID: "third", Text: "third text"}
	for _, doc := range []MemoryDocument{first, second, first, third, first, second} {
		if _, err := retriever.vector(ctx, doc); err != nil {
			t.Fatal(err)
		}
	}
	if embedder.calls[first.Text] != 1 || embedder.calls[second.Text] != 2 || embedder.calls[third.Text] != 1 {
		t.Fatalf("unexpected embed calls: %v", embedder.calls)
	}
	if got := retriever.cache.order.Len(); got != 2 {
		t.Fatalf("cache size = %d, want 2", got)
	}
}

func TestMemoryRetrieverQueryAndDocumentVectorsShareLimit(t *testing.T) {
	embedder := &fixtureEmbedder{calls: map[string]int{}}
	retriever := NewMemoryRetrieverWithCacheCapacity(embedder, 2)
	ctx := context.Background()
	if _, err := retriever.queryVector(ctx, "query one"); err != nil {
		t.Fatal(err)
	}
	if _, err := retriever.vector(ctx, MemoryDocument{ID: "doc", Text: "document text"}); err != nil {
		t.Fatal(err)
	}
	if _, err := retriever.queryVector(ctx, "query two"); err != nil {
		t.Fatal(err)
	}
	if _, err := retriever.queryVector(ctx, "query one"); err != nil {
		t.Fatal(err)
	}
	if embedder.calls["query one"] != 2 || retriever.cache.order.Len() != 2 {
		t.Fatalf("shared cache limit failed: calls=%v size=%d", embedder.calls, retriever.cache.order.Len())
	}
}

func TestMemoryRetrieverReembedsChangedDocumentText(t *testing.T) {
	embedder := &fixtureEmbedder{calls: map[string]int{}}
	retriever := NewMemoryRetrieverWithCacheCapacity(embedder, 2)
	ctx := context.Background()
	for _, text := range []string{"old text", "new text", "new text"} {
		if _, err := retriever.vector(ctx, MemoryDocument{ID: "same-id", Text: text}); err != nil {
			t.Fatal(err)
		}
	}
	if embedder.calls["old text"] != 1 || embedder.calls["new text"] != 1 || retriever.cache.order.Len() != 1 {
		t.Fatalf("stale document cache: calls=%v size=%d", embedder.calls, retriever.cache.order.Len())
	}
}

func TestMemoryRetrieverZeroCacheCapacity(t *testing.T) {
	embedder := &fixtureEmbedder{calls: map[string]int{}}
	retriever := NewMemoryRetrieverWithCacheCapacity(embedder, 0)
	for i := 0; i < 2; i++ {
		if _, err := retriever.queryVector(context.Background(), "uncached query"); err != nil {
			t.Fatal(err)
		}
	}
	if embedder.calls["uncached query"] != 2 || retriever.cache.order.Len() != 0 {
		t.Fatalf("disabled cache retained vectors: calls=%v", embedder.calls)
	}
}
