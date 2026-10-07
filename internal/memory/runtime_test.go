package memory

import (
	"AgentCLI/internal/llm"
	"testing"
)

func TestConversationRuntimeCreatesSavesAndResumes(t *testing.T) {
	client, err := llm.NewOpenAICompatibleClient("test-key", "http://example.test", "test-model")
	if err != nil {
		t.Fatalf("NewOpenAICompatibleClient() error = %v", err)
	}
	rootDirectory := t.TempDir()
	retriever := NewMemoryRetriever(nil)

	created, err := NewConversationRuntime(client, retriever, rootDirectory, "", 100_000, 4096)
	if err != nil {
		t.Fatalf("NewConversationRuntime() error = %v", err)
	}
	root := created.RootContext()
	if root.ConversationID == "" || created.Resumed() || root.Scheduler == nil || root.ContextBuilder == nil {
		t.Fatalf("created runtime/root = %+v/%+v", created, root)
	}
	if root.ContextBuilder.Manager != root.Manager || root.ContextBuilder.Retriever != retriever || root.ContextBuilder.MaxRetrievedTokens != 20_000 {
		t.Fatalf("root context builder is not bound to the runtime: %+v", root.ContextBuilder)
	}

	taskRuntime, err := created.NewTaskRuntime("task-with-context")
	if err != nil {
		t.Fatalf("NewTaskRuntime() error = %v", err)
	}
	if taskRuntime.Manager == nil || taskRuntime.ContextBuilder == nil || taskRuntime.ContextBuilder.Manager != taskRuntime.Manager || taskRuntime.ContextBuilder.Retriever != retriever {
		t.Fatalf("task memory runtime is incomplete: %+v", taskRuntime)
	}
	secondTaskRuntime, err := created.NewTaskRuntime("task-with-context")
	if err != nil {
		t.Fatalf("second NewTaskRuntime() error = %v", err)
	}
	if taskRuntime.Scheduler == secondTaskRuntime.Scheduler || taskRuntime.ConversationID == secondTaskRuntime.ConversationID {
		t.Fatal("task runtimes are not independent")
	}

	if _, err := root.Manager.AddMessage(llm.UserMessage("remember this turn"), ""); err != nil {
		t.Fatalf("AddMessage() error = %v", err)
	}
	if _, err := root.Manager.UpsertFact(FactScopeSession, "constraint", "Do not rename the API.", Metadata{}); err != nil {
		t.Fatalf("UpsertFact() error = %v", err)
	}
	if err := created.Save(); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	resumed, err := NewConversationRuntime(
		client,
		retriever,
		rootDirectory,
		root.ConversationID,
		100_000,
		4096,
	)
	if err != nil {
		t.Fatalf("resume NewConversationRuntime() error = %v", err)
	}
	resumedRoot := resumed.RootContext()
	if !resumed.Resumed() || resumedRoot.Manager.Len() != 1 {
		t.Fatalf("resumed/count = %t/%d", resumed.Resumed(), resumedRoot.Manager.Len())
	}
	if fact, ok := resumedRoot.Manager.GetFact(FactScopeSession, "constraint"); !ok || fact.Content() != "Do not rename the API." {
		t.Fatalf("restored session fact = (%#v, %t)", fact, ok)
	}
	transcript, err := resumedRoot.Transcript.Load(root.ConversationID)
	if err != nil {
		t.Fatalf("Load(transcript) error = %v", err)
	}
	if len(transcript) != 1 || transcript[0].Content() != "remember this turn" {
		t.Fatalf("backfilled transcript = %#v", transcript)
	}
}

func TestConversationRuntimeRejectsInvalidConfiguration(t *testing.T) {
	client, err := llm.NewOpenAICompatibleClient("test-key", "http://example.test", "test-model")
	if err != nil {
		t.Fatalf("NewOpenAICompatibleClient() error = %v", err)
	}
	if _, err := NewConversationRuntime(client, NewMemoryRetriever(nil), t.TempDir(), "../escape", 100_000, 4096); err == nil {
		t.Fatal("NewConversationRuntime() error = nil, want invalid ID error")
	}
	if _, err := NewConversationRuntime(client, nil, t.TempDir(), "", 100_000, 4096); err == nil {
		t.Fatal("NewConversationRuntime() error = nil, want missing retriever error")
	}
}

func TestTaskMemoryManagerCopiesFactsWithoutConversation(t *testing.T) {
	root := NewManager(nil)
	for _, item := range []struct {
		scope FactScope
		key   string
	}{
		{scope: FactScopeSession, key: "task_constraint"},
		{scope: FactScopeProject, key: "language"},
		{scope: FactScopeUser, key: "response_style"},
	} {
		if _, err := root.UpsertFact(item.scope, item.key, "remember "+item.key, Metadata{}); err != nil {
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
	if task.FactLen() != 3 || task.Len() != 0 {
		t.Fatalf("task facts/entries = %d/%d", task.FactLen(), task.Len())
	}
}
