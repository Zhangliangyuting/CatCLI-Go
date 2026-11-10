package multiagent

import (
	baseagent "AgentCLI/internal/agent"
	"AgentCLI/internal/memory"
	"AgentCLI/internal/plan"
	"AgentCLI/internal/rag"
	"context"
	"path/filepath"
	"testing"
)

func TestReferenceContextEmitsWorkspaceEventWithTaskID(t *testing.T) {
	store, err := rag.NewSQLiteStore(filepath.Join(t.TempDir(), "rag.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.ResetIndex(t.Context(), rag.IndexMetadata{EmbeddingModel: "test", EmbeddingDimensions: 2, ChunkerVersion: "test"}); err != nil {
		t.Fatal(err)
	}
	if err := store.ReplaceFile(t.Context(), rag.IndexedFile{Path: "evidence.go", ContentHash: "hash"}, []rag.EmbeddedChunk{{ID: "chunk-1", ContentHash: "chunk-hash", Chunk: rag.Chunk{Path: "evidence.go", Content: "query evidence", Symbol: "query"}, Embedding: []float32{1, 0}}}); err != nil {
		t.Fatal(err)
	}
	retriever, err := rag.NewRetriever(store, nil)
	if err != nil {
		t.Fatal(err)
	}
	formatter, err := rag.NewSearchResultFormatter(memory.ApproxTokenCounter{})
	if err != nil {
		t.Fatal(err)
	}
	provider, err := rag.NewContextProvider(retriever, formatter, 2048, 5)
	if err != nil {
		t.Fatal(err)
	}
	var events []baseagent.Event
	ctx := baseagent.WithObserver(context.Background(), func(event baseagent.Event) { events = append(events, event) })
	ctx = baseagent.WithEventTaskID(ctx, "task-1")
	messages, err := buildReferenceContext(ctx, provider, "query")
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 || len(events) != 1 || events[0].Type != baseagent.EventWorkspaceRetrieval || events[0].TaskID != "task-1" ||
		events[0].WorkspaceRetrieval == nil || len(events[0].WorkspaceRetrieval.Matches) != 1 {
		t.Fatalf("messages=%+v events=%+v", messages, events)
	}
}

func TestSchedulerEmitsTaskEventsAndAddsTaskID(t *testing.T) {
	p := plan.NewPlan("plan_1", "执行计划", "验证事件")
	if err := p.AddTask(plan.NewTask(
		"task_1",
		"读取文件",
		"读取 README",
		plan.ANALYSIS,
		nil,
	)); err != nil {
		t.Fatalf("AddTask() error = %v", err)
	}
	if err := p.TopologicalSort(); err != nil {
		t.Fatalf("TopologicalSort() error = %v", err)
	}

	var events []baseagent.Event
	scheduler := newTaskScheduler(1, 0)
	executor := newTestWorkerExecutor(t, "完成", nil)
	ctx := baseagent.WithObserver(context.Background(), func(event baseagent.Event) {
		events = append(events, event)
	})

	if _, err := scheduler.Execute(
		ctx,
		p,
		executor,
	); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}

	wantTypes := []baseagent.EventType{
		baseagent.EventTaskStarted,
		baseagent.EventMemoryRetrieval,
		baseagent.EventTokenUsage,
		baseagent.EventTaskCompleted,
		baseagent.EventPlanCompleted,
	}
	if len(events) != len(wantTypes) {
		t.Fatalf("event count = %d, want %d: %#v", len(events), len(wantTypes), events)
	}
	for i, wantType := range wantTypes {
		if events[i].Type != wantType {
			t.Errorf("events[%d].Type = %q, want %q", i, events[i].Type, wantType)
		}
	}
	for _, index := range []int{1, 2} {
		if events[index].TaskID != "task_1" {
			t.Fatalf("worker event %d TaskID = %q, want task_1", index, events[index].TaskID)
		}
	}
}
