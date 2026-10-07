package agent

import (
	"AgentCLI/internal/llm"
	"AgentCLI/internal/memory"
	"AgentCLI/internal/tool"
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

type ReActAgent struct {
	llmClient           *llm.OpenAICompatibleClient
	tools               *tool.ToolRegistry
	memoryManager       *memory.Manager
	contextBuilder      *memory.ContextBuilder
	currentQuery        string
	compactionScheduler memory.ContextCompactionScheduler
	requestEstimator    memory.RequestTokenEstimator
	transcript          memory.TranscriptStore
	conversationID      string
	systemMessage       llm.Message
}

var _ Agent = (*ReActAgent)(nil)

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

func WithContextBuilder(builder *memory.ContextBuilder) ReActAgentOption {
	return func(agent *ReActAgent) {
		if builder != nil {
			agent.contextBuilder = builder
		}
	}
}

func WithCompactionScheduler(scheduler memory.ContextCompactionScheduler) ReActAgentOption {
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

func WithSystemPrompt(prompt string) ReActAgentOption {
	return func(agent *ReActAgent) {
		if prompt = strings.TrimSpace(prompt); prompt != "" {
			agent.systemMessage = llm.SystemMessage(prompt)
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
	if agent.contextBuilder == nil {
		agent.contextBuilder = memory.NewContextBuilder(agent.memoryManager, nil, 0)
	}
	return agent
}

func (a *ReActAgent) Run(
	ctx context.Context,
	userInput string,
) (string, error) {
	a.currentQuery = userInput
	if _, err := a.storeMessage(llm.UserMessage(userInput), ""); err != nil {
		return "", fmt.Errorf("store user message: %w", err)
	}

	tools := a.tools.ToolDefinitions()

	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if err := a.compactContextIfNeeded(ctx, tools); err != nil {
			return "", err
		}

		messages, err := a.contextMessagesFor(ctx)
		if err != nil {
			return "", err
		}
		estimatedPromptTokens, err := a.requestEstimator.Estimate(messages, tools)
		if err != nil {
			return "", fmt.Errorf("estimate model request tokens: %w", err)
		}

		result, err := a.llmClient.Chat(
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

		Emit(ctx, Event{
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
			Emit(ctx, Event{
				Type:    EventToolCall,
				Title:   toolCall.Function.Name,
				Content: toolCall.Function.Arguments,
			})

			var args map[string]interface{}
			if err := json.Unmarshal([]byte(toolCall.Function.Arguments), &args); err != nil {
				toolResult := "ERROR: invalid tool arguments: " + err.Error()
				Emit(ctx, Event{
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

			Emit(ctx, Event{
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
) error {
	if a.compactionScheduler == nil {
		return nil
	}
	measure := func() (int, error) {
		messages, err := a.contextMessagesFor(ctx)
		if err != nil {
			return 0, err
		}
		return a.requestEstimator.Estimate(messages, tools)
	}
	decisions, err := a.compactionScheduler.CompactToFit(ctx, a.memoryManager, measure)
	EmitCompactionDecisions(ctx, decisions)
	if err != nil {
		return fmt.Errorf("compact conversation context: %w", err)
	}
	return nil
}

func (a *ReActAgent) ClearHistory() {
	a.memoryManager.Clear()
}

func (a *ReActAgent) contextMessages() ([]llm.Message, error) {
	return a.contextMessagesFor(context.Background())
}

func (a *ReActAgent) contextMessagesFor(ctx context.Context) ([]llm.Message, error) {
	if a.contextBuilder == nil {
		return nil, fmt.Errorf("context builder is nil")
	}
	messages, err := a.contextBuilder.BuildWithReport(ctx, a.currentQuery, func(report memory.RetrievalReport) {
		Emit(ctx, Event{Type: EventMemoryRetrieval, Retrieval: &report})
	})
	if err != nil {
		return nil, fmt.Errorf("build conversation context: %w", err)
	}

	return append([]llm.Message{a.systemMessage}, messages...), nil
}
