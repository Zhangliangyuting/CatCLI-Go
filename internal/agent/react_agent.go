package agent

import (
	"AgentCLI/internal/llm"
	"AgentCLI/internal/memory"
	"AgentCLI/internal/tool"
	"context"
	"encoding/json"
	"fmt"
)

type ReActAgent struct {
	llmClient           *llm.OpenAICompatibleClient
	tools               *tool.ToolRegistry
	memoryManager       *memory.Manager
	compactionScheduler ContextCompactionScheduler
	requestEstimator    memory.RequestTokenEstimator
	transcript          memory.TranscriptStore
	conversationID      string
	systemMessage       llm.Message
}

var _ ObservableAgent = (*ReActAgent)(nil)

// ContextCompactionScheduler is the part of memory.CompactionScheduler used by
// ReActAgent. Keeping the dependency as an interface makes scheduling behavior
// deterministic in agent tests.
type ContextCompactionScheduler interface {
	CompactToFitMeasured(
		ctx context.Context,
		manager *memory.Manager,
		measure memory.ContextTokenMeasurer,
	) ([]memory.CompactionDecision, error)
}

type ReActAgentOption func(*ReActAgent)

// WithMemoryManager makes the agent use a caller-owned memory manager. This is
// the integration point for restored conversation state and persistent facts.
// A nil manager leaves the agent's default in-memory manager unchanged.
func WithMemoryManager(manager *memory.Manager) ReActAgentOption {
	return func(agent *ReActAgent) {
		if manager != nil {
			agent.memoryManager = manager
		}
	}
}

func WithCompactionScheduler(scheduler ContextCompactionScheduler) ReActAgentOption {
	return func(agent *ReActAgent) {
		agent.compactionScheduler = scheduler
	}
}

func WithRequestTokenEstimator(estimator memory.RequestTokenEstimator) ReActAgentOption {
	return func(agent *ReActAgent) {
		if estimator != nil {
			agent.requestEstimator = estimator
		}
	}
}

// WithTranscript records every protocol message when it is added to the
// agent's Manager. The transcript is an append-only audit log; active context
// still comes from the Manager and its resumable ConversationState.
func WithTranscript(store memory.TranscriptStore, conversationID string) ReActAgentOption {
	return func(agent *ReActAgent) {
		if store != nil {
			agent.transcript = store
			agent.conversationID = conversationID
		}
	}
}

func NewReActAgent(
	client *llm.OpenAICompatibleClient,
	tools *tool.ToolRegistry,
	options ...ReActAgentOption,
) *ReActAgent {
	agent := &ReActAgent{
		llmClient:        client,
		tools:            tools,
		memoryManager:    memory.NewManager(nil),
		requestEstimator: memory.NewCalibratedRequestTokenEstimator(nil),
		systemMessage:    llm.SystemMessage("You are a helpful assistant."),
	}
	for _, option := range options {
		if option != nil {
			option(agent)
		}
	}
	return agent
}

func (a *ReActAgent) Run(
	ctx context.Context,
	userInput string,
) (string, error) {
	return a.RunWithObserver(ctx, userInput, nil)
}

func (a *ReActAgent) RunWithObserver(
	ctx context.Context,
	userInput string,
	observer Observer,
) (string, error) {
	if _, err := a.storeMessage(llm.UserMessage(userInput), ""); err != nil {
		return "", fmt.Errorf("store user message: %w", err)
	}

	tools := a.tools.ToolDefinitions()

	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if err := a.compactContextIfNeeded(ctx, tools, observer); err != nil {
			return "", err
		}

		messages, err := a.contextMessages()
		if err != nil {
			return "", err
		}
		estimatedPromptTokens, err := a.requestEstimator.Estimate(messages, tools)
		if err != nil {
			return "", fmt.Errorf("estimate model request tokens: %w", err)
		}

		result, err := a.llmClient.ChatContext(
			ctx,
			messages,
			tools,
		)
		if err != nil {
			return "", err
		}
		a.requestEstimator.Observe(estimatedPromptTokens, result.Usage.PromptTokens)

		if _, err := a.storeMessage(result.Message, ""); err != nil {
			return "", fmt.Errorf("store assistant message: %w", err)
		}

		emit(observer, Event{
			Type:  EventTokenUsage,
			Title: "Token usage",
			Content: fmt.Sprintf(
				"estimated_input=%d input=%d output=%d total=%d",
				estimatedPromptTokens,
				result.Usage.PromptTokens,
				result.Usage.CompletionTokens,
				result.Usage.TotalTokens,
			),
		})

		if len(result.Message.ToolCalls) == 0 {
			return result.Message.Content, nil
		}

		for _, toolCall := range result.Message.ToolCalls {
			emit(observer, Event{
				Type:    EventToolCall,
				Title:   toolCall.Function.Name,
				Content: toolCall.Function.Arguments,
			})

			var args map[string]interface{}
			if err := json.Unmarshal([]byte(toolCall.Function.Arguments), &args); err != nil {
				toolResult := "ERROR: invalid tool arguments: " + err.Error()
				emit(observer, Event{
					Type:    EventToolResult,
					Title:   toolCall.Function.Name,
					Content: toolResult,
				})
				if _, storeErr := a.storeMessage(
					llm.ToolMessage(toolCall.ID, toolResult),
					toolCall.Function.Name,
				); storeErr != nil {
					return "", fmt.Errorf("store tool result: %w", storeErr)
				}
				continue
			}

			toolResult, err := a.tools.Execute(
				ctx,
				toolCall.Function.Name,
				args,
			)
			if err != nil {
				toolResult = "ERROR: " + err.Error()
			}

			emit(observer, Event{
				Type:    EventToolResult,
				Title:   toolCall.Function.Name,
				Content: toolResult,
			})

			if _, storeErr := a.storeMessage(
				llm.ToolMessage(toolCall.ID, toolResult),
				toolCall.Function.Name,
			); storeErr != nil {
				return "", fmt.Errorf("store tool result: %w", storeErr)
			}
		}
	}
}

func (a *ReActAgent) storeMessage(message llm.Message, toolName string) (memory.Entry, error) {
	entry, err := a.memoryManager.AddMessage(message, toolName)
	if err != nil {
		return memory.Entry{}, err
	}
	if a.transcript != nil {
		if err := a.transcript.Append(a.conversationID, entry); err != nil {
			return entry, fmt.Errorf("append message to transcript: %w", err)
		}
	}
	return entry, nil
}

func (a *ReActAgent) compactContextIfNeeded(
	ctx context.Context,
	tools []tool.Definition,
	observer Observer,
) error {
	if a.compactionScheduler == nil {
		return nil
	}
	measure := func() (int, error) {
		messages, err := a.contextMessages()
		if err != nil {
			return 0, err
		}
		return a.requestEstimator.Estimate(messages, tools)
	}
	decisions, err := a.compactionScheduler.CompactToFitMeasured(ctx, a.memoryManager, measure)
	emitCompactionDecisions(observer, decisions)
	if err != nil {
		return fmt.Errorf("compact conversation context: %w", err)
	}
	return nil
}

func (a *ReActAgent) ClearHistory() {
	a.memoryManager.Clear()
}

func (a *ReActAgent) contextMessages() ([]llm.Message, error) {
	messages, err := a.memoryManager.ContextMessages()
	if err != nil {
		return nil, fmt.Errorf("build conversation context: %w", err)
	}

	return append([]llm.Message{a.systemMessage}, messages...), nil
}
