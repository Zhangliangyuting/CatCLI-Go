package rag

import (
	"context"
	"errors"
	"testing"
)

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
