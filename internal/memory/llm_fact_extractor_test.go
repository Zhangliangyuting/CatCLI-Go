package memory

import (
	"AgentCLI/internal/llm"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestMightContainFactIntent(t *testing.T) {
	tests := []struct {
		message string
		want    bool
	}{
		{message: "帮我解释这个函数", want: false},
		{message: "我这个项目是 Golang 写的，以后就用 Go 吧", want: true},
		{message: "Remember that I prefer concise answers", want: true},
		{message: "忘掉之前保存的语言偏好", want: true},
	}
	for _, test := range tests {
		if got := MightContainFactIntent(test.message); got != test.want {
			t.Errorf("MightContainFactIntent(%q) = %t, want %t", test.message, got, test.want)
		}
	}
}

func TestLLMFactExtractorSkipsLLMWhenRulesDoNotMatch(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests++
	}))
	defer server.Close()
	client, err := llm.NewOpenAICompatibleClient("test-key", server.URL, "deepseek-v4-pro")
	if err != nil {
		t.Fatalf("NewOpenAICompatibleClient() error = %v", err)
	}
	extractor, err := NewLLMFactExtractor(client)
	if err != nil {
		t.Fatalf("NewLLMFactExtractor() error = %v", err)
	}

	operations, err := extractor.Extract(context.Background(), "解释一下 manager.go", nil)
	if err != nil {
		t.Fatalf("Extract() error = %v", err)
	}
	if len(operations) != 0 || requests != 0 {
		t.Fatalf("Extract() = %v, requests=%d; want no operations and no request", operations, requests)
	}
}

func TestLLMFactExtractorUsesStructuredJSONAndReturnsOperation(t *testing.T) {
	var request llm.ChatRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(llm.ChatResponse{Choices: []llm.Choice{{
			Message: llm.AssistantMessage(`{"operations":[{"action":"UPSERT","scope":"PROJECT","key":"programming_language","content":"Use Go for this project."}]}`),
		}}})
	}))
	defer server.Close()
	client, err := llm.NewOpenAICompatibleClient("test-key", server.URL, "deepseek-v4-pro")
	if err != nil {
		t.Fatalf("NewOpenAICompatibleClient() error = %v", err)
	}
	extractor, err := NewLLMFactExtractor(client)
	if err != nil {
		t.Fatalf("NewLLMFactExtractor() error = %v", err)
	}

	operations, err := extractor.Extract(
		context.Background(),
		"我这个项目以后默认用 Go",
		nil,
	)
	if err != nil {
		t.Fatalf("Extract() error = %v", err)
	}
	if len(operations) != 1 || operations[0].Scope != FactScopeProject || operations[0].Key != "programming_language" {
		t.Fatalf("Extract() = %#v", operations)
	}
	if request.ResponseFormat == nil || request.ResponseFormat.Type != "json_object" {
		t.Fatalf("response format = %#v, want json_object", request.ResponseFormat)
	}
	if request.Thinking == nil || request.Thinking.Type != "disabled" {
		t.Fatalf("thinking = %#v, want disabled", request.Thinking)
	}
	if request.MaxTokens != DefaultFactExtractionMaxTokens {
		t.Fatalf("max tokens = %d, want %d", request.MaxTokens, DefaultFactExtractionMaxTokens)
	}
}

func TestApplyFactOperationsValidatesWholeBatchBeforeMutation(t *testing.T) {
	manager := NewManager(nil)
	err := ApplyFactOperations(manager, []FactOperation{
		{Action: FactActionUpsert, Scope: FactScopeUser, Key: "language", Content: "Use Go."},
		{Action: FactActionUpsert, Scope: FactScopeUser, Key: "INVALID KEY", Content: "bad"},
	})
	if err == nil {
		t.Fatal("ApplyFactOperations() error = nil, want validation error")
	}
	if manager.FactLen() != 0 {
		t.Fatalf("FactLen() = %d, want 0 after rejected batch", manager.FactLen())
	}
}

func TestLLMFactExtractorRetriesDuplicateOperations(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		content := `{"operations":[{"action":"REMOVE","scope":"PROJECT","key":"database"},{"action":"UPSERT","scope":"PROJECT","key":"database","content":"Use PostgreSQL."}]}`
		if requests == 2 {
			content = `{"operations":[{"action":"UPSERT","scope":"PROJECT","key":"database","content":"Use PostgreSQL."}]}`
		}
		_ = json.NewEncoder(w).Encode(llm.ChatResponse{Choices: []llm.Choice{{Message: llm.AssistantMessage(content)}}})
	}))
	defer server.Close()
	client, err := llm.NewOpenAICompatibleClient("test-key", server.URL, "deepseek-v4-pro")
	if err != nil {
		t.Fatal(err)
	}
	extractor, err := NewLLMFactExtractor(client)
	if err != nil {
		t.Fatal(err)
	}
	operations, err := extractor.Extract(context.Background(), "请记住：本项目现在改用 PostgreSQL，不再使用 SQLite。", nil)
	if err != nil {
		t.Fatal(err)
	}
	if requests != 2 || len(operations) != 1 || operations[0].Action != FactActionUpsert {
		t.Fatalf("requests=%d, operations=%+v", requests, operations)
	}
}
