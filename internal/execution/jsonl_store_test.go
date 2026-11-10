package execution

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func newTestStore(t *testing.T) *JSONLEventStore {
	t.Helper()
	s, err := NewJSONLEventStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func appendEvent(t *testing.T, s EventStore, e Event) Event {
	t.Helper()
	e.ConversationID = "conversation"
	if e.RunID == "" {
		e.RunID = "run"
	}
	stored, err := s.Append(context.Background(), e)
	if err != nil {
		t.Fatal(err)
	}
	return stored
}

func TestJSONLStoreReopenAndReplayInterruptedRun(t *testing.T) {
	s := newTestStore(t)
	appendEvent(t, s, Event{EventHeader: EventHeader{Type: RunStarted}, Run: &RunEventData{Input: "read then edit"}})
	appendEvent(t, s, Event{EventHeader: EventHeader{Type: ToolCallStarted}, ToolCall: &ToolCallEventData{CallID: "one", ToolCallID: "protocol-1", ToolName: "read", Arguments: `{"path":"a.go"}`}})
	appendEvent(t, s, Event{EventHeader: EventHeader{Type: ToolCallCompleted}, ToolCall: &ToolCallEventData{CallID: "one", ToolCallID: "protocol-1", ToolName: "read", Result: "source"}})
	appendEvent(t, s, Event{EventHeader: EventHeader{Type: ToolCallStarted}, ToolCall: &ToolCallEventData{CallID: "two", ToolCallID: "protocol-2", ToolName: "edit", Arguments: `{}`}})
	reopened, _ := NewJSONLEventStore(s.root)
	events, err := reopened.Load(context.Background(), "conversation", 0)
	if err != nil {
		t.Fatal(err)
	}
	runs, err := Replay(events)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].Status != Interrupted || len(runs[0].Calls) != 2 || runs[0].Calls[0].Status != Returned || runs[0].Calls[0].Result != "source" || runs[0].Calls[1].Status != Unknown {
		t.Fatalf("unexpected replay: %+v", runs)
	}
	later, err := reopened.Load(context.Background(), "conversation", 2)
	if err != nil || len(later) != 2 || later[0].Sequence != 3 {
		t.Fatalf("cursor: %v %v", later, err)
	}
}

func TestJSONLStoreIgnoresAndRepairsUnterminatedTail(t *testing.T) {
	for _, tail := range []string{`{"sequence":`, `{"valid":"but no newline"}`} {
		t.Run(tail, func(t *testing.T) {
			s := newTestStore(t)
			appendEvent(t, s, Event{EventHeader: EventHeader{Type: RunStarted}, Run: &RunEventData{}})
			path := filepath.Join(s.root, "conversation.jsonl")
			f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.WriteString(tail); err != nil {
				t.Fatal(err)
			}
			f.Close()
			reopened, _ := NewJSONLEventStore(s.root)
			events, err := reopened.Load(context.Background(), "conversation", 0)
			if err != nil || len(events) != 1 {
				t.Fatalf("tail recovery: %v %v", events, err)
			}
			e := appendEvent(t, reopened, Event{EventHeader: EventHeader{Type: RunCompleted}, Run: &RunEventData{Answer: "done"}})
			if e.Sequence != 2 {
				t.Fatal(e.Sequence)
			}
			events, err = reopened.Load(context.Background(), "conversation", 0)
			if err != nil || len(events) != 2 {
				t.Fatalf("append after tail: %v %v", events, err)
			}
			if _, err = Replay(events); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestJSONLStoreRejectsCommittedCorruption(t *testing.T) {
	s := newTestStore(t)
	appendEvent(t, s, Event{EventHeader: EventHeader{Type: RunStarted}, Run: &RunEventData{}})
	path := filepath.Join(s.root, "conversation.jsonl")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString("{bad}\n")
	f.Close()
	before, _ := os.ReadFile(path)
	if _, err := s.Load(context.Background(), "conversation", 0); err == nil {
		t.Fatal("accepted corrupt line")
	}
	if _, err := s.Append(context.Background(), Event{EventHeader: EventHeader{ConversationID: "conversation", RunID: "run", Type: RunCompleted}, Run: &RunEventData{}}); err == nil {
		t.Fatal("appended after corruption")
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("corruption was silently truncated")
	}
}

func TestJSONLStoreConcurrentAppendAssignsOrderedSequences(t *testing.T) {
	s := newTestStore(t)
	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := s.Append(context.Background(), Event{EventHeader: EventHeader{ConversationID: "conversation", RunID: fmt.Sprintf("run-%d", i), Type: RunStarted}, Run: &RunEventData{}})
			if err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	events, err := s.Load(context.Background(), "conversation", 0)
	if err != nil || len(events) != 24 {
		t.Fatalf("events=%d error=%v", len(events), err)
	}
	if _, err := Replay(events); err != nil {
		t.Fatal(err)
	}
}

func TestJSONLStoreRejectsPathsCancellationAndOversizedEvents(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Load(context.Background(), "../escape", 0); err == nil {
		t.Fatal("accepted path traversal")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Append(ctx, Event{EventHeader: EventHeader{ConversationID: "conversation", RunID: "run", Type: RunStarted}, Run: &RunEventData{}}); err == nil {
		t.Fatal("accepted cancelled append")
	}
	if _, err := s.Append(context.Background(), Event{EventHeader: EventHeader{ConversationID: "conversation", RunID: "run", Type: RunStarted}, Run: &RunEventData{Input: strings.Repeat("x", maxEventBytes)}}); err == nil {
		t.Fatal("accepted oversized event")
	}
	events, err := s.Load(context.Background(), "conversation", 0)
	if err != nil || len(events) != 0 {
		t.Fatalf("failed append changed log: %v %v", events, err)
	}
}

func TestJSONLStoreCachedAppendAndReopenContinueSequence(t *testing.T) {
	s := newTestStore(t)
	for i := 1; i <= 5; i++ {
		e := appendEvent(t, s, Event{EventHeader: EventHeader{Type: RunStarted, RunID: fmt.Sprintf("run-%d", i)}, Run: &RunEventData{}})
		if e.Sequence != uint64(i) {
			t.Fatalf("sequence=%d, want %d", e.Sequence, i)
		}
	}
	info, err := os.Stat(filepath.Join(s.root, "conversation.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	position, exists := s.positions["conversation"]
	if !exists || position.lastSequence != 5 || position.committedBytes != info.Size() {
		t.Fatalf("cached position does not match committed file: %+v size=%d", position, info.Size())
	}
	reopened, _ := NewJSONLEventStore(s.root)
	e := appendEvent(t, reopened, Event{EventHeader: EventHeader{Type: RunCompleted}, Run: &RunEventData{}})
	if e.Sequence != 6 {
		t.Fatalf("reopen lost sequence: %d", e.Sequence)
	}
	events, err := reopened.Load(t.Context(), "conversation", 0)
	if err != nil || len(events) != 6 {
		t.Fatalf("events=%d error=%v", len(events), err)
	}
}

func TestJSONLStoreLoadSeedsCacheBeforeAppending(t *testing.T) {
	s := newTestStore(t)
	appendEvent(t, s, Event{EventHeader: EventHeader{Type: RunStarted}, Run: &RunEventData{}})
	appendEvent(t, s, Event{EventHeader: EventHeader{Type: RunCompleted}, Run: &RunEventData{}})
	reopened, _ := NewJSONLEventStore(s.root)
	// Filtering returned records must not reset the cached sequence to the
	// number of selected records: the cache describes the complete file.
	if _, err := reopened.Load(t.Context(), "conversation", 2); err != nil {
		t.Fatal(err)
	}
	if reopened.positions["conversation"].lastSequence != 2 {
		t.Fatal("load did not cache full log position")
	}
	e := appendEvent(t, reopened, Event{EventHeader: EventHeader{Type: RunStarted, RunID: "next"}, Run: &RunEventData{}})
	if e.Sequence != 3 {
		t.Fatalf("sequence after filtered load=%d", e.Sequence)
	}
}

func TestJSONLStoreCachedAppendDetectsReplacement(t *testing.T) {
	s := newTestStore(t)
	e := appendEvent(t, s, Event{EventHeader: EventHeader{Type: RunStarted, RunID: "old"}, Run: &RunEventData{}})
	path := filepath.Join(s.root, "conversation.jsonl")
	// Replace the file with a different inode but exactly the same byte size.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data = []byte(strings.Replace(string(data), `"run_id":"old"`, `"run_id":"new"`, 1))
	other := filepath.Join(s.root, "replacement")
	if err := os.WriteFile(other, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(other, path); err != nil {
		t.Fatal(err)
	}
	oldInfo := s.positions["conversation"].fileInfo
	e = appendEvent(t, s, Event{EventHeader: EventHeader{Type: RunCompleted, RunID: "new"}, Run: &RunEventData{}})
	if e.Sequence != 2 || os.SameFile(oldInfo, s.positions["conversation"].fileInfo) {
		t.Fatal("replacement was not rescanned")
	}
	events, err := s.Load(t.Context(), "conversation", 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Replay(events); err != nil {
		t.Fatal(err)
	}
}

func TestJSONLStoreOpenFailureInvalidatesCachedPosition(t *testing.T) {
	s := newTestStore(t)
	appendEvent(t, s, Event{EventHeader: EventHeader{Type: RunStarted}, Run: &RunEventData{}})
	path := filepath.Join(s.root, "conversation.jsonl")
	backup := path + ".backup"
	if err := os.Rename(path, backup); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	_, err := s.Append(t.Context(), Event{EventHeader: EventHeader{ConversationID: "conversation", RunID: "run", Type: RunCompleted}, Run: &RunEventData{}})
	if err == nil {
		t.Fatal("expected failed open")
	}
	if _, exists := s.positions["conversation"]; exists {
		t.Fatal("failed open retained cache")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(backup, path); err != nil {
		t.Fatal(err)
	}
	e := appendEvent(t, s, Event{EventHeader: EventHeader{Type: RunCompleted}, Run: &RunEventData{}})
	if e.Sequence != 2 {
		t.Fatalf("recovery sequence=%d", e.Sequence)
	}
}
