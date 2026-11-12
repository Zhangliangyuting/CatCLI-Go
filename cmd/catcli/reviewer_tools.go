package main

import "AgentCLI/internal/tool"

// Reviewers inspect local files through the built-in read-only tools.
func selectReviewerTools(registry *tool.ToolRegistry) *tool.ToolRegistry {
	return registry.Subset("list_dir", "read_file")
}
