package llm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestChatContextSendsConfiguredMaxTokens(t *testing.T) {
	var received ChatRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer server.Close()

	client, err := NewOpenAICompatibleClient("test-api-key", server.URL, "test-model")
	if err != nil {
		t.Fatalf("NewOpenAICompatibleClient() error = %v", err)
	}
	if _, err := client.ChatContextWithOptions(
		context.Background(),
		[]Message{UserMessage("hello")},
		nil,
		ChatOptions{MaxTokens: 32 * 1024},
	); err != nil {
		t.Fatalf("ChatContext() error = %v", err)
	}
	if received.MaxTokens != 32*1024 {
		t.Fatalf("request MaxTokens = %d, want %d", received.MaxTokens, 32*1024)
	}
}

func TestChatContextOmitsMaxTokensByDefault(t *testing.T) {
	requestHasMaxTokens := true
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		_, requestHasMaxTokens = request["max_tokens"]
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer server.Close()

	client, err := NewOpenAICompatibleClient("test-api-key", server.URL, "test-model")
	if err != nil {
		t.Fatalf("NewOpenAICompatibleClient() error = %v", err)
	}
	if _, err := client.ChatContext(context.Background(), []Message{UserMessage("hello")}, nil); err != nil {
		t.Fatalf("ChatContext() error = %v", err)
	}
	if requestHasMaxTokens {
		t.Fatal("ordinary chat request unexpectedly contains max_tokens")
	}
}

func TestChatContextCancelsHTTPRequest(t *testing.T) {
	requestStarted := make(chan struct{})
	transport := roundTripFunc(
		func(r *http.Request) (*http.Response, error) {
			close(requestStarted)
			<-r.Context().Done()
			return nil, r.Context().Err()
		},
	)

	client, err := NewOpenAICompatibleClient(
		"test-api-key",
		"http://example.test",
		"test-model",
	)
	if err != nil {
		t.Fatalf("NewOpenAICompatibleClient() error = %v", err)
	}
	client.HTTPClient = &http.Client{Transport: transport}

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := client.ChatContext(ctx, []Message{
			UserMessage("等待取消"),
		}, nil)
		result <- err
	}()

	select {
	case <-requestStarted:
	case <-time.After(time.Second):
		t.Fatal("HTTP request did not start")
	}

	cancel()

	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("ChatContext() error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("ChatContext() did not return after cancellation")
	}
}
