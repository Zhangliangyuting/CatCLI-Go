package main

import (
	"AgentCLI/internal/agent"
	"AgentCLI/internal/memory"
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestConsoleObserverRetrievalHidesTextUnlessDebug(t *testing.T) {
	event := agent.Event{Type: agent.EventMemoryRetrieval, Retrieval: &memory.RetrievalReport{
		Duration:       5 * time.Millisecond,
		FactMatches:    []memory.MemoryDocument{{ID: "fact:deploy", Text: "private fact", Kind: memory.Fact, Scope: memory.FactScopeProject}},
		ContextMatches: []memory.MemoryDocument{{ID: "active:1", Text: "private conversation", Kind: memory.Conversation}},
		IncludedFacts:  []memory.MemoryDocument{{ID: "fact:deploy", Text: "private fact", Kind: memory.Fact, Scope: memory.FactScopeProject}},
	}}
	var normal bytes.Buffer
	newConsoleObserver(&normal, false)(event)
	if !strings.Contains(normal.String(), "facts=1/1 context=0/1 duration=5ms") || strings.Contains(normal.String(), "private") {
		t.Fatalf("normal retrieval output = %q", normal.String())
	}
	var debug bytes.Buffer
	newConsoleObserver(&debug, true)(event)
	for _, want := range []string{"kind=FACT", "scope=PROJECT", "included=true", "included=false", "private fact", "private conversation"} {
		if !strings.Contains(debug.String(), want) {
			t.Fatalf("debug retrieval output omitted %q: %q", want, debug.String())
		}
	}
}

func TestConsoleObserverNormalModeKeepsProgressAndErrorsConcise(t *testing.T) {
	var output bytes.Buffer
	observer := newConsoleObserver(&output, false)
	for _, event := range []agent.Event{
		{Type: agent.EventTokenUsage, Content: "input=120 output=30"},
		{Type: agent.EventMemoryCompaction, Title: "FULL", Content: "old entries were summarized"},
		{Type: agent.EventToolCall, Title: "read_file", Content: `{"path":"private.txt"}`},
		{Type: agent.EventToolResult, Title: "read_file", Content: "private file contents"},
		{Type: agent.EventToolResult, Title: "read_file", Content: "ERROR: file not found"},
		{Type: agent.EventMemoryFact, Title: "UPSERT", Content: "scope=PROJECT key=database"},
		{Type: agent.EventTaskFailed, TaskID: "task-1", Title: "read", Content: "timeout"},
	} {
		observer(event)
	}
	got := output.String()
	for _, visible := range []string{
		"memory compact FULL", "tool call read_file", "tool result read_file",
		"ERROR: file not found", "memory fact UPSERT", "任务失败", "timeout",
	} {
		if !strings.Contains(got, visible) {
			t.Fatalf("normal output omitted %q: %q", visible, got)
		}
	}
	for _, hidden := range []string{"input=120", "old entries were summarized", "private.txt", "private file contents"} {
		if strings.Contains(got, hidden) {
			t.Fatalf("normal output included debug detail %q: %q", hidden, got)
		}
	}
}

func TestConsoleObserverDebugModeShowsEventDetails(t *testing.T) {
	var output bytes.Buffer
	observer := newConsoleObserver(&output, true)
	for _, event := range []agent.Event{
		{Type: agent.EventTokenUsage, Content: "input=120 output=30"},
		{Type: agent.EventMemoryCompaction, Title: "FULL", Content: "old entries were summarized"},
		{Type: agent.EventToolCall, Title: "read_file", Content: `{"path":"debug.txt"}`},
		{Type: agent.EventToolResult, Title: "read_file", Content: "debug file contents"},
	} {
		observer(event)
	}
	got := output.String()
	for _, detail := range []string{"input=120 output=30", "old entries were summarized", "debug.txt", "debug file contents"} {
		if !strings.Contains(got, detail) {
			t.Fatalf("debug output omitted %q: %q", detail, got)
		}
	}
}

func TestConsoleObserverPrefixesEveryVisibleEventTypeWithEmoji(t *testing.T) {
	tests := []struct {
		event agent.Event
		emoji string
	}{
		{event: agent.Event{Type: agent.EventTokenUsage, Content: "tokens"}, emoji: "🧮"},
		{event: agent.Event{Type: agent.EventMemoryCompaction, Title: "FULL"}, emoji: "🗜️"},
		{event: agent.Event{Type: agent.EventMemoryFact, Title: "UPSERT"}, emoji: "🧠"},
		{event: agent.Event{Type: agent.EventMemoryRetrieval, Retrieval: &memory.RetrievalReport{}}, emoji: "🔎"},
		{event: agent.Event{Type: agent.EventToolCall, Title: "read_file"}, emoji: "🔧"},
		{event: agent.Event{Type: agent.EventToolResult, Title: "read_file"}, emoji: "📦"},
		{event: agent.Event{Type: agent.EventTaskStarted, Title: "task"}, emoji: "▶️"},
		{event: agent.Event{Type: agent.EventTaskCompleted, Title: "task"}, emoji: "✅"},
		{event: agent.Event{Type: agent.EventTaskFailed, Title: "task"}, emoji: "❌"},
		{event: agent.Event{Type: agent.EventTaskCancelled, Title: "task"}, emoji: "⏹️"},
		{event: agent.Event{Type: agent.EventTaskTimeout, Title: "task"}, emoji: "⏱️"},
		{event: agent.Event{Type: agent.EventPlanGenerated, Title: "plan"}, emoji: "🗺️"},
		{event: agent.Event{Type: agent.EventPlanRevised, Title: "plan"}, emoji: "✏️"},
		{event: agent.Event{Type: agent.EventPlanCancelled, Title: "plan"}, emoji: "⏹️"},
		{event: agent.Event{Type: agent.EventPlanReplanning, Title: "plan"}, emoji: "🔄"},
		{event: agent.Event{Type: agent.EventPlanCompleted, Title: "plan"}, emoji: "✅"},
		{event: agent.Event{Type: agent.EventPlanFailed, Title: "plan"}, emoji: "❌"},
		{event: agent.Event{Type: agent.EventPlanTimeout, Title: "plan"}, emoji: "⏱️"},
	}

	for _, test := range tests {
		t.Run(string(test.event.Type), func(t *testing.T) {
			var output bytes.Buffer
			newConsoleObserver(&output, true)(test.event)
			if !strings.Contains(output.String(), test.emoji) {
				t.Fatalf("output for %s = %q, want emoji %q", test.event.Type, output.String(), test.emoji)
			}
		})
	}
}
