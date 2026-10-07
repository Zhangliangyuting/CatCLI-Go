package multiagent

import (
	baseagent "AgentCLI/internal/agent"
	"AgentCLI/internal/llm"
	"AgentCLI/internal/memory"
	"AgentCLI/internal/tool"
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

const WorkerSystemPrompt = "你是执行者，只执行分配给你的当前任务，可以调用工具，并准确报告执行结果。"

// WorkerSubAgent executes one scheduled task with its assigned tools.
type WorkerSubAgent struct {
	*SubAgent
	memoryManager       *memory.Manager
	contextBuilder      *memory.ContextBuilder
	compactionScheduler memory.ContextCompactionScheduler
	requestEstimator    memory.RequestTokenEstimator
	transcript          memory.TranscriptStore
	conversationID      string
}

var _ baseagent.Agent = (*WorkerSubAgent)(nil)

func NewWorkerSubAgent(
	client *llm.OpenAICompatibleClient,
	tools *tool.ToolRegistry,
) (*WorkerSubAgent, error) {
	if tools == nil {
		return nil, fmt.Errorf("worker SubAgent tool registry is nil")
	}
	agent, err := NewSubAgent(RoleWorker, WorkerSystemPrompt, client, tools)
	if err != nil {
		return nil, err
	}
	return &WorkerSubAgent{SubAgent: agent}, nil
}

// ConfigureMemory binds this Worker to one task-scoped memory runtime.
func (agent *WorkerSubAgent) ConfigureMemory(
	memoryContext memory.AgentMemoryContext,
) error {
	if agent == nil {
		return fmt.Errorf("worker SubAgent is nil")
	}
	if memoryContext.Manager == nil || memoryContext.ContextBuilder == nil ||
		memoryContext.Scheduler == nil || memoryContext.Estimator == nil ||
		memoryContext.Transcript == nil {
		return fmt.Errorf("worker task memory dependency is nil")
	}
	if strings.TrimSpace(memoryContext.ConversationID) == "" {
		return fmt.Errorf("worker task conversation ID is empty")
	}
	agent.memoryManager = memoryContext.Manager
	agent.contextBuilder = memoryContext.ContextBuilder
	agent.compactionScheduler = memoryContext.Scheduler
	agent.requestEstimator = memoryContext.Estimator
	agent.transcript = memoryContext.Transcript
	agent.conversationID = memoryContext.ConversationID
	return nil
}

func (agent *WorkerSubAgent) Run(ctx context.Context, input string) (string, error) {
	if agent.memoryManager == nil {
		return agent.SubAgent.Run(ctx, input)
	}
	if _, err := agent.storeMessage(llm.UserMessage(input), ""); err != nil {
		return "", fmt.Errorf("store worker user message: %w", err)
	}

	definitions := agent.tools.ToolDefinitions()
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if err := agent.compactContext(ctx, input, definitions); err != nil {
			return "", err
		}
		messages, err := agent.contextBuilder.BuildWithReport(
			ctx,
			input,
			func(report memory.RetrievalReport) {
				baseagent.Emit(ctx, baseagent.Event{Type: baseagent.EventMemoryRetrieval, Retrieval: &report})
			},
		)
		if err != nil {
			return "", fmt.Errorf("build worker context: %w", err)
		}
		messages = append([]llm.Message{llm.SystemMessage(agent.systemPrompt)}, messages...)
		estimated, err := agent.requestEstimator.Estimate(messages, definitions)
		if err != nil {
			return "", fmt.Errorf("estimate worker request tokens: %w", err)
		}
		result, err := agent.llmClient.ChatWithOptions(ctx, messages, definitions, agent.chatOptions)
		if err != nil {
			return "", fmt.Errorf("run Worker SubAgent: %w", err)
		}
		agent.requestEstimator.Observe(estimated, result.Usage.PromptTokens)
		if _, err := agent.storeMessage(result.Message, ""); err != nil {
			return "", fmt.Errorf("store worker assistant message: %w", err)
		}
		baseagent.Emit(ctx, baseagent.Event{
			Type:    baseagent.EventTokenUsage,
			Title:   "[WORKER] Token usage",
			Content: fmt.Sprintf("estimated_input=%d input=%d output=%d total=%d", estimated, result.Usage.PromptTokens, result.Usage.CompletionTokens, result.Usage.TotalTokens),
		})
		if len(result.Message.ToolCalls) == 0 {
			return result.Message.Content, nil
		}
		for _, call := range result.Message.ToolCalls {
			baseagent.Emit(ctx, baseagent.Event{Type: baseagent.EventToolCall, Title: "[WORKER] " + call.Function.Name, Content: call.Function.Arguments})
			toolResult := agent.executeWorkerTool(ctx, call)
			baseagent.Emit(ctx, baseagent.Event{Type: baseagent.EventToolResult, Title: "[WORKER] " + call.Function.Name, Content: toolResult})
			if _, err := agent.storeMessage(llm.ToolMessage(call.ID, toolResult), call.Function.Name); err != nil {
				return "", fmt.Errorf("store worker tool result: %w", err)
			}
		}
	}
}

func (agent *WorkerSubAgent) compactContext(
	ctx context.Context,
	query string,
	definitions []tool.Definition,
) error {
	measure := func() (int, error) {
		messages, err := agent.contextBuilder.Build(ctx, query)
		if err != nil {
			return 0, err
		}
		messages = append([]llm.Message{llm.SystemMessage(agent.SystemPrompt())}, messages...)
		return agent.requestEstimator.Estimate(messages, definitions)
	}
	decisions, err := agent.compactionScheduler.CompactToFit(ctx, agent.memoryManager, measure)
	baseagent.EmitCompactionDecisions(ctx, decisions)
	if err != nil {
		return fmt.Errorf("compact worker context: %w", err)
	}
	return nil
}

func (agent *WorkerSubAgent) storeMessage(message llm.Message, toolName string) (memory.Entry, error) {
	entry, err := agent.memoryManager.AddMessage(message, toolName)
	if err != nil {
		return memory.Entry{}, err
	}
	if err := agent.transcript.Append(agent.conversationID, entry); err != nil {
		return entry, fmt.Errorf("append worker message to transcript: %w", err)
	}
	return entry, nil
}

func (agent *WorkerSubAgent) executeWorkerTool(
	ctx context.Context,
	call llm.ToolCall,
) string {
	var arguments map[string]interface{}
	if err := json.Unmarshal([]byte(call.Function.Arguments), &arguments); err != nil {
		return "ERROR: invalid tool arguments: " + err.Error()
	}
	result, err := agent.tools.Execute(ctx, call.Function.Name, arguments)
	if err != nil {
		return "ERROR: " + err.Error()
	}
	return result
}
