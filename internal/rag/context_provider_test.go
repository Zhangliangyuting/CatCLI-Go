package rag

import (
	"context"
	"testing"
)

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
