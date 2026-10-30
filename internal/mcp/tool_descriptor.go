package mcp

import (
	"AgentCLI/internal/tool"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

type ToolDescriptor struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"inputSchema"`
}

var unsafeToolCharacters = regexp.MustCompile(`[^A-Za-z0-9_-]`)

// ToolName keeps the usual mcp__server__tool spelling. Ambiguous, transformed
// and long names receive a stable digest and fit model providers' 64-byte limit.
func ToolName(server, name string) string {
	full := "mcp__" + server + "__" + name
	if len(full) <= 64 && !unsafeToolCharacters.MatchString(full) && !strings.Contains(server, "__") && !strings.Contains(name, "__") {
		return full
	}
	clean := unsafeToolCharacters.ReplaceAllString(full, "_")
	if len(clean) > 45 {
		clean = clean[:45]
	}
	digest := sha256.Sum256([]byte(server + "\x00" + name))
	return clean + "_" + hex.EncodeToString(digest[:8])
}

func adaptTool(server string, descriptor ToolDescriptor, client *Client) tool.Tool {
	return tool.Tool{
		Definition: tool.Definition{Type: "function", FunctionDefinition: tool.FunctionDefinition{
			Name:        ToolName(server, descriptor.Name),
			Description: descriptor.Description,
			Parameters:  descriptor.InputSchema,
		}},
		Handler: func(ctx context.Context, args map[string]any) (string, error) {
			result, err := client.CallTool(ctx, descriptor.Name, args)
			if err != nil {
				return "", err
			}
			encoded, err := json.Marshal(result)
			if err != nil {
				return "", err
			}
			if result.IsError {
				return string(encoded), fmt.Errorf("MCP tool %s failed: %s", descriptor.Name, encoded)
			}
			return string(encoded), nil
		},
	}
}
