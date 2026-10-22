package rag

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestContextProviderMeasuresEmbeddingSeparately(t *testing.T) {
	for _, fails := range []bool{false, true} {
		name := "success"
		var embedErr error
		if fails {
			name = "embedding failure"
			embedErr = errors.New("embedding unavailable")
		}
		t.Run(name, func(t *testing.T) {
			const delay = 5 * time.Millisecond
			retriever, err := NewRetriever(staticChunkReader{chunks: []EmbeddedChunk{
				retrievalChunk("manager", "manager.go", "Manager", "type", "type Manager struct{}", []float32{1, 0}),
			}}, queryEmbedder{vector: []float32{1, 0}, delay: delay, err: embedErr})
			if err != nil {
				t.Fatal(err)
			}
			formatter, err := NewSearchResultFormatter(runeTokenCounter{})
			if err != nil {
				t.Fatal(err)
			}
			provider, err := NewContextProvider(retriever, formatter, 2000, 5)
			if err != nil {
				t.Fatal(err)
			}
			var report RetrievalReport
			_, err = provider.BuildWithReport(context.Background(), "Manager", func(got RetrievalReport) { report = got })
			if (err != nil) != fails {
				t.Fatalf("unexpected error: %v", err)
			}
			got := report.Timings
			if got.Load <= 0 || got.Lexical <= 0 || got.Embedding < delay {
				t.Fatalf("stages not measured: %+v", got)
			}
			if fails {
				if got.Ranking != 0 || got.Format != 0 || report.Error == "" {
					t.Fatalf("failed request must retain elapsed stages only: %+v", report)
				}
			} else if got.Ranking <= 0 || got.Format <= 0 {
				t.Fatalf("remaining stages not measured: %+v", got)
			}
			if sum := got.Load + got.Lexical + got.Embedding + got.Ranking + got.Format; sum > report.Duration {
				t.Fatalf("stage timings overlap: sum=%s total=%s", sum, report.Duration)
			}
		})
	}
}

func TestContextProviderReportsSelectionAndFailures(t *testing.T) {
	for _, test := range []struct {
		name     string
		budget   int
		query    string
		storeErr error
		matches  int
		included int
	}{
		{name: "included", budget: 2000, query: "Manager", matches: 1, included: 1},
		{name: "budget excludes result", budget: 1, query: "Manager", matches: 1},
		{name: "no matches", budget: 2000, query: "unrelated"},
		{name: "store failure", budget: 2000, query: "Manager", storeErr: errors.New("database unavailable")},
	} {
		t.Run(test.name, func(t *testing.T) {
			retriever, err := NewRetriever(staticChunkReader{err: test.storeErr, chunks: []EmbeddedChunk{
				retrievalChunk("manager", "manager.go", "Manager", "type", "type Manager struct{}", []float32{1, 0}),
			}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			formatter, err := NewSearchResultFormatter(runeTokenCounter{})
			if err != nil {
				t.Fatal(err)
			}
			provider, err := NewContextProvider(retriever, formatter, test.budget, 5)
			if err != nil {
				t.Fatal(err)
			}
			var report RetrievalReport
			calls := 0
			_, err = provider.BuildWithReport(context.Background(), test.query, func(got RetrievalReport) {
				calls++
				report = got
			})
			if (err != nil) != (test.storeErr != nil) {
				t.Fatalf("BuildWithReport error = %v", err)
			}
			if calls != 1 || len(report.Matches) != test.matches || len(report.Included) != test.included || report.Duration <= 0 {
				t.Fatalf("report = %+v, callbacks=%d", report, calls)
			}
			if test.storeErr != nil && report.Error == "" {
				t.Fatal("failed retrieval must be reported")
			}
			if report.Timings.Load <= 0 || report.Timings.Embedding != 0 {
				t.Fatalf("lexical-only or failed store timing = %+v", report.Timings)
			}
		})
	}
}

func TestContextProviderRetrievesSystemReferenceMessage(t *testing.T) {
	retriever, err := NewRetriever(staticChunkReader{chunks: []EmbeddedChunk{
		retrievalChunk("manager", "internal/memory/manager.go", "Manager.AddMessage", "method", "func (manager *Manager) AddMessage() {}", []float32{1, 0}),
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	formatter, err := NewSearchResultFormatter(runeTokenCounter{})
	if err != nil {
		t.Fatal(err)
	}
	provider, err := NewContextProvider(retriever, formatter, 2000, 5)
	if err != nil {
		t.Fatal(err)
	}
	messages, err := provider.Build(context.Background(), "Manager AddMessage")
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || messages[0].Role != "system" {
		t.Fatalf("Build() = %#v", messages)
	}
}
