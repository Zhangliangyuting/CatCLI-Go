package main

import (
	"AgentCLI/internal/agent"
	"AgentCLI/internal/config"
	"AgentCLI/internal/llm"
	"AgentCLI/internal/tool"
	"bufio"
	"fmt"
	"os"
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
	agentInstance := agent.NewAgent(client, toolRegistry, cfg.Agent.MaxSteps)

	//用户输入循环
	reader := bufio.NewReader(os.Stdin)

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

		answer, err := agentInstance.Run(input)
		if err != nil {
			fmt.Println("agent error:", err)
			continue
		}

		fmt.Println(answer)
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
	fmt.Println("   - 输入你的问题或任务")
	fmt.Println("   - 输入 'clear' 清空对话历史")
	fmt.Println("   - 输入 'exit' 或 'quit' 退出")
	fmt.Println()
}
