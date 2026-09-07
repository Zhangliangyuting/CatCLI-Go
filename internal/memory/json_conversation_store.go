package memory

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const conversationStateVersion = 1

type JSONConversationStore struct {
	mu   sync.Mutex
	root string
	now  func() time.Time
}

type storedConversationState struct {
	Version      int           `json:"version"`
	UpdatedAt    time.Time     `json:"updated_at"`
	Entries      []storedEntry `json:"entries"`
	SessionFacts []storedEntry `json:"session_facts"`
}

func NewJSONConversationStore(root string) (*JSONConversationStore, error) {
	if strings.TrimSpace(root) == "" {
		return nil, fmt.Errorf("conversation root is empty")
	}
	return &JSONConversationStore{root: root, now: time.Now}, nil
}

func (s *JSONConversationStore) Save(conversationID string, state ConversationState) error {
	if s == nil {
		return fmt.Errorf("conversation store is nil")
	}
	path, err := s.statePath(conversationID)
	if err != nil {
		return err
	}
	stored, err := encodeConversationState(state)
	if err != nil {
		return err
	}
	if stored.UpdatedAt.IsZero() {
		stored.UpdatedAt = s.now().UTC()
	}
	data, err := json.MarshalIndent(stored, "", "  ")
	if err != nil {
		return fmt.Errorf("encode conversation state: %w", err)
	}
	data = append(data, '\n')

	s.mu.Lock()
	defer s.mu.Unlock()
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create conversation directory: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".catcli-state-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary conversation state: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return fmt.Errorf("set conversation state permissions: %w", err)
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return fmt.Errorf("write conversation state: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close conversation state: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace conversation state: %w", err)
	}
	return nil
}

func (s *JSONConversationStore) Load(conversationID string) (ConversationState, bool, error) {
	if s == nil {
		return ConversationState{}, false, fmt.Errorf("conversation store is nil")
	}
	path, err := s.statePath(conversationID)
	if err != nil {
		return ConversationState{}, false, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return ConversationState{}, false, nil
	}
	if err != nil {
		return ConversationState{}, false, fmt.Errorf("read conversation state: %w", err)
	}
	var stored storedConversationState
	if err := json.Unmarshal(data, &stored); err != nil {
		return ConversationState{}, false, fmt.Errorf("decode conversation state: %w", err)
	}
	if stored.Version != conversationStateVersion {
		return ConversationState{}, false, fmt.Errorf("unsupported conversation state version %d", stored.Version)
	}
	state, err := decodeConversationState(stored)
	if err != nil {
		return ConversationState{}, false, err
	}
	return state, true, nil
}

func (s *JSONConversationStore) statePath(conversationID string) (string, error) {
	if !validConversationID(conversationID) {
		return "", fmt.Errorf("invalid conversation ID %q", conversationID)
	}
	return filepath.Join(s.root, conversationID, "state.json"), nil
}

func encodeConversationState(state ConversationState) (storedConversationState, error) {
	stored := storedConversationState{
		Version:      conversationStateVersion,
		UpdatedAt:    state.UpdatedAt,
		Entries:      make([]storedEntry, 0, len(state.Entries)),
		SessionFacts: make([]storedEntry, 0, len(state.SessionFacts)),
	}
	seenIDs := make(map[string]struct{}, len(state.Entries)+len(state.SessionFacts))
	for _, entry := range state.Entries {
		if entry.Type() == Fact {
			return storedConversationState{}, fmt.Errorf("conversation entry %q is a fact", entry.ID())
		}
		if _, err := normalizedEntry(entry); err != nil {
			return storedConversationState{}, fmt.Errorf("invalid conversation entry: %w", err)
		}
		if _, duplicate := seenIDs[entry.ID()]; duplicate {
			return storedConversationState{}, fmt.Errorf("duplicate conversation state entry %q", entry.ID())
		}
		seenIDs[entry.ID()] = struct{}{}
		stored.Entries = append(stored.Entries, storedEntryFromEntry(entry))
	}
	for _, fact := range state.SessionFacts {
		if err := validateSessionFact(fact); err != nil {
			return storedConversationState{}, err
		}
		if _, duplicate := seenIDs[fact.ID()]; duplicate {
			return storedConversationState{}, fmt.Errorf("duplicate conversation state entry %q", fact.ID())
		}
		seenIDs[fact.ID()] = struct{}{}
		stored.SessionFacts = append(stored.SessionFacts, storedEntryFromEntry(fact))
	}
	return stored, nil
}

func decodeConversationState(stored storedConversationState) (ConversationState, error) {
	state := ConversationState{
		Entries:      make([]Entry, 0, len(stored.Entries)),
		SessionFacts: make([]Entry, 0, len(stored.SessionFacts)),
		UpdatedAt:    stored.UpdatedAt,
	}
	seenIDs := make(map[string]struct{}, len(stored.Entries)+len(stored.SessionFacts))
	for _, value := range stored.Entries {
		entry, err := value.entry()
		if err != nil {
			return ConversationState{}, fmt.Errorf("restore conversation entry %q: %w", value.ID, err)
		}
		if entry.Type() == Fact {
			return ConversationState{}, fmt.Errorf("conversation entry %q is a fact", entry.ID())
		}
		if _, duplicate := seenIDs[entry.ID()]; duplicate {
			return ConversationState{}, fmt.Errorf("duplicate conversation state entry %q", entry.ID())
		}
		seenIDs[entry.ID()] = struct{}{}
		state.Entries = append(state.Entries, entry)
	}
	for _, value := range stored.SessionFacts {
		fact, err := value.entry()
		if err != nil {
			return ConversationState{}, fmt.Errorf("restore session fact %q: %w", value.ID, err)
		}
		if err := validateSessionFact(fact); err != nil {
			return ConversationState{}, err
		}
		if _, duplicate := seenIDs[fact.ID()]; duplicate {
			return ConversationState{}, fmt.Errorf("duplicate conversation state entry %q", fact.ID())
		}
		seenIDs[fact.ID()] = struct{}{}
		state.SessionFacts = append(state.SessionFacts, fact)
	}
	return state, nil
}

func validateSessionFact(entry Entry) error {
	entry, err := normalizedEntry(entry)
	if err != nil {
		return fmt.Errorf("invalid session fact: %w", err)
	}
	metadata := entry.Metadata()
	if entry.Type() != Fact || metadata.FactScope != FactScopeSession {
		return fmt.Errorf("entry %q is not a SESSION fact", entry.ID())
	}
	if strings.TrimSpace(metadata.FactKey) == "" {
		return fmt.Errorf("session fact %q has an empty key", entry.ID())
	}
	if entry.ID() != factStorageID(FactScopeSession, metadata.FactKey) {
		return fmt.Errorf("session fact ID %q does not match key", entry.ID())
	}
	return nil
}
