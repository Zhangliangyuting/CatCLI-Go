package execution

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestRecorderSharesOneRunAcrossConcurrentToolCalls(t *testing.T) {
	store := newTestStore(t)
	recorder := NewRecorder(store, "conversation")
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	run, err := recorder.StartRun(ctx, "parallel tasks", "", ReActRun)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	started := make(chan struct{}, 8)
	release := make(chan struct{})
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			call, err := run.StartToolCall(ctx, ToolCallEventData{ToolCallID: fmt.Sprintf("model-%d", i), ToolName: "read", Arguments: `{}`})
			if err != nil {
				t.Error(err)
				return
			}
			started <- struct{}{}
			select {
			case <-release:
			case <-ctx.Done():
				t.Error(ctx.Err())
				return
			}
			if err := run.CompleteToolCall(ctx, call.CallID, "evidence", nil); err != nil {
				t.Error(err)
			}
		}(i)
	}
	// Every call can remain in flight independently; recording must not hold
	// the run mutex between start and completion.
	for range 8 {
		select {
		case <-started:
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	close(release)
	wg.Wait()
	err = run.FinishRun(ctx, "done", nil)
	if err != nil {
		t.Fatal(err)
	}
	events, err := recorder.LoadEvents(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	runs, err := Replay(events)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || len(runs[0].Calls) != 8 || runs[0].Status != Completed {
		t.Fatalf("unexpected state: %+v", runs)
	}
	for _, call := range runs[0].Calls {
		if call.Status != Returned || call.Result != "evidence" {
			t.Fatal(call)
		}
	}
}

type resultFailureStore struct{ EventStore }

func (s resultFailureStore) Append(ctx context.Context, e Event) (Event, error) {
	if e.Type == ToolCallCompleted {
		return Event{}, errors.New("result write failed")
	}
	return s.EventStore.Append(ctx, e)
}

func TestRecorderKeepsWriteFailureEvenWhenEngineIgnoresIt(t *testing.T) {
	store := newTestStore(t)
	recorder := NewRecorder(resultFailureStore{store}, "conversation")
	ctx := t.Context()
	run, err := recorder.StartRun(ctx, "task", "", ReActRun)
	if err != nil {
		t.Fatal(err)
	}
	call, err := run.StartToolCall(ctx, ToolCallEventData{ToolCallID: "one", ToolName: "write", Arguments: `{}`})
	if err != nil {
		t.Fatal(err)
	}
	if err := run.CompleteToolCall(ctx, call.CallID, "result", nil); err == nil {
		t.Fatal("expected result persistence failure")
	}
	// Even a caller that ignores the first error cannot record another start
	// or claim that this run finished successfully.
	if _, err := run.StartToolCall(ctx, ToolCallEventData{ToolCallID: "two", ToolName: "write", Arguments: `{}`}); err == nil {
		t.Fatal("accepted another start after persistence failure")
	}
	err = run.FinishRun(ctx, "done", nil)
	if err == nil {
		t.Fatal("completed after persistence failure")
	}
	events, _ := store.Load(t.Context(), "conversation", 0)
	runs, err := Replay(events)
	if err != nil {
		t.Fatal(err)
	}
	if runs[0].Status != Interrupted || runs[0].Calls[0].Status != Unknown {
		t.Fatal(runs)
	}
}

func TestRecorderRejectsDuplicateCompletionAndUseAfterFinish(t *testing.T) {
	recorder := NewRecorder(newTestStore(t), "conversation")
	run, err := recorder.StartRun(t.Context(), "task", "", ReActRun)
	if err != nil {
		t.Fatal(err)
	}
	call, err := run.StartToolCall(t.Context(), ToolCallEventData{ToolCallID: "model", ToolName: "read"})
	if err != nil {
		t.Fatal(err)
	}
	if err := run.FinishRun(t.Context(), "", nil); err == nil {
		t.Fatal("completed with pending call")
	}
	if err := run.CompleteToolCall(t.Context(), call.CallID, "result", nil); err != nil {
		t.Fatal(err)
	}
	if err := run.CompleteToolCall(t.Context(), call.CallID, "result", nil); err == nil {
		t.Fatal("accepted duplicate result")
	}
	if err := run.FinishRun(t.Context(), "done", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := run.StartToolCall(t.Context(), ToolCallEventData{ToolCallID: "next", ToolName: "read"}); err == nil {
		t.Fatal("accepted tool call after run finished")
	}
}
