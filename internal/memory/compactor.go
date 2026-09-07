package memory

import (
	"context"
	"fmt"
)

const (
	sessionCompactTurns  = 4
	fullCompactKeepTurns = 1
)

// CompactionGenerator produces validated semantic content. Implementations may
// call an LLM, while tests can provide deterministic results.
type CompactionGenerator interface {
	CompactToolResult(ctx context.Context, entry Entry) (ToolResultCompactContent, error)
	Summarize(ctx context.Context, kind CompactionKind, entries []Entry) (SummaryContent, error)
}

type CompactionResult struct {
	Kind            CompactionKind
	SourceEntryIDs  []string
	Entry           Entry
	OriginalTokens  int
	CompactedTokens int
}

// CompactionNotReducingError identifies a valid generated replacement that is
// not smaller than its sources. The manager and transcript remain unchanged;
// schedulers may safely skip these sources and try another strategy.
type CompactionNotReducingError struct {
	Kind              CompactionKind
	SourceEntryIDs    []string
	OriginalTokens    int
	ReplacementTokens int
}

func (err *CompactionNotReducingError) Error() string {
	return fmt.Sprintf(
		"%s compaction did not reduce tokens: %d >= %d",
		err.Kind,
		err.ReplacementTokens,
		err.OriginalTokens,
	)
}

// Compactor coordinates selection, generation, transcript archival, and the
// atomic replacement of active Manager entries.
type Compactor struct {
	generator      CompactionGenerator
	transcript     TranscriptStore
	conversationID string
}

func NewCompactor(
	generator CompactionGenerator,
	transcript TranscriptStore,
	conversationID string,
) (*Compactor, error) {
	if generator == nil {
		return nil, fmt.Errorf("compaction generator is nil")
	}
	if transcript == nil {
		return nil, fmt.Errorf("transcript store is nil")
	}
	if !validConversationID(conversationID) {
		return nil, fmt.Errorf("invalid conversation ID %q", conversationID)
	}
	return &Compactor{
		generator:      generator,
		transcript:     transcript,
		conversationID: conversationID,
	}, nil
}

// MicroCompact replaces one original TOOL_RESULT with a shorter TOOL_RESULT.
func (c *Compactor) MicroCompact(
	ctx context.Context,
	manager *Manager,
	entryID string,
) (CompactionResult, error) {
	if c == nil {
		return CompactionResult{}, fmt.Errorf("compactor is nil")
	}
	if manager == nil {
		return CompactionResult{}, fmt.Errorf("memory manager is nil")
	}
	entry, exists := manager.Get(entryID)
	if !exists || entry.Type() == Fact {
		return CompactionResult{}, fmt.Errorf("tool result entry %q does not exist", entryID)
	}
	if entry.Type() != ToolResult {
		return CompactionResult{}, fmt.Errorf("entry %q is not a TOOL_RESULT", entryID)
	}
	if entry.IsCompactedToolResult() {
		return CompactionResult{}, fmt.Errorf("tool result entry %q is already compacted", entryID)
	}
	if err := ctx.Err(); err != nil {
		return CompactionResult{}, err
	}

	content, err := c.generator.CompactToolResult(ctx, entry)
	if err != nil {
		return CompactionResult{}, fmt.Errorf("generate MICRO compaction: %w", err)
	}
	markdown, err := content.RenderMarkdown()
	if err != nil {
		return CompactionResult{}, fmt.Errorf("validate MICRO compaction: %w", err)
	}
	metadata := entry.Metadata()
	metadata.Compaction = CompactionMetadata{
		Kind:           CompactionMicro,
		SourceEntryIDs: []string{entry.ID()},
		OriginalTokens: entry.TokenCount(),
	}
	replacement, err := newCompactedEntry(manager, markdown, ToolResult, metadata)
	if err != nil {
		return CompactionResult{}, err
	}
	return c.commit(manager, []Entry{entry}, replacement, CompactionMicro)
}

// SessionCompact replaces the oldest four complete, uncompacted user turns
// after the latest existing summary with one SESSION summary.
func (c *Compactor) SessionCompact(
	ctx context.Context,
	manager *Manager,
) (CompactionResult, bool, error) {
	if c == nil {
		return CompactionResult{}, false, fmt.Errorf("compactor is nil")
	}
	if manager == nil {
		return CompactionResult{}, false, fmt.Errorf("memory manager is nil")
	}
	sources, available, err := sessionCompactSources(manager.Entries())
	if err != nil {
		return CompactionResult{}, false, err
	}
	if !available {
		return CompactionResult{}, false, nil
	}
	result, err := c.summarizeAndCommit(ctx, manager, CompactionSession, sources)
	if err != nil {
		return CompactionResult{}, false, err
	}
	return result, true, nil
}

func sessionCompactSources(entries []Entry) ([]Entry, bool, error) {
	units, err := conversationUnits(entries)
	if err != nil {
		return nil, false, err
	}
	startUnit := 0
	for index, unit := range units {
		if entries[unit.start].Type() == Summary {
			startUnit = index + 1
		}
	}
	if len(units)-startUnit < sessionCompactTurns {
		return nil, false, nil
	}
	selectedUnits := units[startUnit : startUnit+sessionCompactTurns]
	for _, unit := range selectedUnits {
		if entries[unit.start].Type() == Summary || !unit.complete {
			return nil, false, nil
		}
	}
	sources := append([]Entry(nil), entries[selectedUnits[0].start:selectedUnits[len(selectedUnits)-1].end]...)
	return sources, true, nil
}

// FullCompact replaces the safe historical prefix with one FULL summary while
// retaining the most recent user turn verbatim.
func (c *Compactor) FullCompact(
	ctx context.Context,
	manager *Manager,
) (CompactionResult, bool, error) {
	if c == nil {
		return CompactionResult{}, false, fmt.Errorf("compactor is nil")
	}
	if manager == nil {
		return CompactionResult{}, false, fmt.Errorf("memory manager is nil")
	}
	entries := manager.Entries()
	units, err := conversationUnits(entries)
	if err != nil {
		return CompactionResult{}, false, err
	}
	if len(units) == 0 {
		return CompactionResult{}, false, nil
	}
	rawUnitIndexes := make([]int, 0)
	for index, unit := range units {
		if entries[unit.start].Type() != Summary {
			rawUnitIndexes = append(rawUnitIndexes, index)
		}
	}
	endUnit := len(units)
	if len(rawUnitIndexes) >= fullCompactKeepTurns {
		endUnit = rawUnitIndexes[len(rawUnitIndexes)-fullCompactKeepTurns]
	}
	if endUnit == 0 {
		return CompactionResult{}, false, nil
	}
	lastSourceUnit := units[endUnit-1]
	if !lastSourceUnit.complete {
		return CompactionResult{}, false, nil
	}
	sources := append([]Entry(nil), entries[:lastSourceUnit.end]...)
	if len(sources) == 1 && sources[0].Type() == Summary {
		return CompactionResult{}, false, nil
	}
	result, err := c.summarizeAndCommit(ctx, manager, CompactionFull, sources)
	if err != nil {
		return CompactionResult{}, false, err
	}
	return result, true, nil
}

func (c *Compactor) summarizeAndCommit(
	ctx context.Context,
	manager *Manager,
	kind CompactionKind,
	sources []Entry,
) (CompactionResult, error) {
	if err := ctx.Err(); err != nil {
		return CompactionResult{}, err
	}
	content, err := c.generator.Summarize(ctx, kind, append([]Entry(nil), sources...))
	if err != nil {
		return CompactionResult{}, fmt.Errorf("generate %s compaction: %w", kind, err)
	}
	markdown, err := content.RenderMarkdown()
	if err != nil {
		return CompactionResult{}, fmt.Errorf("validate %s compaction: %w", kind, err)
	}
	metadata := Metadata{Compaction: CompactionMetadata{
		Kind:           kind,
		SourceEntryIDs: entryIDsOf(sources),
		OriginalTokens: tokenTotal(sources),
	}}
	replacement, err := newCompactedEntry(manager, markdown, Summary, metadata)
	if err != nil {
		return CompactionResult{}, err
	}
	return c.commit(manager, sources, replacement, kind)
}

func (c *Compactor) commit(
	manager *Manager,
	sources []Entry,
	replacement Entry,
	kind CompactionKind,
) (CompactionResult, error) {
	originalTokens := tokenTotal(sources)
	if replacement.TokenCount() >= originalTokens {
		return CompactionResult{}, &CompactionNotReducingError{
			Kind:              kind,
			SourceEntryIDs:    entryIDsOf(sources),
			OriginalTokens:    originalTokens,
			ReplacementTokens: replacement.TokenCount(),
		}
	}
	if err := c.transcript.Append(c.conversationID, sources...); err != nil {
		return CompactionResult{}, fmt.Errorf("archive %s compaction sources: %w", kind, err)
	}
	sourceIDs := entryIDsOf(sources)
	if err := manager.ReplaceEntries(sourceIDs, replacement); err != nil {
		return CompactionResult{}, fmt.Errorf("apply %s compaction: %w", kind, err)
	}
	return CompactionResult{
		Kind:            kind,
		SourceEntryIDs:  sourceIDs,
		Entry:           replacement,
		OriginalTokens:  originalTokens,
		CompactedTokens: replacement.TokenCount(),
	}, nil
}

func newCompactedEntry(
	manager *Manager,
	content string,
	typ Type,
	metadata Metadata,
) (Entry, error) {
	manager.entryBuildMu.Lock()
	defer manager.entryBuildMu.Unlock()
	id, err := manager.newID()
	if err != nil {
		return Entry{}, fmt.Errorf("create compacted entry ID: %w", err)
	}
	return NewEntry(
		id,
		content,
		typ,
		manager.now().UTC(),
		metadata,
		manager.tokenCounter.Count(content),
	)
}

type conversationUnit struct {
	start    int
	end      int
	complete bool
}

func conversationUnits(entries []Entry) ([]conversationUnit, error) {
	units := make([]conversationUnit, 0)
	for index := 0; index < len(entries); {
		if entries[index].Type() == Summary {
			units = append(units, conversationUnit{start: index, end: index + 1, complete: true})
			index++
			continue
		}
		message, ok := entries[index].Message()
		if !ok || message.Role != "user" {
			return nil, fmt.Errorf("entry %q does not start a complete user turn", entries[index].ID())
		}
		end := index + 1
		for end < len(entries) {
			if entries[end].Type() == Summary {
				break
			}
			message, ok := entries[end].Message()
			if !ok {
				return nil, fmt.Errorf("entry %q cannot participate in a conversation turn", entries[end].ID())
			}
			if message.Role == "user" {
				break
			}
			end++
		}
		complete, err := completeConversationTurn(entries[index:end])
		if err != nil {
			return nil, err
		}
		units = append(units, conversationUnit{start: index, end: end, complete: complete})
		index = end
	}
	return units, nil
}

func completeConversationTurn(entries []Entry) (bool, error) {
	pendingCalls := make(map[string]struct{})
	for index, entry := range entries {
		message, ok := entry.Message()
		if !ok {
			return false, fmt.Errorf("entry %q is not a protocol message", entry.ID())
		}
		switch message.Role {
		case "user":
			if index != 0 {
				return false, fmt.Errorf("entry %q starts an unexpected user turn", entry.ID())
			}
		case "assistant":
			if len(pendingCalls) > 0 {
				return false, fmt.Errorf("assistant entry %q appears before all tool results", entry.ID())
			}
			for _, call := range message.ToolCalls {
				if call.ID == "" {
					return false, fmt.Errorf("assistant entry %q has an empty tool call ID", entry.ID())
				}
				pendingCalls[call.ID] = struct{}{}
			}
		case "tool":
			if _, expected := pendingCalls[message.ToolCallID]; !expected {
				return false, fmt.Errorf("tool entry %q has no matching assistant call", entry.ID())
			}
			delete(pendingCalls, message.ToolCallID)
		default:
			return false, fmt.Errorf("entry %q has unsupported role %q", entry.ID(), message.Role)
		}
	}
	if len(entries) == 0 || len(pendingCalls) > 0 {
		return false, nil
	}
	lastMessage, _ := entries[len(entries)-1].Message()
	return lastMessage.Role == "assistant" && len(lastMessage.ToolCalls) == 0, nil
}

func entryIDsOf(entries []Entry) []string {
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		ids = append(ids, entry.ID())
	}
	return ids
}

func tokenTotal(entries []Entry) int {
	total := 0
	for _, entry := range entries {
		total += entry.TokenCount()
	}
	return total
}
