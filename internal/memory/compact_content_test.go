package memory

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestToolResultCompactContentValidatesAndRenders(t *testing.T) {
	content := ToolResultCompactContent{
		Overview: "Read internal/memory/manager.go.",
		KeyFindings: []string{
			"Manager stores entries separately from facts.",
			"ConversationMessages preserves tool protocol fields.",
		},
		References:    []string{"internal/memory/manager.go", "Manager.ConversationMessages"},
		OmittedReason: "Unrelated helpers and repetitive fixtures were omitted.",
	}
	markdown, err := content.RenderMarkdown()
	if err != nil {
		t.Fatalf("RenderMarkdown() error = %v", err)
	}
	for _, required := range []string{
		"## Overview",
		"## Key Findings",
		"## Errors\n\n- None.",
		"## References",
		"## Omitted",
	} {
		if !strings.Contains(markdown, required) {
			t.Fatalf("rendered compact tool result does not contain %q:\n%s", required, markdown)
		}
	}

	data, err := json.Marshal(content)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if !strings.Contains(string(data), `"key_findings"`) || !strings.Contains(string(data), `"omitted_reason"`) {
		t.Fatalf("JSON does not use the fixed schema: %s", data)
	}
}

func TestToolResultCompactContentRejectsMissingInformation(t *testing.T) {
	content := ToolResultCompactContent{
		Overview:      "Tool completed.",
		OmittedReason: "Large output omitted.",
	}
	if err := content.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want missing findings/errors error")
	}
}

func TestSummaryContentValidatesAndRendersFixedSections(t *testing.T) {
	content := SummaryContent{
		Goal:             "Implement structured memory compaction.",
		UserRequirements: []string{"Preserve important decisions."},
		Decisions: []SummaryDecision{{
			Topic:  "Compact content models",
			Choice: "Use two content structures",
			Reason: "Tool output and conversation state have different semantics.",
		}},
		Completed: []SummaryCompleted{{
			Action: "Added compaction metadata",
			Result: "Micro-compacted tool results can be identified.",
		}},
		CurrentState: []string{"Compaction execution is not implemented yet."},
		Pending:      []string{"Implement MicroCompact."},
		ImportantFiles: []SummaryFile{{
			Path:   "internal/memory/entry.go",
			Status: "Modified",
			Notes:  "Contains CompactionMetadata.",
		}},
		FactReferences: []string{"PROJECT:memory-design"},
		Continuation:   "Implement the compactors next.",
	}
	markdown, err := content.RenderMarkdown()
	if err != nil {
		t.Fatalf("RenderMarkdown() error = %v", err)
	}
	headings := []string{
		"## Goal",
		"## User Requirements",
		"## Decisions",
		"## Completed",
		"## Current State",
		"## Pending",
		"## Important Files",
		"## Errors",
		"## Fact References",
		"## Continuation",
	}
	previous := -1
	for _, heading := range headings {
		position := strings.Index(markdown, heading)
		if position <= previous {
			t.Fatalf("heading %q is missing or out of order:\n%s", heading, markdown)
		}
		previous = position
	}
	if !strings.Contains(markdown, "Topic: Compact content models") ||
		!strings.Contains(markdown, "Path: internal/memory/entry.go") {
		t.Fatalf("structured details were not rendered:\n%s", markdown)
	}
}

func TestSummaryContentRejectsMissingRequiredContinuationState(t *testing.T) {
	content := SummaryContent{Goal: "A goal"}
	if err := content.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want missing current state error")
	}
	content.CurrentState = []string{"Work is complete."}
	if err := content.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want missing continuation error")
	}
}
