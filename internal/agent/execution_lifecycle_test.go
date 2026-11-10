package agent

import (
	"AgentCLI/internal/execution"
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestRunDeferredExecutionLifecycle(t *testing.T) {
	for _, mode := range []string{"success", "cancelled", "panic", "finish failure", "execution and finish failure"} {
		t.Run(mode, func(t *testing.T) {
			store, err := execution.NewJSONLEventStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			var sink execution.EventStore = store
			if mode == "finish failure" {
				sink = failingExecutionStore{store, execution.RunCompleted}
			}
			if mode == "execution and finish failure" {
				sink = failingExecutionStore{store, execution.RunFailed}
			}
			a, _ := newLoggedTestAgent(t, sink, nil, nil)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			executionErr := errors.New("model unavailable")
			switch mode {
			case "cancelled":
				a.llmClient.HTTPClient.Transport = agentRoundTripFunc(func(*http.Request) (*http.Response, error) {
					cancel()
					return nil, context.Canceled
				})
			case "panic":
				a.llmClient.HTTPClient.Transport = agentRoundTripFunc(func(*http.Request) (*http.Response, error) {
					panic("model panic")
				})
			case "execution and finish failure":
				a.llmClient.HTTPClient.Transport = agentRoundTripFunc(func(*http.Request) (*http.Response, error) {
					return nil, executionErr
				})
			}
			var answer string
			var runErr error
			var caught any
			func() {
				defer func() { caught = recover() }()
				answer, runErr = a.Run(ctx, "task")
			}()
			want := execution.Interrupted
			switch mode {
			case "success":
				want = execution.Completed
				if runErr != nil || answer != "done" {
					t.Fatalf("answer=%q error=%v", answer, runErr)
				}
			case "cancelled":
				want = execution.Cancelled
				if !errors.Is(runErr, context.Canceled) {
					t.Fatalf("lost cancellation: %v", runErr)
				}
			case "panic":
				if caught != "model panic" {
					t.Fatalf("panic was swallowed or changed: %v", caught)
				}
			default:
				if runErr == nil || !strings.Contains(runErr.Error(), "injected disk failure") {
					t.Fatalf("lost deferred persistence error: %v", runErr)
				}
				if mode == "execution and finish failure" && !errors.Is(runErr, executionErr) {
					t.Fatalf("lost execution error: %v", runErr)
				}
			}
			if mode != "panic" && caught != nil {
				t.Fatalf("unexpected panic: %v", caught)
			}
			events, err := store.Load(t.Context(), "conversation", 0)
			if err != nil {
				t.Fatal(err)
			}
			runs, err := execution.Replay(events)
			if err != nil || len(runs) != 1 || runs[0].Status != want {
				t.Fatalf("runs=%+v error=%v, want %s", runs, err, want)
			}
		})
	}
}
