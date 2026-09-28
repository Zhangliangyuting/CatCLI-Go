package main

import (
	"AgentCLI/internal/llm"
	"AgentCLI/internal/memory"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
)

type conversationMemoryRuntime struct {
	conversationID  string
	manager         *memory.Manager
	scheduler       *memory.CompactionScheduler
	conversation    memory.ConversationStore
	transcript      memory.TranscriptStore
	generator       memory.CompactionGenerator
	schedulerConfig memory.CompactionSchedulerConfig
	taskSequence    atomic.Uint64
	resumed         bool
}

type taskMemoryRuntime struct {
	conversationID string
	scheduler      *memory.CompactionScheduler
}

func newConversationMemoryRuntime(
	client *llm.OpenAICompatibleClient,
	root string,
	requestedConversationID string,
	maxInputTokens int,
	compactionMaxTokens int,
) (*conversationMemoryRuntime, error) {
	if client == nil {
		return nil, fmt.Errorf("LLM client is nil")
	}
	if strings.TrimSpace(root) == "" {
		return nil, fmt.Errorf("memory root is empty")
	}
	if maxInputTokens <= 0 {
		return nil, fmt.Errorf("maximum input tokens must be positive")
	}
	if compactionMaxTokens <= 0 {
		return nil, fmt.Errorf("compaction max tokens must be positive")
	}

	factStore, err := memory.NewMarkdownFactStore(
		filepath.Join(root, "memory", "project.md"),
		filepath.Join(root, "memory", "user.md"),
	)
	if err != nil {
		return nil, err
	}
	manager, err := memory.NewManagerWithFactStore(nil, factStore)
	if err != nil {
		return nil, fmt.Errorf("create memory manager: %w", err)
	}
	conversationStore, err := memory.NewJSONConversationStore(filepath.Join(root, "conversations"))
	if err != nil {
		return nil, err
	}
	transcriptStore, err := memory.NewJSONLTranscriptStore(filepath.Join(root, "transcripts"))
	if err != nil {
		return nil, err
	}

	conversationID := strings.TrimSpace(requestedConversationID)
	if conversationID == "" {
		conversationID, err = memory.NewConversationID()
		if err != nil {
			return nil, fmt.Errorf("create conversation ID: %w", err)
		}
	}
	resumed, err := manager.LoadConversation(conversationStore, conversationID)
	if err != nil {
		return nil, err
	}
	// Backfill active entries as well as recording future messages. Append is
	// idempotent, so sources already archived by an earlier compaction are not
	// duplicated.
	if err := transcriptStore.Append(conversationID, manager.Entries()...); err != nil {
		return nil, fmt.Errorf("backfill conversation transcript: %w", err)
	}

	generator, err := memory.NewLLMCompactionGeneratorWithMaxTokens(client, compactionMaxTokens)
	if err != nil {
		return nil, err
	}
	compactor, err := memory.NewCompactor(generator, transcriptStore, conversationID)
	if err != nil {
		return nil, err
	}
	scheduler, err := memory.NewCompactionScheduler(
		compactor,
		memory.DefaultCompactionSchedulerConfig(maxInputTokens),
	)
	if err != nil {
		return nil, err
	}

	return &conversationMemoryRuntime{
		conversationID:  conversationID,
		manager:         manager,
		scheduler:       scheduler,
		conversation:    conversationStore,
		transcript:      transcriptStore,
		generator:       generator,
		schedulerConfig: memory.DefaultCompactionSchedulerConfig(maxInputTokens),
		resumed:         resumed,
	}, nil
}

func (runtime *conversationMemoryRuntime) newTaskScheduler(
	taskID string,
) (*memory.CompactionScheduler, error) {
	taskRuntime, err := runtime.newTaskMemoryRuntime(taskID)
	if err != nil {
		return nil, err
	}
	return taskRuntime.scheduler, nil
}

func (runtime *conversationMemoryRuntime) newTaskMemoryRuntime(
	taskID string,
) (*taskMemoryRuntime, error) {
	if runtime == nil || runtime.generator == nil || runtime.transcript == nil {
		return nil, fmt.Errorf("conversation memory runtime is not initialized")
	}
	sequence := runtime.taskSequence.Add(1)
	digest := sha256.Sum256([]byte(taskID))
	childConversationID := fmt.Sprintf(
		"%s-task-%d-%s",
		runtime.conversationID,
		sequence,
		hex.EncodeToString(digest[:4]),
	)
	compactor, err := memory.NewCompactor(
		runtime.generator,
		runtime.transcript,
		childConversationID,
	)
	if err != nil {
		return nil, err
	}
	scheduler, err := memory.NewCompactionScheduler(compactor, runtime.schedulerConfig)
	if err != nil {
		return nil, err
	}
	return &taskMemoryRuntime{
		conversationID: childConversationID,
		scheduler:      scheduler,
	}, nil
}

func (runtime *conversationMemoryRuntime) save() error {
	if runtime == nil || runtime.manager == nil || runtime.conversation == nil || runtime.scheduler == nil {
		return fmt.Errorf("conversation memory runtime is not initialized")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, retentionErr := runtime.scheduler.EnforceStorageBudget(ctx, runtime.manager)
	// Preserve the complete current state even if retention could not run.
	saveErr := runtime.manager.SaveConversation(runtime.conversation, runtime.conversationID)
	if retentionErr != nil {
		retentionErr = fmt.Errorf("enforce Manager storage budget: %w", retentionErr)
	}
	return errors.Join(retentionErr, saveErr)
}

func newTaskMemoryManager(root *memory.Manager) (*memory.Manager, error) {
	if root == nil {
		return nil, fmt.Errorf("root memory manager is nil")
	}
	taskManager := memory.NewManager(nil)
	for _, fact := range root.Facts() {
		if err := taskManager.AddEntry(fact); err != nil {
			return nil, fmt.Errorf("copy fact %q into task memory: %w", fact.ID(), err)
		}
	}
	return taskManager, nil
}
