package memory

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestMarkdownFactStoreRoundTripUpdateAndRemove(t *testing.T) {
	root := t.TempDir()
	projectPath := filepath.Join(root, "project.md")
	userPath := filepath.Join(root, "user.md")
	store, err := NewMarkdownFactStore(projectPath, userPath)
	if err != nil {
		t.Fatalf("NewMarkdownFactStore() error = %v", err)
	}

	for _, fact := range []StoredFact{
		{Scope: FactScopeProject, Key: "build:test", Content: "Run `go test ./...`."},
		{Scope: FactScopeProject, Key: "language", Content: "Use Go 1.26."},
		{Scope: FactScopeUser, Key: "language", Content: "Reply in Chinese."},
	} {
		if err := store.Upsert(fact); err != nil {
			t.Fatalf("Upsert(%+v) error = %v", fact, err)
		}
	}
	if err := store.Upsert(StoredFact{
		Scope:   FactScopeProject,
		Key:     "build:test",
		Content: "Run focused tests, then `go test ./...`.",
	}); err != nil {
		t.Fatalf("update fact error = %v", err)
	}

	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	want := []StoredFact{
		{Scope: FactScopeProject, Key: "build:test", Content: "Run focused tests, then `go test ./...`."},
		{Scope: FactScopeProject, Key: "language", Content: "Use Go 1.26."},
		{Scope: FactScopeUser, Key: "language", Content: "Reply in Chinese."},
	}
	if !reflect.DeepEqual(loaded, want) {
		t.Fatalf("Load() = %#v, want %#v", loaded, want)
	}

	if err := store.Remove(FactScopeProject, "language"); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	loaded, err = store.Load()
	if err != nil {
		t.Fatalf("Load() after Remove error = %v", err)
	}
	if !reflect.DeepEqual(loaded, []StoredFact{want[0], want[2]}) {
		t.Fatalf("Load() after Remove = %#v", loaded)
	}

	data, err := os.ReadFile(projectPath)
	if err != nil {
		t.Fatalf("ReadFile(project) error = %v", err)
	}
	if !strings.Contains(string(data), "Run focused tests") || !strings.Contains(string(data), "catcli-fact:") {
		t.Fatalf("project Markdown is not editable managed text:\n%s", data)
	}
}

func TestMarkdownFactStoreRejectsSessionFacts(t *testing.T) {
	root := t.TempDir()
	store, err := NewMarkdownFactStore(
		filepath.Join(root, "project.md"),
		filepath.Join(root, "user.md"),
	)
	if err != nil {
		t.Fatalf("NewMarkdownFactStore() error = %v", err)
	}
	if err := store.Upsert(StoredFact{
		Scope: FactScopeSession, Key: "task", Content: "temporary",
	}); err == nil {
		t.Fatal("Upsert(SESSION) error = nil, want rejection")
	}
	if err := store.Remove(FactScopeSession, "task"); err == nil {
		t.Fatal("Remove(SESSION) error = nil, want rejection")
	}
}

func TestManagerLoadsAndPersistsFacts(t *testing.T) {
	root := t.TempDir()
	store, err := NewMarkdownFactStore(
		filepath.Join(root, "project.md"),
		filepath.Join(root, "user.md"),
	)
	if err != nil {
		t.Fatalf("NewMarkdownFactStore() error = %v", err)
	}
	if err := store.Upsert(StoredFact{
		Scope: FactScopeProject, Key: "language", Content: "Use Go.",
	}); err != nil {
		t.Fatalf("seed project fact error = %v", err)
	}

	manager, err := NewManagerWithFactStore(TokenCounterFunc(func(content string) int {
		return len(content)
	}), store)
	if err != nil {
		t.Fatalf("NewManagerWithFactStore() error = %v", err)
	}
	if fact, ok := manager.GetFact(FactScopeProject, "language"); !ok || fact.Content() != "Use Go." {
		t.Fatalf("loaded project fact = (%+v, %v)", fact, ok)
	}
	if _, err := manager.UpsertFact(FactScopeUser, "style", "Prefer concise answers.", Metadata{}); err != nil {
		t.Fatalf("UpsertFact(USER) error = %v", err)
	}
	if _, err := manager.UpsertFact(FactScopeSession, "task", "Do not rename APIs.", Metadata{}); err != nil {
		t.Fatalf("UpsertFact(SESSION) error = %v", err)
	}

	reloaded, err := NewManagerWithFactStore(nil, store)
	if err != nil {
		t.Fatalf("reload manager error = %v", err)
	}
	if reloaded.FactLen() != 2 {
		t.Fatalf("reloaded FactLen() = %d, want 2 persistent facts", reloaded.FactLen())
	}
	if _, ok := reloaded.GetFact(FactScopeSession, "task"); ok {
		t.Fatal("SESSION fact was persisted")
	}
	removed, err := reloaded.RemoveFact(FactScopeProject, "language")
	if err != nil || !removed {
		t.Fatalf("RemoveFact(PROJECT) = (%v, %v), want (true, nil)", removed, err)
	}
	finalManager, err := NewManagerWithFactStore(nil, store)
	if err != nil {
		t.Fatalf("final reload error = %v", err)
	}
	if _, ok := finalManager.GetFact(FactScopeProject, "language"); ok {
		t.Fatal("removed PROJECT fact was restored")
	}
}
