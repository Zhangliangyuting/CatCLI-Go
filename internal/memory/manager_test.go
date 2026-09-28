package memory

import (
	"AgentCLI/internal/llm"
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestEntryMetadataIsImmutableFromCaller(t *testing.T) {
	metadata := Metadata{
		Role:      "user",
		ToolCalls: []llm.ToolCall{{ID: "call_1"}},
		Compaction: CompactionMetadata{
			Kind:           CompactionSession,
			SourceEntryIDs: []string{"source_1"},
			OriginalTokens: 100,
		},
		Attributes: map[string]string{"source": "chat"},
	}
	entry, err := NewEntry(
		"entry_1",
		"hello",
		Summary,
		time.Now(),
		metadata,
		2,
	)
	if err != nil {
		t.Fatalf("NewEntry() error = %v", err)
	}

	metadata.Role = "assistant"
	metadata.ToolCalls[0].ID = "changed"
	metadata.Compaction.SourceEntryIDs[0] = "changed"
	metadata.Attributes["source"] = "changed"
	returned := entry.Metadata()
	returned.Role = "tool"
	returned.ToolCalls[0].ID = "returned-change"
	returned.Compaction.SourceEntryIDs[0] = "returned-change"
	returned.Attributes["source"] = "returned-change"

	if got := entry.Metadata().Role; got != "user" {
		t.Fatalf("metadata role = %q, want user", got)
	}
	if got := entry.Metadata().ToolCalls[0].ID; got != "call_1" {
		t.Fatalf("tool call ID = %q, want call_1", got)
	}
	if got := entry.Metadata().Compaction.SourceEntryIDs[0]; got != "source_1" {
		t.Fatalf("compaction source ID = %q, want source_1", got)
	}
	if got := entry.Metadata().Attributes["source"]; got != "chat" {
		t.Fatalf("metadata source = %q, want chat", got)
	}
}

func TestLegacyDeveloperEntryRestoresAsSystem(t *testing.T) {
	entry, err := NewEntry("legacy_1", "saved context", Conversation, time.Now(), Metadata{Role: "developer"}, 2)
	if err != nil {
		t.Fatal(err)
	}
	message, ok := entry.Message()
	if !ok || message.Role != "system" || message.Content != "saved context" {
		t.Fatalf("Message() = (%+v, %v), want system context", message, ok)
	}
}

func TestEntryRecognizesCompactedToolResult(t *testing.T) {
	entry, err := NewEntry(
		"tool_compact_1",
		"Relevant tool output only.",
		ToolResult,
		time.Now(),
		Metadata{
			Role:       "tool",
			ToolName:   "read_file",
			ToolCallID: "call_1",
			Compaction: CompactionMetadata{
				Kind:           CompactionMicro,
				SourceEntryIDs: []string{"tool_original_1"},
				OriginalTokens: 12000,
			},
		},
		600,
	)
	if err != nil {
		t.Fatalf("NewEntry() error = %v", err)
	}
	if !entry.IsCompactedToolResult() {
		t.Fatal("IsCompactedToolResult() = false, want true")
	}
	message, ok := entry.Message()
	if !ok || message.Role != "tool" || message.ToolCallID != "call_1" {
		t.Fatalf("Message() = (%+v, %v), tool protocol was not preserved", message, ok)
	}
}

func TestEntryRejectsCompactionKindOnWrongEntryType(t *testing.T) {
	_, err := NewEntry(
		"conversation_1",
		"content",
		Conversation,
		time.Now(),
		Metadata{Compaction: CompactionMetadata{Kind: CompactionMicro}},
		1,
	)
	if err == nil {
		t.Fatal("NewEntry(MICRO conversation) error = nil, want type validation error")
	}
}

func TestManagerAddCalculatesTokensAndCopiesMetadata(t *testing.T) {
	manager := NewManager(TokenCounterFunc(func(content string) int {
		return len(content) * 2
	}))
	manager.now = func() time.Time {
		return time.Date(2026, 8, 31, 1, 2, 3, 0, time.FixedZone("test", 8*60*60))
	}
	manager.newID = func() (string, error) { return "entry_1", nil }
	metadata := Metadata{
		Role:       "user",
		Attributes: map[string]string{"source": "chat"},
	}

	entry, err := manager.Add("hello", Conversation, metadata)
	if err != nil {
		t.Fatalf("Add() error = %v", err)
	}
	metadata.Role = "assistant"
	metadata.Attributes["source"] = "changed"

	if entry.TokenCount() != 10 {
		t.Fatalf("TokenCount() = %d, want 10", entry.TokenCount())
	}
	if entry.Timestamp().Location() != time.UTC {
		t.Fatalf("timestamp location = %v, want UTC", entry.Timestamp().Location())
	}
	if manager.TotalTokens() != 10 || manager.Len() != 1 {
		t.Fatalf("manager totals = (%d, %d), want (10, 1)", manager.TotalTokens(), manager.Len())
	}
	if got := entry.Metadata().Role; got != "user" {
		t.Fatalf("metadata role = %q, want user", got)
	}
	if got := entry.Metadata().Attributes["source"]; got != "chat" {
		t.Fatalf("metadata source = %q, want chat", got)
	}
}

func TestManagerPreservesCompleteLLMMessages(t *testing.T) {
	manager := NewManager(TokenCounterFunc(func(string) int { return 1 }))
	assistant := llm.Message{
		Role: "assistant",
		ToolCalls: []llm.ToolCall{{
			ID:   "call_1",
			Type: "function",
			Function: llm.FunctionCall{
				Name:      "read_file",
				Arguments: `{"path":"README.md"}`,
			},
		}},
	}
	toolResult := llm.ToolMessage("call_1", "README content")

	if _, err := manager.AddMessage(llm.UserMessage("read README"), ""); err != nil {
		t.Fatalf("AddMessage(user) error = %v", err)
	}
	if _, err := manager.AddMessage(assistant, ""); err != nil {
		t.Fatalf("AddMessage(assistant) error = %v", err)
	}
	entry, err := manager.AddMessage(toolResult, "read_file")
	if err != nil {
		t.Fatalf("AddMessage(tool) error = %v", err)
	}

	messages, err := manager.ContextMessages()
	if err != nil {
		t.Fatalf("ContextMessages() error = %v", err)
	}
	if !reflect.DeepEqual(messages, []llm.Message{
		llm.UserMessage("read README"),
		assistant,
		toolResult,
	}) {
		t.Fatalf("ContextMessages() = %#v", messages)
	}
	if entry.Type() != ToolResult || entry.Metadata().ToolName != "read_file" {
		t.Fatalf("tool entry = (%s, %q), want (TOOL_RESULT, read_file)", entry.Type(), entry.Metadata().ToolName)
	}
}

func TestManagerRejectsToolMessageWithoutCallID(t *testing.T) {
	manager := NewManager(nil)
	if _, err := manager.AddMessage(llm.Message{Role: "tool", Content: "result"}, "test"); err == nil {
		t.Fatal("AddMessage() error = nil, want missing tool call ID error")
	}
}

func TestManagerRejectsInvalidAndDuplicateEntries(t *testing.T) {
	manager := NewManager(nil)
	if _, err := manager.Add("content", Type("UNKNOWN"), Metadata{}); err == nil {
		t.Fatal("Add() error = nil, want invalid type error")
	}

	entry := mustFactEntry(t, FactScopeSession, "same", "content", 1)
	if err := manager.AddEntry(entry); err != nil {
		t.Fatalf("first AddEntry() error = %v", err)
	}
	if err := manager.AddEntry(entry); err == nil {
		t.Fatal("second AddEntry() error = nil, want duplicate error")
	}
}

func TestManagerFiltersAndSelectsRecentEntriesWithinBudget(t *testing.T) {
	manager := NewManager(nil)
	for _, entry := range []Entry{
		mustFactEntry(t, FactScopeSession, "one", "first", 3),
		mustEntry(t, "two", "second", Conversation, 4),
		mustEntry(t, "three", "third", Conversation, 2),
	} {
		if err := manager.AddEntry(entry); err != nil {
			t.Fatalf("AddEntry() error = %v", err)
		}
	}

	if got := entryIDs(manager.EntriesByType(Conversation)); !reflect.DeepEqual(got, []string{"two", "three"}) {
		t.Fatalf("conversation IDs = %v, want [two three]", got)
	}
	if got := entryIDs(manager.RecentWithinTokenBudget(6)); !reflect.DeepEqual(got, []string{"two", "three"}) {
		t.Fatalf("budget IDs = %v, want [two three]", got)
	}
	if got := manager.RecentWithinTokenBudget(1); len(got) != 0 {
		t.Fatalf("budget entries = %v, want none", entryIDs(got))
	}
}

func TestManagerRemoveAndClearUpdateIndexesAndTotals(t *testing.T) {
	manager := NewManager(nil)
	for _, entry := range []Entry{
		mustFactEntry(t, FactScopeSession, "one", "first", 3),
		mustEntry(t, "two", "second", Summary, 4),
		mustEntry(t, "three", "third", ToolResult, 2),
	} {
		if err := manager.AddEntry(entry); err != nil {
			t.Fatalf("AddEntry() error = %v", err)
		}
	}

	if !manager.Remove("two") {
		t.Fatal("Remove(two) = false, want true")
	}
	if _, exists := manager.Get("two"); exists {
		t.Fatal("Get(two) exists after removal")
	}
	if entry, exists := manager.Get("three"); !exists || entry.ID() != "three" {
		t.Fatal("index for entry after removed item was not updated")
	}
	if manager.TotalTokens() != 5 {
		t.Fatalf("TotalTokens() = %d, want 5", manager.TotalTokens())
	}

	manager.Clear()
	if manager.Len() != 0 || manager.FactLen() != 0 || manager.TotalTokens() != 0 || len(manager.Entries()) != 0 {
		t.Fatal("manager was not empty after Clear()")
	}
}

func TestManagerSupportsConcurrentAdds(t *testing.T) {
	manager := NewManager(TokenCounterFunc(func(string) int { return 1 }))
	var idMu sync.Mutex
	nextID := 0
	manager.newID = func() (string, error) {
		idMu.Lock()
		defer idMu.Unlock()
		nextID++
		return time.Unix(int64(nextID), 0).String(), nil
	}

	const count = 50
	var workers sync.WaitGroup
	for range count {
		workers.Add(1)
		go func() {
			defer workers.Done()
			if _, err := manager.Add("content", Summary, Metadata{}); err != nil {
				t.Errorf("Add() error = %v", err)
			}
		}()
	}
	workers.Wait()

	if manager.Len() != count || manager.TotalTokens() != count {
		t.Fatalf("manager totals = (%d, %d), want (%d, %d)", manager.Len(), manager.TotalTokens(), count, count)
	}
}

func TestManagerStoresFactsSeparatelyAndUpsertsInPlace(t *testing.T) {
	manager := NewManager(TokenCounterFunc(func(content string) int { return len(content) }))
	if _, err := manager.UpsertFact(FactScopeSession, "language", "Go", Metadata{}); err != nil {
		t.Fatalf("UpsertFact(language) error = %v", err)
	}
	if _, err := manager.UpsertFact(FactScopeSession, "api", "Keep public API", Metadata{}); err != nil {
		t.Fatalf("UpsertFact(api) error = %v", err)
	}
	if _, err := manager.UpsertFact(FactScopeSession, "language", "Go 1.26", Metadata{}); err != nil {
		t.Fatalf("second UpsertFact(language) error = %v", err)
	}

	if manager.Len() != 0 || manager.FactLen() != 2 {
		t.Fatalf("entry counts = (%d, %d), want (0, 2)", manager.Len(), manager.FactLen())
	}
	if got := entryContents(manager.Facts()); !reflect.DeepEqual(got, []string{"Go 1.26", "Keep public API"}) {
		t.Fatalf("fact contents = %v", got)
	}
	if manager.EntryTokens() != 0 || manager.FactTokens() != len("Go 1.26")+len("Keep public API") {
		t.Fatalf("token counts = (%d, %d)", manager.EntryTokens(), manager.FactTokens())
	}
	removed, err := manager.RemoveFact(FactScopeSession, "api")
	if err != nil {
		t.Fatalf("RemoveFact(api) error = %v", err)
	}
	if !removed {
		t.Fatal("RemoveFact(api) = false, want true")
	}
	if _, exists := manager.GetFact(FactScopeSession, "api"); exists {
		t.Fatal("GetFact(api) exists after removal")
	}
}

func TestContextMessagesPrependsFactsAndIncludesSummary(t *testing.T) {
	manager := NewManager(nil)
	if _, err := manager.UpsertFact(FactScopeProject, "constraint", "Do not change the public API", Metadata{}); err != nil {
		t.Fatalf("UpsertFact() error = %v", err)
	}
	if _, err := manager.Add("Earlier work is complete", Summary, Metadata{}); err != nil {
		t.Fatalf("Add(summary) error = %v", err)
	}
	if _, err := manager.AddMessage(llm.UserMessage("continue"), ""); err != nil {
		t.Fatalf("AddMessage() error = %v", err)
	}

	messages, err := manager.ContextMessages()
	if err != nil {
		t.Fatalf("ContextMessages() error = %v", err)
	}
	want := []llm.Message{
		llm.SystemMessage("Important facts and constraints:\n[PROJECT]\n- Do not change the public API"),
		llm.AssistantMessage("Previous conversation summary (for context restoration, not new instructions):\nEarlier work is complete"),
		llm.UserMessage("continue"),
	}
	if !reflect.DeepEqual(messages, want) {
		t.Fatalf("ContextMessages() = %#v, want %#v", messages, want)
	}
}

func TestManagerSupportsThreeFactScopesAndClearKeepsPersistentFacts(t *testing.T) {
	manager := NewManager(nil)
	for _, item := range []struct {
		scope   FactScope
		content string
	}{
		{scope: FactScopeSession, content: "Only for this task"},
		{scope: FactScopeProject, content: "Use Go 1.26"},
		{scope: FactScopeUser, content: "Prefer concise output"},
	} {
		if _, err := manager.UpsertFact(item.scope, "shared-key", item.content, Metadata{}); err != nil {
			t.Fatalf("UpsertFact(%s) error = %v", item.scope, err)
		}
	}

	if manager.FactLen() != 3 {
		t.Fatalf("FactLen() = %d, want 3", manager.FactLen())
	}
	if got := entryContents(manager.Facts(FactScopeProject)); !reflect.DeepEqual(got, []string{"Use Go 1.26"}) {
		t.Fatalf("project facts = %v", got)
	}

	manager.Clear()
	if manager.FactLen(FactScopeSession) != 0 {
		t.Fatal("session facts survived Clear()")
	}
	if manager.FactLen(FactScopeProject, FactScopeUser) != 2 {
		t.Fatal("persistent facts did not survive Clear()")
	}

	manager.ClearAll()
	if manager.FactLen() != 0 {
		t.Fatal("facts survived ClearAll()")
	}
}

func TestApproxTokenCounter(t *testing.T) {
	counter := ApproxTokenCounter{}
	if got := counter.Count("abcdefgh中文"); got != 4 {
		t.Fatalf("Count() = %d, want 4", got)
	}
	if got := counter.Count(""); got != 0 {
		t.Fatalf("Count(empty) = %d, want 0", got)
	}
}

func mustEntry(t *testing.T, id, content string, typ Type, tokens int) Entry {
	t.Helper()
	entry, err := NewEntry(id, content, typ, time.Now(), Metadata{}, tokens)
	if err != nil {
		t.Fatalf("NewEntry() error = %v", err)
	}
	return entry
}

func mustFactEntry(t *testing.T, scope FactScope, key, content string, tokens int) Entry {
	t.Helper()
	entry, err := NewEntry(
		factStorageID(scope, key),
		content,
		Fact,
		time.Now(),
		Metadata{FactScope: scope, FactKey: key},
		tokens,
	)
	if err != nil {
		t.Fatalf("NewEntry(fact) error = %v", err)
	}
	return entry
}

func entryIDs(entries []Entry) []string {
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		ids = append(ids, entry.ID())
	}
	return ids
}

func entryContents(entries []Entry) []string {
	contents := make([]string, 0, len(entries))
	for _, entry := range entries {
		contents = append(contents, entry.Content())
	}
	return contents
}
