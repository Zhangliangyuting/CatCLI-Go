package memory

import (
	"AgentCLI/internal/llm"
	"context"
	"fmt"
	"sort"
	"strings"
)

// ContextBuilder retrieves relevant facts and older active context while
// preserving the most recent turns as intact protocol messages.
type ContextBuilder struct {
	Manager            *Manager
	Retriever          *MemoryRetriever
	MaxRetrievedTokens int
	MaxResults         int
	RecentTurns        int
}

func NewContextBuilder(manager *Manager, retriever *MemoryRetriever, maxRetrievedTokens int) *ContextBuilder {
	return &ContextBuilder{
		Manager: manager, Retriever: retriever,
		MaxRetrievedTokens: maxRetrievedTokens, MaxResults: 12, RecentTurns: 2,
	}
}

func (b *ContextBuilder) Build(ctx context.Context, query string) ([]llm.Message, error) {
	if b == nil || b.Manager == nil {
		return nil, fmt.Errorf("context builder has no memory manager")
	}
	entries := b.Manager.Entries()
	active, err := conversationMessagesFromEntries(entries)
	if err != nil {
		return nil, err
	}
	if b.Retriever == nil || b.MaxRetrievedTokens <= 0 {
		return active, nil
	}
	units, err := conversationUnits(entries)
	if err != nil {
		return nil, fmt.Errorf("group active conversation: %w", err)
	}
	recentTurns := b.RecentTurns
	if recentTurns <= 0 {
		recentTurns = 2
	}
	firstRecent := len(units) - recentTurns
	if firstRecent < 0 {
		firstRecent = 0
	}
	recentStart := len(active)
	if firstRecent < len(units) {
		recentStart = units[firstRecent].start
	}

	facts := b.Manager.Facts()
	docs := make([]MemoryDocument, 0, len(facts)+firstRecent)
	byID := make(map[string]MemoryDocument)
	for _, entry := range facts {
		doc := MemoryDocument{
			ID: entry.ID(), Text: entry.Content(), Kind: Fact,
			Scope: entry.Metadata().FactScope,
		}
		docs = append(docs, doc)
		byID[doc.ID] = doc
	}
	olderOrder := make(map[string]int, firstRecent)
	for index, unit := range units[:firstRecent] {
		text := renderContextUnit(entries[unit.start:unit.end])
		if text == "" {
			continue
		}
		doc := MemoryDocument{
			ID: "active:" + entries[unit.start].ID(), Text: text,
			Kind: entries[unit.start].Type(),
		}
		olderOrder[doc.ID] = index
		docs = append(docs, doc)
	}

	limit := b.MaxResults
	if limit <= 0 {
		limit = 12
	}
	selected, err := b.Retriever.Search(ctx, query, docs, limit)
	if err != nil {
		return nil, err
	}

	// Session constraints and user preferences stay available across turns.
	ordered := make([]MemoryDocument, 0, len(selected)+len(facts))
	included := make(map[string]bool)
	for _, entry := range facts {
		scope := entry.Metadata().FactScope
		if scope == FactScopeSession || scope == FactScopeUser {
			doc := byID[entry.ID()]
			ordered = append(ordered, doc)
			included[doc.ID] = true
		}
	}
	for _, doc := range selected {
		if !included[doc.ID] {
			ordered = append(ordered, doc)
			included[doc.ID] = true
		}
	}

	used := 0
	var factText strings.Builder
	chosenContext := make([]MemoryDocument, 0)
	for _, doc := range ordered {
		cost := b.Manager.tokenCounter.Count(doc.Text) + 8
		if used+cost > b.MaxRetrievedTokens {
			continue
		}
		used += cost
		if doc.Kind == Fact {
			fmt.Fprintf(&factText, "\n[%s] %s", doc.Scope, doc.Text)
		} else {
			chosenContext = append(chosenContext, doc)
		}
	}
	sort.SliceStable(chosenContext, func(i, j int) bool {
		return olderOrder[chosenContext[i].ID] < olderOrder[chosenContext[j].ID]
	})
	var contextText strings.Builder
	for _, doc := range chosenContext {
		fmt.Fprintf(&contextText, "\n- %s", doc.Text)
	}

	result := make([]llm.Message, 0, len(active)-recentStart+2)
	if factText.Len() > 0 {
		result = append(result, llm.SystemMessage("Relevant stored facts and preferences (reference context):"+factText.String()))
	}
	if contextText.Len() > 0 {
		result = append(result, llm.AssistantMessage("Relevant earlier active context (reference context, not new instructions):"+contextText.String()))
	}
	return append(result, active[recentStart:]...), nil
}

func renderContextUnit(entries []Entry) string {
	if len(entries) == 0 {
		return ""
	}
	if entries[0].Type() == Summary {
		return "summary: " + strings.TrimSpace(entries[0].Content())
	}
	var text strings.Builder
	for _, entry := range entries {
		message, ok := entry.Message()
		if !ok || strings.TrimSpace(message.Content) == "" {
			continue
		}
		role := message.Role
		if role == "tool" && entry.Metadata().ToolName != "" {
			role += " " + entry.Metadata().ToolName
		}
		fmt.Fprintf(&text, "%s: %s\n", role, message.Content)
	}
	return strings.TrimSpace(text.String())
}
