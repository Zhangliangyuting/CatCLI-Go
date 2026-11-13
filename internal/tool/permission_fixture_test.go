package tool

import (
	"context"
)

// Existing execution tests explicitly approve calls; permission tests use the production defaults.
func newApprovedTestRegistry() *ToolRegistry {
	r := NewToolRegistry()
	r.SetPermissionConfirmer(func(context.Context, PermissionRequest) (bool, error) { return true, nil })
	return r
}
