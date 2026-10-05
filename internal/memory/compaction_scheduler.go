package memory

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

const (
	defaultMicroThreshold     = 0.60
	defaultSessionThreshold   = 0.75
	defaultFullThreshold      = 0.85
	defaultEmergencyThreshold = 0.95
	defaultSessionTarget      = 0.65
	defaultMaxCompactions     = 3
	extraStrategyAttempts     = 3
)

type CompactionAction string

const (
	CompactionActionNone    CompactionAction = "NONE"
	CompactionActionMicro   CompactionAction = "MICRO"
	CompactionActionSession CompactionAction = "SESSION"
	CompactionActionFull    CompactionAction = "FULL"
	CompactionActionSkip    CompactionAction = "SKIP"
	CompactionActionEvict   CompactionAction = "EVICT"
)

type CompactionSchedulerConfig struct {
	MaxContextTokens              int
	MicroThreshold                float64
	SessionThreshold              float64
	FullThreshold                 float64
	EmergencyThreshold            float64
	SessionTarget                 float64
	EstimatedSessionSummaryTokens int
	MinimumMicroSourceTokens      int
	MaxCompactionsPerRequest      int
}

func DefaultCompactionSchedulerConfig(maxContextTokens int) CompactionSchedulerConfig {
	estimatedSummaryTokens := maxContextTokens / 20
	if estimatedSummaryTokens < 1 {
		estimatedSummaryTokens = 1
	}
	minimumMicroTokens := maxContextTokens / 100
	if minimumMicroTokens < 1 {
		minimumMicroTokens = 1
	}
	return CompactionSchedulerConfig{
		MaxContextTokens:              maxContextTokens,
		MicroThreshold:                defaultMicroThreshold,
		SessionThreshold:              defaultSessionThreshold,
		FullThreshold:                 defaultFullThreshold,
		EmergencyThreshold:            defaultEmergencyThreshold,
		SessionTarget:                 defaultSessionTarget,
		EstimatedSessionSummaryTokens: estimatedSummaryTokens,
		MinimumMicroSourceTokens:      minimumMicroTokens,
		MaxCompactionsPerRequest:      defaultMaxCompactions,
	}
}

func (config CompactionSchedulerConfig) Validate() error {
	if config.MaxContextTokens <= 0 {
		return fmt.Errorf("maximum context tokens must be positive")
	}
	if !(config.MicroThreshold > 0 &&
		config.MicroThreshold < config.SessionTarget &&
		config.SessionTarget < config.SessionThreshold &&
		config.SessionThreshold < config.FullThreshold &&
		config.FullThreshold < config.EmergencyThreshold &&
		config.EmergencyThreshold <= 1) {
		return fmt.Errorf("invalid compaction threshold ordering")
	}
	if config.EstimatedSessionSummaryTokens <= 0 {
		return fmt.Errorf("estimated session summary tokens must be positive")
	}
	if config.MinimumMicroSourceTokens <= 0 {
		return fmt.Errorf("minimum MicroCompact source tokens must be positive")
	}
	if config.MaxCompactionsPerRequest <= 0 {
		return fmt.Errorf("maximum compactions per request must be positive")
	}
	return nil
}

type CompactionDecision struct {
	Action       CompactionAction
	Emergency    bool
	BeforeTokens int
	AfterTokens  int
	UsageBefore  float64
	UsageAfter   float64
	Reason       string
	Result       CompactionResult
}

type CompactionScheduler struct {
	compactor       *Compactor
	config          CompactionSchedulerConfig
	storageBudget   StorageBudget
	skippedMu       sync.RWMutex
	skippedMicroIDs map[string]struct{}
}

// ContextTokenMeasurer returns the current complete request estimate. The
// scheduler invokes it again after every memory mutation.
type ContextTokenMeasurer func() (int, error)

// ContextBudgetExceededError means protected/current context still cannot fit
// after all safe compaction attempts. Callers must not send that context to the
// model or silently truncate it.
type ContextBudgetExceededError struct {
	CurrentTokens int
	MaximumTokens int
	Attempts      int
}

func (err *ContextBudgetExceededError) Error() string {
	return fmt.Sprintf(
		"context remains over budget after %d compaction operations: tokens=%d maximum=%d usage=%.1f%%",
		err.Attempts,
		err.CurrentTokens,
		err.MaximumTokens,
		float64(err.CurrentTokens)/float64(err.MaximumTokens)*100,
	)
}

func NewCompactionScheduler(
	compactor *Compactor,
	config CompactionSchedulerConfig,
) (*CompactionScheduler, error) {
	if compactor == nil {
		return nil, fmt.Errorf("compactor is nil")
	}
	if err := config.Validate(); err != nil {
		return nil, err
	}
	return &CompactionScheduler{
		compactor:       compactor,
		config:          config,
		storageBudget:   DefaultStorageBudget(),
		skippedMicroIDs: make(map[string]struct{}),
	}, nil
}

// CompactToFit performs bounded, progressive compaction before one
// model request. The supplied measurer must return the complete logical request
// size, including memory, system prompts, tool definitions, and other messages.
// It re-measures after every operation and aims for SessionTarget as a low-water
// mark. If safe compaction is exhausted while the hard input budget is still
// exceeded, it returns ContextBudgetExceededError.
func (scheduler *CompactionScheduler) CompactToFit(
	ctx context.Context,
	manager *Manager,
	measure ContextTokenMeasurer,
) ([]CompactionDecision, error) {
	if scheduler == nil || scheduler.compactor == nil {
		return nil, fmt.Errorf("compaction scheduler is nil")
	}
	if manager == nil {
		return nil, fmt.Errorf("memory manager is nil")
	}
	if measure == nil {
		return nil, fmt.Errorf("context token measurer is nil")
	}

	var decisions []CompactionDecision
	successfulCompactions := 0
	attempts := 0
	attemptLimit := scheduler.config.MaxCompactionsPerRequest + extraStrategyAttempts
	forceMicro := false
	forceFull := false
	fullBlocked := false
	microRejected := false
	microRejections := 0
	for successfulCompactions < scheduler.config.MaxCompactionsPerRequest && attempts < attemptLimit {
		if err := ctx.Err(); err != nil {
			return decisions, err
		}
		beforeTokens, err := measure()
		if err != nil {
			return decisions, fmt.Errorf("measure context before compaction: %w", err)
		}
		usage := float64(beforeTokens) / float64(scheduler.config.MaxContextTokens)
		if successfulCompactions > 0 && usage <= scheduler.config.SessionTarget {
			break
		}

		var decision CompactionDecision
		switch {
		case forceMicro:
			decision = scheduler.initialDecision(beforeTokens)
			decision.Emergency = usage >= scheduler.config.EmergencyThreshold
			decision, err = scheduler.microOrNoop(
				ctx,
				manager,
				decision,
				"trying another TOOL_RESULT after an ineffective compaction strategy",
			)
		case forceFull:
			decision = scheduler.initialDecision(beforeTokens)
			decision.Emergency = usage >= scheduler.config.EmergencyThreshold
			decision, err = scheduler.fullOrNoop(
				ctx,
				manager,
				decision,
				"MicroCompact candidates were ineffective; trying FullCompact",
			)
		case fullBlocked && usage >= scheduler.config.FullThreshold:
			decision = scheduler.initialDecision(beforeTokens)
			decision.Emergency = usage >= scheduler.config.EmergencyThreshold
			decision, err = scheduler.microOrNoop(
				ctx,
				manager,
				decision,
				"FullCompact was ineffective; trying an eligible TOOL_RESULT",
			)
		default:
			decision, err = scheduler.compactIfNeeded(ctx, manager, beforeTokens)
		}
		attempts++
		if err != nil {
			var notReducing *CompactionNotReducingError
			if errors.As(err, &notReducing) {
				skipped := scheduler.skippedDecision(beforeTokens, notReducing)
				decisions = append(decisions, skipped)
				switch notReducing.Kind {
				case CompactionMicro:
					scheduler.markMicroSkipped(notReducing.SourceEntryIDs...)
					microRejected = true
					microRejections++
					if microRejections >= 2 && !fullBlocked {
						forceMicro = false
						forceFull = true
					} else {
						forceMicro = true
						forceFull = false
					}
				case CompactionSession:
					forceMicro = false
					forceFull = true
				case CompactionFull:
					fullBlocked = true
					forceMicro = true
					forceFull = false
				}
				continue
			}
			return decisions, err
		}
		if decision.Action == CompactionActionNone {
			if forceFull {
				fullBlocked = true
				forceFull = false
				forceMicro = true
				continue
			}
			if forceMicro {
				forceMicro = false
				if microRejected && !fullBlocked {
					forceFull = true
					continue
				}
			}
			break
		}
		forceMicro = false
		forceFull = false
		afterTokens, err := measure()
		if err != nil {
			return decisions, fmt.Errorf("measure context after compaction: %w", err)
		}
		decision.BeforeTokens = beforeTokens
		decision.AfterTokens = afterTokens
		decision.UsageBefore = usage
		decision.UsageAfter = float64(afterTokens) / float64(scheduler.config.MaxContextTokens)
		decisions = append(decisions, decision)
		if decision.AfterTokens >= decision.BeforeTokens {
			return decisions, fmt.Errorf(
				"%s compaction did not reduce context tokens: %d -> %d",
				decision.Action,
				decision.BeforeTokens,
				decision.AfterTokens,
			)
		}
		successfulCompactions++
	}

	currentTokens, err := measure()
	if err != nil {
		return decisions, fmt.Errorf("measure final context: %w", err)
	}
	if currentTokens > scheduler.config.MaxContextTokens {
		return decisions, &ContextBudgetExceededError{
			CurrentTokens: currentTokens,
			MaximumTokens: scheduler.config.MaxContextTokens,
			Attempts:      attempts,
		}
	}
	return decisions, nil
}

func (scheduler *CompactionScheduler) compactIfNeeded(
	ctx context.Context,
	manager *Manager,
	beforeTokens int,
) (CompactionDecision, error) {
	if beforeTokens < 0 {
		return CompactionDecision{}, fmt.Errorf("measured context tokens cannot be negative")
	}
	usage := float64(beforeTokens) / float64(scheduler.config.MaxContextTokens)
	decision := scheduler.initialDecision(beforeTokens)
	if err := ctx.Err(); err != nil {
		return decision, err
	}

	switch {
	case usage >= scheduler.config.EmergencyThreshold:
		decision.Emergency = true
		result, compacted, err := scheduler.compactor.FullCompact(ctx, manager)
		if err != nil {
			return decision, err
		}
		if compacted {
			return scheduler.completedDecision(decision, manager, CompactionActionFull, result,
				"emergency usage triggered direct FullCompact"), nil
		}
		return scheduler.microOrNoop(ctx, manager, decision,
			"emergency FullCompact had no safe historical prefix")

	case usage >= scheduler.config.FullThreshold:
		return scheduler.fullOrNoop(ctx, manager, decision,
			"usage reached the FullCompact threshold")

	case usage >= scheduler.config.SessionThreshold:
		sources, available, err := sessionCompactSources(manager.Entries())
		if err != nil {
			return decision, err
		}
		if !available {
			decision.Reason = "fewer than four complete turns are available for SessionCompact"
			return decision, nil
		}
		estimatedTokens := beforeTokens - tokenTotal(sources) + scheduler.config.EstimatedSessionSummaryTokens
		estimatedUsage := float64(estimatedTokens) / float64(scheduler.config.MaxContextTokens)
		if estimatedUsage > scheduler.config.SessionTarget {
			decision.Reason = fmt.Sprintf(
				"SessionCompact estimate %.3f exceeds target %.3f",
				estimatedUsage,
				scheduler.config.SessionTarget,
			)
			return decision, nil
		}
		result, compacted, err := scheduler.compactor.SessionCompact(ctx, manager)
		if err != nil {
			return decision, err
		}
		if !compacted {
			decision.Reason = "SessionCompact candidate changed before execution"
			return decision, nil
		}
		return scheduler.completedDecision(decision, manager, CompactionActionSession, result,
			"SessionCompact is estimated to reach the target usage"), nil

	case usage >= scheduler.config.MicroThreshold:
		return scheduler.microOrNoop(ctx, manager, decision,
			"usage reached the MicroCompact threshold")

	default:
		decision.Reason = "usage is below the MicroCompact threshold"
		return decision, nil
	}
}

func (scheduler *CompactionScheduler) initialDecision(beforeTokens int) CompactionDecision {
	usage := float64(beforeTokens) / float64(scheduler.config.MaxContextTokens)
	return CompactionDecision{
		Action:       CompactionActionNone,
		BeforeTokens: beforeTokens,
		AfterTokens:  beforeTokens,
		UsageBefore:  usage,
		UsageAfter:   usage,
	}
}

func (scheduler *CompactionScheduler) fullOrNoop(
	ctx context.Context,
	manager *Manager,
	decision CompactionDecision,
	reason string,
) (CompactionDecision, error) {
	result, compacted, err := scheduler.compactor.FullCompact(ctx, manager)
	if err != nil {
		return decision, err
	}
	if !compacted {
		decision.Reason = reason + "; no safe historical prefix exists"
		return decision, nil
	}
	return scheduler.completedDecision(
		decision,
		manager,
		CompactionActionFull,
		result,
		reason,
	), nil
}

func (scheduler *CompactionScheduler) microOrNoop(
	ctx context.Context,
	manager *Manager,
	decision CompactionDecision,
	reason string,
) (CompactionDecision, error) {
	decision.Reason = reason
	candidate, available := largestMicroCandidate(
		manager.Entries(),
		scheduler.config.MinimumMicroSourceTokens,
		scheduler.skippedMicroSnapshot(),
	)
	if !available {
		decision.Reason = reason + "; no eligible TOOL_RESULT exists"
		return decision, nil
	}
	result, err := scheduler.compactor.MicroCompact(ctx, manager, candidate.ID())
	if err != nil {
		return decision, err
	}
	return scheduler.completedDecision(decision, manager, CompactionActionMicro, result, reason), nil
}

func (scheduler *CompactionScheduler) skippedDecision(
	beforeTokens int,
	err *CompactionNotReducingError,
) CompactionDecision {
	decision := scheduler.initialDecision(beforeTokens)
	decision.Action = CompactionActionSkip
	decision.AfterTokens = beforeTokens
	decision.Reason = fmt.Sprintf(
		"skipped non-reducing %s candidate: replacement=%d original=%d sources=%v",
		err.Kind,
		err.ReplacementTokens,
		err.OriginalTokens,
		err.SourceEntryIDs,
	)
	decision.Result = CompactionResult{
		Kind:            err.Kind,
		SourceEntryIDs:  append([]string(nil), err.SourceEntryIDs...),
		OriginalTokens:  err.OriginalTokens,
		CompactedTokens: err.ReplacementTokens,
	}
	return decision
}

func (scheduler *CompactionScheduler) markMicroSkipped(entryIDs ...string) {
	scheduler.skippedMu.Lock()
	defer scheduler.skippedMu.Unlock()
	for _, entryID := range entryIDs {
		scheduler.skippedMicroIDs[entryID] = struct{}{}
	}
}

func (scheduler *CompactionScheduler) skippedMicroSnapshot() map[string]struct{} {
	scheduler.skippedMu.RLock()
	defer scheduler.skippedMu.RUnlock()
	result := make(map[string]struct{}, len(scheduler.skippedMicroIDs))
	for entryID := range scheduler.skippedMicroIDs {
		result[entryID] = struct{}{}
	}
	return result
}

func (scheduler *CompactionScheduler) completedDecision(
	decision CompactionDecision,
	manager *Manager,
	action CompactionAction,
	result CompactionResult,
	reason string,
) CompactionDecision {
	afterTokens := manager.TotalTokens()
	decision.Action = action
	decision.AfterTokens = afterTokens
	decision.UsageAfter = float64(afterTokens) / float64(scheduler.config.MaxContextTokens)
	decision.Reason = reason
	decision.Result = result
	return decision
}

func largestMicroCandidate(
	entries []Entry,
	minimumTokens int,
	excluded map[string]struct{},
) (Entry, bool) {
	var selected Entry
	found := false
	for _, entry := range entries {
		if _, skip := excluded[entry.ID()]; skip {
			continue
		}
		if entry.Type() != ToolResult || entry.Metadata().Compaction.Kind != CompactionNone ||
			entry.TokenCount() < minimumTokens {
			continue
		}
		if !found || entry.TokenCount() > selected.TokenCount() {
			selected = entry
			found = true
		}
	}
	return selected, found
}
