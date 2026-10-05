package main

import (
	"AgentCLI/internal/llm"
	"AgentCLI/internal/memory"
	"testing"
)

func TestConversationMemoryRuntimeCreatesSavesAndResumes(t *testing.T) {
	client, err := llm.NewOpenAICompatibleClient("test-key", "http://example.test", "test-model")
	if err != nil {
		t.Fatalf("NewOpenAICompatibleClient() error = %v", err)
	}
	root := t.TempDir()
	retriever := memory.NewMemoryRetriever(nil)

	created, err := newConversationMemoryRuntime(client, retriever, root, "", 100_000, 4096)
	if err != nil {
		t.Fatalf("newConversationMemoryRuntime() error = %v", err)
	}
	if created.conversationID == "" || created.resumed || created.scheduler == nil || created.contextBuilder == nil {
		t.Fatalf("created runtime = %+v", created)
	}
	if created.contextBuilder.Manager != created.manager || created.contextBuilder.Retriever != retriever || created.contextBuilder.MaxRetrievedTokens != 20_000 {
		t.Fatalf("root context builder is not bound to the runtime: %+v", created.contextBuilder)
	}
	taskRuntime, err := created.newTaskMemoryRuntime("task-with-context")
	if err != nil {
		t.Fatalf("newTaskMemoryRuntime() error = %v", err)
	}
	if taskRuntime.manager == nil || taskRuntime.contextBuilder == nil || taskRuntime.contextBuilder.Manager != taskRuntime.manager || taskRuntime.contextBuilder.Retriever != retriever {
		t.Fatalf("task memory runtime is incomplete: %+v", taskRuntime)
	}
	firstTaskScheduler, err := created.newTaskScheduler("task/with unsafe characters")
	if err != nil {
		t.Fatalf("newTaskScheduler() error = %v", err)
	}
	secondTaskScheduler, err := created.newTaskScheduler("task/with unsafe characters")
	if err != nil {
		t.Fatalf("second newTaskScheduler() error = %v", err)
	}
	if firstTaskScheduler == nil || secondTaskScheduler == nil || firstTaskScheduler == secondTaskScheduler {
		t.Fatal("task schedulers are not independent")
	}
	if _, err := created.manager.AddMessage(llm.UserMessage("remember this turn"), ""); err != nil {
		t.Fatalf("AddMessage() error = %v", err)
	}
	if _, err := created.manager.UpsertFact(
		memory.FactScopeSession,
		"constraint",
		"Do not rename the API.",
		memory.Metadata{},
	); err != nil {
		t.Fatalf("UpsertFact() error = %v", err)
	}
	if err := created.save(); err != nil {
		t.Fatalf("save() error = %v", err)
	}

	resumed, err := newConversationMemoryRuntime(
		client,
		retriever,
		root,
		created.conversationID,
		100_000,
		4096,
	)
	if err != nil {
		t.Fatalf("resume newConversationMemoryRuntime() error = %v", err)
	}
	if !resumed.resumed {
		t.Fatal("resumed = false, want true")
	}
	if resumed.manager.Len() != 1 {
		t.Fatalf("restored entry count = %d, want 1", resumed.manager.Len())
	}
	if fact, ok := resumed.manager.GetFact(memory.FactScopeSession, "constraint"); !ok || fact.Content() != "Do not rename the API." {
		t.Fatalf("restored session fact = (%#v, %t)", fact, ok)
	}
	transcript, err := resumed.transcript.Load(created.conversationID)
	if err != nil {
		t.Fatalf("Load(transcript) error = %v", err)
	}
	if len(transcript) != 1 || transcript[0].Content() != "remember this turn" {
		t.Fatalf("backfilled transcript = %#v", transcript)
	}
}

func TestConversationMemoryRuntimeRejectsInvalidConversationID(t *testing.T) {
	client, err := llm.NewOpenAICompatibleClient("test-key", "http://example.test", "test-model")
	if err != nil {
		t.Fatalf("NewOpenAICompatibleClient() error = %v", err)
	}

	if _, err := newConversationMemoryRuntime(client, memory.NewMemoryRetriever(nil), t.TempDir(), "../escape", 100_000, 4096); err == nil {
		t.Fatal("newConversationMemoryRuntime() error = nil, want invalid ID error")
	}
}

func TestConversationMemoryRuntimeRequiresSharedRetriever(t *testing.T) {
	client, err := llm.NewOpenAICompatibleClient("test-key", "http://example.test", "test-model")
	if err != nil {
		t.Fatalf("NewOpenAICompatibleClient() error = %v", err)
	}
	if _, err := newConversationMemoryRuntime(client, nil, t.TempDir(), "", 100_000, 4096); err == nil {
		t.Fatal("newConversationMemoryRuntime() error = nil, want missing retriever error")
	}
}

func TestTaskMemoryManagerCopiesAllFactScopesWithoutConversation(t *testing.T) {
	root := memory.NewManager(nil)
	for _, item := range []struct {
		scope memory.FactScope
		key   string
	}{
		{scope: memory.FactScopeSession, key: "task_constraint"},
		{scope: memory.FactScopeProject, key: "language"},
		{scope: memory.FactScopeUser, key: "response_style"},
	} {
		if _, err := root.UpsertFact(item.scope, item.key, "remember "+item.key, memory.Metadata{}); err != nil {
			t.Fatalf("UpsertFact(%s) error = %v", item.scope, err)
		}
	}
	if _, err := root.AddMessage(llm.UserMessage("root-only history"), ""); err != nil {
		t.Fatalf("AddMessage() error = %v", err)
	}

	task, err := newTaskMemoryManager(root)
	if err != nil {
		t.Fatalf("newTaskMemoryManager() error = %v", err)
	}
	if task.FactLen() != 3 {
		t.Fatalf("task FactLen() = %d, want 3", task.FactLen())
	}
	if task.Len() != 0 {
		t.Fatalf("task Len() = %d, want isolated conversation", task.Len())
	}
}
