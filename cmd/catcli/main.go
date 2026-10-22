package main

import (
	"AgentCLI/internal/agent"
	"AgentCLI/internal/agent/multiagent"
	"AgentCLI/internal/cli"
	"AgentCLI/internal/config"
	"AgentCLI/internal/llm"
	"AgentCLI/internal/memory"
	"AgentCLI/internal/rag"
	"AgentCLI/internal/routing"
	"AgentCLI/internal/tool"
	workspacepkg "AgentCLI/internal/workspace"
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync/atomic"
)

func main() {
	printBanner()
	printHelp()
	currentWorkspace, err := workspacepkg.FromCurrentDirectory()
	if err != nil {
		fmt.Println("workspace error:", err)
		return
	}

	// 加载环境变量配置
	cfg, err := config.Load()
	if err != nil {
		fmt.Println("config error:", err)
		return
	}

	// 创建llm客户端
	client, err := llm.NewOpenAICompatibleClient(cfg.OpenAICompatible.APIKey, cfg.OpenAICompatible.BaseURL, cfg.OpenAICompatible.Model)
	if err != nil {
		fmt.Println("failed to create LLM client:", err)
		return
	}

	// 创建 ToolRegistry
	toolRegistry := tool.NewToolRegistry()

	//1. 注册工具 -- 硬编码
	/*
		toolRegistry.RegisterTool(tool.ListDirDefinition(), tool.ListDirHandler)
		toolRegistry.RegisterTool(tool.ReadFileDefinition(), tool.ReadFileHandler)
		toolRegistry.RegisterTool(tool.EditFileDefinition(), tool.EditFileHandler)
		toolRegistry.RegisterTool(tool.CreateProjectDefinition(), tool.CreateProjectHandler)
		toolRegistry.RegisterTool(tool.ExecuteCommandDefinition(), tool.ExecuteCommandHandler)
		toolRegistry.RegisterTool(tool.WriteFileDefinition(), tool.WriteFileHandler)
	*/

	//2. 注册工具 -- 通过 Provider动态注册
	//toolRegistry.RegisterProvider(tool.NewBuiltinProvider(currentWorkspace))

	//3. 注册工具 -- 配置文件控制启用的Provider和工具
	providers, err := tool.Providers(cfg.Providers.Enabled, currentWorkspace)
	if err != nil {
		fmt.Println("provider error:", err)
		return
	}

	if err := toolRegistry.RegisterEnabledTools(providers, cfg.Tools.Enabled); err != nil {
		fmt.Println("tool register error:", err)
		return
	}

	// Hybrid retrieval uses the configured embeddings model when available.
	var embedder llm.Embedder
	if model := strings.TrimSpace(cfg.Embedding.Model); model != "" {
		apiKey := strings.TrimSpace(cfg.Embedding.APIKey)
		if apiKey == "" {
			apiKey = client.APIKey
		}
		baseURL := strings.TrimSpace(cfg.Embedding.BaseURL)
		if baseURL == "" {
			baseURL = client.BaseURL
		}
		embedder = &llm.OpenAIEmbeddingClient{
			APIKey: apiKey, BaseURL: baseURL, Model: model, HTTPClient: client.HTTPClient,
		}
	}
	referenceProvider, ragRuntime, err := buildWorkspaceRAG(
		context.Background(), currentWorkspace, embedder, cfg,
	)
	if err != nil {
		fmt.Println("workspace RAG error:", err)
		return
	}
	if ragRuntime != nil {
		defer ragRuntime.Close()
	}
	retriever := memory.NewMemoryRetriever(embedder)
	// 创建或恢复当前对话的短期记忆，并接入共享检索器和自动压缩。
	memoryRuntime, err := memory.NewConversationRuntime(
		client,
		retriever,
		currentWorkspace.MetadataDir(),
		os.Getenv("CATCLI_CONVERSATION_ID"),
		cfg.OpenAICompatible.UsableInputTokens(),
		cfg.OpenAICompatible.CompactionMaxTokens,
	)
	if err != nil {
		fmt.Println("memory runtime error:", err)
		return
	}
	rootMemory := memoryRuntime.RootContext()
	agentInstance := agent.NewReActAgent(
		client,
		toolRegistry,
		agent.WithMemoryManager(rootMemory.Manager),
		agent.WithContextBuilder(rootMemory.ContextBuilder),
		agent.WithCompactionScheduler(rootMemory.Scheduler),
		agent.WithRequestTokenEstimator(rootMemory.Estimator),
		agent.WithTranscript(rootMemory.Transcript, rootMemory.ConversationID),
		agent.WithReferenceContextProvider(referenceProvider),
	)
	factExtractor, err := memory.NewLLMFactExtractor(client)
	if err != nil {
		fmt.Println("fact extractor error:", err)
		return
	}
	factAwareReactAgent, err := agent.NewFactAwareAgent(
		agentInstance,
		rootMemory.Manager,
		factExtractor,
	)
	if err != nil {
		fmt.Println("fact-aware agent error:", err)
		return
	}
	eventObserver := newConsoleObserver(os.Stdout, cfg.Debug)
	reader := bufio.NewReader(os.Stdin)

	plannerSubAgent, err := multiagent.NewPlannerSubAgent(
		client,
	)
	if err != nil {
		fmt.Println("planner SubAgent error:", err)
		return
	}
	reviewerSubAgent, err := multiagent.NewReviewerSubAgent(
		client,
		toolRegistry.Subset("list_dir", "read_file"),
	)
	if err != nil {
		fmt.Println("reviewer SubAgent error:", err)
		return
	}
	modeRouter := routing.NewHybridModeRouter(client)
	planAgent, err := multiagent.NewPlanAndExecuteAgent(
		plannerSubAgent,
		client,
		toolRegistry,
		reviewerSubAgent,
		memoryRuntime,
		cli.NewCLIPlanDecisionProvider(reader),
		cfg.Agent.MaxReplanAttempts,
		cfg.Agent.MaxWorkers,
		cfg.Agent.TaskTimeout,
		cfg.Agent.PlanTimeout,
		multiagent.WithReferenceContextProvider(referenceProvider),
	)
	if err != nil {
		fmt.Println("plan-and-execute SubAgent error:", err)
		return
	}
	factAwarePlanAgent, err := agent.NewFactAwareAgent(
		planAgent,
		rootMemory.Manager,
		factExtractor,
	)
	if err != nil {
		fmt.Println("fact-aware plan agent error:", err)
		return
	}

	//用户输入循环
	if memoryRuntime.Resumed() {
		fmt.Printf("Resumed conversation: %s\n", rootMemory.ConversationID)
	} else {
		fmt.Printf("New conversation: %s\n", rootMemory.ConversationID)
		fmt.Printf("Resume with CATCLI_CONVERSATION_ID=%s\n", rootMemory.ConversationID)
	}
	fmt.Println("AgentCLI started. Type exit to quit.")

	for {
		fmt.Print("> ")

		input, err := reader.ReadString('\n')
		if err != nil {
			fmt.Println("read input error:", err)
			return
		}

		input = strings.TrimSpace(input)
		if input == "" {
			continue
		}

		if input == "exit" || input == "quit" {
			fmt.Println("bye")
			return
		}

		if input == "clear" {
			agentInstance.ClearHistory()
			if err := memoryRuntime.Save(); err != nil {
				fmt.Println("save conversation error:", err)
				continue
			}
			fmt.Println("History cleared.")
			continue
		}

		answer, err := runWithInterrupt(
			func(ctx context.Context) (string, error) {
				return runRoutedInput(
					agent.WithObserver(ctx, eventObserver),
					modeRouter,
					factAwareReactAgent,
					factAwarePlanAgent,
					input,
				)
			},
		)
		saveErr := memoryRuntime.Save()
		if err != nil {
			printRunError("agent error", err)
			if saveErr != nil {
				fmt.Println("save conversation error:", saveErr)
			}
			continue
		}
		if saveErr != nil {
			fmt.Println("save conversation error:", saveErr)
			continue
		}

		fmt.Println(answer)
	}

}

// workspaceRAG keeps indexing independent of requests. Incompatible or missing
// indexes stay hidden until a complete indexing pass succeeds.
type workspaceRAG struct {
	provider *rag.ContextProvider
	store    *rag.SQLiteStore
	ready    atomic.Bool
	cancel   context.CancelFunc
	done     chan struct{}
}

func (runtime *workspaceRAG) Build(ctx context.Context, query string) ([]llm.Message, error) {
	return runtime.BuildWithReport(ctx, query, nil)
}

func (runtime *workspaceRAG) BuildWithReport(ctx context.Context, query string, onReport func(rag.RetrievalReport)) ([]llm.Message, error) {
	if !runtime.ready.Load() {
		if onReport != nil {
			onReport(rag.RetrievalReport{SkippedReason: "index_not_ready"})
		}
		return nil, nil
	}
	return runtime.provider.BuildWithReport(ctx, query, onReport)
}

func (runtime *workspaceRAG) Close() error {
	runtime.cancel()
	<-runtime.done
	return runtime.store.Close()
}

func buildWorkspaceRAG(
	ctx context.Context,
	work *workspacepkg.Workspace,
	embedder llm.Embedder,
	cfg config.Config,
) (agent.ReferenceContextProvider, *workspaceRAG, error) {
	if !cfg.RAG.Enabled || embedder == nil {
		return nil, nil, nil
	}
	store, err := rag.NewSQLiteStore(filepath.Join(work.MetadataDir(), "rag.db"))
	if err != nil {
		return nil, nil, err
	}
	closeOnError := func(err error) (agent.ReferenceContextProvider, *workspaceRAG, error) {
		_ = store.Close()
		return nil, nil, err
	}
	chunkers := rag.NewDefaultChunkerSet(rag.ChunkConfig{})
	indexer, err := rag.NewWorkspaceIndexer(
		work,
		chunkers,
		embedder,
		store,
		rag.WorkspaceIndexerConfig{
			MaxFileBytes:       cfg.RAG.MaxFileBytes,
			EmbeddingBatchSize: cfg.RAG.EmbeddingBatchSize,
			EmbeddingModel:     cfg.Embedding.Model,
		},
	)
	if err != nil {
		return closeOnError(err)
	}
	metadata, exists, err := store.LoadIndexMetadata(ctx)
	if err != nil {
		return closeOnError(err)
	}
	retriever, err := rag.NewRetriever(store, embedder)
	if err != nil {
		return closeOnError(err)
	}
	formatter, err := rag.NewSearchResultFormatter(memory.ApproxTokenCounter{})
	if err != nil {
		return closeOnError(err)
	}
	maxContextTokens := min(cfg.RAG.MaxContextTokens, cfg.OpenAICompatible.UsableInputTokens()/5)
	if maxContextTokens <= 0 {
		return closeOnError(fmt.Errorf("workspace RAG context budget is empty"))
	}
	provider, err := rag.NewContextProvider(
		retriever,
		formatter,
		maxContextTokens,
		cfg.RAG.SearchLimit,
	)
	if err != nil {
		return closeOnError(err)
	}
	indexCtx, cancel := context.WithCancel(ctx)
	runtime := &workspaceRAG{
		provider: provider, store: store, cancel: cancel, done: make(chan struct{}),
	}
	runtime.ready.Store(exists && metadata.EmbeddingModel == strings.TrimSpace(cfg.Embedding.Model) &&
		metadata.ChunkerVersion == chunkers.Version())
	fmt.Printf("Workspace RAG: indexing in background (existing index available=%t)\n", runtime.ready.Load())
	go func() {
		defer close(runtime.done)
		report, err := indexer.Index(indexCtx)
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				fmt.Fprintf(os.Stderr, "\nWorkspace RAG indexing error (chat remains available): %v\n", err)
			}
			return
		}
		runtime.ready.Store(true)
		fmt.Printf("\nWorkspace RAG ready: indexed=%d unchanged=%d deleted=%d chunks=%d rebuilt=%t\n",
			report.IndexedFiles, report.UnchangedFiles, report.DeletedFiles, report.Chunks, report.RebuiltIndex)
	}()
	return runtime, runtime, nil
}

func runRoutedInput(
	ctx context.Context,
	router routing.ModeRouter,
	reactAgent agent.Agent,
	planAgent agent.Agent,
	input string,
) (string, error) {
	decision, err := router.Route(ctx, input)
	if err != nil {
		return "", err
	}

	if decision.Source != routing.SourceExplicit {
		fmt.Printf(
			"[router] 自动选择 %s：%s\n",
			decision.Mode,
			decision.Reason,
		)
	}

	switch decision.Mode {
	case routing.ModeReact:
		return reactAgent.Run(ctx, decision.Input)
	case routing.ModePlan:
		return planAgent.Run(ctx, decision.Input)
	default:
		return "", fmt.Errorf(
			"unsupported execution mode %q",
			decision.Mode,
		)
	}
}

func runWithInterrupt(
	run func(context.Context) (string, error),
) (string, error) {
	ctx, stop := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
	)
	defer stop()

	return run(ctx)
}

func printRunError(prefix string, err error) {
	switch {
	case errors.Is(err, context.Canceled):
		fmt.Println("当前执行已取消")
	case errors.Is(err, context.DeadlineExceeded):
		fmt.Println("当前执行已超时")
	default:
		fmt.Printf("%s: %v\n", prefix, err)
	}
}

func printBanner() {
	fmt.Println("\033[38;5;33m╔══════════════════════════════════════════════════════╗")
	fmt.Println("\033[38;5;39m║       ██████╗ █████╗ ████████╗ ██████╗██╗     ██╗    ║")
	fmt.Println("\033[38;5;45m║      ██╔════╝██╔══██╗╚══██╔══╝██╔════╝██║     ██║    ║")
	fmt.Println("\033[38;5;51m║     ██║     ███████║   ██║   ██║     ██║     ██║     ║")
	fmt.Println("\033[38;5;87m║    ██║     ██╔══██║   ██║   ██║     ██║     ██║      ║")
	fmt.Println("\033[38;5;123m║   ╚██████╗██║  ██║   ██║   ╚██████╗███████╗██║       ║")
	fmt.Println("\033[38;5;159m║  ╚═════╝╚═╝  ╚═╝   ╚═╝    ╚═════╝╚══════╝╚═╝         ║")
	fmt.Println("\033[38;5;75m╚══════════════════════════════════════════════════════╝")
	fmt.Println("\033[38;5;245m              A tiny Go Agent CLI v0.1.0\033[0m")
	fmt.Println()
}

func printHelp() {
	fmt.Println("💡 提示:")
	fmt.Println("   - 直接输入任务时自动选择 ReAct 或 Plan 模式")
	fmt.Println("   - 输入 '/react <任务>' 强制使用 ReAct 模式")
	fmt.Println("   - 输入 '/plan <任务>' 强制使用 Plan 模式")
	fmt.Println("   - 输入 'clear' 清空对话历史")
	fmt.Println("   - 输入 'exit' 或 'quit' 退出")
	fmt.Println()
}
