package multiagent

import (
	"AgentCLI/internal/llm"
	"AgentCLI/internal/memory"
	"AgentCLI/internal/tool"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

type recordedChatRequests struct {
	mu       sync.Mutex
	requests []llm.ChatRequest
}

func newTestMemoryRuntime(
	t *testing.T,
	client *llm.OpenAICompatibleClient,
) *memory.ConversationRuntime {
	t.Helper()
	runtime, err := memory.NewConversationRuntime(
		client,
		memory.NewMemoryRetriever(nil),
		t.TempDir(),
		"",
		100_000,
		4096,
	)
	if err != nil {
		t.Fatalf("NewConversationRuntime() error = %v", err)
	}
	return runtime
}

func newTestWorkerExecutor(
	t *testing.T,
	workerResponse string,
	reviewer *ReviewerSubAgent,
) *TaskExecutor {
	t.Helper()
	client, _ := newRecordedChatClient(t, workerResponse)
	return newTaskExecutor(
		client,
		tool.NewToolRegistry(),
		newTestMemoryRuntime(t, client),
		reviewer,
	)
}

func (recording *recordedChatRequests) append(request llm.ChatRequest) {
	recording.mu.Lock()
	defer recording.mu.Unlock()
	recording.requests = append(recording.requests, request)
}

func (recording *recordedChatRequests) count() int {
	recording.mu.Lock()
	defer recording.mu.Unlock()
	return len(recording.requests)
}

func (recording *recordedChatRequests) lastMessages() []llm.Message {
	recording.mu.Lock()
	defer recording.mu.Unlock()
	if len(recording.requests) == 0 {
		return nil
	}
	return append([]llm.Message(nil), recording.requests[len(recording.requests)-1].Messages...)
}

func newRecordedChatClient(
	t *testing.T,
	response string,
) (*llm.OpenAICompatibleClient, *recordedChatRequests) {
	t.Helper()
	recording := &recordedChatRequests{}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		defer request.Body.Close()
		var chatRequest llm.ChatRequest
		if err := json.NewDecoder(request.Body).Decode(&chatRequest); err != nil {
			http.Error(writer, err.Error(), http.StatusBadRequest)
			return
		}
		recording.append(chatRequest)
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(llm.ChatResponse{
			Choices: []llm.Choice{{
				Message:      llm.AssistantMessage(response),
				FinishReason: "stop",
			}},
		})
	}))
	t.Cleanup(server.Close)
	return &llm.OpenAICompatibleClient{
		APIKey:     "test",
		BaseURL:    server.URL,
		Model:      "test-model",
		HTTPClient: server.Client(),
	}, recording
}
