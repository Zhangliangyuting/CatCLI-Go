package memory

import "time"

type storedEntry struct {
	ID         string    `json:"id"`
	Content    string    `json:"content"`
	Type       Type      `json:"type"`
	Timestamp  time.Time `json:"timestamp"`
	Metadata   Metadata  `json:"metadata"`
	TokenCount int       `json:"token_count"`
}

func storedEntryFromEntry(entry Entry) storedEntry {
	return storedEntry{
		ID:         entry.ID(),
		Content:    entry.Content(),
		Type:       entry.Type(),
		Timestamp:  entry.Timestamp(),
		Metadata:   entry.Metadata(),
		TokenCount: entry.TokenCount(),
	}
}

func (stored storedEntry) entry() (Entry, error) {
	return NewEntry(
		stored.ID,
		stored.Content,
		stored.Type,
		stored.Timestamp,
		stored.Metadata,
		stored.TokenCount,
	)
}
