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

func TestChatSendsConfiguredMaxTokens(t *testing.T) {
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
	if _, err := client.ChatWithOptions(
		context.Background(),
		[]Message{UserMessage("hello")},
		nil,
		ChatOptions{MaxTokens: 32 * 1024},
	); err != nil {
		t.Fatalf("Chat() error = %v", err)
	}
	if received.MaxTokens != 32*1024 {
		t.Fatalf("request MaxTokens = %d, want %d", received.MaxTokens, 32*1024)
	}
}

func TestChatOmitsMaxTokensByDefault(t *testing.T) {
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
	if _, err := client.Chat(context.Background(), []Message{UserMessage("hello")}, nil); err != nil {
		t.Fatalf("Chat() error = %v", err)
	}
	if requestHasMaxTokens {
		t.Fatal("ordinary chat request unexpectedly contains max_tokens")
	}
}

func TestChatSendsSystemContext(t *testing.T) {
	for _, model := range []string{"deepseek-v4-pro", "other-compatible-model"} {
		t.Run(model, func(t *testing.T) {
			var received ChatRequest
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := json.NewDecoder(r.Body).Decode(&received); err != nil {
					t.Errorf("decode request: %v", err)
					http.Error(w, "invalid request", http.StatusBadRequest)
					return
				}
				_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
			}))
			defer server.Close()

			client, err := NewOpenAICompatibleClient("test-api-key", server.URL, model)
			if err != nil {
				t.Fatal(err)
			}
			messages := []Message{SystemMessage("agent policy"), SystemMessage("project fact"), UserMessage("question")}
			if _, err := client.Chat(context.Background(), messages, nil); err != nil {
				t.Fatal(err)
			}
			if len(received.Messages) != 3 || received.Messages[0].Role != "system" || received.Messages[1].Role != "system" || received.Messages[1].Content != "project fact" || received.Messages[2].Role != "user" {
				t.Fatalf("received messages = %#v", received.Messages)
			}
		})
	}
}

func TestChatCancelsHTTPRequest(t *testing.T) {
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
		_, err := client.Chat(ctx, []Message{
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
			t.Fatalf("Chat() error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Chat() did not return after cancellation")
	}
}

func TestDisableThinkingIfSupportedKeepsOtherOptions(t *testing.T) {
	for _, test := range []struct {
		model        string
		wantThinking bool
	}{
		{model: " DeepSeek-V4-Pro ", wantThinking: true},
		{model: "gpt-5", wantThinking: false},
		{model: "other-compatible-model", wantThinking: false},
	} {
		t.Run(test.model, func(t *testing.T) {
			client := &OpenAICompatibleClient{Model: test.model}
			options := ChatOptions{
				MaxTokens:      1024,
				ResponseFormat: &ResponseFormat{Type: "json_object"},
			}
			client.DisableThinkingIfSupported(&options)
			if options.MaxTokens != 1024 || options.ResponseFormat == nil || options.ResponseFormat.Type != "json_object" {
				t.Fatalf("other options changed: %#v", options)
			}
			if test.wantThinking {
				if options.Thinking == nil || options.Thinking.Type != "disabled" {
					t.Fatalf("thinking = %#v, want disabled", options.Thinking)
				}
			} else if options.Thinking != nil {
				t.Fatalf("thinking = %#v, want omitted", options.Thinking)
			}
		})
	}
}
