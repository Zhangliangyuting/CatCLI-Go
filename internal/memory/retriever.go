package memory

import (
	"container/list"
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"unicode"
)

const (
	// These defaults are calibrated for the local BGE-M3 setup. Relative
	// cutoffs also keep weak matches out when many documents score above zero.
	minSemanticSimilarity = 0.60
	minSemanticFraction   = 0.80
	minLexicalFraction    = 0.35
)

// EmbeddingProvider must use the same model for stored text and queries.
type EmbeddingProvider interface {
	Embed(ctx context.Context, text string) ([]float32, error)
}

type MemoryDocument struct {
	ID    string
	Text  string
	Kind  Type
	Scope FactScope
}

// MemoryRetriever combines BM25 and vector rankings. Embeddings are optional;
// when no provider is configured it uses BM25 alone.
type MemoryRetriever struct {
	embedder EmbeddingProvider
	mu       sync.Mutex
	cache    *vectorLRU
}

// DefaultVectorCacheEntries bounds document and query embeddings together.
// A BGE-M3 float32 vector uses about 4 KiB before cache overhead.
const DefaultVectorCacheEntries = 1024

type cachedVector struct {
	key    string
	text   string
	vector []float32
}

type vectorLRU struct {
	capacity int
	order    *list.List
	entries  map[string]*list.Element
}

func newVectorLRU(capacity int) *vectorLRU {
	return &vectorLRU{capacity: capacity, order: list.New(), entries: make(map[string]*list.Element)}
}

func (cache *vectorLRU) get(key, text string) ([]float32, bool) {
	element, ok := cache.entries[key]
	if !ok {
		return nil, false
	}
	entry := element.Value.(cachedVector)
	if entry.text != text {
		cache.order.Remove(element)
		delete(cache.entries, key)
		return nil, false
	}
	cache.order.MoveToFront(element)
	return entry.vector, true
}

func (cache *vectorLRU) put(key, text string, vector []float32) {
	if cache.capacity <= 0 {
		return
	}
	if element, ok := cache.entries[key]; ok {
		element.Value = cachedVector{key: key, text: text, vector: vector}
		cache.order.MoveToFront(element)
		return
	}
	cache.entries[key] = cache.order.PushFront(cachedVector{key: key, text: text, vector: vector})
	if cache.order.Len() > cache.capacity {
		oldest := cache.order.Back()
		delete(cache.entries, oldest.Value.(cachedVector).key)
		cache.order.Remove(oldest)
	}
}

func NewMemoryRetriever(embedder EmbeddingProvider) *MemoryRetriever {
	return NewMemoryRetrieverWithCacheCapacity(embedder, DefaultVectorCacheEntries)
}

// NewMemoryRetrieverWithCacheCapacity sets a shared entry limit for query and
// document vectors. Zero disables caching; negative capacities are treated as zero.
func NewMemoryRetrieverWithCacheCapacity(embedder EmbeddingProvider, capacity int) *MemoryRetriever {
	if capacity < 0 {
		capacity = 0
	}
	return &MemoryRetriever{embedder: embedder, cache: newVectorLRU(capacity)}
}

func (r *MemoryRetriever) Search(ctx context.Context, query string, documents []MemoryDocument, limit int) ([]MemoryDocument, error) {
	if r == nil || limit <= 0 || strings.TrimSpace(query) == "" || len(documents) == 0 {
		return nil, nil
	}
	queryTerms := terms(query)
	type item struct {
		doc                     MemoryDocument
		terms                   []string
		lexical, semantic, rank float64
	}
	items := make([]item, 0, len(documents))
	frequency := make(map[string]int)
	averageLength := 0.0
	for _, doc := range documents {
		if doc.ID == "" || strings.TrimSpace(doc.Text) == "" {
			continue
		}
		tokens := terms(doc.Text)
		items = append(items, item{doc: doc, terms: tokens})
		averageLength += float64(len(tokens))
		seen := make(map[string]bool)
		for _, token := range tokens {
			if !seen[token] {
				frequency[token]++
				seen[token] = true
			}
		}
	}
	if len(items) == 0 {
		return nil, nil
	}
	averageLength /= float64(len(items))
	if averageLength == 0 {
		averageLength = 1
	}
	for i := range items {
		counts := make(map[string]int)
		for _, token := range items[i].terms {
			counts[token]++
		}
		seen := make(map[string]bool)
		for _, token := range queryTerms {
			if seen[token] {
				continue
			}
			seen[token] = true
			tf := float64(counts[token])
			if tf == 0 {
				continue
			}
			idf := math.Log(1 + (float64(len(items)-frequency[token])+0.5)/(float64(frequency[token])+0.5))
			items[i].lexical += idf * tf * 2.2 / (tf + 1.2*(0.25+0.75*float64(len(items[i].terms))/averageLength))
		}
	}
	if r.embedder != nil {
		queryVector, err := r.queryVector(ctx, query)
		if err != nil {
			return nil, fmt.Errorf("embed memory query: %w", err)
		}
		for i := range items {
			vector, err := r.vector(ctx, items[i].doc)
			if err != nil {
				return nil, fmt.Errorf("embed memory %q: %w", items[i].doc.ID, err)
			}
			items[i].semantic = cosine(queryVector, vector)
		}
	}
	bestLexical, bestSemantic := 0.0, 0.0
	for _, candidate := range items {
		bestLexical = math.Max(bestLexical, candidate.lexical)
		bestSemantic = math.Max(bestSemantic, candidate.semantic)
	}
	semanticCutoff := math.Max(minSemanticSimilarity, bestSemantic*minSemanticFraction)
	lexicalCutoff := bestLexical * minLexicalFraction

	// Reciprocal-rank fusion avoids treating BM25 and cosine scores as if
	// they were measured on the same scale. Only sufficiently relevant
	// candidates receive a rank; limit remains an upper bound, not a target.
	lexical := append([]item(nil), items...)
	sort.SliceStable(lexical, func(i, j int) bool { return lexical[i].lexical > lexical[j].lexical })
	ranks := make(map[string]float64)
	for i, candidate := range lexical {
		if candidate.lexical > 0 && candidate.lexical >= lexicalCutoff {
			ranks[candidate.doc.ID] += 1 / float64(60+i+1)
		}
	}
	if r.embedder != nil {
		semantic := append([]item(nil), items...)
		sort.SliceStable(semantic, func(i, j int) bool { return semantic[i].semantic > semantic[j].semantic })
		for i, candidate := range semantic {
			if candidate.semantic >= semanticCutoff {
				ranks[candidate.doc.ID] += 1 / float64(60+i+1)
			}
		}
	}
	for i := range items {
		items[i].rank = ranks[items[i].doc.ID]
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].rank > items[j].rank })
	result := make([]MemoryDocument, 0, limit)
	for _, candidate := range items {
		if candidate.rank <= 0 || len(result) == limit {
			break
		}
		result = append(result, candidate.doc)
	}
	return result, nil
}

func (r *MemoryRetriever) queryVector(ctx context.Context, query string) ([]float32, error) {
	return r.cachedEmbedding(ctx, "query:"+query, query)
}

func (r *MemoryRetriever) vector(ctx context.Context, doc MemoryDocument) ([]float32, error) {
	return r.cachedEmbedding(ctx, "document:"+doc.ID, doc.Text)
}

func (r *MemoryRetriever) cachedEmbedding(ctx context.Context, key, text string) ([]float32, error) {
	r.mu.Lock()
	cached, ok := r.cache.get(key, text)
	r.mu.Unlock()
	if ok {
		return cached, nil
	}
	vector, err := r.embedder.Embed(ctx, text)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	// Another search may have cached the same text during the embedding call.
	if cached, ok := r.cache.get(key, text); ok {
		r.mu.Unlock()
		return cached, nil
	}
	r.cache.put(key, text, vector)
	r.mu.Unlock()
	return vector, nil
}

func cosine(a, b []float32) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, aa, bb float64
	for i := range a {
		x, y := float64(a[i]), float64(b[i])
		dot += x * y
		aa += x * x
		bb += y * y
	}
	if aa == 0 || bb == 0 {
		return 0
	}
	return dot / math.Sqrt(aa*bb)
}

// CJK bigrams permit Chinese phrase matching without a separate tokenizer.
func terms(s string) []string {
	var out []string
	var word []rune
	var cjk []rune
	flushWord := func() {
		if len(word) > 0 {
			token := string(word)
			if !commonRetrievalWord[token] {
				out = append(out, token)
			}
			word = nil
		}
	}
	flushCJK := func() {
		if len(cjk) == 1 {
			out = append(out, string(cjk))
		}
		for i := 0; i+1 < len(cjk); i++ {
			out = append(out, string(cjk[i:i+2]))
		}
		cjk = nil
	}
	for _, char := range strings.ToLower(s) {
		if unicode.Is(unicode.Han, char) {
			flushWord()
			cjk = append(cjk, char)
			continue
		}
		flushCJK()
		if unicode.IsLetter(char) || unicode.IsDigit(char) || char == '_' {
			word = append(word, char)
		} else {
			flushWord()
		}
	}
	flushCJK()
	flushWord()
	return out
}

var commonRetrievalWord = map[string]bool{
	"a": true, "an": true, "and": true, "are": true, "do": true, "does": true,
	"for": true, "in": true, "is": true, "of": true, "on": true, "or": true,
	"the": true, "to": true, "what": true, "where": true, "which": true,
}
