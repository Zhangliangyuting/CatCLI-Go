package agent

import (
	"AgentCLI/internal/llm"
	"AgentCLI/internal/memory"
	"AgentCLI/internal/tool"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type verificationTools []tool.Tool

func (provider verificationTools) Tools() []tool.Tool { return provider }

func TestRelevantMarkedFactIsVerifiedOnDemandAndCachedForManager(t *testing.T) {
	toolCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request llm.ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode chat request: %v", err)
			return
		}
		for _, definition := range request.ToolDefinitions {
			if definition.FunctionDefinition.Name == "write_file" {
				t.Error("mutating tool was exposed to fact verifier")
			}
		}
		message := llm.AssistantMessage(`{"results":[{"scope":"PROJECT","key":"toolchain.go.version","status":"current","evidence":"go.mod declares go 1.24"}]}`)
		if toolCalls == 0 {
			if len(request.ToolDefinitions) != 1 || request.ToolDefinitions[0].FunctionDefinition.Name != "read_file" {
				t.Errorf("verification tools = %#v, want read_file only", request.ToolDefinitions)
			}
			message = llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{{
				ID: "read-current", Type: "function",
				Function: llm.FunctionCall{Name: "read_file", Arguments: `{"path":"go.mod"}`},
			}}}
		}
		toolCalls++
		_ = json.NewEncoder(w).Encode(llm.ChatResponse{Choices: []llm.Choice{{Message: message}}})
	}))
	defer server.Close()
	client, err := llm.NewOpenAICompatibleClient("test", server.URL, "test-model")
	if err != nil {
		t.Fatal(err)
	}
	registry := tool.NewToolRegistry()
	registry.RegisterProvider(verificationTools{
		{Definition: tool.Definition{Type: "function", FunctionDefinition: tool.FunctionDefinition{Name: "read_file"}}, Permission: tool.Allow, ReadOnly: true,
			Handler: func(context.Context, map[string]interface{}) (string, error) { return "go 1.24", nil }},
		{Definition: tool.Definition{Type: "function", FunctionDefinition: tool.FunctionDefinition{Name: "write_file"}}, Permission: tool.Allow,
			Handler: func(context.Context, map[string]interface{}) (string, error) { return "written", nil }},
	})
	manager := memory.NewManager(nil)
	_, err = manager.UpsertFact(memory.FactScopeProject, "toolchain.go.version", "This project uses Go 1.24", memory.Metadata{NeedsVerification: true})
	if err != nil {
		t.Fatal(err)
	}

	builder := memory.NewContextBuilder(manager, memory.NewMemoryRetriever(nil), 1000)
	manager.SetFactVerifier(func(ctx context.Context, facts []memory.Entry) (map[string]memory.FactVerificationResult, error) {
		return VerifyFacts(ctx, client, registry, facts)
	})
	if toolCalls != 0 {
		t.Fatalf("verification ran before the fact was requested: %d chat calls", toolCalls)
	}
	messages, err := builder.Build(context.Background(), "Go version")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, message := range messages {
		found = found || strings.Contains(message.Content, "Go 1.24")
	}
	if !found {
		t.Fatalf("verified current fact was not injected: %#v", messages)
	}
	if _, err := builder.Build(context.Background(), "Go version"); err != nil {
		t.Fatal(err)
	}
	if toolCalls != 2 {
		t.Fatalf("chat calls = %d, want one tool round and one result call; second context build should use cache", toolCalls)
	}
}

func TestVerifyFactsReturnsReplacementContentForStaleFact(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request llm.ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode chat request: %v", err)
			return
		}
		message := llm.AssistantMessage(`{"results":[{"scope":"PROJECT","key":"toolchain.go.version","status":"stale","content":"This project uses Go 1.25","evidence":"go.mod declares go 1.25"}]}`)
		if len(request.Messages) == 2 {
			message = llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{{
				ID: "read-current", Type: "function",
				Function: llm.FunctionCall{Name: "read_file", Arguments: `{}`},
			}}}
		}
		_ = json.NewEncoder(w).Encode(llm.ChatResponse{Choices: []llm.Choice{{Message: message}}})
	}))
	defer server.Close()
	client, err := llm.NewOpenAICompatibleClient("test", server.URL, "test-model")
	if err != nil {
		t.Fatal(err)
	}
	registry := tool.NewToolRegistry()
	registry.RegisterProvider(verificationTools{{
		Definition: tool.Definition{Type: "function", FunctionDefinition: tool.FunctionDefinition{Name: "read_file"}},
		Permission: tool.Allow, ReadOnly: true,
		Handler: func(context.Context, map[string]interface{}) (string, error) { return "go 1.25", nil },
	}})
	manager := memory.NewManager(nil)
	fact, err := manager.UpsertFact(memory.FactScopeProject, "toolchain.go.version", "This project uses Go 1.24", memory.Metadata{NeedsVerification: true})
	if err != nil {
		t.Fatal(err)
	}
	outcomes, err := VerifyFacts(context.Background(), client, registry, []memory.Entry{fact})
	if err != nil {
		t.Fatal(err)
	}
	result := outcomes[fact.ID()]
	if result.Outcome != memory.FactVerifiedStale || result.Content != "This project uses Go 1.25" || result.Evidence == "" {
		t.Fatalf("verification result = %+v; want stale result with replacement content and evidence", result)
	}
}

func TestVerifyFactsRunsChecksConcurrently(t *testing.T) {
	var active, maxActive, arrivals atomic.Int32
	release := make(chan struct{})
	var releaseOnce sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request llm.ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode chat request: %v", err)
			return
		}
		factKey := "parallel_a"
		if strings.Contains(request.Messages[1].Content, "parallel_b") {
			factKey = "parallel_b"
		}
		message := llm.AssistantMessage(fmt.Sprintf(`{"results":[{"scope":"PROJECT","key":%q,"status":"current","evidence":"read source"}]}`, factKey))
		if len(request.Messages) == 2 {
			current := active.Add(1)
			defer active.Add(-1)
			for seen := maxActive.Load(); current > seen; seen = maxActive.Load() {
				if maxActive.CompareAndSwap(seen, current) {
					break
				}
			}
			if arrivals.Add(1) >= 2 {
				releaseOnce.Do(func() { close(release) })
			}
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
			message = llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{{
				ID: "read-" + factKey, Type: "function",
				Function: llm.FunctionCall{Name: "read_file", Arguments: `{}`},
			}}}
		}
		_ = json.NewEncoder(w).Encode(llm.ChatResponse{Choices: []llm.Choice{{Message: message}}})
	}))
	defer server.Close()
	client, err := llm.NewOpenAICompatibleClient("test", server.URL, "test-model")
	if err != nil {
		t.Fatal(err)
	}
	registry := tool.NewToolRegistry()
	registry.RegisterProvider(verificationTools{{
		Definition: tool.Definition{Type: "function", FunctionDefinition: tool.FunctionDefinition{Name: "read_file"}},
		Permission: tool.Allow, ReadOnly: true,
		Handler: func(context.Context, map[string]interface{}) (string, error) { return "source", nil },
	}})
	manager := memory.NewManager(nil)
	facts := make([]memory.Entry, 0, 2)
	for _, key := range []string{"parallel_a", "parallel_b"} {
		fact, err := manager.UpsertFact(memory.FactScopeProject, key, "Fact "+key, memory.Metadata{NeedsVerification: true})
		if err != nil {
			t.Fatal(err)
		}
		facts = append(facts, fact)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	outcomes, err := VerifyFacts(ctx, client, registry, facts)
	if err != nil {
		t.Fatal(err)
	}
	if maxActive.Load() < 2 {
		t.Fatalf("maximum concurrent LLM checks = %d, want at least 2", maxActive.Load())
	}
	for _, fact := range facts {
		if outcomes[fact.ID()].Outcome != memory.FactVerifiedCurrent {
			t.Fatalf("verification for %s = %q, want current", fact.ID(), outcomes[fact.ID()].Outcome)
		}
	}
}
