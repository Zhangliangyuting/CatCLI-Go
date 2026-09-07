package memory

import (
	"AgentCLI/internal/llm"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

type sequencedCompactionGenerator struct {
	toolContents   []ToolResultCompactContent
	toolCalls      []string
	summaryContent SummaryContent
}

func (generator *sequencedCompactionGenerator) CompactToolResult(
	_ context.Context,
	entry Entry,
) (ToolResultCompactContent, error) {
	generator.toolCalls = append(generator.toolCalls, entry.ID())
	index := len(generator.toolCalls) - 1
	if index >= len(generator.toolContents) {
		index = len(generator.toolContents) - 1
	}
	return generator.toolContents[index], nil
}

func (generator *sequencedCompactionGenerator) Summarize(
	context.Context,
	CompactionKind,
	[]Entry,
) (SummaryContent, error) {
	return generator.summaryContent, nil
}

func compactContentNamed(name string) ToolResultCompactContent {
	return ToolResultCompactContent{
		Overview:      name,
		KeyFindings:   []string{"result"},
		OmittedReason: "test",
	}
}

func TestCompactToFitRepeatsUntilLowWaterMark(t *testing.T) {
	manager := NewManager(TokenCounterFunc(func(string) int { return 350 }))
	nextID := 0
	manager.newID = func() (string, error) {
		nextID++
		return "low-water-compact-" + string(rune('0'+nextID)), nil
	}
	entries := []Entry{
		mustEntry(t, "existing-summary", "summary", Summary, 200),
		mustStoredEntry(t, "tool-1", "large", ToolResult, compactTestTime(), Metadata{Role: "tool", ToolCallID: "call-1"}, 400),
		mustStoredEntry(t, "tool-2", "large", ToolResult, compactTestTime(), Metadata{Role: "tool", ToolCallID: "call-2"}, 400),
		mustStoredEntry(t, "tool-3", "large", ToolResult, compactTestTime(), Metadata{Role: "tool", ToolCallID: "call-3"}, 400),
	}
	for _, entry := range entries {
		if err := manager.AddEntry(entry); err != nil {
			t.Fatalf("AddEntry() error = %v", err)
		}
	}
	scheduler := mustScheduler(t, 2000, &recordingTranscriptStore{})

	decisions, err := scheduler.CompactToFit(context.Background(), manager)
	if err != nil {
		t.Fatalf("CompactToFit() error = %v", err)
	}
	if len(decisions) != 2 {
		t.Fatalf("decision count = %d, want 2", len(decisions))
	}
	if manager.TotalTokens() != 1300 {
		t.Fatalf("final tokens = %d, want 1300", manager.TotalTokens())
	}
}

func TestCompactToFitStopsAfterBoundAndRejectsOversizedContext(t *testing.T) {
	manager := NewManager(TokenCounterFunc(func(string) int { return 150 }))
	nextID := 0
	manager.newID = func() (string, error) {
		nextID++
		return "bounded-compact-" + string(rune('0'+nextID)), nil
	}
	toolCalls := make([]llm.ToolCall, 0, 6)
	entries := []Entry{
		mustStoredEntry(t, "current-user", "read files", Conversation, compactTestTime(), Metadata{Role: "user"}, 100),
	}
	for index := 1; index <= 6; index++ {
		callID := "bounded-call-" + string(rune('0'+index))
		toolCalls = append(toolCalls, llm.ToolCall{
			ID: callID, Type: "function", Function: llm.FunctionCall{Name: "read_file"},
		})
	}
	entries = append(entries, mustStoredEntry(
		t,
		"current-assistant",
		"",
		Conversation,
		compactTestTime(),
		Metadata{Role: "assistant", ToolCalls: toolCalls},
		100,
	))
	for index, call := range toolCalls {
		entries = append(entries, mustStoredEntry(
			t,
			"bounded-tool-"+string(rune('0'+index+1)),
			"large output",
			ToolResult,
			compactTestTime(),
			Metadata{Role: "tool", ToolCallID: call.ID, ToolName: "read_file"},
			200,
		))
	}
	for _, entry := range entries {
		if err := manager.AddEntry(entry); err != nil {
			t.Fatalf("AddEntry() error = %v", err)
		}
	}
	scheduler := mustScheduler(t, 1000, &recordingTranscriptStore{})

	decisions, err := scheduler.CompactToFit(context.Background(), manager)
	var budgetErr *ContextBudgetExceededError
	if !errors.As(err, &budgetErr) {
		t.Fatalf("CompactToFit() error = %v, want ContextBudgetExceededError", err)
	}
	if len(decisions) != 3 || budgetErr.Attempts != 3 {
		t.Fatalf("decisions=%d attempts=%d, want 3", len(decisions), budgetErr.Attempts)
	}
	if manager.TotalTokens() != 1250 {
		t.Fatalf("final tokens = %d, want 1250", manager.TotalTokens())
	}
}

func TestCompactToFitMeasuredUsesCompleteRequestTokens(t *testing.T) {
	manager := compactTestManager()
	if err := manager.AddEntry(mustEntry(t, "summary", "small", Summary, 100)); err != nil {
		t.Fatalf("AddEntry() error = %v", err)
	}
	scheduler := mustScheduler(t, 1000, &recordingTranscriptStore{})

	measurements := 0
	measure := func() (int, error) {
		measurements++
		return 1100, nil
	}
	decisions, err := scheduler.CompactToFitMeasured(context.Background(), manager, measure)
	var budgetErr *ContextBudgetExceededError
	if !errors.As(err, &budgetErr) {
		t.Fatalf("CompactToFitMeasured() error = %v, want ContextBudgetExceededError", err)
	}
	if len(decisions) != 0 || budgetErr.CurrentTokens != 1100 {
		t.Fatalf("decisions=%v budget error=%+v", decisions, budgetErr)
	}
	if measurements < 2 {
		t.Fatalf("measurements = %d, want at least initial and final measurements", measurements)
	}
}

func TestCompactToFitMeasuredTriggersFromRequestOverhead(t *testing.T) {
	manager := compactTestManager()
	toolResult := mustStoredEntry(
		t,
		"overhead-tool",
		"large output",
		ToolResult,
		compactTestTime(),
		Metadata{Role: "tool", ToolCallID: "call-overhead", ToolName: "read_file"},
		200,
	)
	if err := manager.AddEntry(toolResult); err != nil {
		t.Fatalf("AddEntry() error = %v", err)
	}
	scheduler := mustScheduler(t, 1000, &recordingTranscriptStore{})
	measure := func() (int, error) {
		// The manager alone is only 20%% full. System prompt, tool definitions,
		// and request framing raise the complete request above the 60%% trigger.
		return manager.TotalTokens() + 450, nil
	}

	decisions, err := scheduler.CompactToFitMeasured(context.Background(), manager, measure)
	if err != nil {
		t.Fatalf("CompactToFitMeasured() error = %v", err)
	}
	if len(decisions) != 1 || decisions[0].Action != CompactionActionMicro {
		t.Fatalf("decisions = %+v, want one MicroCompact", decisions)
	}
	if decisions[0].BeforeTokens != 650 || decisions[0].AfterTokens >= decisions[0].BeforeTokens {
		t.Fatalf("decision token measurements = %+v", decisions[0])
	}
}

func TestCompactToFitSkipsNonReducingMicroAndTriesNextCandidate(t *testing.T) {
	manager := NewManager(TokenCounterFunc(func(content string) int {
		switch {
		case strings.Contains(content, "OVERSIZED"):
			return 600
		case strings.Contains(content, "SMALL"):
			return 50
		default:
			return 1
		}
	}))
	manager.newID = func() (string, error) { return "replacement", nil }
	for _, entry := range []Entry{
		mustStoredEntry(t, "tool-largest", "first", ToolResult, compactTestTime(), Metadata{Role: "tool", ToolCallID: "call-1"}, 500),
		mustStoredEntry(t, "tool-next", "second", ToolResult, compactTestTime(), Metadata{Role: "tool", ToolCallID: "call-2"}, 400),
	} {
		if err := manager.AddEntry(entry); err != nil {
			t.Fatalf("AddEntry() error = %v", err)
		}
	}
	generator := &sequencedCompactionGenerator{
		toolContents: []ToolResultCompactContent{
			compactContentNamed("OVERSIZED"),
			compactContentNamed("SMALL"),
		},
		summaryContent: validSummaryContent(),
	}
	scheduler := schedulerWithGenerator(t, 1600, generator, &recordingTranscriptStore{})
	decisions, err := scheduler.CompactToFitMeasured(context.Background(), manager, func() (int, error) {
		return manager.TotalTokens() + 100, nil
	})
	if err != nil {
		t.Fatalf("CompactToFitMeasured() error = %v", err)
	}
	if got := []CompactionAction{decisions[0].Action, decisions[1].Action}; !reflect.DeepEqual(got, []CompactionAction{CompactionActionSkip, CompactionActionMicro}) {
		t.Fatalf("decision actions = %v, want [SKIP MICRO]", got)
	}
	if !reflect.DeepEqual(generator.toolCalls, []string{"tool-largest", "tool-next"}) {
		t.Fatalf("Micro candidates = %v, want largest then next", generator.toolCalls)
	}
	if _, exists := manager.Get("tool-largest"); !exists {
		t.Fatal("non-reducing source was modified")
	}
	toolCallsBefore := len(generator.toolCalls)
	if _, err := scheduler.CompactToFitMeasured(context.Background(), manager, func() (int, error) {
		return manager.TotalTokens() + 500, nil
	}); err != nil {
		t.Fatalf("second CompactToFitMeasured() error = %v", err)
	}
	if len(generator.toolCalls) != toolCallsBefore {
		t.Fatalf("previously skipped candidate was retried: calls=%v", generator.toolCalls)
	}
}

func TestCompactToFitFallsBackToFullAfterMicroCandidatesDoNotReduce(t *testing.T) {
	manager := compactionFallbackManager(t, 100)
	generator := &sequencedCompactionGenerator{
		toolContents: []ToolResultCompactContent{
			compactContentNamed("OVERSIZED"),
			compactContentNamed("OVERSIZED"),
		},
		summaryContent: validSummaryContent(),
	}
	transcript := &recordingTranscriptStore{}
	scheduler := schedulerWithGenerator(t, 1600, generator, transcript)
	decisions, err := scheduler.CompactToFitMeasured(context.Background(), manager, func() (int, error) {
		return manager.TotalTokens() + 100, nil
	})
	if err != nil {
		t.Fatalf("CompactToFitMeasured() error = %v", err)
	}
	actions := make([]CompactionAction, 0, len(decisions))
	for _, decision := range decisions {
		actions = append(actions, decision.Action)
	}
	if !reflect.DeepEqual(actions, []CompactionAction{CompactionActionSkip, CompactionActionSkip, CompactionActionFull}) {
		t.Fatalf("decision actions = %v, want [SKIP SKIP FULL]", actions)
	}
	if len(transcript.batches) != 1 {
		t.Fatalf("archived batches = %d, want only successful FullCompact", len(transcript.batches))
	}
}

func TestCompactToFitReturnsBudgetErrorOnlyAfterIneffectiveStrategies(t *testing.T) {
	manager := incompleteToolTurnManager(t, 1000)
	generator := &sequencedCompactionGenerator{
		toolContents: []ToolResultCompactContent{
			compactContentNamed("OVERSIZED"),
			compactContentNamed("OVERSIZED"),
		},
		summaryContent: validSummaryContent(),
	}
	scheduler := schedulerWithGenerator(t, 1600, generator, &recordingTranscriptStore{})
	decisions, err := scheduler.CompactToFitMeasured(context.Background(), manager, func() (int, error) {
		return manager.TotalTokens() + 800, nil
	})
	var budgetErr *ContextBudgetExceededError
	if !errors.As(err, &budgetErr) {
		t.Fatalf("CompactToFitMeasured() error = %v, want ContextBudgetExceededError", err)
	}
	if len(decisions) != 2 || decisions[0].Action != CompactionActionSkip || decisions[1].Action != CompactionActionSkip {
		t.Fatalf("decisions = %+v, want two skipped Micro candidates", decisions)
	}
	if budgetErr.Attempts < 3 {
		t.Fatalf("budget attempts = %d, want alternatives exhausted", budgetErr.Attempts)
	}
}

func TestCompactToFitAllowsOriginalContextWhenIneffectiveButUnderHardBudget(t *testing.T) {
	manager := incompleteToolTurnManager(t, 1000)
	generator := &sequencedCompactionGenerator{
		toolContents: []ToolResultCompactContent{
			compactContentNamed("OVERSIZED"),
			compactContentNamed("OVERSIZED"),
		},
		summaryContent: validSummaryContent(),
	}
	scheduler := schedulerWithGenerator(t, 1600, generator, &recordingTranscriptStore{})
	decisions, err := scheduler.CompactToFitMeasured(context.Background(), manager, func() (int, error) {
		return manager.TotalTokens() + 100, nil
	})
	if err != nil {
		t.Fatalf("CompactToFitMeasured() error = %v, want original context to proceed", err)
	}
	if len(decisions) != 2 || decisions[0].Action != CompactionActionSkip || decisions[1].Action != CompactionActionSkip {
		t.Fatalf("decisions = %+v, want two skipped candidates", decisions)
	}
}

func TestCompactionSchedulerDoesNothingBelowSixtyPercent(t *testing.T) {
	manager := compactTestManager()
	if err := manager.AddEntry(mustEntry(t, "summary", "small", Summary, 599)); err != nil {
		t.Fatalf("AddEntry() error = %v", err)
	}
	scheduler := mustScheduler(t, 1000, &recordingTranscriptStore{})
	decision, err := scheduler.CompactIfNeeded(context.Background(), manager)
	if err != nil {
		t.Fatalf("CompactIfNeeded() error = %v", err)
	}
	if decision.Action != CompactionActionNone || decision.BeforeTokens != 599 || decision.AfterTokens != 599 {
		t.Fatalf("decision = %+v, want no-op at 59.9%%", decision)
	}
}

func TestCompactionSchedulerSelectsLargestToolResultAtSixtyPercent(t *testing.T) {
	manager := compactTestManager()
	for _, entry := range []Entry{
		mustStoredEntry(t, "tool-small", strings.Repeat("s", 200), ToolResult, compactTestTime(), Metadata{
			Role: "tool", ToolCallID: "call-small",
		}, 200),
		mustStoredEntry(t, "tool-large", strings.Repeat("l", 400), ToolResult, compactTestTime(), Metadata{
			Role: "tool", ToolCallID: "call-large",
		}, 400),
	} {
		if err := manager.AddEntry(entry); err != nil {
			t.Fatalf("AddEntry() error = %v", err)
		}
	}
	transcript := &recordingTranscriptStore{}
	scheduler := mustScheduler(t, 1000, transcript)
	decision, err := scheduler.CompactIfNeeded(context.Background(), manager)
	if err != nil {
		t.Fatalf("CompactIfNeeded() error = %v", err)
	}
	if decision.Action != CompactionActionMicro || decision.Result.SourceEntryIDs[0] != "tool-large" {
		t.Fatalf("decision = %+v, want largest TOOL_RESULT MicroCompact", decision)
	}
	if len(transcript.batches) != 1 || transcript.batches[0][0].ID() != "tool-large" {
		t.Fatalf("archived batch = %#v", transcript.batches)
	}
	if manager.Entries()[0].ID() != "tool-small" {
		t.Fatal("scheduler compacted more than one tool result in one pass")
	}
}

func TestCompactionSchedulerRunsSessionOnlyWhenEstimateReachesTarget(t *testing.T) {
	t.Run("reaches target", func(t *testing.T) {
		manager := compactTestManager()
		for turn := 1; turn <= 5; turn++ {
			for _, entry := range compactTestTurn(t, turn, false) {
				if err := manager.AddEntry(entry); err != nil {
					t.Fatalf("AddEntry() error = %v", err)
				}
			}
		}
		scheduler := mustScheduler(t, 1333, &recordingTranscriptStore{})
		decision, err := scheduler.CompactIfNeeded(context.Background(), manager)
		if err != nil {
			t.Fatalf("CompactIfNeeded() error = %v", err)
		}
		if decision.Action != CompactionActionSession || decision.Result.Kind != CompactionSession {
			t.Fatalf("decision = %+v, want SessionCompact", decision)
		}
	})

	t.Run("estimate remains above target", func(t *testing.T) {
		manager := compactTestManager()
		projectFact := mustFactEntry(t, FactScopeProject, "large", "persistent context", 670)
		if err := manager.AddEntry(projectFact); err != nil {
			t.Fatalf("AddEntry(fact) error = %v", err)
		}
		for turn := 1; turn <= 4; turn++ {
			for _, entry := range schedulerSimpleTurn(t, turn, 10) {
				if err := manager.AddEntry(entry); err != nil {
					t.Fatalf("AddEntry() error = %v", err)
				}
			}
		}
		scheduler := mustScheduler(t, 1000, &recordingTranscriptStore{})
		decision, err := scheduler.CompactIfNeeded(context.Background(), manager)
		if err != nil {
			t.Fatalf("CompactIfNeeded() error = %v", err)
		}
		if decision.Action != CompactionActionNone || !strings.Contains(decision.Reason, "exceeds target") {
			t.Fatalf("decision = %+v, want deferred SessionCompact", decision)
		}
		if manager.Len() != 8 {
			t.Fatal("manager changed despite failed Session estimate")
		}
	})
}

func TestCompactionSchedulerRunsDirectFullAtEightyFivePercent(t *testing.T) {
	manager := compactTestManager()
	turns := make([][]Entry, 0, 3)
	for turn := 1; turn <= 3; turn++ {
		entries := schedulerSimpleTurn(t, turn, 150)
		turns = append(turns, entries)
		for _, entry := range entries {
			if err := manager.AddEntry(entry); err != nil {
				t.Fatalf("AddEntry() error = %v", err)
			}
		}
	}
	scheduler := mustScheduler(t, 1000, &recordingTranscriptStore{})
	decision, err := scheduler.CompactIfNeeded(context.Background(), manager)
	if err != nil {
		t.Fatalf("CompactIfNeeded() error = %v", err)
	}
	if decision.Action != CompactionActionFull || decision.Emergency {
		t.Fatalf("decision = %+v, want non-emergency FullCompact", decision)
	}
	if !reflect.DeepEqual(entryIDs(manager.Entries()[1:]), entryIDsOf(turns[2])) {
		t.Fatal("FullCompact did not retain the latest turn")
	}
}

func TestCompactionSchedulerEmergencyFallsBackToMicro(t *testing.T) {
	manager := compactTestManager()
	entries := []Entry{
		mustStoredEntry(t, "user-current", "run tool", Conversation, compactTestTime(), Metadata{Role: "user"}, 50),
		mustStoredEntry(t, "assistant-current", "", Conversation, compactTestTime(), Metadata{
			Role: "assistant",
			ToolCalls: []llm.ToolCall{{
				ID: "call-current", Type: "function", Function: llm.FunctionCall{Name: "read_file"},
			}},
		}, 100),
		mustStoredEntry(t, "tool-current", strings.Repeat("large", 200), ToolResult, compactTestTime(), Metadata{
			Role: "tool", ToolCallID: "call-current", ToolName: "read_file",
		}, 800),
	}
	for _, entry := range entries {
		if err := manager.AddEntry(entry); err != nil {
			t.Fatalf("AddEntry() error = %v", err)
		}
	}
	scheduler := mustScheduler(t, 1000, &recordingTranscriptStore{})
	decision, err := scheduler.CompactIfNeeded(context.Background(), manager)
	if err != nil {
		t.Fatalf("CompactIfNeeded() error = %v", err)
	}
	if decision.Action != CompactionActionMicro || !decision.Emergency {
		t.Fatalf("decision = %+v, want emergency Micro fallback", decision)
	}
}

func TestCompactionSchedulerConfigRejectsInvalidOrdering(t *testing.T) {
	config := DefaultCompactionSchedulerConfig(1000)
	config.FullThreshold = config.SessionThreshold
	if _, err := NewCompactionScheduler(&Compactor{}, config); err == nil {
		t.Fatal("NewCompactionScheduler() error = nil, want threshold validation error")
	}
}

func mustScheduler(
	t *testing.T,
	maxTokens int,
	transcript TranscriptStore,
) *CompactionScheduler {
	t.Helper()
	generator := &fakeCompactionGenerator{
		toolContent:    validToolCompactContent(),
		summaryContent: validSummaryContent(),
	}
	compactor := mustCompactor(t, generator, transcript)
	config := DefaultCompactionSchedulerConfig(maxTokens)
	scheduler, err := NewCompactionScheduler(compactor, config)
	if err != nil {
		t.Fatalf("NewCompactionScheduler() error = %v", err)
	}
	return scheduler
}

func schedulerWithGenerator(
	t *testing.T,
	maxTokens int,
	generator CompactionGenerator,
	transcript TranscriptStore,
) *CompactionScheduler {
	t.Helper()
	compactor := mustCompactor(t, generator, transcript)
	scheduler, err := NewCompactionScheduler(compactor, DefaultCompactionSchedulerConfig(maxTokens))
	if err != nil {
		t.Fatalf("NewCompactionScheduler() error = %v", err)
	}
	return scheduler
}

func compactionFallbackManager(t *testing.T, summaryTokens int) *Manager {
	t.Helper()
	manager := NewManager(TokenCounterFunc(func(content string) int {
		switch {
		case strings.Contains(content, "## Overview"):
			return 600
		case strings.Contains(content, "## Goal"):
			return summaryTokens
		default:
			return 1
		}
	}))
	nextID := 0
	manager.newID = func() (string, error) {
		nextID++
		return "fallback-replacement-" + string(rune('0'+nextID)), nil
	}
	call1 := llm.ToolCall{ID: "fallback-call-1", Type: "function", Function: llm.FunctionCall{Name: "read_file"}}
	call2 := llm.ToolCall{ID: "fallback-call-2", Type: "function", Function: llm.FunctionCall{Name: "read_file"}}
	entries := []Entry{
		mustStoredEntry(t, "fallback-user-1", "inspect", Conversation, compactTestTime(), Metadata{Role: "user"}, 10),
		mustStoredEntry(t, "fallback-assistant-tools", "", Conversation, compactTestTime(), Metadata{Role: "assistant", ToolCalls: []llm.ToolCall{call1, call2}}, 10),
		mustStoredEntry(t, "fallback-tool-1", "large one", ToolResult, compactTestTime(), Metadata{Role: "tool", ToolCallID: call1.ID}, 500),
		mustStoredEntry(t, "fallback-tool-2", "large two", ToolResult, compactTestTime(), Metadata{Role: "tool", ToolCallID: call2.ID}, 400),
		mustStoredEntry(t, "fallback-assistant-1", "done", Conversation, compactTestTime(), Metadata{Role: "assistant"}, 10),
		mustStoredEntry(t, "fallback-user-2", "continue", Conversation, compactTestTime(), Metadata{Role: "user"}, 10),
		mustStoredEntry(t, "fallback-assistant-2", "ok", Conversation, compactTestTime(), Metadata{Role: "assistant"}, 10),
	}
	for _, entry := range entries {
		if err := manager.AddEntry(entry); err != nil {
			t.Fatalf("AddEntry() error = %v", err)
		}
	}
	return manager
}

func incompleteToolTurnManager(t *testing.T, compactedTokens int) *Manager {
	t.Helper()
	manager := NewManager(TokenCounterFunc(func(content string) int {
		if strings.Contains(content, "## Overview") {
			return compactedTokens
		}
		return 1
	}))
	nextID := 0
	manager.newID = func() (string, error) {
		nextID++
		return "incomplete-replacement-" + string(rune('0'+nextID)), nil
	}
	call1 := llm.ToolCall{ID: "incomplete-call-1", Type: "function", Function: llm.FunctionCall{Name: "read_file"}}
	call2 := llm.ToolCall{ID: "incomplete-call-2", Type: "function", Function: llm.FunctionCall{Name: "read_file"}}
	entries := []Entry{
		mustStoredEntry(t, "incomplete-user", "inspect", Conversation, compactTestTime(), Metadata{Role: "user"}, 10),
		mustStoredEntry(t, "incomplete-assistant", "", Conversation, compactTestTime(), Metadata{Role: "assistant", ToolCalls: []llm.ToolCall{call1, call2}}, 10),
		mustStoredEntry(t, "incomplete-tool-1", "large one", ToolResult, compactTestTime(), Metadata{Role: "tool", ToolCallID: call1.ID}, 500),
		mustStoredEntry(t, "incomplete-tool-2", "large two", ToolResult, compactTestTime(), Metadata{Role: "tool", ToolCallID: call2.ID}, 400),
	}
	for _, entry := range entries {
		if err := manager.AddEntry(entry); err != nil {
			t.Fatalf("AddEntry() error = %v", err)
		}
	}
	return manager
}

func schedulerSimpleTurn(t *testing.T, turn, tokensPerEntry int) []Entry {
	t.Helper()
	suffix := string(rune('0' + turn))
	return []Entry{
		mustStoredEntry(t, "scheduler-user-"+suffix, "request", Conversation, compactTestTime(), Metadata{Role: "user"}, tokensPerEntry),
		mustStoredEntry(t, "scheduler-assistant-"+suffix, "answer", Conversation, compactTestTime(), Metadata{Role: "assistant"}, tokensPerEntry),
	}
}
