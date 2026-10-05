package memory

import (
	"AgentCLI/internal/llm"
	"context"
	"os"
	"reflect"
	"strings"
	"testing"
)

func addTestTurn(t *testing.T, manager *Manager, user, assistant string) {
	t.Helper()
	for _, message := range []llm.Message{llm.UserMessage(user), llm.AssistantMessage(assistant)} {
		if _, err := manager.AddMessage(message, ""); err != nil {
			t.Fatal(err)
		}
	}
}

func TestContextBuilderRetrievesFactsAndOlderActiveTurns(t *testing.T) {
	manager := NewManager(nil)
	for _, fact := range []struct {
		scope   FactScope
		key     string
		content string
	}{
		{FactScopeSession, "language", "Answer in Chinese"},
		{FactScopeUser, "brevity", "Prefer brief replies"},
		{FactScopeProject, "database", "The database is SQLite"},
		{FactScopeProject, "language", "The project uses Go"},
	} {
		if _, err := manager.UpsertFact(fact.scope, fact.key, fact.content, Metadata{}); err != nil {
			t.Fatal(err)
		}
	}
	addTestTurn(t, manager, "Where is the deployment guide?", "The guide is in docs/deploy.md")
	addTestTurn(t, manager, "What language is this project?", "The project uses Go")
	addTestTurn(t, manager, "What is the current status?", "Ready")
	current := llm.UserMessage("Where is the deployment guide?")
	if _, err := manager.AddMessage(current, ""); err != nil {
		t.Fatal(err)
	}

	builder := NewContextBuilder(manager, NewMemoryRetriever(nil), 1000)
	messages, err := builder.Build(context.Background(), current.Content)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 4 || messages[0].Role != "assistant" {
		t.Fatalf("messages = %#v", messages)
	}
	for _, message := range messages {
		if strings.Contains(message.Content, "Answer in Chinese") || strings.Contains(message.Content, "Prefer brief replies") {
			t.Fatalf("unrelated SESSION or USER fact was injected: %#v", messages)
		}
	}
	if !strings.Contains(messages[0].Content, "docs/deploy.md") || strings.Contains(messages[0].Content, "project uses Go") {
		t.Fatalf("older active turn selection = %#v", messages[0])
	}
	if got := messages[len(messages)-1]; !reflect.DeepEqual(got, current) {
		t.Fatalf("current user message = %#v", got)
	}

	messages, err = builder.Build(context.Background(), "SQLite")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(messages[0].Content, "SQLite") || strings.Contains(messages[0].Content, "project uses Go") {
		t.Fatalf("PROJECT fact selection = %#v", messages[0])
	}
}

func TestContextBuilderPreservesRecentToolProtocol(t *testing.T) {
	manager := NewManager(TokenCounterFunc(func(content string) int { return len([]rune(content)) }))
	if _, err := manager.UpsertFact(FactScopeUser, "language", "请使用中文", Metadata{}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.UpsertFact(FactScopeProject, "deploy", "部署脚本在 scripts/deploy.sh", Metadata{}); err != nil {
		t.Fatal(err)
	}
	old := []llm.Message{
		llm.UserMessage("部署脚本在哪里"),
		{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "old_call", Type: "function", Function: llm.FunctionCall{Name: "read_file", Arguments: `{}`}}}},
		llm.ToolMessage("old_call", "脚本位于 scripts/deploy.sh"),
		llm.AssistantMessage("部署脚本在 scripts/deploy.sh"),
	}
	for _, message := range old {
		if _, err := manager.AddMessage(message, "read_file"); err != nil {
			t.Fatal(err)
		}
	}
	addTestTurn(t, manager, "检查状态", "状态正常")
	recent := []llm.Message{
		llm.UserMessage("请继续部署"),
		{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "current_call", Type: "function", Function: llm.FunctionCall{Name: "read_file", Arguments: `{}`}}}},
		llm.ToolMessage("current_call", "当前脚本内容"),
	}
	for _, message := range recent {
		if _, err := manager.AddMessage(message, "read_file"); err != nil {
			t.Fatal(err)
		}
	}
	builder := NewContextBuilder(manager, NewMemoryRetriever(nil), 1000)
	messages, err := builder.Build(context.Background(), "部署脚本")
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 7 || messages[0].Role != "system" || messages[1].Role != "assistant" {
		t.Fatalf("messages = %#v", messages)
	}
	if strings.Contains(messages[0].Content, "请使用中文") {
		t.Fatalf("unrelated USER fact was injected: %#v", messages[0])
	}
	if !strings.Contains(messages[1].Content, "scripts/deploy.sh") {
		t.Fatalf("older tool turn not retrieved: %#v", messages[1])
	}
	wantRecent := append([]llm.Message{llm.UserMessage("检查状态"), llm.AssistantMessage("状态正常")}, recent...)
	if !reflect.DeepEqual(messages[2:], wantRecent) {
		t.Fatalf("recent protocol changed: got %#v, want %#v", messages[2:], wantRecent)
	}

	builder.MaxRetrievedTokens = 14
	limited, err := builder.Build(context.Background(), "部署脚本")
	if err != nil {
		t.Fatal(err)
	}
	if len(limited) != 5 {
		t.Fatalf("retrieval budget not applied: %#v", limited)
	}
	if !reflect.DeepEqual(limited, wantRecent) {
		t.Fatalf("recent protocol changed under retrieval budget: %#v", limited)
	}
}

func TestContextBuilderLimitsFactsAndOlderContextSeparately(t *testing.T) {
	manager := NewManager(nil)
	for _, fact := range []struct {
		scope        FactScope
		key, content string
	}{
		{FactScopeSession, "one", "deploy fact one"},
		{FactScopeUser, "two", "deploy fact two"},
		{FactScopeProject, "three", "deploy fact three"},
		{FactScopeProject, "unrelated", "database SQLite"},
	} {
		if _, err := manager.UpsertFact(fact.scope, fact.key, fact.content, Metadata{}); err != nil {
			t.Fatal(err)
		}
	}
	addTestTurn(t, manager, "deploy old turn one", "completed one")
	addTestTurn(t, manager, "deploy old turn two", "completed two")
	addTestTurn(t, manager, "recent status", "ready")
	if _, err := manager.AddMessage(llm.UserMessage("deploy"), ""); err != nil {
		t.Fatal(err)
	}

	builder := NewContextBuilder(manager, NewMemoryRetriever(nil), 1000)
	builder.MaxFactResults = 2
	builder.MaxContextResults = 1
	messages, err := builder.Build(context.Background(), "deploy")
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 5 || messages[0].Role != "system" || messages[1].Role != "assistant" {
		t.Fatalf("messages = %#v", messages)
	}
	if strings.Count(messages[0].Content, "deploy fact") != 2 || strings.Contains(messages[0].Content, "SQLite") {
		t.Fatalf("fact quota or relevance failed: %#v", messages[0])
	}
	if !strings.Contains(messages[1].Content, "old turn one") || strings.Contains(messages[1].Content, "old turn two") {
		t.Fatalf("context quota failed: %#v", messages[1])
	}
}

func TestContextBuilderSharedBudgetGivesBothCategoriesSpace(t *testing.T) {
	manager := NewManager(TokenCounterFunc(func(content string) int { return len([]rune(content)) }))
	for _, key := range []string{"one", "two", "three"} {
		if _, err := manager.UpsertFact(FactScopeProject, key, "deploy "+key, Metadata{}); err != nil {
			t.Fatal(err)
		}
	}
	addTestTurn(t, manager, "deploy", "done")
	addTestTurn(t, manager, "recent status", "ready")
	if _, err := manager.AddMessage(llm.UserMessage("deploy"), ""); err != nil {
		t.Fatal(err)
	}

	builder := NewContextBuilder(manager, NewMemoryRetriever(nil), 60)
	messages, err := builder.Build(context.Background(), "deploy")
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 5 || messages[0].Role != "system" || messages[1].Role != "assistant" {
		t.Fatalf("facts crowded out older conversation: %#v", messages)
	}
}

func TestContextBuilderReportsMatchesAndIncludedDocuments(t *testing.T) {
	manager := NewManager(TokenCounterFunc(func(content string) int { return len([]rune(content)) }))
	if _, err := manager.UpsertFact(FactScopeProject, "deploy", "deploy fact", Metadata{}); err != nil {
		t.Fatal(err)
	}
	addTestTurn(t, manager, "deploy", "done")
	addTestTurn(t, manager, "recent", "ready")
	if _, err := manager.AddMessage(llm.UserMessage("deploy"), ""); err != nil {
		t.Fatal(err)
	}
	builder := NewContextBuilder(manager, NewMemoryRetriever(nil), 19)
	var report RetrievalReport
	calls := 0
	_, err := builder.BuildWithReport(context.Background(), "deploy", func(got RetrievalReport) {
		report = got
		calls++
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || len(report.FactMatches) != 1 || len(report.ContextMatches) != 1 ||
		len(report.IncludedFacts) != 1 || len(report.IncludedContext) != 0 {
		t.Fatalf("unexpected retrieval report: %#v (calls=%d)", report, calls)
	}
	if report.FactMatches[0].Kind != Fact || report.FactMatches[0].Scope != FactScopeProject ||
		report.ContextMatches[0].Kind != Conversation {
		t.Fatalf("wrong document metadata: %#v", report)
	}
}

func TestContextBuilderTouchesOnlyFactsIncludedByTokenBudget(t *testing.T) {
	counter := TokenCounterFunc(func(content string) int { return len([]rune(content)) })
	manager := NewManager(counter)
	for _, fact := range []struct {
		key, content string
	}{
		{key: "primary", content: "deploy primary"},
		{key: "secondary", content: "deploy secondary"},
	} {
		if _, err := manager.UpsertFact(FactScopeSession, fact.key, fact.content, Metadata{}); err != nil {
			t.Fatal(err)
		}
	}
	builder := NewContextBuilder(manager, NewMemoryRetriever(nil), counter.Count("deploy primary")+8)
	var report RetrievalReport
	if _, err := builder.BuildWithReport(context.Background(), "deploy", func(got RetrievalReport) { report = got }); err != nil {
		t.Fatal(err)
	}
	if len(report.IncludedFacts) != 1 || report.IncludedFacts[0].ID != "SESSION:primary" {
		t.Fatalf("included facts = %#v", report.IncludedFacts)
	}
	if got := entryContents(manager.Facts()); !reflect.DeepEqual(got, []string{"deploy secondary", "deploy primary"}) {
		t.Fatalf("fact LRU order = %v", got)
	}
}

func TestContextBuilderRetrievesActiveSummary(t *testing.T) {
	manager := NewManager(nil)
	if _, err := manager.Add("数据库迁移使用 SQLite", Summary, Metadata{}); err != nil {
		t.Fatal(err)
	}
	addTestTurn(t, manager, "其他话题", "好的")
	current := llm.UserMessage("数据库迁移使用什么？")
	if _, err := manager.AddMessage(current, ""); err != nil {
		t.Fatal(err)
	}
	builder := NewContextBuilder(manager, NewMemoryRetriever(nil), 1000)
	messages, err := builder.Build(context.Background(), current.Content)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 4 || messages[0].Role != "assistant" || !strings.Contains(messages[0].Content, "SQLite") {
		t.Fatalf("summary retrieval = %#v", messages)
	}
	if !reflect.DeepEqual(messages[1:], []llm.Message{llm.UserMessage("其他话题"), llm.AssistantMessage("好的"), current}) {
		t.Fatalf("recent messages = %#v", messages[1:])
	}
}

func TestContextBuilderWithLiveEmbedding(t *testing.T) {
	baseURL := os.Getenv("CATCLI_TEST_EMBEDDING_BASE_URL")
	if baseURL == "" {
		t.Skip("set CATCLI_TEST_EMBEDDING_BASE_URL to test the live embedding service")
	}
	manager := NewManager(nil)
	if _, err := manager.UpsertFact(FactScopeProject, "database", "项目使用 SQLite 保存会话快照", Metadata{}); err != nil {
		t.Fatal(err)
	}
	latest := llm.UserMessage("会话快照存在哪里？")
	if _, err := manager.AddMessage(latest, ""); err != nil {
		t.Fatal(err)
	}
	embedder := &llm.OpenAIEmbeddingClient{APIKey: "ollama", BaseURL: baseURL, Model: "bge-m3"}
	builder := NewContextBuilder(manager, NewMemoryRetriever(embedder), 1000)
	messages, err := builder.Build(context.Background(), latest.Content)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 || messages[0].Role != "system" || !strings.Contains(messages[0].Content, "SQLite") || !reflect.DeepEqual(messages[1], latest) {
		t.Fatalf("live working context = %#v", messages)
	}
}
