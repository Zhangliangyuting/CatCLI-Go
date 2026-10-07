package agent

import (
	"AgentCLI/internal/llm"
	"AgentCLI/internal/memory"
	"AgentCLI/internal/tool"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
)

type recordingContextCompactionScheduler struct {
	calls       int
	managerLens []int
	measured    []int
	decision    memory.CompactionDecision
	err         error
}

func (scheduler *recordingContextCompactionScheduler) CompactToFit(
	_ context.Context,
	manager *memory.Manager,
	measure memory.ContextTokenMeasurer,
) ([]memory.CompactionDecision, error) {
	scheduler.calls++
	scheduler.managerLens = append(scheduler.managerLens, manager.Len())
	if measure != nil {
		tokens, err := measure()
		if err != nil {
			return nil, err
		}
		scheduler.measured = append(scheduler.measured, tokens)
	}
	if scheduler.decision.Action == memory.CompactionActionNone {
		return nil, scheduler.err
	}
	return []memory.CompactionDecision{scheduler.decision}, scheduler.err
}

type recordingRequestTokenEstimator struct {
	estimates []int
	next      int
	observed  [][2]int
}

func (estimator *recordingRequestTokenEstimator) Estimate(
	[]llm.Message,
	[]tool.Definition,
) (int, error) {
	estimator.estimates = append(estimator.estimates, estimator.next)
	return estimator.next, nil
}

func (estimator *recordingRequestTokenEstimator) Observe(estimated, actual int) {
	estimator.observed = append(estimator.observed, [2]int{estimated, actual})
}

func TestReActAgentUsesMemoryManagerForToolConversation(t *testing.T) {
	toolCall := llm.ToolCall{
		ID:   "call_1",
		Type: "function",
		Function: llm.FunctionCall{
			Name:      "missing_tool",
			Arguments: `{}`,
		},
	}

	var requestMu sync.Mutex
	requests := make([]llm.ChatRequest, 0, 2)
	transport := agentRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		var request llm.ChatRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			return nil, err
		}

		requestMu.Lock()
		requests = append(requests, request)
		call := len(requests)
		requestMu.Unlock()

		var response llm.ChatResponse
		if call == 1 {
			response.Choices = []llm.Choice{{
				Message: llm.Message{
					Role:      "assistant",
					ToolCalls: []llm.ToolCall{toolCall},
				},
			}}
		} else {
			response.Choices = []llm.Choice{{
				Message: llm.AssistantMessage("done"),
			}}
		}
		encoded, err := json.Marshal(response)
		if err != nil {
			return nil, err
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(string(encoded))),
		}, nil
	})

	client, err := llm.NewOpenAICompatibleClient("test-key", "http://example.test", "test-model")
	if err != nil {
		t.Fatalf("NewOpenAICompatibleClient() error = %v", err)
	}
	client.HTTPClient = &http.Client{Transport: transport}
	scheduler := &recordingContextCompactionScheduler{}
	agent := NewReActAgent(
		client,
		tool.NewToolRegistry(),
		WithCompactionScheduler(scheduler),
	)

	answer, err := agent.Run(context.Background(), "use a tool")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if answer != "done" {
		t.Fatalf("Run() = %q, want done", answer)
	}

	requestMu.Lock()
	defer requestMu.Unlock()
	if len(requests) != 2 {
		t.Fatalf("request count = %d, want 2", len(requests))
	}
	if scheduler.calls != 2 || !reflect.DeepEqual(scheduler.managerLens, []int{1, 3}) {
		t.Fatalf("scheduler calls = %d, manager lengths = %v, want (2, [1 3])", scheduler.calls, scheduler.managerLens)
	}
	secondMessages := requests[1].Messages
	if len(secondMessages) != 4 {
		t.Fatalf("second request message count = %d, want 4", len(secondMessages))
	}
	if !reflect.DeepEqual(secondMessages[2].ToolCalls, []llm.ToolCall{toolCall}) {
		t.Fatalf("assistant tool calls = %#v, want %#v", secondMessages[2].ToolCalls, []llm.ToolCall{toolCall})
	}
	if secondMessages[3].Role != "tool" || secondMessages[3].ToolCallID != "call_1" {
		t.Fatalf("tool message = %#v, want role=tool and tool_call_id=call_1", secondMessages[3])
	}

	toolEntries := agent.memoryManager.EntriesByType(memory.ToolResult)
	if len(toolEntries) != 1 || toolEntries[0].Metadata().ToolName != "missing_tool" {
		t.Fatalf("tool result entries = %#v", toolEntries)
	}
}

type agentRoundTripFunc func(*http.Request) (*http.Response, error)

func (f agentRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestReActAgentUsesInjectedMemoryManager(t *testing.T) {
	manager := memory.NewManager(nil)
	if _, err := manager.AddMessage(llm.UserMessage("restored context"), ""); err != nil {
		t.Fatalf("AddMessage() error = %v", err)
	}

	agent := NewReActAgent(
		nil,
		tool.NewToolRegistry(),
		WithMemoryManager(manager),
	)

	if agent.memoryManager != manager {
		t.Fatal("agent did not retain the injected memory manager")
	}
	messages, err := agent.contextMessages()
	if err != nil {
		t.Fatalf("contextMessages() error = %v", err)
	}
	want := []llm.Message{
		llm.SystemMessage("You are a helpful assistant."),
		llm.UserMessage("restored context"),
	}
	if !reflect.DeepEqual(messages, want) {
		t.Fatalf("context messages = %#v, want %#v", messages, want)
	}
}

func TestReActAgentClearHistoryClearsManagedContext(t *testing.T) {
	agent := NewReActAgent(nil, tool.NewToolRegistry())
	if _, err := agent.memoryManager.AddMessage(llm.UserMessage("hello"), ""); err != nil {
		t.Fatalf("AddMessage() error = %v", err)
	}

	agent.ClearHistory()
	messages, err := agent.contextMessages()
	if err != nil {
		t.Fatalf("contextMessages() error = %v", err)
	}
	if !reflect.DeepEqual(messages, []llm.Message{llm.SystemMessage("You are a helpful assistant.")}) {
		t.Fatalf("messages after clear = %#v", messages)
	}
}

func TestReActAgentCompactsBeforeEachLLMRequest(t *testing.T) {
	requestCount := 0
	transport := agentRoundTripFunc(func(*http.Request) (*http.Response, error) {
		requestCount++
		response := llm.ChatResponse{Choices: []llm.Choice{{
			Message: llm.AssistantMessage("done"),
		}}}
		encoded, err := json.Marshal(response)
		if err != nil {
			return nil, err
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(string(encoded))),
		}, nil
	})
	client, err := llm.NewOpenAICompatibleClient("test-key", "http://example.test", "test-model")
	if err != nil {
		t.Fatalf("NewOpenAICompatibleClient() error = %v", err)
	}
	client.HTTPClient = &http.Client{Transport: transport}
	scheduler := &recordingContextCompactionScheduler{decision: memory.CompactionDecision{
		Action:       memory.CompactionActionMicro,
		BeforeTokens: 600,
		AfterTokens:  400,
		UsageBefore:  0.60,
		UsageAfter:   0.40,
		Reason:       "test compaction",
	}}
	agent := NewReActAgent(
		client,
		tool.NewToolRegistry(),
		WithCompactionScheduler(scheduler),
	)
	var events []Event
	ctx := WithObserver(context.Background(), func(event Event) {
		events = append(events, event)
	})
	answer, err := agent.Run(ctx, "hello")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if answer != "done" || requestCount != 1 {
		t.Fatalf("Run() = (%q, requests=%d), want (done, 1)", answer, requestCount)
	}
	if scheduler.calls != 1 || !reflect.DeepEqual(scheduler.managerLens, []int{1}) {
		t.Fatalf("scheduler calls = %d, manager lengths = %v", scheduler.calls, scheduler.managerLens)
	}
	if len(events) < 1 || events[0].Type != EventMemoryCompaction || events[0].Title != "MICRO" {
		t.Fatalf("first event = %#v, want memory compaction", events)
	}
}

func TestReActAgentStopsWhenCompactionFails(t *testing.T) {
	compactionError := errors.New("summary unavailable")
	scheduler := &recordingContextCompactionScheduler{err: compactionError}
	agent := NewReActAgent(
		nil,
		tool.NewToolRegistry(),
		WithCompactionScheduler(scheduler),
	)
	_, err := agent.Run(context.Background(), "hello")
	if !errors.Is(err, compactionError) {
		t.Fatalf("Run() error = %v, want compaction error", err)
	}
	if scheduler.calls != 1 {
		t.Fatalf("scheduler calls = %d, want 1", scheduler.calls)
	}
}

func TestReActAgentMeasuresCompleteRequestAndCalibratesFromPromptUsage(t *testing.T) {
	transport := agentRoundTripFunc(func(*http.Request) (*http.Response, error) {
		response := llm.ChatResponse{
			Choices: []llm.Choice{{Message: llm.AssistantMessage("done")}},
			Usage:   llm.Usage{PromptTokens: 8451, CompletionTokens: 10, TotalTokens: 8461},
		}
		encoded, err := json.Marshal(response)
		if err != nil {
			return nil, err
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(string(encoded))),
		}, nil
	})
	client, err := llm.NewOpenAICompatibleClient("test-key", "http://example.test", "test-model")
	if err != nil {
		t.Fatalf("NewOpenAICompatibleClient() error = %v", err)
	}
	client.HTTPClient = &http.Client{Transport: transport}
	scheduler := &recordingContextCompactionScheduler{}
	estimator := &recordingRequestTokenEstimator{next: 7000}
	agent := NewReActAgent(
		client,
		tool.NewToolRegistry(),
		WithCompactionScheduler(scheduler),
		WithRequestTokenEstimator(estimator),
	)

	if _, err := agent.Run(context.Background(), "hello"); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !reflect.DeepEqual(scheduler.measured, []int{7000}) {
		t.Fatalf("scheduler measurements = %v, want [7000]", scheduler.measured)
	}
	if !reflect.DeepEqual(estimator.observed, [][2]int{{7000, 8451}}) {
		t.Fatalf("observations = %v, want [(7000,8451)]", estimator.observed)
	}
}

func TestReActAgentRecordsEveryProtocolMessageInTranscript(t *testing.T) {
	toolCall := llm.ToolCall{
		ID:   "call_transcript",
		Type: "function",
		Function: llm.FunctionCall{
			Name:      "missing_tool",
			Arguments: `{}`,
		},
	}
	requestCount := 0
	transport := agentRoundTripFunc(func(*http.Request) (*http.Response, error) {
		requestCount++
		message := llm.AssistantMessage("done")
		if requestCount == 1 {
			message = llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{toolCall}}
		}
		encoded, err := json.Marshal(llm.ChatResponse{Choices: []llm.Choice{{Message: message}}})
		if err != nil {
			return nil, err
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(string(encoded))),
		}, nil
	})
	client, err := llm.NewOpenAICompatibleClient("test-key", "http://example.test", "test-model")
	if err != nil {
		t.Fatalf("NewOpenAICompatibleClient() error = %v", err)
	}
	client.HTTPClient = &http.Client{Transport: transport}
	transcript := &recordingAgentTranscript{}
	agent := NewReActAgent(
		client,
		tool.NewToolRegistry(),
		WithTranscript(transcript, "conversation_1"),
	)

	if _, err := agent.Run(context.Background(), "run tool"); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	wantRoles := []string{"user", "assistant", "tool", "assistant"}
	if len(transcript.entries) != len(wantRoles) {
		t.Fatalf("transcript entry count = %d, want %d", len(transcript.entries), len(wantRoles))
	}
	for index, wantRole := range wantRoles {
		message, ok := transcript.entries[index].Message()
		if !ok || message.Role != wantRole {
			t.Fatalf("transcript entry %d = %#v, want role %s", index, transcript.entries[index], wantRole)
		}
	}
}
