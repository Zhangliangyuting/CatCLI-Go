package memory

import (
	"AgentCLI/internal/llm"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestJSONLTranscriptStoreRoundTrip(t *testing.T) {
	store, err := NewJSONLTranscriptStore(filepath.Join(t.TempDir(), "transcripts"))
	if err != nil {
		t.Fatalf("NewJSONLTranscriptStore() error = %v", err)
	}
	timestamp := time.Date(2026, 9, 2, 10, 11, 12, 0, time.UTC)
	entries := []Entry{
		mustStoredEntry(t, "user-1", "inspect README", Conversation, timestamp, Metadata{Role: "user"}, 3),
		mustStoredEntry(t, "assistant-1", "", Conversation, timestamp.Add(time.Second), Metadata{
			Role: "assistant",
			ToolCalls: []llm.ToolCall{{
				ID:   "call-1",
				Type: "function",
				Function: llm.FunctionCall{
					Name:      "read_file",
					Arguments: `{"path":"README.md"}`,
				},
			}},
		}, 8),
		mustStoredEntry(t, "tool-1", "README contents", ToolResult, timestamp.Add(2*time.Second), Metadata{
			Role:       "tool",
			ToolName:   "read_file",
			ToolCallID: "call-1",
			Compaction: CompactionMetadata{
				Kind:           CompactionMicro,
				SourceEntryIDs: []string{"tool-original-1"},
				OriginalTokens: 5000,
			},
			Attributes: map[string]string{"status": "ok"},
		}, 4),
	}

	if err := store.Append("session_20260902", entries[:2]...); err != nil {
		t.Fatalf("first Append() error = %v", err)
	}
	if err := store.Append("session_20260902", entries[2]); err != nil {
		t.Fatalf("second Append() error = %v", err)
	}
	if err := store.Append("session_20260902", entries...); err != nil {
		t.Fatalf("idempotent Append() error = %v", err)
	}
	reopened, err := NewJSONLTranscriptStore(store.root)
	if err != nil {
		t.Fatalf("reopen transcript store error = %v", err)
	}
	if err := reopened.Append("session_20260902", entries...); err != nil {
		t.Fatalf("idempotent Append() after reopen error = %v", err)
	}
	loaded, err := store.Load("session_20260902")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !reflect.DeepEqual(loaded, entries) {
		t.Fatalf("Load() = %#v, want %#v", loaded, entries)
	}
}

func TestJSONLTranscriptStoreValidatesSessionID(t *testing.T) {
	store, err := NewJSONLTranscriptStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSONLTranscriptStore() error = %v", err)
	}
	if err := store.Append("../outside", mustEntry(t, "one", "content", Summary, 1)); err == nil {
		t.Fatal("Append(path traversal) error = nil, want rejection")
	}
	entries, err := store.Load("missing-session")
	if err != nil {
		t.Fatalf("Load(missing) error = %v", err)
	}
	if entries != nil {
		t.Fatalf("Load(missing) = %#v, want nil", entries)
	}
}

func mustStoredEntry(
	t *testing.T,
	id string,
	content string,
	typ Type,
	timestamp time.Time,
	metadata Metadata,
	tokens int,
) Entry {
	t.Helper()
	entry, err := NewEntry(id, content, typ, timestamp, metadata, tokens)
	if err != nil {
		t.Fatalf("NewEntry() error = %v", err)
	}
	return entry
}
