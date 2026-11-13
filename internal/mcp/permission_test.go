package mcp

import (
	"AgentCLI/internal/tool"
	"context"
	"errors"
	"testing"
)

type permissionProvider struct{ t tool.Tool }

func (p permissionProvider) Tools() []tool.Tool { return []tool.Tool{p.t} }
func TestMCPPermissionBeforeTransport(t *testing.T) {
	for _, server := range []string{"brave", "fetch", "custom"} {
		// A nil client would panic if transport were reached before authorization.
		adapted := adaptTool(server, ToolDescriptor{Name: "fetch"}, nil)
		r := tool.NewToolRegistry()
		r.RegisterProvider(permissionProvider{adapted})
		r.SetPermissionConfirmer(func(_ context.Context, request tool.PermissionRequest) (bool, error) {
			if request.Tool != ToolName(server, "fetch") {
				t.Fatal(request.Tool)
			}
			return false, nil
		})
		if _, err := r.Execute(t.Context(), ToolName(server, "fetch"), map[string]any{"url": "https://example.com"}); !errors.Is(err, tool.ErrPermissionDenied) {
			t.Fatal(err)
		}
	}
}
