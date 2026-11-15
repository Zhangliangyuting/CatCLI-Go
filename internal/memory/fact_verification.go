package memory

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"sync"
)

type FactVerificationOutcome string

const (
	FactVerifiedCurrent     FactVerificationOutcome = "current"
	FactVerifiedStale       FactVerificationOutcome = "stale"
	FactVerificationUnknown FactVerificationOutcome = "unknown"
)

type factVerificationRecord struct {
	contentHash [32]byte
	outcome     FactVerificationOutcome
}

// FactVerificationResult reports a source-check outcome and, when stale, the
// replacement content supported by that source.
type FactVerificationResult struct {
	Outcome  FactVerificationOutcome
	Content  string
	Evidence string
}

// FactVerifier checks selected marked facts against their current sources.
// It is configured by the application layer to keep memory independent of
// the agent and tool implementations.
type FactVerifier func(context.Context, []Entry) (map[string]FactVerificationResult, error)

type factVerificationCache struct {
	mu       sync.RWMutex
	verifyMu sync.Mutex
	records  map[string]factVerificationRecord
	verify   FactVerifier
}

// SetFactVerifier configures lazy source verification for facts selected for
// context injection. Task managers share this session-scoped verifier.
func (m *Manager) SetFactVerifier(verifier FactVerifier) {
	if m == nil || m.verification == nil {
		return
	}
	m.verification.mu.Lock()
	m.verification.verify = verifier
	m.verification.mu.Unlock()
}

// VerifyFactsIfNeeded serializes verification so concurrent workers do not
// check the same selected fact more than once in the same session.
func (m *Manager) VerifyFactsIfNeeded(ctx context.Context, entries []Entry) (map[string]FactVerificationResult, error) {
	outcomes := make(map[string]FactVerificationResult, len(entries))
	if m == nil || m.verification == nil {
		return outcomes, nil
	}
	m.verification.verifyMu.Lock()
	defer m.verification.verifyMu.Unlock()

	var pending []Entry
	for _, entry := range entries {
		if entry.Type() != Fact || !entry.Metadata().NeedsVerification {
			continue
		}
		if outcome, checked := m.factVerification(entry); checked {
			outcomes[entry.ID()] = FactVerificationResult{Outcome: outcome, Content: entry.Content()}
		} else {
			pending = append(pending, entry)
		}
	}
	if len(pending) == 0 {
		return outcomes, nil
	}
	m.verification.mu.RLock()
	verify := m.verification.verify
	m.verification.mu.RUnlock()
	if verify == nil {
		return outcomes, fmt.Errorf("no fact verifier is configured")
	}
	checked, err := verify(ctx, pending)
	for _, entry := range pending {
		result := checked[entry.ID()]
		outcome := result.Outcome
		if outcome != FactVerifiedCurrent && outcome != FactVerifiedStale && outcome != FactVerificationUnknown {
			outcome = FactVerificationUnknown
		}
		result.Outcome = outcome
		if outcome == FactVerifiedCurrent {
			// The current outcome confirms this exact content, not whatever
			// value might later replace it in the Manager.
			result.Content = entry.Content()
		}
		verifiedEntry := entry
		if outcome == FactVerifiedStale && strings.TrimSpace(result.Content) != "" &&
			strings.TrimSpace(result.Evidence) != "" && strings.TrimSpace(result.Content) != strings.TrimSpace(entry.Content()) {
			metadata := entry.Metadata()
			updated, updateErr := m.UpsertFact(metadata.FactScope, metadata.FactKey, result.Content, Metadata{NeedsVerification: true})
			if updateErr != nil {
				err = errors.Join(err, fmt.Errorf("update stale fact %s: %w", entry.ID(), updateErr))
			} else {
				verifiedEntry = updated
				result.Outcome = FactVerifiedCurrent
				result.Content = updated.Content()
				outcome = FactVerifiedCurrent
			}
		}
		if recordErr := m.recordFactVerification(verifiedEntry, outcome); recordErr != nil {
			err = errors.Join(err, fmt.Errorf("cache verification for %s: %w", entry.ID(), recordErr))
			result.Outcome = FactVerificationUnknown
		}
		outcomes[entry.ID()] = result
	}
	return outcomes, err
}

// recordFactVerification caches a source-check result for this Manager's
// session. The cache resets when a new Manager is created and is invalidated
// automatically if the fact content changes.
func (m *Manager) recordFactVerification(entry Entry, outcome FactVerificationOutcome) error {
	if m == nil || entry.Type() != Fact || !entry.Metadata().NeedsVerification {
		return fmt.Errorf("entry is not marked for source verification")
	}
	switch outcome {
	case FactVerifiedCurrent, FactVerifiedStale, FactVerificationUnknown:
	default:
		return fmt.Errorf("invalid fact verification outcome %q", outcome)
	}
	metadata := entry.Metadata()
	current, exists := m.GetFact(metadata.FactScope, metadata.FactKey)
	if !exists || current.Content() != entry.Content() {
		return fmt.Errorf("fact changed before verification result was recorded")
	}
	if m.verification == nil {
		return fmt.Errorf("fact verification cache is unavailable")
	}
	m.verification.mu.Lock()
	m.verification.records[entry.ID()] = factVerificationRecord{
		contentHash: sha256.Sum256([]byte(entry.Content())),
		outcome:     outcome,
	}
	m.verification.mu.Unlock()
	return nil
}

func (m *Manager) factVerification(entry Entry) (FactVerificationOutcome, bool) {
	if m == nil || entry.Type() != Fact || m.verification == nil {
		return "", false
	}
	m.verification.mu.RLock()
	record, exists := m.verification.records[entry.ID()]
	m.verification.mu.RUnlock()
	if !exists || record.contentHash != sha256.Sum256([]byte(entry.Content())) {
		return "", false
	}
	return record.outcome, true
}
