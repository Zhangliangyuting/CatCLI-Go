package main

import (
	"AgentCLI/internal/execution"
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadExecutionLogValidatesWithoutChangingHistory(t *testing.T) {
	root := t.TempDir()
	store, _ := execution.NewJSONLEventStore(filepath.Join(root, "execution"))
	for _, e := range []execution.Event{
		{EventHeader: execution.EventHeader{Type: execution.RunStarted, RunID: "finished"}, Run: &execution.RunEventData{}},
		{EventHeader: execution.EventHeader{Type: execution.RunCompleted, RunID: "finished"}, Run: &execution.RunEventData{}},
		{EventHeader: execution.EventHeader{Type: execution.RunStarted, RunID: "cancelled"}, Run: &execution.RunEventData{}},
		{EventHeader: execution.EventHeader{Type: execution.RunCancelled, RunID: "cancelled"}, Run: &execution.RunEventData{}},
		{EventHeader: execution.EventHeader{Type: execution.RunStarted, RunID: "unfinished"}, Run: &execution.RunEventData{}},
		{EventHeader: execution.EventHeader{Type: execution.ToolCallStarted, RunID: "unfinished"}, ToolCall: &execution.ToolCallEventData{CallID: "call", ToolCallID: "model-call", ToolName: "edit"}},
	} {
		e.ConversationID = "conversation"
		if _, err := store.Append(context.Background(), e); err != nil {
			t.Fatal(err)
		}
	}
	_, runs, err := loadExecutionLog(root, "conversation")
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 3 || runs[0].Status != execution.Completed || runs[1].Status != execution.Cancelled || runs[2].Status != execution.Interrupted {
		t.Fatalf("unexpected startup state: %+v", runs)
	}

	events, err := store.Load(context.Background(), "conversation", 0)
	if err != nil || len(events) != 6 {
		t.Fatalf("inspection changed log: %v %v", events, err)
	}
}

func TestPromptLastRunContinueOrAbandon(t *testing.T) {
	for _, choice := range []string{"y\n", "n\n", "\n", "invalid\ny\n"} {
		t.Run(choice, func(t *testing.T) {
			store, _ := execution.NewJSONLEventStore(t.TempDir())
			for _, id := range []string{"older", "latest"} {
				if _, err := store.Append(t.Context(), execution.Event{EventHeader: execution.EventHeader{ConversationID: "conversation", RunID: id, Type: execution.RunStarted}, Run: &execution.RunEventData{Input: "task-" + id}}); err != nil {
					t.Fatal(err)
				}
			}
			var out bytes.Buffer
			resumed := false
			events, err := store.Load(t.Context(), "conversation", 0)
			if err != nil {
				t.Fatal(err)
			}
			runs, err := execution.Replay(events)
			if err != nil {
				t.Fatal(err)
			}
			err = promptInterruptedRun(t.Context(), promptNoLoadStore{store}, "conversation", runs, bufio.NewReader(strings.NewReader(choice)), &out, func(ctx context.Context, run execution.RunState) (string, error) {
				resumed = true
				if run.RunID != "latest" {
					t.Fatalf("wrong source: %s", run.RunID)
				}
				_, err := store.Append(ctx, execution.Event{EventHeader: execution.EventHeader{ConversationID: "conversation", RunID: "continuation", Type: execution.RunStarted}, Run: &execution.RunEventData{Input: run.Input, ResumedFrom: run.RunID}})
				return "continued", err
			})
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out.String(), "task-latest") || strings.Contains(out.String(), "task-older") {
				t.Fatalf("prompted wrong run: %s", out.String())
			}
			events, _ = store.Load(t.Context(), "conversation", 0)
			runs, err = execution.Replay(events)
			if err != nil {
				t.Fatal(err)
			}
			wantResume := strings.Contains(choice, "y")
			if resumed != wantResume {
				t.Fatalf("resume=%t", resumed)
			}
			want := execution.Abandoned
			if wantResume {
				want = execution.Resumed
			}
			if runs[1].Status != want || runs[0].Status != execution.Interrupted {
				t.Fatalf("unexpected states: %+v", runs)
			}
		})
	}
}

func TestLoadExecutionLogRestoresCheckpointAndSuffix(t *testing.T) {
	root := t.TempDir()
	store, err := execution.NewJSONLEventStore(filepath.Join(root, "execution"))
	if err != nil {
		t.Fatal(err)
	}
	for i := range 50 {
		_, err := store.Append(t.Context(), execution.Event{
			EventHeader: execution.EventHeader{ConversationID: "conversation", RunID: fmt.Sprintf("run-%d", i), Type: execution.RunStarted},
			Run:         &execution.RunEventData{Kind: execution.ReActRun},
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	checkpoint := filepath.Join(root, "execution", "conversation.jsonl.checkpoint.json")
	if _, err := os.Stat(checkpoint); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(t.Context(), execution.Event{
		EventHeader: execution.EventHeader{ConversationID: "conversation", RunID: "run-49", Type: execution.RunCompleted},
		Run:         &execution.RunEventData{Answer: "done"},
	}); err != nil {
		t.Fatal(err)
	}
	for _, corrupt := range []bool{false, true} {
		if corrupt {
			if err := os.WriteFile(checkpoint, []byte("broken snapshot"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		_, runs, err := loadExecutionLog(root, "conversation")
		if err != nil || len(runs) != 50 || runs[49].Status != execution.Completed || runs[49].Answer != "done" {
			t.Fatalf("corrupt=%t runs=%+v error=%v", corrupt, runs, err)
		}
		var out bytes.Buffer
		if err := promptInterruptedRun(t.Context(), nil, "conversation", runs, nil, &out, nil); err != nil || out.Len() != 0 {
			t.Fatalf("checkpoint resurrected an older interrupted Run: %q %v", out.String(), err)
		}
	}
}

func TestPromptDoesNotFallBackToOlderInterruptedRun(t *testing.T) {
	for _, terminal := range []execution.EventType{execution.RunCompleted, execution.RunFailed, execution.RunCancelled, execution.RunAbandoned} {
		t.Run(string(terminal), func(t *testing.T) {
			store, err := execution.NewJSONLEventStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			for _, e := range []execution.Event{
				{EventHeader: execution.EventHeader{RunID: "older", Type: execution.RunStarted}, Run: &execution.RunEventData{Input: "unfinished older task"}},
				{EventHeader: execution.EventHeader{RunID: "latest", Type: execution.RunStarted}, Run: &execution.RunEventData{Input: "latest task"}},
				{EventHeader: execution.EventHeader{RunID: "latest", Type: terminal}, Run: &execution.RunEventData{}},
			} {
				e.ConversationID = "conversation"
				if _, err := store.Append(t.Context(), e); err != nil {
					t.Fatal(err)
				}
			}
			events, err := store.Load(t.Context(), "conversation", 0)
			if err != nil {
				t.Fatal(err)
			}
			runs, err := execution.Replay(events)
			if err != nil {
				t.Fatal(err)
			}
			var out bytes.Buffer
			err = promptInterruptedRun(t.Context(), promptNoLoadStore{store}, "conversation", runs, bufio.NewReader(strings.NewReader("n\n")), &out, func(context.Context, execution.RunState) (string, error) {
				t.Fatal("must not resume an older run")
				return "", nil
			})
			if err != nil || out.Len() != 0 {
				t.Fatalf("unexpected prompt: %q, error: %v", out.String(), err)
			}
			after, err := store.Load(t.Context(), "conversation", 0)
			if err != nil || len(after) != len(events) {
				t.Fatalf("prompt changed history: %v, %v", after, err)
			}
		})
	}
}

func TestPromptWithNoRuns(t *testing.T) {
	var out bytes.Buffer
	if err := promptInterruptedRun(t.Context(), nil, "conversation", nil, nil, &out, nil); err != nil || out.Len() != 0 {
		t.Fatalf("unexpected prompt: %q, error: %v", out.String(), err)
	}
}

func TestPromptEOFDoesNotAbandonRun(t *testing.T) {
	store, _ := execution.NewJSONLEventStore(t.TempDir())
	store.Append(t.Context(), execution.Event{EventHeader: execution.EventHeader{ConversationID: "conversation", RunID: "run", Type: execution.RunStarted}, Run: &execution.RunEventData{}})
	events, err := store.Load(t.Context(), "conversation", 0)
	if err != nil {
		t.Fatal(err)
	}
	runs, err := execution.Replay(events)
	if err != nil {
		t.Fatal(err)
	}
	err = promptInterruptedRun(t.Context(), promptNoLoadStore{store}, "conversation", runs, bufio.NewReader(strings.NewReader("")), io.Discard, func(context.Context, execution.RunState) (string, error) {
		t.Fatal("unexpected resume")
		return "", nil
	})
	if !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	events, _ = store.Load(t.Context(), "conversation", 0)
	if len(events) != 1 {
		t.Fatal("EOF changed history")
	}
}

// Startup already loaded and replayed the log. Prompting must only use the
// supplied snapshot, which Resume also consumes without reloading history.
type promptNoLoadStore struct{ execution.EventStore }

func (promptNoLoadStore) Load(context.Context, string, uint64) ([]execution.Event, error) {
	return nil, errors.New("prompt must not load execution events again")
}
