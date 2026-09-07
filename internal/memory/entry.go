package memory

import (
	"AgentCLI/internal/llm"
	"fmt"
	"strings"
	"time"
)

// Type identifies how a memory entry should be used when building context.
type Type string

const (
	Conversation Type = "CONVERSATION"
	Fact         Type = "FACT"
	Summary      Type = "SUMMARY"
	ToolResult   Type = "TOOL_RESULT"
)

// FactScope controls how long a fact should live.
type FactScope string

const (
	FactScopeSession FactScope = "SESSION"
	FactScopeProject FactScope = "PROJECT"
	FactScopeUser    FactScope = "USER"
)

// CompactionKind identifies the compression operation applied to an entry.
// An empty value means the entry still contains its original content.
type CompactionKind string

const (
	CompactionNone    CompactionKind = ""
	CompactionMicro   CompactionKind = "MICRO"
	CompactionSession CompactionKind = "SESSION"
	CompactionFull    CompactionKind = "FULL"
)

func (k CompactionKind) Valid() bool {
	switch k {
	case CompactionNone, CompactionMicro, CompactionSession, CompactionFull:
		return true
	default:
		return false
	}
}

// CompactionMetadata records where compacted content came from. The original
// entries themselves remain available in the transcript store.
type CompactionMetadata struct {
	Kind           CompactionKind `json:"kind,omitempty"`
	SourceEntryIDs []string       `json:"source_entry_ids,omitempty"`
	OriginalTokens int            `json:"original_tokens,omitempty"`
}

func (s FactScope) Valid() bool {
	switch s {
	case FactScopeSession, FactScopeProject, FactScopeUser:
		return true
	default:
		return false
	}
}

// Valid reports whether t is a supported memory type.
func (t Type) Valid() bool {
	switch t {
	case Conversation, Fact, Summary, ToolResult:
		return true
	default:
		return false
	}
}

// Metadata preserves the protocol fields needed to reconstruct an LLM message.
// Attributes remains available for memory-specific information that is not
// part of the chat protocol.
type Metadata struct {
	Role       string             `json:"role,omitempty"`
	ToolName   string             `json:"tool_name,omitempty"`
	ToolCallID string             `json:"tool_call_id,omitempty"`
	ToolCalls  []llm.ToolCall     `json:"tool_calls,omitempty"`
	FactScope  FactScope          `json:"fact_scope,omitempty"`
	FactKey    string             `json:"fact_key,omitempty"`
	Compaction CompactionMetadata `json:"compaction,omitempty"`
	Attributes map[string]string  `json:"attributes,omitempty"`
}

// Entry is an immutable memory value. Metadata is copied on both input and
// output so callers cannot mutate an entry after it has been created.
type Entry struct {
	id         string
	content    string
	typ        Type
	timestamp  time.Time
	metadata   Metadata
	tokenCount int
}

func NewEntry(
	id string,
	content string,
	typ Type,
	timestamp time.Time,
	metadata Metadata,
	tokenCount int,
) (Entry, error) {
	if strings.TrimSpace(id) == "" {
		return Entry{}, fmt.Errorf("memory entry ID is empty")
	}
	if !typ.Valid() {
		return Entry{}, fmt.Errorf("invalid memory type %q", typ)
	}
	if timestamp.IsZero() {
		return Entry{}, fmt.Errorf("memory entry timestamp is zero")
	}
	if tokenCount < 0 {
		return Entry{}, fmt.Errorf("memory entry token count is negative")
	}
	if !metadata.Compaction.Kind.Valid() {
		return Entry{}, fmt.Errorf("invalid compaction kind %q", metadata.Compaction.Kind)
	}
	if metadata.Compaction.OriginalTokens < 0 {
		return Entry{}, fmt.Errorf("compaction original token count is negative")
	}
	switch metadata.Compaction.Kind {
	case CompactionNone:
		if len(metadata.Compaction.SourceEntryIDs) > 0 || metadata.Compaction.OriginalTokens != 0 {
			return Entry{}, fmt.Errorf("compaction metadata has no kind")
		}
	case CompactionMicro:
		if typ != ToolResult {
			return Entry{}, fmt.Errorf("MICRO compaction requires a TOOL_RESULT entry")
		}
	case CompactionSession, CompactionFull:
		if typ != Summary {
			return Entry{}, fmt.Errorf("%s compaction requires a SUMMARY entry", metadata.Compaction.Kind)
		}
	}
	if metadata.Compaction.Kind != CompactionNone && len(metadata.Compaction.SourceEntryIDs) == 0 {
		return Entry{}, fmt.Errorf("%s compaction has no source entries", metadata.Compaction.Kind)
	}

	return Entry{
		id:         id,
		content:    content,
		typ:        typ,
		timestamp:  timestamp,
		metadata:   cloneMetadata(metadata),
		tokenCount: tokenCount,
	}, nil
}

func (e Entry) ID() string {
	return e.id
}

func (e Entry) Content() string {
	return e.content
}

func (e Entry) Type() Type {
	return e.typ
}

func (e Entry) Timestamp() time.Time {
	return e.timestamp
}

func (e Entry) Metadata() Metadata {
	return cloneMetadata(e.metadata)
}

func (e Entry) TokenCount() int {
	return e.tokenCount
}

// IsCompactedToolResult reports whether this is a protocol-preserving tool
// result whose original content has been replaced by MicroCompact output.
func (e Entry) IsCompactedToolResult() bool {
	return e.Type() == ToolResult &&
		e.Metadata().Compaction.Kind == CompactionMicro
}

// Message reconstructs the exact chat-protocol message represented by a
// CONVERSATION or TOOL_RESULT entry. Other entry types are semantic memories
// and therefore do not map directly to one protocol message.
func (e Entry) Message() (llm.Message, bool) {
	metadata := e.Metadata()
	switch e.Type() {
	case Conversation:
		if metadata.Role != "system" && metadata.Role != "user" && metadata.Role != "assistant" {
			return llm.Message{}, false
		}
		return llm.Message{
			Role:      metadata.Role,
			Content:   e.Content(),
			ToolCalls: metadata.ToolCalls,
		}, true
	case ToolResult:
		if metadata.Role != "tool" || strings.TrimSpace(metadata.ToolCallID) == "" {
			return llm.Message{}, false
		}
		return llm.Message{
			Role:       metadata.Role,
			Content:    e.Content(),
			ToolCallID: metadata.ToolCallID,
		}, true
	default:
		return llm.Message{}, false
	}
}

func cloneMetadata(metadata Metadata) Metadata {
	cloned := metadata
	cloned.ToolCalls = append([]llm.ToolCall(nil), metadata.ToolCalls...)
	cloned.Compaction.SourceEntryIDs = append(
		[]string(nil),
		metadata.Compaction.SourceEntryIDs...,
	)
	if len(metadata.Attributes) > 0 {
		cloned.Attributes = make(map[string]string, len(metadata.Attributes))
		for key, value := range metadata.Attributes {
			cloned.Attributes[key] = value
		}
	} else {
		cloned.Attributes = nil
	}
	return cloned
}
