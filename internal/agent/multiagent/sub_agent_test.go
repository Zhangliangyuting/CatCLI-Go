package multiagent

import (
	baseagent "AgentCLI/internal/agent"
	"AgentCLI/internal/llm"
	"AgentCLI/internal/memory"
	"AgentCLI/internal/plan"
	"AgentCLI/internal/tool"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type workerMemoryScheduler struct{}

func (workerMemoryScheduler) CompactToFit(
	context.Context,
	*memory.Manager,
	memory.ContextTokenMeasurer,
) ([]memory.CompactionDecision, error) {
	return nil, nil
}

type workerTranscript struct {
	conversationIDs []string
	entries         []memory.Entry
}

func (store *workerTranscript) Append(conversationID string, entries ...memory.Entry) error {
	store.conversationIDs = append(store.conversationIDs, conversationID)
	store.entries = append(store.entries, entries...)
	return nil
}

func (store *workerTranscript) Load(string) ([]memory.Entry, error) {
	return append([]memory.Entry(nil), store.entries...), nil
}

var _ memory.ContextCompactionScheduler = workerMemoryScheduler{}

func TestNewWorkerSubAgentUsesRoleDefaultPrompt(t *testing.T) {
	client := &llm.OpenAICompatibleClient{}
	registry := tool.NewToolRegistry()

	agent, err := NewWorkerSubAgent(client, registry)
	if err != nil {
		t.Fatalf("NewWorkerSubAgent() error = %v", err)
	}
	if agent.Role() != RoleWorker {
		t.Fatalf("Role() = %s, want WORKER", agent.Role())
	}
	if !strings.Contains(agent.SystemPrompt(), "执行者") {
		t.Fatalf("SystemPrompt() = %q", agent.SystemPrompt())
	}
}

func TestWorkerSubAgentUsesTaskScopedMemory(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	worker, err := NewWorkerSubAgent(&llm.OpenAICompatibleClient{
		APIKey: "test", BaseURL: server.URL, Model: "test", HTTPClient: server.Client(),
	}, tool.NewToolRegistry())
	if err != nil {
		t.Fatalf("NewWorkerSubAgent() error = %v", err)
	}
	manager := memory.NewManager(nil)
	transcript := &workerTranscript{}
	if err := worker.ConfigureMemory(
		memory.AgentMemoryContext{
			ConversationID: "root-task-1",
			Manager:        manager,
			ContextBuilder: memory.NewContextBuilder(manager, nil, 0),
			Scheduler:      workerMemoryScheduler{},
			Estimator:      memory.NewCalibratedRequestTokenEstimator(nil),
			Transcript:     transcript,
		},
	); err != nil {
		t.Fatalf("ConfigureMemory() error = %v", err)
	}
	result, err := worker.Run(context.Background(), "perform task")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result != "done" || manager.Len() != 2 || len(transcript.entries) != 2 {
		t.Fatalf("result/manager/transcript = %q/%d/%d", result, manager.Len(), len(transcript.entries))
	}
	for _, conversationID := range transcript.conversationIDs {
		if conversationID != "root-task-1" {
			t.Fatalf("transcript conversation ID = %q", conversationID)
		}
	}
}

func TestPlannerSubAgentGeneratesStructuredPlan(t *testing.T) {
	var request llm.ChatRequest
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, incoming *http.Request) {
		defer incoming.Body.Close()
		if err := json.NewDecoder(incoming.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"{\"goal\":\"g\",\"summary\":\"s\",\"tasks\":[{\"id\":\"one\",\"name\":\"n\",\"description\":\"d\",\"type\":\"ANALYSIS\",\"dependencies\":[],\"read_resources\":[],\"write_resources\":[]}] }"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	planner, err := NewPlannerSubAgent(
		&llm.OpenAICompatibleClient{APIKey: "test", BaseURL: server.URL, Model: "test", HTTPClient: server.Client()},
	)
	if err != nil {
		t.Fatalf("NewPlannerSubAgent() error = %v", err)
	}
	var events []baseagent.Event
	ctx := baseagent.WithObserver(context.Background(), func(event baseagent.Event) {
		events = append(events, event)
	})
	generated, err := planner.Generate(ctx, []llm.Message{llm.UserMessage("goal")})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if generated.Goal() != "g" || len(generated.ExecutionOrder()) != 1 {
		t.Fatalf("generated plan = %#v", generated)
	}
	if len(request.Messages) == 0 || request.Messages[0].Content != strings.TrimSpace(PlannerSystemPrompt) {
		t.Fatalf("planner system message = %#v", request.Messages)
	}
	if len(events) != 1 || events[0].Type != baseagent.EventTokenUsage || !strings.HasPrefix(events[0].Title, "[PLANNER]") {
		t.Fatalf("planner events = %#v", events)
	}
}

func TestReviewerSubAgentReturnsStructuredDecision(t *testing.T) {
	var request llm.ChatRequest
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, incoming *http.Request) {
		defer incoming.Body.Close()
		if err := json.NewDecoder(incoming.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"{\"approved\":false,\"feedback\":\"missing tests\"}"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	reviewer, err := NewReviewerSubAgent(
		&llm.OpenAICompatibleClient{APIKey: "test", BaseURL: server.URL, Model: "deepseek-test", HTTPClient: server.Client()},
		nil,
	)
	if err != nil {
		t.Fatalf("NewReviewerSubAgent() error = %v", err)
	}
	review, err := reviewer.ReviewResult(context.Background(), TaskExecution{
		TaskID:   "task_1",
		TaskType: plan.VERIFICATION,
		PlanGoal: "ship",
		Prompt:   "检查测试结果并读取 evidence.txt",
	}, "not verified")
	if err != nil {
		t.Fatalf("ReviewResult() error = %v", err)
	}
	if review.Approved || review.Feedback != "missing tests" {
		t.Fatalf("review = %#v", review)
	}
	var reviewInput struct {
		TaskPrompt string `json:"task_prompt"`
	}
	if err := json.Unmarshal([]byte(request.Messages[len(request.Messages)-1].Content), &reviewInput); err != nil {
		t.Fatalf("decode review input: %v", err)
	}
	if reviewInput.TaskPrompt != "检查测试结果并读取 evidence.txt" {
		t.Fatalf("review task prompt = %q", reviewInput.TaskPrompt)
	}
	if request.ResponseFormat == nil || request.ResponseFormat.Type != "json_object" {
		t.Fatalf("response format = %#v", request.ResponseFormat)
	}
	if request.Thinking == nil || request.Thinking.Type != "disabled" {
		t.Fatalf("thinking = %#v", request.Thinking)
	}
}

func TestRoleSubAgentConstructorsRejectInvalidConfiguration(t *testing.T) {
	if _, err := NewPlannerSubAgent(nil); err == nil {
		t.Fatal("NewPlannerSubAgent() error = nil, want nil client error")
	}
	if _, err := NewWorkerSubAgent(&llm.OpenAICompatibleClient{}, nil); err == nil {
		t.Fatal("NewWorkerSubAgent() error = nil, want nil tools error")
	}
	if _, err := NewReviewerSubAgent(nil, nil); err == nil {
		t.Fatal("NewReviewerSubAgent() error = nil, want nil client error")
	}
}
