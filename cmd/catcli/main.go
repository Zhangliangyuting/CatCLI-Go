package main

import (
	"AgentCLI/internal/agent"
	"AgentCLI/internal/config"
	"AgentCLI/internal/llm"
	"AgentCLI/internal/plan"
	"AgentCLI/internal/routing"
	"AgentCLI/internal/tool"
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
)

func main() {
	printBanner()
	printHelp()

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
	//toolRegistry.RegisterProvider(tool.NewBuiltinProvider())

	//3. 注册工具 -- 配置文件控制启用的Provider和工具
	providers, err := tool.Providers(cfg.Providers.Enabled)
	if err != nil {
		fmt.Println("provider error:", err)
		return
	}

	if err := toolRegistry.RegisterEnabledTools(providers, cfg.Tools.Enabled); err != nil {
		fmt.Println("tool register error:", err)
		return
	}

	// 创建 Agent
	agentInstance := agent.NewReActAgent(client, toolRegistry)
	eventObserver := agent.SynchronizedObserver(printAgentEvent)
	reader := bufio.NewReader(os.Stdin)

	planner := plan.NewLLMPlanGenerator(client)
	modeRouter := routing.NewHybridModeRouter(client)
	planAgent := agent.NewPlanAndExecuteAgent(
		planner,
		func() agent.Agent {
			return agent.NewReActAgent(
				client,
				toolRegistry,
			)
		},
		&cliPlanReviewer{reader: reader},
		cfg.Agent.MaxReplanAttempts,
		cfg.Agent.MaxWorkers,
		cfg.Agent.TaskTimeout,
		cfg.Agent.PlanTimeout,
	)

	//用户输入循环
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
			fmt.Println("History cleared.")
			continue
		}

		answer, err := runWithInterrupt(
			func(ctx context.Context) (string, error) {
				return runRoutedInput(
					ctx,
					modeRouter,
					agentInstance,
					planAgent,
					input,
					eventObserver,
				)
			},
		)
		if err != nil {
			printRunError("agent error", err)
			continue
		}

		fmt.Println(answer)
	}

}

func runRoutedInput(
	ctx context.Context,
	router routing.ModeRouter,
	reactAgent agent.ObservableAgent,
	planAgent agent.ObservableAgent,
	input string,
	observer agent.Observer,
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
		return reactAgent.RunWithObserver(
			ctx,
			decision.Input,
			observer,
		)
	case routing.ModePlan:
		return planAgent.RunWithObserver(
			ctx,
			decision.Input,
			observer,
		)
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

func printAgentEvent(event agent.Event) {
	prefix := "[agent]"
	if event.TaskID != "" {
		prefix = "[" + event.TaskID + "]"
	}

	switch event.Type {
	case agent.EventTokenUsage:
		fmt.Printf("%s token: %s\n", prefix, event.Content)
	case agent.EventToolCall:
		fmt.Printf(
			"%s tool call %s: %s\n",
			prefix,
			event.Title,
			event.Content,
		)
	case agent.EventToolResult:
		fmt.Printf(
			"%s tool result %s:\n%s\n",
			prefix,
			event.Title,
			event.Content,
		)
	case agent.EventTaskStarted:
		fmt.Printf(
			"\n%s 开始执行：%s\n%s\n",
			prefix,
			event.Title,
			event.Content,
		)
	case agent.EventTaskCompleted:
		fmt.Printf(
			"\n%s 任务完成：%s\n%s\n",
			prefix,
			event.Title,
			event.Content,
		)
	case agent.EventTaskFailed:
		fmt.Printf(
			"\n%s 任务失败：%s\n%s\n",
			prefix,
			event.Title,
			event.Content,
		)
	case agent.EventTaskCancelled:
		fmt.Printf(
			"\n%s 任务已取消：%s\n%s\n",
			prefix,
			event.Title,
			event.Content,
		)
	case agent.EventTaskTimeout:
		fmt.Printf(
			"\n%s 任务执行超时：%s\n%s\n",
			prefix,
			event.Title,
			event.Content,
		)
	case agent.EventPlanGenerated, agent.EventPlanRevised,
		agent.EventPlanCancelled:
		fmt.Printf("\n[plan] %s\n", event.Title)
	case agent.EventPlanReplanning, agent.EventPlanCompleted,
		agent.EventPlanFailed, agent.EventPlanTimeout:
		fmt.Printf(
			"\n[plan] %s\n%s\n",
			event.Title,
			event.Content,
		)
	}
}

type cliPlanReviewer struct {
	reader *bufio.Reader
}

func (r *cliPlanReviewer) Review(
	p *plan.Plan,
) (agent.PlanAction, string, error) {
	for {
		fmt.Println(p.Visualize())
		fmt.Print("[e] 执行  [r] 修改计划  [c] 取消: ")

		input, err := r.reader.ReadString('\n')
		if err != nil {
			return "", "", err
		}

		switch strings.ToLower(strings.TrimSpace(input)) {
		case "e", "execute":
			return agent.PlanExecute, "", nil
		case "r", "revise", "replan":
			fmt.Print("请输入计划修改意见: ")

			feedback, err := r.reader.ReadString('\n')
			if err != nil {
				return "", "", err
			}

			feedback = strings.TrimSpace(feedback)
			if feedback == "" {
				fmt.Println("修改意见不能为空")
				continue
			}

			return agent.PlanRevise, feedback, nil
		case "c", "cancel":
			return agent.PlanCancel, "", nil
		default:
			fmt.Println("无效选项，请输入 e、r 或 c")
		}
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
