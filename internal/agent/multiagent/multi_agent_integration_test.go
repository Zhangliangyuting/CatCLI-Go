package multiagent

import (
	baseagent "AgentCLI/internal/agent"
	"AgentCLI/internal/llm"
	"AgentCLI/internal/memory"
	"AgentCLI/internal/plan"
	"AgentCLI/internal/tool"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type integrationPlanDecision struct{}

func (integrationPlanDecision) Decide(*plan.Plan) (PlanAction, string, error) {
	return PlanExecute, "", nil
}

func TestMultiAgentPlannerWorkerReviewerFlow(t *testing.T) {
	var requestMu sync.Mutex
	requestRoles := make([]AgentRole, 0, 5)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		defer request.Body.Close()
		var chatRequest llm.ChatRequest
		if err := json.NewDecoder(request.Body).Decode(&chatRequest); err != nil {
			http.Error(writer, err.Error(), http.StatusBadRequest)
			return
		}
		if len(chatRequest.Messages) == 0 {
			http.Error(writer, "messages are empty", http.StatusBadRequest)
			return
		}

		systemPrompt := chatRequest.Messages[0].Content
		role := roleFromSystemPrompt(systemPrompt)
		requestMu.Lock()
		requestRoles = append(requestRoles, role)
		requestMu.Unlock()

		switch role {
		case RolePlanner:
			writeIntegrationChatResponse(writer, llm.Message{
				Role:    "assistant",
				Content: `{"goal":"检查项目证据","summary":"读取并复核证据","tasks":[{"id":"inspect","name":"读取证据","description":"读取 evidence.txt 并报告内容","type":"FILE_READ","dependencies":[],"read_resources":["evidence.txt"],"write_resources":[]}]}`,
			})
		case RoleWorker:
			if hasToolResult(chatRequest.Messages) {
				writeIntegrationChatResponse(writer, llm.AssistantMessage("evidence inspected: verified"))
				return
			}
			writeIntegrationToolCall(writer, "worker_read", "read_file", `{"path":"evidence.txt"}`)
		case RoleReviewer:
			for _, definition := range chatRequest.ToolDefinitions {
				name := definition.FunctionDefinition.Name
				if name != "read_file" && name != "list_dir" {
					http.Error(writer, "reviewer received non-read-only tool: "+name, http.StatusBadRequest)
					return
				}
			}
			if hasToolResult(chatRequest.Messages) {
				writeIntegrationChatResponse(writer, llm.AssistantMessage(`{"approved":true,"feedback":"independently verified"}`))
				return
			}
			writeIntegrationToolCall(writer, "reviewer_read", "read_file", `{"path":"evidence.txt"}`)
		default:
			http.Error(writer, "unknown role", http.StatusBadRequest)
		}
	}))
	defer server.Close()

	client := &llm.OpenAICompatibleClient{
		APIKey:     "test",
		BaseURL:    server.URL,
		Model:      "test-model",
		HTTPClient: server.Client(),
	}
	registry := tool.NewToolRegistry()
	readCalls := 0
	registry.RegisterTool(tool.ReadFileDefinition(), func(context.Context, map[string]interface{}) (string, error) {
		readCalls++
		return "verified", nil
	})
	registry.RegisterTool(tool.WriteFileDefinition(), func(context.Context, map[string]interface{}) (string, error) {
		return "", fmt.Errorf("write must not be called")
	})

	planner, err := NewPlannerSubAgent(client)
	if err != nil {
		t.Fatalf("create planner: %v", err)
	}
	reviewer, err := NewReviewerSubAgent(client, registry.Subset("read_file", "list_dir"))
	if err != nil {
		t.Fatalf("create reviewer: %v", err)
	}
	memoryRuntime, err := memory.NewConversationRuntime(
		client,
		memory.NewMemoryRetriever(nil),
		t.TempDir(),
		"",
		100_000,
		4096,
	)
	if err != nil {
		t.Fatalf("create memory runtime: %v", err)
	}
	orchestrator, err := NewPlanAndExecuteAgent(
		planner,
		client,
		registry,
		reviewer,
		memoryRuntime,
		integrationPlanDecision{},
		0,
		1,
		0,
		0,
	)
	if err != nil {
		t.Fatalf("create orchestrator: %v", err)
	}

	var events []baseagent.Event
	ctx := baseagent.WithObserver(context.Background(), func(event baseagent.Event) {
		events = append(events, event)
	})
	result, err := orchestrator.Run(ctx, "检查 evidence.txt")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !strings.Contains(result, "evidence inspected: verified") {
		t.Fatalf("result = %q", result)
	}
	if readCalls != 2 {
		t.Fatalf("read_file calls = %d, want worker + reviewer calls", readCalls)
	}
	for _, role := range []AgentRole{RolePlanner, RoleWorker, RoleReviewer} {
		if !containsAgentRole(events, role) {
			t.Fatalf("events omitted role %s: %#v", role, events)
		}
	}
	if !containsEventType(events, baseagent.EventResultReview) {
		t.Fatalf("events omitted result review: %#v", events)
	}

	requestMu.Lock()
	defer requestMu.Unlock()
	for _, role := range []AgentRole{RolePlanner, RoleWorker, RoleReviewer} {
		found := false
		for _, requestedRole := range requestRoles {
			if requestedRole == role {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("LLM requests omitted role %s: %v", role, requestRoles)
		}
	}
}

func roleFromSystemPrompt(prompt string) AgentRole {
	switch {
	case strings.Contains(prompt, "任务规划专家"):
		return RolePlanner
	case strings.Contains(prompt, "独立的任务结果检查者"):
		return RoleReviewer
	case strings.Contains(prompt, "执行者"):
		return RoleWorker
	default:
		return ""
	}
}

func hasToolResult(messages []llm.Message) bool {
	for _, message := range messages {
		if message.Role == "tool" {
			return true
		}
	}
	return false
}

func containsEventType(events []baseagent.Event, eventType baseagent.EventType) bool {
	for _, event := range events {
		if event.Type == eventType {
			return true
		}
	}
	return false
}

func writeIntegrationToolCall(
	writer http.ResponseWriter,
	id string,
	name string,
	arguments string,
) {
	writeIntegrationChatResponse(writer, llm.Message{
		Role: "assistant",
		ToolCalls: []llm.ToolCall{{
			ID:   id,
			Type: "function",
			Function: llm.FunctionCall{
				Name:      name,
				Arguments: arguments,
			},
		}},
	})
}

func writeIntegrationChatResponse(writer http.ResponseWriter, message llm.Message) {
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(map[string]interface{}{
		"choices": []map[string]interface{}{{
			"message":       message,
			"finish_reason": "stop",
		}},
		"usage": map[string]int{
			"prompt_tokens":     10,
			"completion_tokens": 5,
			"total_tokens":      15,
		},
	})
}

func containsAgentRole(events []baseagent.Event, role AgentRole) bool {
	for _, event := range events {
		if strings.HasPrefix(event.Title, "["+string(role)+"]") {
			return true
		}
	}
	return false
}
