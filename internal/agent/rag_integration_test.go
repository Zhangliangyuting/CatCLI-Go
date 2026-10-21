package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"AgentCLI/internal/llm"
	"AgentCLI/internal/memory"
	"AgentCLI/internal/rag"
	"AgentCLI/internal/tool"
	"AgentCLI/internal/workspace"
)

type integrationEmbedder struct{}

func (integrationEmbedder) Embed(context.Context, string) ([]float32, error) {
	return []float32{1, 0}, nil
}

func (integrationEmbedder) EmbedBatch(_ context.Context, texts []string) ([][]float32, error) {
	vectors := make([][]float32, len(texts))
	for index := range texts {
		vectors[index] = []float32{1, 0}
	}
	return vectors, nil
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestReActAgentReceivesIndexedWorkspaceContext(t *testing.T) {
	root := t.TempDir()
	source := "package evidence\n\nfunc WorkspaceAnswer() string { return \"indexed-value\" }\n"
	if err := os.WriteFile(filepath.Join(root, "evidence.go"), []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	work, err := workspace.New(root)
	if err != nil {
		t.Fatal(err)
	}
	store, err := rag.NewSQLiteStore(filepath.Join(work.MetadataDir(), "rag.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	embedder := integrationEmbedder{}
	indexer, err := rag.NewWorkspaceIndexer(
		work,
		rag.NewDefaultChunkerSet(rag.ChunkConfig{}),
		embedder,
		store,
		rag.WorkspaceIndexerConfig{EmbeddingModel: "integration-embedding"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := indexer.Index(context.Background()); err != nil {
		t.Fatal(err)
	}
	retriever, err := rag.NewRetriever(store, embedder)
	if err != nil {
		t.Fatal(err)
	}
	formatter, err := rag.NewSearchResultFormatter(memory.ApproxTokenCounter{})
	if err != nil {
		t.Fatal(err)
	}
	provider, err := rag.NewContextProvider(retriever, formatter, 2048, 10)
	if err != nil {
		t.Fatal(err)
	}

	var captured llm.ChatRequest
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if err := json.NewDecoder(request.Body).Decode(&captured); err != nil {
			return nil, err
		}
		body, err := json.Marshal(llm.ChatResponse{
			Choices: []llm.Choice{{Message: llm.AssistantMessage("found indexed workspace context")}},
		})
		if err != nil {
			return nil, err
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(bytes.NewReader(body)),
			Request:    request,
		}, nil
	})}
	client := &llm.OpenAICompatibleClient{
		APIKey: "test", BaseURL: "http://rag.integration", Model: "test-chat", HTTPClient: httpClient,
	}
	agent := NewReActAgent(client, tool.NewToolRegistry(), WithReferenceContextProvider(provider))
	answer, err := agent.Run(context.Background(), "Where is WorkspaceAnswer implemented?")
	if err != nil {
		t.Fatal(err)
	}
	if answer != "found indexed workspace context" {
		t.Fatalf("answer = %q", answer)
	}
	joined := ""
	for _, message := range captured.Messages {
		joined += "\n" + message.Content
	}
	for _, want := range []string{"evidence.go", "WorkspaceAnswer", "indexed-value", "untrusted reference data"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("model request does not contain %q:\n%s", want, joined)
		}
	}
}
