package agent

import (
	"AgentCLI/internal/execution"
	"AgentCLI/internal/llm"
	"AgentCLI/internal/tool"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type failingExecutionStore struct {
	execution.EventStore
	failType execution.EventType
}

func (s failingExecutionStore) Append(ctx context.Context, event execution.Event) (execution.Event, error) {
	if event.Type == s.failType {
		return execution.Event{}, errors.New("injected disk failure")
	}
	return s.EventStore.Append(ctx, event)
}

// Shared by execution/resume tests; model responses stay entirely in process.
func newLoggedTestAgent(t *testing.T, store execution.EventStore, handler tool.Handler, calls []llm.ToolCall) (*ReActAgent, *int) {
	t.Helper()
	requests := 0
	client, err := llm.NewOpenAICompatibleClient("test", "http://example.test", "test")
	if err != nil {
		t.Fatal(err)
	}
	client.HTTPClient = &http.Client{Transport: agentRoundTripFunc(func(*http.Request) (*http.Response, error) {
		requests++
		message := llm.AssistantMessage("done")
		if requests == 1 && len(calls) > 0 {
			message = llm.Message{Role: "assistant", ToolCalls: calls}
		}
		data, err := json.Marshal(llm.ChatResponse{Choices: []llm.Choice{{Message: message}}})
		if err != nil {
			return nil, err
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(string(data))),
		}, nil
	})}
	registry := newApprovedTestRegistry()
	registry.RegisterTool(tool.Definition{
		Type: "function",
		FunctionDefinition: tool.FunctionDefinition{
			Name:       "write",
			Parameters: map[string]any{"type": "object"},
		},
	}, handler)
	return NewReActAgent(client, registry, WithRecorder(execution.NewRecorder(store, "conversation"))), &requests
}

func testExecutionCall(id, arguments string) llm.ToolCall {
	return llm.ToolCall{
		ID:       id,
		Type:     "function",
		Function: llm.FunctionCall{Name: "write", Arguments: arguments},
	}
}
