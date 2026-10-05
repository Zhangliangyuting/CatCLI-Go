package memory

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func storageTestScheduler(t *testing.T, generator CompactionGenerator, transcript TranscriptStore, budget StorageBudget) *CompactionScheduler {
	t.Helper()
	scheduler, err := NewCompactionScheduler(mustCompactor(t, generator, transcript), DefaultCompactionSchedulerConfig(10000))
	if err != nil {
		t.Fatal(err)
	}
	scheduler.storageBudget = budget
	return scheduler
}

func TestStorageBudgetEvictsLeastRecentlyUsedFactsAndPersistsRemoval(t *testing.T) {
	root := t.TempDir()
	store, err := NewMarkdownFactStore(filepath.Join(root, "project.md"), filepath.Join(root, "user.md"))
	if err != nil {
		t.Fatal(err)
	}
	manager, err := NewManagerWithFactStore(nil, store)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"one", "two", "three"} {
		if _, err := manager.UpsertFact(FactScopeProject, key, key, Metadata{}); err != nil {
			t.Fatal(err)
		}
	}
	if !manager.TouchFact("PROJECT:one") {
		t.Fatal("TouchFact() did not find fact")
	}
	for _, entry := range compactTestTurn(t, 1, false) {
		if err := manager.AddEntry(entry); err != nil {
			t.Fatal(err)
		}
	}
	scheduler := storageTestScheduler(t, &fakeCompactionGenerator{summaryContent: validSummaryContent()}, &recordingTranscriptStore{},
		StorageBudget{MaxEntries: 4, TargetEntries: 3, MaxBytes: 1 << 20, TargetBytes: 1 << 19, MaxFacts: 1})
	decisions, err := scheduler.EnforceStorageBudget(context.Background(), manager)
	if err != nil {
		t.Fatal(err)
	}
	if len(decisions) != 2 || decisions[0].Result.SourceEntryIDs[0] != "PROJECT:two" || decisions[1].Result.SourceEntryIDs[0] != "PROJECT:three" {
		t.Fatalf("LRU eviction decisions = %+v", decisions)
	}
	if got := entryContents(manager.Facts()); !reflect.DeepEqual(got, []string{"one"}) {
		t.Fatalf("remaining facts = %v", got)
	}
	reloaded, err := NewManagerWithFactStore(nil, store)
	if err != nil {
		t.Fatal(err)
	}
	if got := entryContents(reloaded.Facts()); !reflect.DeepEqual(got, []string{"one"}) {
		t.Fatalf("persisted facts after eviction = %v", got)
	}
}

func storageTestTurns(t *testing.T, manager *Manager, count int) {
	t.Helper()
	for turn := 1; turn <= count; turn++ {
		for _, entry := range compactTestTurn(t, turn, false) {
			if err := manager.AddEntry(entry); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestStorageBudgetCompressesBeforeEvicting(t *testing.T) {
	manager := compactTestManager()
	storageTestTurns(t, manager, 3)
	if _, err := manager.UpsertFact(FactScopeSession, "preference", "keep this", Metadata{}); err != nil {
		t.Fatal(err)
	}
	transcript := &recordingTranscriptStore{}
	generator := &fakeCompactionGenerator{summaryContent: validSummaryContent()}
	scheduler := storageTestScheduler(t, generator, transcript, StorageBudget{MaxEntries: 5, TargetEntries: 4, MaxBytes: 1 << 20, TargetBytes: 1 << 19, MaxFacts: 512})
	decisions, err := scheduler.EnforceStorageBudget(context.Background(), manager)
	if err != nil {
		t.Fatal(err)
	}
	if len(decisions) != 1 || decisions[0].Action != CompactionActionFull {
		t.Fatalf("decisions = %+v", decisions)
	}
	entries := manager.Entries()
	if len(entries) != 3 || entries[0].Type() != Summary || entries[1].ID() != "user-3" {
		t.Fatalf("unexpected retained entries: %+v", entries)
	}
	if len(manager.Facts()) != 1 || len(transcript.batches) != 1 || len(transcript.batches[0]) != 4 {
		t.Fatal("fact or archived source missing")
	}
}

func TestStorageBudgetEvictsArchivedSummaryAfterCompression(t *testing.T) {
	manager := compactTestManager()
	storageTestTurns(t, manager, 3)
	transcript := &recordingTranscriptStore{}
	scheduler := storageTestScheduler(t, &fakeCompactionGenerator{summaryContent: validSummaryContent()}, transcript,
		StorageBudget{MaxEntries: 5, TargetEntries: 2, MaxBytes: 1 << 20, TargetBytes: 1 << 19, MaxFacts: 512})
	decisions, err := scheduler.EnforceStorageBudget(context.Background(), manager)
	if err != nil {
		t.Fatal(err)
	}
	if len(decisions) != 2 || decisions[0].Action != CompactionActionFull || decisions[1].Action != CompactionActionEvict {
		t.Fatalf("decisions = %+v", decisions)
	}
	entries := manager.Entries()
	if len(entries) != 2 || entries[0].ID() != "user-3" || entries[1].ID() != "assistant-final-3" {
		t.Fatalf("unexpected retained entries: %+v", entries)
	}
	if len(transcript.batches) != 2 || len(transcript.batches[1]) != 1 || transcript.batches[1][0].Type() != Summary {
		t.Fatal("evicted summary was not archived")
	}
}

func TestStorageBudgetDoesNotEvictOnCompressionFailure(t *testing.T) {
	manager := compactTestManager()
	storageTestTurns(t, manager, 3)
	transcript := &recordingTranscriptStore{}
	scheduler := storageTestScheduler(t, &fakeCompactionGenerator{err: errors.New("model unavailable")}, transcript,
		StorageBudget{MaxEntries: 5, TargetEntries: 2, MaxBytes: 1 << 20, TargetBytes: 1 << 19, MaxFacts: 512})
	_, err := scheduler.EnforceStorageBudget(context.Background(), manager)
	if err == nil || !strings.Contains(err.Error(), "model unavailable") {
		t.Fatalf("error = %v", err)
	}
	if manager.Len() != 6 || len(transcript.batches) != 0 {
		t.Fatal("Manager changed despite compression failure")
	}
}

func TestStorageBudgetProtectsNewestTurn(t *testing.T) {
	manager := compactTestManager()
	storageTestTurns(t, manager, 1)
	if _, err := manager.UpsertFact(FactScopeSession, "protected", "protected fact", Metadata{}); err != nil {
		t.Fatal(err)
	}
	transcript := &recordingTranscriptStore{}
	before := manager.StorageUsage()
	scheduler := storageTestScheduler(t, &fakeCompactionGenerator{summaryContent: validSummaryContent()}, transcript,
		StorageBudget{MaxEntries: 10, TargetEntries: 5, MaxBytes: before.Bytes - 1, TargetBytes: before.Bytes / 2, MaxFacts: 512})
	decisions, err := scheduler.EnforceStorageBudget(context.Background(), manager)
	var budgetErr *StorageBudgetExceededError
	if !errors.As(err, &budgetErr) || manager.Len() != 2 || manager.FactLen() != 1 || len(transcript.batches) != 0 {
		t.Fatalf("error = %v, entries = %d, facts = %d", err, manager.Len(), manager.FactLen())
	}
	if len(decisions) != 0 {
		t.Fatalf("decisions = %+v", decisions)
	}
}

func TestStorageBudgetDoesNotEvictWhenArchiveFails(t *testing.T) {
	manager := compactTestManager()
	storageTestTurns(t, manager, 3)
	transcript := &recordingTranscriptStore{err: errors.New("disk unavailable")}
	scheduler := storageTestScheduler(t, &fakeCompactionGenerator{summaryContent: validSummaryContent()}, transcript,
		StorageBudget{MaxEntries: 5, TargetEntries: 2, MaxBytes: 1 << 20, TargetBytes: 1 << 19, MaxFacts: 512})
	_, err := scheduler.EnforceStorageBudget(context.Background(), manager)
	if err == nil || !strings.Contains(err.Error(), "disk unavailable") || manager.Len() != 6 {
		t.Fatalf("error = %v, entries = %d", err, manager.Len())
	}
}

func TestStorageBudgetCanTriggerOnEstimatedBytes(t *testing.T) {
	manager := compactTestManager()
	storageTestTurns(t, manager, 3)
	before := manager.StorageUsage()
	if before.Entries != 6 || before.Bytes <= 1024 {
		t.Fatalf("unexpected usage = %+v", before)
	}
	transcript := &recordingTranscriptStore{}
	scheduler := storageTestScheduler(t, &fakeCompactionGenerator{summaryContent: validSummaryContent()}, transcript,
		StorageBudget{MaxEntries: 20, TargetEntries: 15, MaxBytes: before.Bytes - 1, TargetBytes: before.Bytes / 2, MaxFacts: 512})
	decisions, err := scheduler.EnforceStorageBudget(context.Background(), manager)
	if err != nil {
		t.Fatal(err)
	}
	if len(decisions) == 0 || manager.StorageUsage().Bytes >= before.Bytes {
		t.Fatalf("byte limit did not reduce usage: decisions=%+v", decisions)
	}
}
