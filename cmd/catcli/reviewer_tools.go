package main

import (
	"AgentCLI/internal/mcp"
	"AgentCLI/internal/tool"
)

// Prefer the configured filesystem server's read-only tools. Fall back per
// operation when that server is disabled, unavailable, or filters out a tool.
func selectReviewerTools(registry *tool.ToolRegistry) *tool.ToolRegistry {
	available := make(map[string]bool)
	if registry != nil {
		for _, definition := range registry.ToolDefinitions() {
			available[definition.FunctionDefinition.Name] = true
		}
	}
	var names []string
	for _, pair := range [][2]string{
		{mcp.ToolName("filesystem", "list_directory"), "list_dir"},
		{mcp.ToolName("filesystem", "read_text_file"), "read_file"},
	} {
		if available[pair[0]] {
			names = append(names, pair[0])
		} else {
			names = append(names, pair[1])
		}
	}
	return registry.Subset(names...)
}
