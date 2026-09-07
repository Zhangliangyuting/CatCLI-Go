package memory

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

type JSONLTranscriptStore struct {
	mu          sync.Mutex
	root        string
	seen        map[string]map[string]struct{}
	initialized map[string]bool
}

func NewJSONLTranscriptStore(root string) (*JSONLTranscriptStore, error) {
	if strings.TrimSpace(root) == "" {
		return nil, fmt.Errorf("transcript root is empty")
	}
	return &JSONLTranscriptStore{
		root:        root,
		seen:        make(map[string]map[string]struct{}),
		initialized: make(map[string]bool),
	}, nil
}

func (s *JSONLTranscriptStore) Append(conversationID string, entries ...Entry) error {
	if s == nil {
		return fmt.Errorf("transcript store is nil")
	}
	path, err := s.conversationPath(conversationID)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := validateEntryForTranscript(entry); err != nil {
			return err
		}
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.initializeSeenLocked(conversationID, path); err != nil {
		return err
	}
	seen := s.seen[conversationID]
	missing := make([]Entry, 0, len(entries))
	pending := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		if _, exists := seen[entry.ID()]; exists {
			continue
		}
		if _, exists := pending[entry.ID()]; exists {
			continue
		}
		pending[entry.ID()] = struct{}{}
		missing = append(missing, entry)
	}
	if len(missing) == 0 {
		return nil
	}
	if err := os.MkdirAll(s.root, 0o700); err != nil {
		return fmt.Errorf("create transcript directory: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open transcript: %w", err)
	}
	encoder := json.NewEncoder(file)
	for _, entry := range missing {
		if err := encoder.Encode(storedEntryFromEntry(entry)); err != nil {
			file.Close()
			return fmt.Errorf("append transcript entry: %w", err)
		}
		seen[entry.ID()] = struct{}{}
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close transcript: %w", err)
	}
	return nil
}

// initializeSeenLocked loads existing IDs once so Append is idempotent across
// restarts. This lets agents record entries when created while compactors can
// still defensively archive the same source entries without duplicating JSONL.
func (s *JSONLTranscriptStore) initializeSeenLocked(conversationID, path string) error {
	if s.initialized[conversationID] {
		return nil
	}
	seen := make(map[string]struct{})
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		s.seen[conversationID] = seen
		s.initialized[conversationID] = true
		return nil
	}
	if err != nil {
		return fmt.Errorf("open transcript for deduplication: %w", err)
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	for {
		var stored storedEntry
		if err := decoder.Decode(&stored); err != nil {
			if err == io.EOF {
				break
			}
			return fmt.Errorf("decode transcript for deduplication: %w", err)
		}
		if strings.TrimSpace(stored.ID) == "" {
			return fmt.Errorf("transcript contains an entry with an empty ID")
		}
		seen[stored.ID] = struct{}{}
	}
	s.seen[conversationID] = seen
	s.initialized[conversationID] = true
	return nil
}

func (s *JSONLTranscriptStore) Load(conversationID string) ([]Entry, error) {
	if s == nil {
		return nil, fmt.Errorf("transcript store is nil")
	}
	path, err := s.conversationPath(conversationID)
	if err != nil {
		return nil, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open transcript: %w", err)
	}
	defer file.Close()

	decoder := json.NewDecoder(file)
	var entries []Entry
	for {
		var stored storedEntry
		if err := decoder.Decode(&stored); err != nil {
			if err == io.EOF {
				break
			}
			return nil, fmt.Errorf("decode transcript: %w", err)
		}
		entry, err := stored.entry()
		if err != nil {
			return nil, fmt.Errorf("restore transcript entry %q: %w", stored.ID, err)
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

func (s *JSONLTranscriptStore) conversationPath(conversationID string) (string, error) {
	if !validConversationID(conversationID) {
		return "", fmt.Errorf("invalid transcript conversation ID %q", conversationID)
	}
	return filepath.Join(s.root, conversationID+".jsonl"), nil
}

func validConversationID(conversationID string) bool {
	if conversationID == "" {
		return false
	}
	for _, char := range conversationID {
		if (char >= 'a' && char <= 'z') ||
			(char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') ||
			char == '-' || char == '_' {
			continue
		}
		return false
	}
	return true
}

func validateEntryForTranscript(entry Entry) error {
	_, err := normalizedEntry(entry)
	if err != nil {
		return fmt.Errorf("invalid transcript entry: %w", err)
	}
	return nil
}
