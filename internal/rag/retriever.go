package rag

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
	"unicode"

	"AgentCLI/internal/llm"
)

const (
	minCodeSemanticSimilarity = 0.60
	minCodeSemanticFraction   = 0.80
	minCodeLexicalFraction    = 0.35
	reciprocalRankConstant    = 60
)

type SearchResult struct {
	Chunk
	ID            string
	LexicalScore  float64
	SemanticScore float64
	Score         float64
}

// RetrievalTimings measures non-overlapping stages. Embedding includes the
// complete service request; Ranking includes cosine scoring and rank fusion.
type RetrievalTimings struct {
	Load      time.Duration
	Lexical   time.Duration
	Embedding time.Duration
	Ranking   time.Duration
	Format    time.Duration
}

// Retriever combines code-aware BM25 and embedding cosine rankings. When no
// embedder is configured, exact and lexical workspace search remain available.
type Retriever struct {
	store    ChunkReader
	embedder llm.Embedder
}

func NewRetriever(store ChunkReader, embedder llm.Embedder) (*Retriever, error) {
	if store == nil {
		return nil, fmt.Errorf("chunk reader is nil")
	}
	return &Retriever{store: store, embedder: embedder}, nil
}

func (retriever *Retriever) Search(ctx context.Context, query string, limit int) ([]SearchResult, error) {
	results, _, err := retriever.SearchWithTimings(ctx, query, limit)
	return results, err
}

func (retriever *Retriever) SearchWithTimings(ctx context.Context, query string, limit int) ([]SearchResult, RetrievalTimings, error) {
	var timings RetrievalTimings
	results, err := retriever.search(ctx, query, limit, &timings)
	return results, timings, err
}

func (retriever *Retriever) search(ctx context.Context, query string, limit int, timings *RetrievalTimings) ([]SearchResult, error) {
	if retriever == nil || limit <= 0 || strings.TrimSpace(query) == "" {
		return nil, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	started := time.Now()
	chunks, err := retriever.store.LoadChunks(ctx)
	timings.Load = time.Since(started)
	if err != nil {
		return nil, fmt.Errorf("load RAG index: %w", err)
	}
	if len(chunks) == 0 {
		return nil, nil
	}

	started = time.Now()
	queryTerms := queryCodeTerms(query)
	items := buildRetrievalItems(chunks)
	timings.Lexical = time.Since(started)
	if len(items) == 0 {
		return nil, nil
	}
	scoreBM25(queryTerms, items)
	timings.Lexical = time.Since(started)
	if retriever.embedder != nil {
		if err := retriever.scoreSemantic(ctx, query, items, timings); err != nil {
			return nil, err
		}
	}
	started = time.Now()
	fuseRetrievalRanks(items, retriever.embedder != nil)

	sort.SliceStable(items, func(left, right int) bool {
		if items[left].rank != items[right].rank {
			return items[left].rank > items[right].rank
		}
		if items[left].semantic != items[right].semantic {
			return items[left].semantic > items[right].semantic
		}
		if items[left].lexical != items[right].lexical {
			return items[left].lexical > items[right].lexical
		}
		if items[left].chunk.Path != items[right].chunk.Path {
			return items[left].chunk.Path < items[right].chunk.Path
		}
		return items[left].chunk.StartLine < items[right].chunk.StartLine
	})
	results := make([]SearchResult, 0, min(limit, len(items)))
	for _, item := range items {
		if item.rank <= 0 || len(results) == limit {
			break
		}
		results = append(results, SearchResult{
			Chunk:         item.chunk.Chunk,
			ID:            item.chunk.ID,
			LexicalScore:  item.lexical,
			SemanticScore: item.semantic,
			Score:         item.rank,
		})
	}
	timings.Ranking += time.Since(started)
	return results, nil
}

type retrievalItem struct {
	chunk                   EmbeddedChunk
	terms                   []string
	lexical, semantic, rank float64
}

func buildRetrievalItems(chunks []EmbeddedChunk) []*retrievalItem {
	items := make([]*retrievalItem, 0, len(chunks))
	for _, chunk := range chunks {
		if strings.TrimSpace(chunk.ID) == "" || strings.TrimSpace(chunk.Content) == "" {
			continue
		}
		terms := make([]string, 0)
		terms = append(terms, codeTerms(chunk.Path)...)
		for range 3 {
			terms = append(terms, codeTerms(chunk.Symbol)...)
		}
		for range 2 {
			terms = append(terms, codeTerms(chunk.Heading)...)
		}
		terms = append(terms, codeTerms(chunk.Kind)...)
		terms = append(terms, codeTerms(chunk.Language)...)
		terms = append(terms, codeTerms(chunk.Content)...)
		items = append(items, &retrievalItem{chunk: chunk, terms: terms})
	}
	return items
}

func scoreBM25(query []string, items []*retrievalItem) {
	if len(query) == 0 || len(items) == 0 {
		return
	}
	frequency := make(map[string]int)
	averageLength := 0.0
	for _, item := range items {
		averageLength += float64(len(item.terms))
		seen := make(map[string]bool)
		for _, term := range item.terms {
			if !seen[term] {
				frequency[term]++
				seen[term] = true
			}
		}
	}
	averageLength /= float64(len(items))
	if averageLength == 0 {
		averageLength = 1
	}
	for _, item := range items {
		counts := make(map[string]int)
		for _, term := range item.terms {
			counts[term]++
		}
		seen := make(map[string]bool)
		for _, term := range query {
			if seen[term] {
				continue
			}
			seen[term] = true
			termFrequency := float64(counts[term])
			if termFrequency == 0 {
				continue
			}
			inverseFrequency := math.Log(1 + (float64(len(items)-frequency[term])+0.5)/(float64(frequency[term])+0.5))
			item.lexical += inverseFrequency * termFrequency * 2.2 /
				(termFrequency + 1.2*(0.25+0.75*float64(len(item.terms))/averageLength))
		}
	}
}

func (retriever *Retriever) scoreSemantic(ctx context.Context, query string, items []*retrievalItem, timings *RetrievalTimings) error {
	started := time.Now()
	queryVector, err := retriever.embedder.Embed(ctx, query)
	timings.Embedding = time.Since(started)
	if err != nil {
		return fmt.Errorf("embed RAG query: %w", err)
	}
	if len(queryVector) == 0 {
		return fmt.Errorf("RAG query embedding is empty")
	}
	started = time.Now()
	defer func() { timings.Ranking += time.Since(started) }()
	for _, item := range items {
		if len(item.chunk.Embedding) != len(queryVector) {
			return fmt.Errorf(
				"chunk %q embedding dimension is %d, query dimension is %d",
				item.chunk.ID,
				len(item.chunk.Embedding),
				len(queryVector),
			)
		}
		item.semantic = vectorCosine(queryVector, item.chunk.Embedding)
	}
	return nil
}

func fuseRetrievalRanks(items []*retrievalItem, semanticEnabled bool) {
	bestLexical, bestSemantic := 0.0, 0.0
	for _, item := range items {
		bestLexical = math.Max(bestLexical, item.lexical)
		bestSemantic = math.Max(bestSemantic, item.semantic)
	}
	lexicalCutoff := bestLexical * minCodeLexicalFraction
	semanticCutoff := math.Max(minCodeSemanticSimilarity, bestSemantic*minCodeSemanticFraction)

	lexical := append([]*retrievalItem(nil), items...)
	sort.SliceStable(lexical, func(left, right int) bool {
		return lexical[left].lexical > lexical[right].lexical
	})
	for position, item := range lexical {
		if item.lexical > 0 && item.lexical >= lexicalCutoff {
			item.rank += 1 / float64(reciprocalRankConstant+position+1)
		}
	}
	if !semanticEnabled {
		return
	}
	semantic := append([]*retrievalItem(nil), items...)
	sort.SliceStable(semantic, func(left, right int) bool {
		return semantic[left].semantic > semantic[right].semantic
	})
	for position, item := range semantic {
		if item.semantic >= semanticCutoff {
			item.rank += 1 / float64(reciprocalRankConstant+position+1)
		}
	}
}

func vectorCosine(left, right []float32) float64 {
	var dot, leftMagnitude, rightMagnitude float64
	for index := range left {
		leftValue := float64(left[index])
		rightValue := float64(right[index])
		dot += leftValue * rightValue
		leftMagnitude += leftValue * leftValue
		rightMagnitude += rightValue * rightValue
	}
	if leftMagnitude == 0 || rightMagnitude == 0 {
		return 0
	}
	return dot / math.Sqrt(leftMagnitude*rightMagnitude)
}

// codeTerms keeps complete identifiers while also splitting snake_case and
// CamelCase names. CJK text uses overlapping bigrams.
func codeTerms(text string) []string {
	var terms []string
	var identifier []rune
	var cjk []rune
	flushIdentifier := func() {
		if len(identifier) == 0 {
			return
		}
		whole := strings.ToLower(string(identifier))
		if !codeStopWord[whole] {
			terms = append(terms, whole)
		}
		for _, part := range splitCodeIdentifier(identifier) {
			part = strings.ToLower(part)
			if part != whole && !codeStopWord[part] {
				terms = append(terms, part)
			}
		}
		identifier = nil
	}
	flushCJK := func() {
		if len(cjk) == 1 {
			terms = append(terms, string(cjk))
		}
		for index := 0; index+1 < len(cjk); index++ {
			terms = append(terms, string(cjk[index:index+2]))
		}
		cjk = nil
	}
	for _, character := range text {
		if unicode.Is(unicode.Han, character) {
			flushIdentifier()
			cjk = append(cjk, character)
			continue
		}
		flushCJK()
		if unicode.IsLetter(character) || unicode.IsDigit(character) || character == '_' {
			identifier = append(identifier, character)
		} else {
			flushIdentifier()
		}
	}
	flushCJK()
	flushIdentifier()
	return terms
}

func splitCodeIdentifier(identifier []rune) []string {
	var parts []string
	start := 0
	appendPart := func(end int) {
		if end > start {
			part := strings.Trim(string(identifier[start:end]), "_")
			if part != "" {
				parts = append(parts, part)
			}
		}
	}
	for index := 1; index < len(identifier); index++ {
		previous, current := identifier[index-1], identifier[index]
		if current == '_' {
			appendPart(index)
			start = index + 1
			continue
		}
		if previous == '_' {
			continue
		}
		camelBoundary := unicode.IsLower(previous) && unicode.IsUpper(current)
		acronymBoundary := unicode.IsUpper(previous) && unicode.IsUpper(current) &&
			index+1 < len(identifier) && unicode.IsLower(identifier[index+1])
		digitBoundary := unicode.IsDigit(previous) != unicode.IsDigit(current)
		if camelBoundary || acronymBoundary || digitBoundary {
			appendPart(index)
			start = index
		}
	}
	appendPart(len(identifier))
	return parts
}

var codeStopWord = map[string]bool{
	"a": true, "an": true, "and": true, "are": true, "do": true,
	"does": true, "for": true, "in": true, "is": true, "of": true,
	"on": true, "or": true, "the": true, "to": true, "what": true,
	"where": true, "which": true,
}
