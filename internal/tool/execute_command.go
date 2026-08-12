package tool

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

func ExecuteCommandDefinition() Definition {
	return Definition{
		Type: "function",
		FunctionDefinition: FunctionDefinition{
			Name:        "execute_command",
			Description: "执行一个 shell 命令并返回 stdout/stderr。适合运行测试、格式化、查看版本等命令。",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"command": map[string]interface{}{
						"type":        "string",
						"description": "要执行的 shell 命令",
					},
					"timeout_seconds": map[string]interface{}{
						"type":        "number",
						"description": "命令超时时间，默认 10 秒",
					},
				},
				"required": []string{"command"},
			},
		},
	}
}

func ExecuteCommandHandler(args map[string]interface{}) (string, error) {
	command, ok := args["command"].(string)
	if !ok || strings.TrimSpace(command) == "" {
		return "", fmt.Errorf("command is required")
	}

	command = strings.TrimSpace(command)

	if containsDangerousShellSyntax(command) {
		return "", fmt.Errorf("command contains unsupported shell syntax")
	}

	parts := strings.Fields(command)
	if len(parts) == 0 {
		return "", fmt.Errorf("command is empty")
	}

	if !isAllowedCommand(parts) {
		return "", fmt.Errorf("command is not allowed: %s", command)
	}

	timeoutSeconds := 10
	if value, ok := args["timeout_seconds"].(float64); ok && value > 0 {
		timeoutSeconds = int(value)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutSeconds)*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, parts[0], parts[1:]...)
	output, err := cmd.CombinedOutput()

	if ctx.Err() == context.DeadlineExceeded {
		return string(output), fmt.Errorf("command timed out after %d seconds", timeoutSeconds)
	}
	if err != nil {
		return string(output), fmt.Errorf("command failed: %w", err)
	}

	return string(output), nil
}

func containsDangerousShellSyntax(command string) bool {
	dangerous := []string{
		";", "&&", "||", "|", ">", ">>", "<",
		"$", "`",
	}

	for _, item := range dangerous {
		if strings.Contains(command, item) {
			return true
		}
	}

	return false
}

func isAllowedCommand(parts []string) bool {
	if len(parts) == 0 {
		return false
	}

	switch parts[0] {
	case "pwd":
		return len(parts) == 1

	case "ls":
		return true

	case "go":
		if len(parts) < 2 {
			return false
		}

		switch parts[1] {
		case "test", "run", "fmt":
			return true
		case "mod":
			return len(parts) >= 3 && parts[2] == "tidy"
		default:
			return false
		}

	default:
		return false
	}
}
