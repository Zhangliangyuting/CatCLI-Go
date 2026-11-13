package multiagent

import (
	"AgentCLI/internal/tool"
	"context"
)

// Existing execution tests explicitly approve calls; permission tests use the production defaults.
func newApprovedTestRegistry() *tool.ToolRegistry {
	r := tool.NewToolRegistry()
	r.SetPermissionConfirmer(func(context.Context, tool.PermissionRequest) (bool, error) { return true, nil })
	return r
}
