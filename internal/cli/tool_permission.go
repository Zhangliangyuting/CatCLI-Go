package cli

import (
	"AgentCLI/internal/tool"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

type ContextLineReader interface {
	ReadStringContext(context.Context, byte) (string, error)
}

// ToolPermissionConfirmer asks for this exact call only. No implicit session grants.
func ToolPermissionConfirmer(reader ContextLineReader, output io.Writer) tool.PermissionConfirmer {
	return func(ctx context.Context, request tool.PermissionRequest) (bool, error) {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		data, err := json.MarshalIndent(request.Arguments, "", "  ")
		if err != nil {
			return false, err
		}
		// Quote untrusted names/reasons so terminal control characters stay inert.
		if _, err := fmt.Fprintf(output, "\n工具权限确认 %q (%s)\n原因: %q\n参数:\n%s\n允许本次调用？[y/N]: ", request.Tool, request.Operation, request.Reason, data); err != nil {
			return false, err
		}
		line, err := reader.ReadStringContext(ctx, '\n')
		if err != nil {
			return false, err
		}
		answer := strings.ToLower(strings.TrimSpace(line))
		return answer == "y" || answer == "yes", ctx.Err()
	}
}
