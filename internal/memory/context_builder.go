package memory

import (
	"AgentCLI/internal/llm"
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// RetrievalReport distinguishes search hits from documents that fit the
// retrieved-context token budget.
type RetrievalReport struct {
	Duration        time.Duration
	FactMatches     []MemoryDocument
	ContextMatches  []MemoryDocument
	IncludedFacts   []MemoryDocument
	IncludedContext []MemoryDocument
}

// ContextBuilder retrieves relevant facts and older active context while
// preserving the most recent turns as intact protocol messages.
type ContextBuilder struct {
	Manager            *Manager
	Retriever          *MemoryRetriever
	MaxRetrievedTokens int
	MaxFactResults     int
	MaxContextResults  int
	RecentTurns        int
}

func NewContextBuilder(manager *Manager, retriever *MemoryRetriever, maxRetrievedTokens int) *ContextBuilder {
	return &ContextBuilder{
		Manager: manager, Retriever: retriever,
		MaxRetrievedTokens: maxRetrievedTokens,
		MaxFactResults:     6, MaxContextResults: 6, RecentTurns: 2,
	}
}

func (b *ContextBuilder) Build(ctx context.Context, query string) ([]llm.Message, error) {
	return b.BuildWithReport(ctx, query, nil)
}

func (b *ContextBuilder) BuildWithReport(ctx context.Context, query string, report func(RetrievalReport)) ([]llm.Message, error) {
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
	factDocs := make([]MemoryDocument, 0, len(facts))
	factsByID := make(map[string]Entry, len(facts))
	for _, entry := range facts {
		doc := MemoryDocument{
			ID: entry.ID(), Text: entry.Content(), Kind: Fact,
			Scope: entry.Metadata().FactScope,
		}
		factDocs = append(factDocs, doc)
		factsByID[entry.ID()] = entry
	}
	contextDocs := make([]MemoryDocument, 0, firstRecent)
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
		contextDocs = append(contextDocs, doc)
	}

	factLimit := b.MaxFactResults
	if factLimit <= 0 {
		factLimit = 6
	}
	contextLimit := b.MaxContextResults
	if contextLimit <= 0 {
		contextLimit = 6
	}
	started := time.Now()
	selectedFacts, err := b.Retriever.Search(ctx, query, factDocs, factLimit)
	if err != nil {
		return nil, err
	}
	selectedContext, err := b.Retriever.Search(ctx, query, contextDocs, contextLimit)
	if err != nil {
		return nil, err
	}
	retrieval := RetrievalReport{Duration: time.Since(started), FactMatches: selectedFacts, ContextMatches: selectedContext}

	used := 0
	chosenFacts := make([]MemoryDocument, 0, len(selectedFacts))
	chosenContext := make([]MemoryDocument, 0)
	include := func(doc MemoryDocument) {
		cost := b.Manager.tokenCounter.Count(doc.Text) + 8
		if used+cost > b.MaxRetrievedTokens {
			return
		}
		used += cost
		if doc.Kind == Fact {
			chosenFacts = append(chosenFacts, doc)
		} else {
			chosenContext = append(chosenContext, doc)
			retrieval.IncludedContext = append(retrieval.IncludedContext, doc)
		}
	}
	// First decide what would actually fit in the context. A marked fact that
	// gets dropped by the token budget is not used, so it should not trigger a
	// source check.
	// Give both categories an opportunity to use the shared token budget.
	for index := 0; index < len(selectedFacts) || index < len(selectedContext); index++ {
		if index < len(selectedFacts) {
			include(selectedFacts[index])
		}
		if index < len(selectedContext) {
			include(selectedContext[index])
		}
	}
	selectedMarkedFacts := make([]Entry, 0, len(chosenFacts))
	for _, doc := range chosenFacts {
		if fact, ok := factsByID[doc.ID]; ok && fact.Metadata().NeedsVerification {
			selectedMarkedFacts = append(selectedMarkedFacts, fact)
		}
	}
	// Verify only facts that are relevant and fit in this request's context.
	verificationOutcomes, err := b.Manager.VerifyFactsIfNeeded(ctx, selectedMarkedFacts)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
	}
	var factText strings.Builder
	for _, doc := range chosenFacts {
		reservedCost := b.Manager.tokenCounter.Count(doc.Text) + 8
		fact, ok := factsByID[doc.ID]
		if ok && fact.Metadata().NeedsVerification {
			verified := verificationOutcomes[fact.ID()]
			if verified.Outcome != FactVerifiedCurrent {
				used -= reservedCost
				continue
			}
			metadata := fact.Metadata()
			current, exists := b.Manager.GetFact(metadata.FactScope, metadata.FactKey)
			if !exists || current.Content() != verified.Content {
				used -= reservedCost
				continue
			}
			doc.Text = verified.Content
		}
		used -= reservedCost
		actualCost := b.Manager.tokenCounter.Count(doc.Text) + 8
		if used+actualCost > b.MaxRetrievedTokens {
			continue
		}
		used += actualCost
		fmt.Fprintf(&factText, "\n[%s] %s", doc.Scope, doc.Text)
		retrieval.IncludedFacts = append(retrieval.IncludedFacts, doc)
		b.Manager.TouchFact(doc.ID)
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
	if report != nil {
		report(retrieval)
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
