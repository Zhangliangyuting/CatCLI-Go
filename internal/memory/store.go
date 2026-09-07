package memory

import "time"

// NewConversationID creates a filesystem-safe random identifier suitable for
// ConversationStore and TranscriptStore keys.
func NewConversationID() (string, error) {
	return randomID()
}

// StoredFact is the persistence representation of a project or user fact.
// Token counts are recalculated by Manager with its configured counter.
type StoredFact struct {
	Scope   FactScope
	Key     string
	Content string
}

// FactStore persists facts that survive the current session. Implementations
// must reject SESSION facts rather than silently making them persistent.
type FactStore interface {
	Load() ([]StoredFact, error)
	Upsert(fact StoredFact) error
	Remove(scope FactScope, key string) error
}

// TranscriptStore persists the exact entries removed or produced during a
// conversation. Entries are stored in order and can be reconstructed losslessly.
type TranscriptStore interface {
	Append(conversationID string, entries ...Entry) error
	Load(conversationID string) ([]Entry, error)
}

// ConversationState is the resumable, conversation-local view of a Manager.
// Entries includes summaries; only SESSION facts belong here because PROJECT
// and USER facts are restored independently through FactStore.
type ConversationState struct {
	Entries      []Entry
	SessionFacts []Entry
	UpdatedAt    time.Time
}

// ConversationStore persists the current compacted state of one conversation.
// The boolean returned by Load distinguishes a missing conversation from an
// existing conversation whose state is empty.
type ConversationStore interface {
	Save(conversationID string, state ConversationState) error
	Load(conversationID string) (ConversationState, bool, error)
}
