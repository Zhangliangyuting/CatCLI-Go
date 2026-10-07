package memory

import (
	"AgentCLI/internal/llm"
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

// AgentMemoryContext contains the memory resources used by one Agent
// execution context. A root conversation and every child task each receive
// their own context.
type AgentMemoryContext struct {
	ConversationID string
	Manager        *Manager
	ContextBuilder *ContextBuilder
	Scheduler      ContextCompactionScheduler
	Estimator      RequestTokenEstimator
	Transcript     TranscriptStore
}

// ContextCompactionScheduler is the compaction capability required by an
// Agent memory context.
type ContextCompactionScheduler interface {
	CompactToFit(
		ctx context.Context,
		manager *Manager,
		measure ContextTokenMeasurer,
	) ([]CompactionDecision, error)
}

// TaskRuntime owns the isolated memory context of one plan task.
type TaskRuntime struct {
	AgentMemoryContext
}

// ConversationRuntime owns the root conversation memory and the shared
// services needed to derive isolated task runtimes.
type ConversationRuntime struct {
	root            AgentMemoryContext
	rootScheduler   *CompactionScheduler
	conversation    ConversationStore
	generator       CompactionGenerator
	retriever       *MemoryRetriever
	retrievedTokens int
	schedulerConfig CompactionSchedulerConfig
	taskSequence    atomic.Uint64
	resumed         bool
}

func NewConversationRuntime(
	client *llm.OpenAICompatibleClient,
	retriever *MemoryRetriever,
	rootDirectory string,
	requestedConversationID string,
	maxInputTokens int,
	compactionMaxTokens int,
) (*ConversationRuntime, error) {
	if client == nil {
		return nil, fmt.Errorf("LLM client is nil")
	}
	if retriever == nil {
		return nil, fmt.Errorf("memory retriever is nil")
	}
	if strings.TrimSpace(rootDirectory) == "" {
		return nil, fmt.Errorf("memory root is empty")
	}
	if maxInputTokens <= 0 {
		return nil, fmt.Errorf("maximum input tokens must be positive")
	}
	if compactionMaxTokens <= 0 {
		return nil, fmt.Errorf("compaction max tokens must be positive")
	}

	factStore, err := NewMarkdownFactStore(
		filepath.Join(rootDirectory, "memory", "project.md"),
		filepath.Join(rootDirectory, "memory", "user.md"),
	)
	if err != nil {
		return nil, err
	}
	manager, err := NewManagerWithFactStore(nil, factStore)
	if err != nil {
		return nil, fmt.Errorf("create memory manager: %w", err)
	}
	conversationStore, err := NewJSONConversationStore(filepath.Join(rootDirectory, "conversations"))
	if err != nil {
		return nil, err
	}
	transcriptStore, err := NewJSONLTranscriptStore(filepath.Join(rootDirectory, "transcripts"))
	if err != nil {
		return nil, err
	}

	conversationID := strings.TrimSpace(requestedConversationID)
	if conversationID == "" {
		conversationID, err = NewConversationID()
		if err != nil {
			return nil, fmt.Errorf("create conversation ID: %w", err)
		}
	}
	resumed, err := manager.LoadConversation(conversationStore, conversationID)
	if err != nil {
		return nil, err
	}
	if err := transcriptStore.Append(conversationID, manager.Entries()...); err != nil {
		return nil, fmt.Errorf("backfill conversation transcript: %w", err)
	}

	generator, err := NewLLMCompactionGeneratorWithMaxTokens(client, compactionMaxTokens)
	if err != nil {
		return nil, err
	}
	schedulerConfig := DefaultCompactionSchedulerConfig(maxInputTokens)
	compactor, err := NewCompactor(generator, transcriptStore, conversationID)
	if err != nil {
		return nil, err
	}
	scheduler, err := NewCompactionScheduler(compactor, schedulerConfig)
	if err != nil {
		return nil, err
	}
	retrievedTokens := maxInputTokens / 5

	return &ConversationRuntime{
		root: AgentMemoryContext{
			ConversationID: conversationID,
			Manager:        manager,
			ContextBuilder: NewContextBuilder(manager, retriever, retrievedTokens),
			Scheduler:      scheduler,
			Estimator:      NewCalibratedRequestTokenEstimator(nil),
			Transcript:     transcriptStore,
		},
		rootScheduler:   scheduler,
		conversation:    conversationStore,
		generator:       generator,
		retriever:       retriever,
		retrievedTokens: retrievedTokens,
		schedulerConfig: schedulerConfig,
		resumed:         resumed,
	}, nil
}

func (runtime *ConversationRuntime) RootContext() AgentMemoryContext {
	if runtime == nil {
		return AgentMemoryContext{}
	}
	return runtime.root
}

func (runtime *ConversationRuntime) Resumed() bool {
	return runtime != nil && runtime.resumed
}

func (runtime *ConversationRuntime) NewTaskRuntime(taskID string) (*TaskRuntime, error) {
	if runtime == nil || runtime.generator == nil || runtime.root.Transcript == nil {
		return nil, fmt.Errorf("conversation memory runtime is not initialized")
	}
	sequence := runtime.taskSequence.Add(1)
	digest := sha256.Sum256([]byte(taskID))
	conversationID := fmt.Sprintf(
		"%s-task-%d-%s",
		runtime.root.ConversationID,
		sequence,
		hex.EncodeToString(digest[:4]),
	)
	compactor, err := NewCompactor(runtime.generator, runtime.root.Transcript, conversationID)
	if err != nil {
		return nil, err
	}
	scheduler, err := NewCompactionScheduler(compactor, runtime.schedulerConfig)
	if err != nil {
		return nil, err
	}
	manager, err := newTaskMemoryManager(runtime.root.Manager)
	if err != nil {
		return nil, err
	}
	return &TaskRuntime{AgentMemoryContext: AgentMemoryContext{
		ConversationID: conversationID,
		Manager:        manager,
		ContextBuilder: NewContextBuilder(manager, runtime.retriever, runtime.retrievedTokens),
		Scheduler:      scheduler,
		Estimator:      runtime.root.Estimator,
		Transcript:     runtime.root.Transcript,
	}}, nil
}

func (runtime *ConversationRuntime) Save() error {
	if runtime == nil || runtime.root.Manager == nil || runtime.conversation == nil || runtime.rootScheduler == nil {
		return fmt.Errorf("conversation memory runtime is not initialized")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, retentionErr := runtime.rootScheduler.EnforceStorageBudget(ctx, runtime.root.Manager)
	saveErr := runtime.root.Manager.SaveConversation(
		runtime.conversation,
		runtime.root.ConversationID,
	)
	if retentionErr != nil {
		retentionErr = fmt.Errorf("enforce Manager storage budget: %w", retentionErr)
	}
	return errors.Join(retentionErr, saveErr)
}

func newTaskMemoryManager(root *Manager) (*Manager, error) {
	if root == nil {
		return nil, fmt.Errorf("root memory manager is nil")
	}
	taskManager := NewManager(nil)
	for _, fact := range root.Facts() {
		if err := taskManager.AddEntry(fact); err != nil {
			return nil, fmt.Errorf("copy fact %q into task memory: %w", fact.ID(), err)
		}
	}
	return taskManager, nil
}
