package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"AgentCLI/internal/config"
	"AgentCLI/internal/llm"
	"AgentCLI/internal/memory"
	"AgentCLI/internal/rag"
	"AgentCLI/internal/tool"
	"AgentCLI/internal/workspace"
)

// Run with CATCLI_TEST_LIVE_RAG=1 go test ./internal/agent -run TestLiveWorkspaceRAG -v -count=1.
// This sends synthetic fixture text to the configured embedding and chat services.
func TestLiveWorkspaceRAG(t *testing.T) {
	if os.Getenv("CATCLI_TEST_LIVE_RAG") != "1" {
		t.Skip("set CATCLI_TEST_LIVE_RAG=1 to use real embedding and chat services")
	}
	t.Chdir("../..") // Load the same config/config.yaml and .env as the CLI.
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(cfg.Embedding.Model) == "" {
		t.Fatal("configure CATCLI_EMBEDDING_MODEL for the live RAG test")
	}
	client, err := llm.NewOpenAICompatibleClient(cfg.OpenAICompatible.APIKey, cfg.OpenAICompatible.BaseURL, cfg.OpenAICompatible.Model)
	if err != nil {
		t.Fatal(err)
	}
	apiKey, baseURL := cfg.Embedding.APIKey, cfg.Embedding.BaseURL
	if strings.TrimSpace(apiKey) == "" {
		apiKey = client.APIKey
	}
	if strings.TrimSpace(baseURL) == "" {
		baseURL = client.BaseURL
	}
	embedder := &llm.OpenAIEmbeddingClient{APIKey: apiKey, BaseURL: baseURL, Model: cfg.Embedding.Model}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	var nonce [12]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		t.Fatal(err)
	}
	answerToken := "rag-live-" + hex.EncodeToString(nonce[:])
	root := t.TempDir()
	source := fmt.Sprintf("package evidence\n\n// WorkspaceAnswer returns the verification token.\nfunc WorkspaceAnswer() string { return %q }\n", answerToken)
	if err := os.WriteFile(filepath.Join(root, "evidence.go"), []byte(source), 0o600); err != nil {
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
	indexer, err := rag.NewWorkspaceIndexer(work, rag.NewDefaultChunkerSet(rag.ChunkConfig{}), embedder, store,
		rag.WorkspaceIndexerConfig{EmbeddingModel: cfg.Embedding.Model})
	if err != nil {
		t.Fatal(err)
	}
	report, err := indexer.Index(ctx)
	if err != nil {
		t.Fatalf("live indexing: %v", err)
	}
	if report.IndexedFiles != 1 || report.Chunks == 0 {
		t.Fatalf("unexpected index report: %+v", report)
	}
	t.Logf("embedding model=%s; indexed files=%d chunks=%d", cfg.Embedding.Model, report.IndexedFiles, report.Chunks)
	retriever, err := rag.NewRetriever(store, embedder)
	if err != nil {
		t.Fatal(err)
	}
	formatter, err := rag.NewSearchResultFormatter(memory.ApproxTokenCounter{})
	if err != nil {
		t.Fatal(err)
	}
	provider, err := rag.NewContextProvider(retriever, formatter, 2048, 5)
	if err != nil {
		t.Fatal(err)
	}
	query := "根据工作区参考源码，WorkspaceAnswer 函数返回的完整字符串是什么？请同时给出来源文件名。"
	references, err := provider.Build(ctx, query)
	if err != nil {
		t.Fatalf("live retrieval: %v", err)
	}
	if len(references) != 1 || !strings.Contains(references[0].Content, answerToken) {
		t.Fatal("retrieved context does not contain the verification token")
	}
	t.Log("real query embedding and retrieval verified")
	// No file tools: the model must obtain the random answer from RAG context.
	agent := NewReActAgent(client, tool.NewToolRegistry(), WithReferenceContextProvider(provider))
	answer, err := agent.Run(ctx, query)
	if err != nil {
		t.Fatalf("live chat: %v", err)
	}
	if !strings.Contains(answer, answerToken) || !strings.Contains(answer, "evidence.go") {
		t.Fatalf("answer must include the exact verification token and source file: %s", answer)
	}
	t.Logf("chat model=%s; answer: %s", cfg.OpenAICompatible.Model, answer)
}
