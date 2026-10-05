package memory

import (
	"context"
	"errors"
	"fmt"
)

// StorageBudget bounds conversation data independently of the tokens selected
// for a model request. Facts have a separate count limit and do not consume the
// conversation entry or byte budgets.
type StorageBudget struct {
	MaxEntries    int
	TargetEntries int
	MaxBytes      int
	TargetBytes   int
	MaxFacts      int
}

func DefaultStorageBudget() StorageBudget {
	return StorageBudget{
		MaxEntries: 2048, TargetEntries: 1536,
		MaxBytes: 16 << 20, TargetBytes: 12 << 20,
		MaxFacts: 512,
	}
}

func (budget StorageBudget) Validate() error {
	if budget.MaxEntries <= 0 || budget.TargetEntries <= 0 || budget.TargetEntries >= budget.MaxEntries ||
		budget.MaxBytes <= 0 || budget.TargetBytes <= 0 || budget.TargetBytes >= budget.MaxBytes ||
		budget.MaxFacts <= 0 {
		return fmt.Errorf("invalid Manager storage budget")
	}
	return nil
}

type StorageUsage struct {
	Entries int
	Bytes   int
}

func (usage StorageUsage) exceeds(budget StorageBudget) bool {
	return usage.Entries > budget.MaxEntries || usage.Bytes > budget.MaxBytes
}

func (usage StorageUsage) aboveTarget(budget StorageBudget) bool {
	return usage.Entries > budget.TargetEntries || usage.Bytes > budget.TargetBytes
}

// StorageBudgetExceededError means the newest conversation unit alone exceeds
// the configured conversation bound after compressible history is exhausted.
type StorageBudgetExceededError struct {
	Usage  StorageUsage
	Budget StorageBudget
}

func (err *StorageBudgetExceededError) Error() string {
	return fmt.Sprintf("conversation storage remains over budget: entries=%d/%d bytes=%d/%d; only the newest conversation unit remains",
		err.Usage.Entries, err.Budget.MaxEntries, err.Usage.Bytes, err.Budget.MaxBytes)
}

// StorageUsage counts conversation entries and estimates only their heap
// footprint. Facts are governed separately by StorageBudget.MaxFacts.
func (m *Manager) StorageUsage() StorageUsage {
	if m == nil {
		return StorageUsage{}
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	usage := StorageUsage{Entries: len(m.entries)}
	for _, entry := range m.entries {
		usage.Bytes += approximateEntryBytes(entry)
	}
	return usage
}

func approximateEntryBytes(entry Entry) int {
	bytes := 256 + len(entry.id) + len(entry.content) + len(entry.typ)
	meta := entry.metadata
	bytes += len(meta.Role) + len(meta.ToolName) + len(meta.ToolCallID) + len(meta.FactScope) + len(meta.FactKey) + len(meta.Compaction.Kind)
	for _, id := range meta.Compaction.SourceEntryIDs {
		bytes += 16 + len(id)
	}
	for _, call := range meta.ToolCalls {
		bytes += 64 + len(call.ID) + len(call.Type) + len(call.Function.Name) + len(call.Function.Arguments)
	}
	for key, value := range meta.Attributes {
		bytes += 32 + len(key) + len(value)
	}
	return bytes
}

// EnforceStorageBudget first evicts facts above their independent count limit
// in least-recently-used order. Conversation limits are then enforced by
// compacting old units and, when no useful compaction remains, archiving and
// removing the oldest complete unit. Facts are never evicted to satisfy the
// conversation byte or entry limits, and the newest conversation unit remains.
func (scheduler *CompactionScheduler) EnforceStorageBudget(ctx context.Context, manager *Manager) ([]CompactionDecision, error) {
	if scheduler == nil || scheduler.compactor == nil || manager == nil {
		return nil, fmt.Errorf("storage budget requires a scheduler and Manager")
	}
	budget := scheduler.storageBudget
	if err := budget.Validate(); err != nil {
		return nil, err
	}
	decisions := make([]CompactionDecision, 0)
	for manager.FactLen() > budget.MaxFacts {
		if err := ctx.Err(); err != nil {
			return decisions, err
		}
		fact, evicted, err := manager.evictLeastRecentlyUsedFact()
		if err != nil {
			return decisions, fmt.Errorf("evict least recently used fact: %w", err)
		}
		if !evicted {
			break
		}
		decisions = append(decisions, CompactionDecision{
			Action: CompactionActionEvict, Reason: "Fact count limit: removed least recently used fact",
			BeforeTokens: fact.TokenCount(), Result: CompactionResult{
				Kind: CompactionNone, SourceEntryIDs: []string{fact.ID()}, OriginalTokens: fact.TokenCount(),
			},
		})
	}

	usage := manager.StorageUsage()
	if !usage.exceeds(budget) {
		return decisions, nil
	}
	// Each successful operation removes an entry or changes the oldest unit.
	// The extra attempts allow for one nonreducing summary and byte growth.
	attemptLimit := usage.Entries*2 + 4
	for attempts := 0; attempts < attemptLimit && usage.aboveTarget(budget); attempts++ {
		if err := ctx.Err(); err != nil {
			return decisions, err
		}
		before := usage
		result, available, err := scheduler.compactor.FullCompact(ctx, manager)
		var notReducing *CompactionNotReducingError
		if err != nil && !errors.As(err, &notReducing) {
			return decisions, fmt.Errorf("compress Manager storage: %w", err)
		}
		if err == nil && available {
			usage = manager.StorageUsage()
			decisions = append(decisions, CompactionDecision{
				Action: CompactionActionFull, Reason: "Manager storage limit: compress old conversation",
				BeforeTokens: result.OriginalTokens, AfterTokens: result.CompactedTokens, Result: result,
			})
			if usage.Entries < before.Entries || usage.Bytes < before.Bytes {
				continue
			}
		}
		entries := manager.Entries()
		units, err := conversationUnits(entries)
		if err != nil {
			return decisions, fmt.Errorf("select Manager eviction unit: %w", err)
		}
		if len(units) < 2 || !units[0].complete {
			break
		}
		oldest := append([]Entry(nil), entries[units[0].start:units[0].end]...)
		if err := scheduler.compactor.transcript.Append(scheduler.compactor.conversationID, oldest...); err != nil {
			return decisions, fmt.Errorf("archive Manager eviction sources: %w", err)
		}
		if err := manager.removePrefix(entryIDsOf(oldest)); err != nil {
			return decisions, fmt.Errorf("evict Manager prefix: %w", err)
		}
		usage = manager.StorageUsage()
		decisions = append(decisions, CompactionDecision{
			Action: CompactionActionEvict, Reason: "Manager storage limit: archived and removed oldest complete conversation unit",
			BeforeTokens: tokenTotal(oldest),
		})
	}
	if usage.exceeds(budget) {
		return decisions, &StorageBudgetExceededError{Usage: usage, Budget: budget}
	}
	return decisions, nil
}
