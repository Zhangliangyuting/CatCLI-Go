package memory

import (
	"context"
	"strings"
	"testing"
)

func TestFactVerificationCacheIsSessionLocalAndContentBound(t *testing.T) {
	manager := NewManager(nil)
	fact, err := manager.UpsertFact(FactScopeProject, "toolchain.go.version", "This project uses Go 1.24", Metadata{NeedsVerification: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, checked := manager.factVerification(fact); checked {
		t.Fatal("new fact unexpectedly has a verification result")
	}
	if err := manager.recordFactVerification(fact, FactVerifiedCurrent); err != nil {
		t.Fatal(err)
	}
	if _, checked := manager.factVerification(fact); !checked {
		t.Fatal("verified fact has no session cache entry")
	}
	changed := fact
	changed.content = "This project uses Go 1.25"
	if _, checked := manager.factVerification(changed); checked {
		t.Fatal("verification result was reused for changed fact content")
	}

	// A new Manager represents a restarted session, so it must verify again.
	restarted := NewManager(nil)
	if _, err := restarted.UpsertFact(FactScopeProject, "toolchain.go.version", fact.Content(), Metadata{NeedsVerification: true}); err != nil {
		t.Fatal(err)
	}
	restartedFact, exists := restarted.GetFact(FactScopeProject, "toolchain.go.version")
	if !exists {
		t.Fatal("fact was not restored in new manager")
	}
	if _, checked := restarted.factVerification(restartedFact); checked {
		t.Fatal("new manager reused the prior session's verification result")
	}
	if err := manager.recordFactVerification(fact, FactVerifiedStale); err != nil {
		t.Fatal(err)
	}
	builder := NewContextBuilder(manager, NewMemoryRetriever(nil), 1000)
	messages, err := builder.Build(context.Background(), "Go version")
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range messages {
		if strings.Contains(message.Content, "Go 1.24") {
			t.Fatal("stale marked fact was injected")
		}
	}
}

func TestStaleFactWithEvidenceIsReplacedBeforeInjection(t *testing.T) {
	manager := NewManager(nil)
	oldFact, err := manager.UpsertFact(FactScopeProject, "toolchain.go.version", "This project uses Go 1.24", Metadata{NeedsVerification: true})
	if err != nil {
		t.Fatal(err)
	}
	manager.SetFactVerifier(func(_ context.Context, facts []Entry) (map[string]FactVerificationResult, error) {
		if len(facts) != 1 || facts[0].ID() != oldFact.ID() {
			t.Fatalf("facts to verify = %#v, want the old version fact", facts)
		}
		return map[string]FactVerificationResult{
			oldFact.ID(): {
				Outcome: FactVerifiedStale, Content: "This project uses Go 1.25",
				Evidence: "go.mod declares go 1.25",
			},
		}, nil
	})

	builder := NewContextBuilder(manager, NewMemoryRetriever(nil), 1000)
	messages, err := builder.Build(context.Background(), "Go version")
	if err != nil {
		t.Fatal(err)
	}
	updated, exists := manager.GetFact(FactScopeProject, "toolchain.go.version")
	if !exists || updated.Content() != "This project uses Go 1.25" {
		t.Fatalf("updated fact = %q, %t", updated.Content(), exists)
	}
	if outcome, checked := manager.factVerification(updated); !checked || outcome != FactVerifiedCurrent {
		t.Fatalf("updated fact verification = %q, %t; want current", outcome, checked)
	}
	var injected strings.Builder
	for _, message := range messages {
		injected.WriteString(message.Content)
	}
	if !strings.Contains(injected.String(), "Go 1.25") || strings.Contains(injected.String(), "Go 1.24") {
		t.Fatalf("context did not use the replacement fact: %s", injected.String())
	}
}
