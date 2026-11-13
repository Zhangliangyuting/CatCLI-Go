package agent

import (
	"AgentCLI/internal/execution"
	"AgentCLI/internal/llm"
	"AgentCLI/internal/memory"
	"AgentCLI/internal/rag"
	"AgentCLI/internal/tool"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

type ReActAgent struct {
	recorder            *execution.Recorder
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
	ragContext          *rag.ContextProvider
	referenceMessages   []llm.Message
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

func WithRAGContext(provider *rag.ContextProvider) ReActAgentOption {
	return func(agent *ReActAgent) {
		agent.ragContext = provider
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

// WithRecorder enables execution recording through the shared execution module.
func WithRecorder(recorder *execution.Recorder) ReActAgentOption {
	return func(a *ReActAgent) { a.recorder = recorder }
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

func (a *ReActAgent) Run(ctx context.Context, userInput string) (answer string, runErr error) {
	input, sourceID := userInput, ""
	if resume, ok := ctx.Value(resumeContextKey{}).(resumeRequest); ok {
		input, sourceID = resume.originalInput, resume.sourceID
	}
	run, err := a.recorder.StartRun(ctx, input, sourceID, execution.ReActRun)
	if err != nil {
		return "", err
	}
	returned := false
	defer func() {
		// A panic leaves the Run open; only a returned execution gets an end event.
		if returned {
			if finishErr := run.FinishRun(ctx, answer, runErr); finishErr != nil && !errors.Is(runErr, finishErr) {
				runErr = errors.Join(runErr, finishErr)
			}
		}
	}()
	answer, runErr = a.run(ctx, userInput, run)
	returned = true
	return answer, runErr
}

func (a *ReActAgent) run(
	ctx context.Context,
	userInput string,
	recorder *execution.RunRecorder,
) (string, error) {
	a.currentQuery = userInput
	if resume, ok := ctx.Value(resumeContextKey{}).(resumeRequest); ok {
		a.currentQuery = resume.originalInput
	}
	referenceMessages, err := buildReferenceContext(ctx, a.ragContext, a.currentQuery)
	if err != nil {
		return "", err
	}
	a.referenceMessages = referenceMessages
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

			var args map[string]any
			var toolResult string
			var toolErr error
			var approved *tool.AuthorizedCall
			if err := json.Unmarshal([]byte(toolCall.Function.Arguments), &args); err != nil {
				toolErr = fmt.Errorf("invalid tool arguments: %w", err)
			} else {
				approved, toolErr = a.tools.Authorize(ctx, toolCall.Function.Name, args)
			}
			if err := ctx.Err(); err != nil {
				return "", err
			}
			if toolErr == nil {
				var verificationData string
				if recorder != nil {
					verificationData, _ = approved.PrepareVerification(ctx)
				}
				if err := ctx.Err(); err != nil {
					return "", err
				}
				var call execution.ToolCallEventData
				if recorder != nil {
					call, err = recorder.StartToolCall(ctx, execution.ToolCallEventData{
						ToolCallID: toolCall.ID, ToolName: toolCall.Function.Name, Arguments: toolCall.Function.Arguments,
						VerificationData: verificationData,
					})
					if err != nil {
						return "", err
					}
				}
				toolResult, toolErr = approved.Execute(ctx)
				if toolErr != nil {
					toolResult = "ERROR: " + toolErr.Error()
				}
				if recorder != nil {
					if err := recorder.CompleteToolCall(ctx, call.CallID, toolResult, toolErr); err != nil {
						return "", err
					}
				}
			} else {
				// Rejected requests are feedback to the model, not started executions.
				toolResult = "ERROR: " + toolErr.Error()
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

func buildReferenceContext(
	ctx context.Context,
	provider *rag.ContextProvider,
	query string,
) ([]llm.Message, error) {
	if provider == nil {
		return nil, nil
	}
	messages, err := provider.BuildWithReport(ctx, query, func(report rag.RetrievalReport) {
		Emit(ctx, Event{Type: EventWorkspaceRetrieval, WorkspaceRetrieval: &report})
	})
	if err != nil {
		return nil, fmt.Errorf("build reference context: %w", err)
	}
	return messages, nil
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

	result := make([]llm.Message, 0, 1+len(a.referenceMessages)+len(messages))
	result = append(result, a.systemMessage)
	result = append(result, a.referenceMessages...)
	result = append(result, messages...)
	return result, nil
}
