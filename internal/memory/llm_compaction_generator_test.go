package memory

import (
	"AgentCLI/internal/llm"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestLLMCompactionGeneratorUsesJSONModeAndDisablesDeepSeekThinking(t *testing.T) {
	var request llm.ChatRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(llm.ChatResponse{Choices: []llm.Choice{{
			Message:      llm.AssistantMessage(`{"overview":"ok"}`),
			FinishReason: "stop",
		}}})
	}))
	defer server.Close()

	client, err := llm.NewOpenAICompatibleClient("test-key", server.URL, "deepseek-v4-pro")
	if err != nil {
		t.Fatalf("NewOpenAICompatibleClient() error = %v", err)
	}
	generator, err := NewLLMCompactionGeneratorWithMaxTokens(client, 1024)
	if err != nil {
		t.Fatalf("NewLLMCompactionGenerator() error = %v", err)
	}

	if _, err := generator.complete(context.Background(), "return json"); err != nil {
		t.Fatalf("complete() error = %v", err)
	}
	if request.ResponseFormat == nil || request.ResponseFormat.Type != "json_object" {
		t.Fatalf("response format = %#v, want json_object", request.ResponseFormat)
	}
	if request.Thinking == nil || request.Thinking.Type != "disabled" {
		t.Fatalf("thinking = %#v, want disabled", request.Thinking)
	}
	if request.MaxTokens != 1024 {
		t.Fatalf("max tokens = %d, want 1024", request.MaxTokens)
	}
}

func TestLLMCompactionGeneratorRetriesEmptyContent(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		message := llm.AssistantMessage("")
		if requests == 2 {
			message = llm.AssistantMessage(`{"overview":"retry worked"}`)
		}
		_ = json.NewEncoder(w).Encode(llm.ChatResponse{Choices: []llm.Choice{{
			Message:      message,
			FinishReason: "stop",
		}}})
	}))
	defer server.Close()

	client, err := llm.NewOpenAICompatibleClient("test-key", server.URL, "deepseek-v4-pro")
	if err != nil {
		t.Fatalf("NewOpenAICompatibleClient() error = %v", err)
	}
	generator, err := NewLLMCompactionGeneratorWithMaxTokens(client, 1024)
	if err != nil {
		t.Fatalf("NewLLMCompactionGenerator() error = %v", err)
	}

	content, err := generator.complete(context.Background(), "return json")
	if err != nil {
		t.Fatalf("complete() error = %v", err)
	}
	if requests != 2 || !strings.Contains(content, "retry worked") {
		t.Fatalf("complete() = (%q, requests=%d), want successful second response", content, requests)
	}
}
