package memory

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

type FactAction string

const (
	FactActionUpsert FactAction = "UPSERT"
	FactActionRemove FactAction = "REMOVE"
)

func (action FactAction) Valid() bool {
	return action == FactActionUpsert || action == FactActionRemove
}

// FactOperation is a validated mutation proposed by a FactExtractor.
type FactOperation struct {
	Action  FactAction `json:"action"`
	Scope   FactScope  `json:"scope"`
	Key     string     `json:"key"`
	Content string     `json:"content,omitempty"`
}

var factKeyPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._:-]{0,127}$`)

func (operation FactOperation) Validate() error {
	if !operation.Action.Valid() {
		return fmt.Errorf("invalid fact action %q", operation.Action)
	}
	if !operation.Scope.Valid() {
		return fmt.Errorf("invalid fact scope %q", operation.Scope)
	}
	if !factKeyPattern.MatchString(strings.TrimSpace(operation.Key)) {
		return fmt.Errorf("invalid fact key %q", operation.Key)
	}
	if operation.Action == FactActionUpsert && strings.TrimSpace(operation.Content) == "" {
		return fmt.Errorf("UPSERT fact %q has empty content", operation.Key)
	}
	return nil
}

// FactExtractor discovers explicit memory mutations from one user message.
// Implementations return no operations when the message is not worth storing.
type FactExtractor interface {
	Extract(ctx context.Context, userMessage string, existingFacts []Entry) ([]FactOperation, error)
}

// ApplyFactOperations validates the complete batch before mutating the manager.
func ApplyFactOperations(manager *Manager, operations []FactOperation) error {
	if manager == nil {
		return fmt.Errorf("memory manager is nil")
	}
	seen := make(map[string]struct{}, len(operations))
	for index, operation := range operations {
		if err := operation.Validate(); err != nil {
			return fmt.Errorf("fact operation %d: %w", index, err)
		}
		identity := string(operation.Scope) + ":" + operation.Key
		if _, duplicate := seen[identity]; duplicate {
			return fmt.Errorf("fact operation %d duplicates %q", index, identity)
		}
		seen[identity] = struct{}{}
	}

	for _, operation := range operations {
		switch operation.Action {
		case FactActionUpsert:
			if _, err := manager.UpsertFact(
				operation.Scope,
				operation.Key,
				operation.Content,
				Metadata{},
			); err != nil {
				return fmt.Errorf("upsert %s fact %q: %w", operation.Scope, operation.Key, err)
			}
		case FactActionRemove:
			if _, err := manager.RemoveFact(operation.Scope, operation.Key); err != nil {
				return fmt.Errorf("remove %s fact %q: %w", operation.Scope, operation.Key, err)
			}
		}
	}
	return nil
}
