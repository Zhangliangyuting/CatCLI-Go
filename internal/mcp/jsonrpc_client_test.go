package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"testing"
	"time"
)

type testTransport struct {
	receive func(Message)
	failed  func(error)
	sent    chan Message
}

func (t *testTransport) Start(receive func(Message), failed func(error)) error {
	t.receive, t.failed = receive, failed
	return nil
}
func (t *testTransport) Send(ctx context.Context, message Message) error {
	select {
	case t.sent <- message:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (t *testTransport) Close() error { return nil }

func TestJSONRPCMatchesConcurrentOutOfOrderResponses(t *testing.T) {
	transport := &testTransport{sent: make(chan Message, 64)}
	client, err := NewJSONRPCClient(transport, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	var wait sync.WaitGroup
	for index := 0; index < 64; index++ {
		wait.Go(func() {
			var result int
			if err := client.Call(context.Background(), "echo", index, &result); err != nil {
				t.Error(err)
				return
			}
			if result != index {
				t.Errorf("result=%d, want %d", result, index)
			}
		})
	}
	requests := make([]Message, 64)
	for i := range requests {
		requests[i] = <-transport.sent
	}
	for i := len(requests) - 1; i >= 0; i-- {
		request := requests[i]
		transport.receive(Message{JSONRPC: "2.0", ID: request.ID, Result: request.Params})
	}
	wait.Wait()
	if len(client.pending) != 0 {
		t.Fatal("pending requests leaked")
	}
}

func TestJSONRPCTimeoutNotifiesCancellationAndIgnoresLateResponse(t *testing.T) {
	transport := &testTransport{sent: make(chan Message, 8)}
	client, _ := NewJSONRPCClient(transport, 20*time.Millisecond)
	defer client.Close()
	err := client.Call(context.Background(), "tools/call", map[string]any{}, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error=%v", err)
	}
	request := <-transport.sent
	select {
	case notification := <-transport.sent:
		if notification.Method != "notifications/cancelled" {
			t.Fatal(notification.Method)
		}
		var params struct {
			RequestID json.RawMessage `json:"requestId"`
		}
		if err := json.Unmarshal(notification.Params, &params); err != nil || string(params.RequestID) != string(request.ID) {
			t.Fatalf("cancel params=%s", notification.Params)
		}
	case <-time.After(time.Second):
		t.Fatal("missing cancellation")
	}
	transport.receive(Message{JSONRPC: "2.0", ID: request.ID, Result: json.RawMessage(`{}`)})
	if len(client.pending) != 0 {
		t.Fatal("pending requests leaked")
	}
}

func TestJSONRPCDisconnectReleasesPendingAndAnswersServerRequests(t *testing.T) {
	transport := &testTransport{sent: make(chan Message, 8)}
	client, _ := NewJSONRPCClient(transport, time.Second)
	defer client.Close()
	transport.receive(Message{JSONRPC: "2.0", ID: json.RawMessage(`"ping-id"`), Method: "ping"})
	pingReply := <-transport.sent
	if string(pingReply.Result) != "{}" || string(pingReply.ID) != `"ping-id"` {
		t.Fatalf("reply=%+v", pingReply)
	}
	transport.receive(Message{JSONRPC: "2.0", ID: json.RawMessage(`99`), Method: "sampling/createMessage"})
	if reply := <-transport.sent; reply.Error == nil || reply.Error.Code != -32601 {
		t.Fatalf("reply=%+v", reply)
	}
	done := make(chan error, 1)
	go func() { done <- client.Call(context.Background(), "wait", nil, nil) }()
	<-transport.sent
	transport.failed(io.EOF)
	if err := <-done; !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	if err := client.Call(context.Background(), "wait", nil, nil); !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
}

func TestConcurrentInitializationHonorsWaitingCallerCancellation(t *testing.T) {
	transport := &testTransport{sent: make(chan Message, 8)}
	rpc, _ := NewJSONRPCClient(transport, time.Second)
	defer rpc.Close()
	client := NewClient(rpc)
	firstCtx, cancelFirst := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- client.Initialize(firstCtx) }()
	<-transport.sent
	secondCtx, cancelSecond := context.WithCancel(context.Background())
	cancelSecond()
	secondDone := make(chan error, 1)
	go func() { secondDone <- client.Initialize(secondCtx) }()
	select {
	case err := <-secondDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(100 * time.Millisecond):
		cancelFirst()
		<-done
		t.Fatal("initialization mutex ignored cancellation")
	}
	cancelFirst()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
