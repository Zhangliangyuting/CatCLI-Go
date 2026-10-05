package memory

import (
	"AgentCLI/internal/llm"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

type fakeCompactionGenerator struct {
	toolContent    ToolResultCompactContent
	summaryContent SummaryContent
	toolSources    []string
	summarySources []string
	summaryKind    CompactionKind
	err            error
}

func (generator *fakeCompactionGenerator) CompactToolResult(
	_ context.Context,
	entry Entry,
) (ToolResultCompactContent, error) {
	generator.toolSources = []string{entry.ID()}
	return generator.toolContent, generator.err
}

func (generator *fakeCompactionGenerator) Summarize(
	_ context.Context,
	kind CompactionKind,
	entries []Entry,
) (SummaryContent, error) {
	generator.summaryKind = kind
	generator.summarySources = entryIDsOf(entries)
	return generator.summaryContent, generator.err
}

type recordingTranscriptStore struct {
	conversationIDs []string
	batches         [][]Entry
	err             error
}

func (store *recordingTranscriptStore) Append(conversationID string, entries ...Entry) error {
	if store.err != nil {
		return store.err
	}
	store.conversationIDs = append(store.conversationIDs, conversationID)
	store.batches = append(store.batches, append([]Entry(nil), entries...))
	return nil
}

func (store *recordingTranscriptStore) Load(string) ([]Entry, error) {
	var entries []Entry
	for _, batch := range store.batches {
		entries = append(entries, batch...)
	}
	return entries, nil
}

func TestMicroCompactPreservesToolProtocolAndArchivesOriginal(t *testing.T) {
	manager := compactTestManager()
	assistant := mustStoredEntry(t, "assistant-call", "", Conversation, compactTestTime(), Metadata{
		Role: "assistant",
		ToolCalls: []llm.ToolCall{{
			ID:       "call-1",
			Type:     "function",
			Function: llm.FunctionCall{Name: "read_file", Arguments: `{"path":"large.txt"}`},
		}},
	}, 20)
	original := mustStoredEntry(t, "tool-original", strings.Repeat("large output ", 200), ToolResult, compactTestTime(), Metadata{
		Role:       "tool",
		ToolName:   "read_file",
		ToolCallID: "call-1",
	}, 1000)
	for _, entry := range []Entry{
		mustStoredEntry(t, "user-1", "read it", Conversation, compactTestTime(), Metadata{Role: "user"}, 10),
		assistant,
		original,
	} {
		if err := manager.AddEntry(entry); err != nil {
			t.Fatalf("AddEntry() error = %v", err)
		}
	}

	generator := &fakeCompactionGenerator{toolContent: validToolCompactContent()}
	transcript := &recordingTranscriptStore{}
	compactor := mustCompactor(t, generator, transcript)
	result, err := compactor.MicroCompact(context.Background(), manager, original.ID())
	if err != nil {
		t.Fatalf("MicroCompact() error = %v", err)
	}
	if result.Kind != CompactionMicro || result.OriginalTokens != 1000 {
		t.Fatalf("MicroCompact() result = %+v", result)
	}
	if !result.Entry.IsCompactedToolResult() {
		t.Fatal("replacement is not recognized as a compacted tool result")
	}
	metadata := result.Entry.Metadata()
	if metadata.Role != "tool" || metadata.ToolCallID != "call-1" || metadata.ToolName != "read_file" {
		t.Fatalf("tool protocol metadata changed: %+v", metadata)
	}
	if !reflect.DeepEqual(metadata.Compaction.SourceEntryIDs, []string{"tool-original"}) {
		t.Fatalf("source IDs = %v", metadata.Compaction.SourceEntryIDs)
	}
	if len(transcript.batches) != 1 || !reflect.DeepEqual(transcript.batches[0], []Entry{original}) {
		t.Fatalf("archived entries = %#v", transcript.batches)
	}
	if got := entryIDs(manager.Entries()); !reflect.DeepEqual(got, []string{"user-1", "assistant-call", "compact-1"}) {
		t.Fatalf("manager entries = %v", got)
	}
	messages, err := manager.ConversationMessages()
	if err != nil {
		t.Fatalf("ConversationMessages() error = %v", err)
	}
	if messages[2].Role != "tool" || messages[2].ToolCallID != "call-1" {
		t.Fatalf("compacted protocol message = %+v", messages[2])
	}
}

func TestSessionCompactReplacesOldestFourCompleteTurns(t *testing.T) {
	manager := compactTestManager()
	var expectedSources []string
	for turn := 1; turn <= 5; turn++ {
		turnEntries := compactTestTurn(t, turn, turn == 2)
		if turn <= sessionCompactTurns {
			expectedSources = append(expectedSources, entryIDsOf(turnEntries)...)
		}
		for _, entry := range turnEntries {
			if err := manager.AddEntry(entry); err != nil {
				t.Fatalf("AddEntry(turn %d) error = %v", turn, err)
			}
		}
	}

	generator := &fakeCompactionGenerator{summaryContent: validSummaryContent()}
	transcript := &recordingTranscriptStore{}
	compactor := mustCompactor(t, generator, transcript)
	result, compacted, err := compactor.SessionCompact(context.Background(), manager)
	if err != nil {
		t.Fatalf("SessionCompact() error = %v", err)
	}
	if !compacted || result.Kind != CompactionSession {
		t.Fatalf("SessionCompact() = (%+v, %v), want compacted SESSION", result, compacted)
	}
	if !reflect.DeepEqual(result.SourceEntryIDs, expectedSources) ||
		!reflect.DeepEqual(generator.summarySources, expectedSources) {
		t.Fatalf("session sources = %v, want %v", result.SourceEntryIDs, expectedSources)
	}
	if generator.summaryKind != CompactionSession {
		t.Fatalf("summary kind = %s, want SESSION", generator.summaryKind)
	}
	remaining := manager.Entries()
	if remaining[0].Type() != Summary || remaining[0].Metadata().Compaction.Kind != CompactionSession {
		t.Fatalf("first remaining entry = %+v, want SESSION summary", remaining[0])
	}
	if got := entryIDs(remaining[1:]); !reflect.DeepEqual(got, entryIDsOf(compactTestTurn(t, 5, false))) {
		t.Fatalf("uncompacted recent turn = %v", got)
	}
	if _, compacted, err := compactor.SessionCompact(context.Background(), manager); err != nil || compacted {
		t.Fatalf("second SessionCompact() = (%v, %v), want no-op", compacted, err)
	}
}

func TestSessionCompactDoesNotSplitIncompleteToolTurn(t *testing.T) {
	manager := compactTestManager()
	for turn := 1; turn <= 3; turn++ {
		for _, entry := range compactTestTurn(t, turn, false) {
			if err := manager.AddEntry(entry); err != nil {
				t.Fatalf("AddEntry() error = %v", err)
			}
		}
	}
	incomplete := []Entry{
		mustStoredEntry(t, "user-4", "run tool", Conversation, compactTestTime(), Metadata{Role: "user"}, 100),
		mustStoredEntry(t, "assistant-tool-4", "", Conversation, compactTestTime(), Metadata{
			Role: "assistant",
			ToolCalls: []llm.ToolCall{{
				ID: "pending-call", Type: "function", Function: llm.FunctionCall{Name: "read_file"},
			}},
		}, 100),
	}
	for _, entry := range incomplete {
		if err := manager.AddEntry(entry); err != nil {
			t.Fatalf("AddEntry(incomplete) error = %v", err)
		}
	}
	originalIDs := entryIDs(manager.Entries())
	compactor := mustCompactor(t, &fakeCompactionGenerator{summaryContent: validSummaryContent()}, &recordingTranscriptStore{})
	if _, compacted, err := compactor.SessionCompact(context.Background(), manager); err != nil || compacted {
		t.Fatalf("SessionCompact(incomplete) = (%v, %v), want no-op", compacted, err)
	}
	if !reflect.DeepEqual(entryIDs(manager.Entries()), originalIDs) {
		t.Fatal("incomplete turn was modified")
	}
}

func TestFullCompactPromotesSessionSummaryAndKeepsRecentTurn(t *testing.T) {
	manager := compactTestManager()
	priorSummary := mustStoredEntry(t, "session-summary", "old summary", Summary, compactTestTime(), Metadata{
		Compaction: CompactionMetadata{
			Kind: CompactionSession, SourceEntryIDs: []string{"old-1", "old-2"}, OriginalTokens: 500,
		},
	}, 100)
	if err := manager.AddEntry(priorSummary); err != nil {
		t.Fatalf("AddEntry(summary) error = %v", err)
	}
	turns := make([][]Entry, 0, 3)
	for turn := 1; turn <= 3; turn++ {
		entries := compactTestTurn(t, turn, false)
		turns = append(turns, entries)
		for _, entry := range entries {
			if err := manager.AddEntry(entry); err != nil {
				t.Fatalf("AddEntry(turn) error = %v", err)
			}
		}
	}
	wantSources := []string{priorSummary.ID()}
	wantSources = append(wantSources, entryIDsOf(turns[0])...)
	wantSources = append(wantSources, entryIDsOf(turns[1])...)

	generator := &fakeCompactionGenerator{summaryContent: validSummaryContent()}
	transcript := &recordingTranscriptStore{}
	compactor := mustCompactor(t, generator, transcript)
	result, compacted, err := compactor.FullCompact(context.Background(), manager)
	if err != nil {
		t.Fatalf("FullCompact() error = %v", err)
	}
	if !compacted || result.Kind != CompactionFull || !reflect.DeepEqual(result.SourceEntryIDs, wantSources) {
		t.Fatalf("FullCompact() = (%+v, %v), want sources %v", result, compacted, wantSources)
	}
	if !reflect.DeepEqual(generator.summarySources, wantSources) {
		t.Fatalf("generator sources = %v, want SESSION summary and raw turns %v", generator.summarySources, wantSources)
	}
	remaining := manager.Entries()
	if len(remaining) != 3 || remaining[0].Metadata().Compaction.Kind != CompactionFull {
		t.Fatalf("remaining entries = %#v", remaining)
	}
	if !reflect.DeepEqual(entryIDs(remaining[1:]), entryIDsOf(turns[2])) {
		t.Fatal("most recent turn was not retained verbatim")
	}
}

func TestFullCompactDoesNotRecompactFullSummary(t *testing.T) {
	manager := compactTestManager()
	priorFull := mustStoredEntry(t, "full-summary", "stable full summary", Summary, compactTestTime(), Metadata{
		Compaction: CompactionMetadata{
			Kind: CompactionFull, SourceEntryIDs: []string{"old-1", "old-2"}, OriginalTokens: 500,
		},
	}, 100)
	if err := manager.AddEntry(priorFull); err != nil {
		t.Fatalf("AddEntry(FULL summary) error = %v", err)
	}
	priorSession := mustStoredEntry(t, "session-summary", "newer session summary", Summary, compactTestTime(), Metadata{
		Compaction: CompactionMetadata{
			Kind: CompactionSession, SourceEntryIDs: []string{"session-1"}, OriginalTokens: 300,
		},
	}, 80)
	if err := manager.AddEntry(priorSession); err != nil {
		t.Fatalf("AddEntry(SESSION summary) error = %v", err)
	}
	turns := make([][]Entry, 0, 2)
	for turn := 1; turn <= 2; turn++ {
		entries := compactTestTurn(t, turn, false)
		turns = append(turns, entries)
		for _, entry := range entries {
			if err := manager.AddEntry(entry); err != nil {
				t.Fatalf("AddEntry(turn) error = %v", err)
			}
		}
	}

	wantSources := []string{priorSession.ID()}
	wantSources = append(wantSources, entryIDsOf(turns[0])...)
	compactor := mustCompactor(t, &fakeCompactionGenerator{summaryContent: validSummaryContent()}, &recordingTranscriptStore{})
	result, compacted, err := compactor.FullCompact(context.Background(), manager)
	if err != nil {
		t.Fatalf("FullCompact() error = %v", err)
	}
	if !compacted || !reflect.DeepEqual(result.SourceEntryIDs, wantSources) {
		t.Fatalf("FullCompact() = (%+v, %v), want sources %v", result, compacted, wantSources)
	}
	remaining := manager.Entries()
	if remaining[0].ID() != priorFull.ID() || remaining[1].Metadata().Compaction.Kind != CompactionFull {
		t.Fatalf("remaining entries = %#v", remaining)
	}
}

func TestFullCompactLimitsEachSummaryToEightRawTurns(t *testing.T) {
	manager := compactTestManager()
	turns := make([][]Entry, 0, 11)
	for turn := 1; turn <= 11; turn++ {
		entries := compactTestTurn(t, turn, false)
		turns = append(turns, entries)
		for _, entry := range entries {
			if err := manager.AddEntry(entry); err != nil {
				t.Fatalf("AddEntry(turn) error = %v", err)
			}
		}
	}
	wantSources := make([]string, 0, fullCompactMaxUnits*2)
	for _, turn := range turns[:fullCompactMaxUnits] {
		wantSources = append(wantSources, entryIDsOf(turn)...)
	}

	generator := &fakeCompactionGenerator{summaryContent: validSummaryContent()}
	compactor := mustCompactor(t, generator, &recordingTranscriptStore{})
	result, compacted, err := compactor.FullCompact(context.Background(), manager)
	if err != nil {
		t.Fatalf("FullCompact() error = %v", err)
	}
	if !compacted || !reflect.DeepEqual(result.SourceEntryIDs, wantSources) {
		t.Fatalf("FullCompact() = (%+v, %v), want at most %d units", result, compacted, fullCompactMaxUnits)
	}
	wantRemaining := make([]string, 0, 1+len(turns[fullCompactMaxUnits:])*2)
	wantRemaining = append(wantRemaining, result.Entry.ID())
	for _, turn := range turns[fullCompactMaxUnits:] {
		wantRemaining = append(wantRemaining, entryIDsOf(turn)...)
	}
	if got := entryIDs(manager.Entries()); !reflect.DeepEqual(got, wantRemaining) {
		t.Fatalf("remaining entries = %v, want %v", got, wantRemaining)
	}
}

func TestCompactionFailureDoesNotMutateManager(t *testing.T) {
	manager := compactTestManager()
	original := mustStoredEntry(t, "tool-original", strings.Repeat("output", 100), ToolResult, compactTestTime(), Metadata{
		Role: "tool", ToolCallID: "call-1",
	}, 1000)
	if err := manager.AddEntry(original); err != nil {
		t.Fatalf("AddEntry() error = %v", err)
	}
	transcript := &recordingTranscriptStore{err: errors.New("disk full")}
	compactor := mustCompactor(t, &fakeCompactionGenerator{toolContent: validToolCompactContent()}, transcript)
	if _, err := compactor.MicroCompact(context.Background(), manager, original.ID()); err == nil {
		t.Fatal("MicroCompact() error = nil, want transcript failure")
	}
	if !reflect.DeepEqual(manager.Entries(), []Entry{original}) {
		t.Fatal("manager changed after transcript failure")
	}
}

func TestMicroCompactRejectsOutputThatDoesNotReduceTokens(t *testing.T) {
	manager := NewManager(TokenCounterFunc(func(string) int { return 1000 }))
	manager.newID = func() (string, error) { return "compact-large", nil }
	original := mustStoredEntry(t, "tool-original", "output", ToolResult, compactTestTime(), Metadata{
		Role: "tool", ToolCallID: "call-1",
	}, 1000)
	if err := manager.AddEntry(original); err != nil {
		t.Fatalf("AddEntry() error = %v", err)
	}
	transcript := &recordingTranscriptStore{}
	compactor := mustCompactor(t, &fakeCompactionGenerator{toolContent: validToolCompactContent()}, transcript)
	_, err := compactor.MicroCompact(context.Background(), manager, original.ID())
	var notReducing *CompactionNotReducingError
	if !errors.As(err, &notReducing) {
		t.Fatal("MicroCompact() error = nil, want no-token-reduction error")
	}
	if notReducing.Kind != CompactionMicro || !reflect.DeepEqual(notReducing.SourceEntryIDs, []string{original.ID()}) {
		t.Fatalf("non-reducing error = %+v", notReducing)
	}
	if len(transcript.batches) != 0 {
		t.Fatal("non-reducing output was archived despite being rejected")
	}
	if !reflect.DeepEqual(manager.Entries(), []Entry{original}) {
		t.Fatal("manager changed after non-reducing output")
	}
}

func TestReplaceEntriesRejectsNonContiguousSources(t *testing.T) {
	manager := compactTestManager()
	for _, entry := range []Entry{
		mustEntry(t, "one", "one", Summary, 10),
		mustEntry(t, "two", "two", Summary, 10),
		mustEntry(t, "three", "three", Summary, 10),
	} {
		if err := manager.AddEntry(entry); err != nil {
			t.Fatalf("AddEntry() error = %v", err)
		}
	}
	replacement := mustEntry(t, "replacement", "summary", Summary, 5)
	if err := manager.ReplaceEntries([]string{"one", "three"}, replacement); err == nil {
		t.Fatal("ReplaceEntries(non-contiguous) error = nil, want rejection")
	}
	if got := entryIDs(manager.Entries()); !reflect.DeepEqual(got, []string{"one", "two", "three"}) {
		t.Fatalf("entries changed after rejected replacement: %v", got)
	}
}

func TestDecodeJSONObjectAcceptsFencedJSON(t *testing.T) {
	var content ToolResultCompactContent
	err := decodeJSONObject("```json\n"+`{"overview":"ok","key_findings":["found"],"errors":[],"references":[],"omitted_reason":"large"}`+"\n```", &content)
	if err != nil {
		t.Fatalf("decodeJSONObject() error = %v", err)
	}
	if content.Overview != "ok" || !reflect.DeepEqual(content.KeyFindings, []string{"found"}) {
		t.Fatalf("decoded content = %+v", content)
	}
}

func compactTestManager() *Manager {
	manager := NewManager(TokenCounterFunc(func(content string) int {
		return len(content)/100 + 1
	}))
	nextID := 0
	manager.newID = func() (string, error) {
		nextID++
		return "compact-" + string(rune('0'+nextID)), nil
	}
	manager.now = compactTestTime
	return manager
}

func compactTestTime() time.Time {
	return time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
}

func compactTestTurn(t *testing.T, turn int, withTool bool) []Entry {
	t.Helper()
	suffix := string(rune('0' + turn))
	entries := []Entry{
		mustStoredEntry(t, "user-"+suffix, "user request "+suffix, Conversation, compactTestTime(), Metadata{Role: "user"}, 100),
	}
	if withTool {
		callID := "call-" + suffix
		entries = append(entries,
			mustStoredEntry(t, "assistant-tool-"+suffix, "", Conversation, compactTestTime(), Metadata{
				Role: "assistant",
				ToolCalls: []llm.ToolCall{{
					ID: callID, Type: "function", Function: llm.FunctionCall{Name: "read_file"},
				}},
			}, 100),
			mustStoredEntry(t, "tool-"+suffix, "tool output", ToolResult, compactTestTime(), Metadata{
				Role: "tool", ToolCallID: callID, ToolName: "read_file",
			}, 100),
		)
	}
	entries = append(entries, mustStoredEntry(
		t,
		"assistant-final-"+suffix,
		"assistant answer "+suffix,
		Conversation,
		compactTestTime(),
		Metadata{Role: "assistant"},
		100,
	))
	return entries
}

func validToolCompactContent() ToolResultCompactContent {
	return ToolResultCompactContent{
		Overview:      "Tool completed.",
		KeyFindings:   []string{"Relevant result retained."},
		OmittedReason: "Repetitive output omitted.",
	}
}

func validSummaryContent() SummaryContent {
	return SummaryContent{
		Goal:         "Continue the current task.",
		CurrentState: []string{"Earlier work was summarized."},
		Pending:      []string{"Continue implementation."},
		Continuation: "Use the retained recent turn and continue.",
	}
}

func mustCompactor(
	t *testing.T,
	generator CompactionGenerator,
	transcript TranscriptStore,
) *Compactor {
	t.Helper()
	compactor, err := NewCompactor(generator, transcript, "conversation-1")
	if err != nil {
		t.Fatalf("NewCompactor() error = %v", err)
	}
	return compactor
}
