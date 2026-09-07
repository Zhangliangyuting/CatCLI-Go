package memory

import (
	"AgentCLI/internal/llm"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestJSONConversationStoreRestoresManagerState(t *testing.T) {
	root := filepath.Join(t.TempDir(), "conversations")
	store, err := NewJSONConversationStore(root)
	if err != nil {
		t.Fatalf("NewJSONConversationStore() error = %v", err)
	}
	store.now = func() time.Time {
		return time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	}

	source := NewManager(nil)
	if _, err := source.UpsertFact(FactScopeProject, "language", "Use Go.", Metadata{}); err != nil {
		t.Fatalf("UpsertFact(PROJECT) error = %v", err)
	}
	if _, err := source.UpsertFact(FactScopeSession, "task", "Keep the API stable.", Metadata{}); err != nil {
		t.Fatalf("UpsertFact(SESSION) error = %v", err)
	}
	timestamp := time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)
	entries := []Entry{
		mustStoredEntry(t, "summary-1", "Earlier work completed.", Summary, timestamp, Metadata{}, 5),
		mustStoredEntry(t, "user-1", "Continue.", Conversation, timestamp.Add(time.Second), Metadata{Role: "user"}, 2),
		mustStoredEntry(t, "assistant-1", "", Conversation, timestamp.Add(2*time.Second), Metadata{
			Role: "assistant",
			ToolCalls: []llm.ToolCall{{
				ID:       "call-1",
				Type:     "function",
				Function: llm.FunctionCall{Name: "read_file", Arguments: `{"path":"README.md"}`},
			}},
		}, 4),
		mustStoredEntry(t, "tool-1", "Relevant README contents.", ToolResult, timestamp.Add(3*time.Second), Metadata{
			Role:       "tool",
			ToolName:   "read_file",
			ToolCallID: "call-1",
			Compaction: CompactionMetadata{
				Kind:           CompactionMicro,
				SourceEntryIDs: []string{"tool-original-1"},
				OriginalTokens: 5000,
			},
		}, 300),
	}
	for _, entry := range entries {
		if err := source.AddEntry(entry); err != nil {
			t.Fatalf("AddEntry() error = %v", err)
		}
	}
	if err := source.SaveConversation(store, "conversation_1"); err != nil {
		t.Fatalf("SaveConversation() error = %v", err)
	}

	statePath := filepath.Join(root, "conversation_1", "state.json")
	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("ReadFile(state.json) error = %v", err)
	}
	if !strings.Contains(string(data), `"version": 1`) || !strings.Contains(string(data), `"session_facts"`) {
		t.Fatalf("state.json has unexpected format:\n%s", data)
	}

	target := NewManager(nil)
	if _, err := target.UpsertFact(FactScopeProject, "language", "Use Go.", Metadata{}); err != nil {
		t.Fatalf("target UpsertFact(PROJECT) error = %v", err)
	}
	if _, err := target.UpsertFact(FactScopeUser, "style", "Be concise.", Metadata{}); err != nil {
		t.Fatalf("target UpsertFact(USER) error = %v", err)
	}
	if _, err := target.UpsertFact(FactScopeSession, "stale", "Remove me.", Metadata{}); err != nil {
		t.Fatalf("target UpsertFact(stale SESSION) error = %v", err)
	}
	if _, err := target.Add("stale entry", Summary, Metadata{}); err != nil {
		t.Fatalf("target Add(stale) error = %v", err)
	}

	loaded, err := target.LoadConversation(store, "conversation_1")
	if err != nil {
		t.Fatalf("LoadConversation() error = %v", err)
	}
	if !loaded {
		t.Fatal("LoadConversation() = false, want true")
	}
	if !reflect.DeepEqual(target.Entries(), entries) {
		t.Fatalf("restored entries = %#v, want %#v", target.Entries(), entries)
	}
	if _, exists := target.GetFact(FactScopeSession, "stale"); exists {
		t.Fatal("stale SESSION fact survived restore")
	}
	if fact, exists := target.GetFact(FactScopeSession, "task"); !exists || fact.Content() != "Keep the API stable." {
		t.Fatalf("restored SESSION fact = (%+v, %v)", fact, exists)
	}
	if target.FactLen(FactScopeProject, FactScopeUser) != 2 {
		t.Fatal("persistent facts were not preserved")
	}
}

func TestJSONConversationStoreMissingAndInvalidConversation(t *testing.T) {
	store, err := NewJSONConversationStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSONConversationStore() error = %v", err)
	}
	state, exists, err := store.Load("missing")
	if err != nil || exists || len(state.Entries) != 0 {
		t.Fatalf("Load(missing) = (%+v, %v, %v)", state, exists, err)
	}
	if err := store.Save("../outside", ConversationState{}); err == nil {
		t.Fatal("Save(path traversal) error = nil, want rejection")
	}
	projectFact := mustFactEntry(t, FactScopeProject, "language", "Use Go.", 2)
	if err := store.Save("conversation-1", ConversationState{SessionFacts: []Entry{projectFact}}); err == nil {
		t.Fatal("Save(PROJECT fact as session state) error = nil, want rejection")
	}
}

func TestRestoreConversationStateIsAtomicOnInvalidState(t *testing.T) {
	manager := NewManager(nil)
	original := mustEntry(t, "original", "keep", Summary, 2)
	if err := manager.AddEntry(original); err != nil {
		t.Fatalf("AddEntry(original) error = %v", err)
	}
	invalid := ConversationState{Entries: []Entry{
		mustEntry(t, "duplicate", "one", Summary, 1),
		mustEntry(t, "duplicate", "two", Summary, 1),
	}}
	if err := manager.RestoreConversationState(invalid); err == nil {
		t.Fatal("RestoreConversationState() error = nil, want duplicate error")
	}
	if !reflect.DeepEqual(manager.Entries(), []Entry{original}) {
		t.Fatalf("manager changed after failed restore: %#v", manager.Entries())
	}
}
