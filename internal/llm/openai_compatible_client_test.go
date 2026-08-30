package llm

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
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
