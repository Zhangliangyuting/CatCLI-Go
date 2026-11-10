package mcp

import (
	"AgentCLI/internal/tool"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
)

// verifyFilesystemWrite reads through the same MCP server as the original
// write. It confirms the current postcondition, not the historical call result.
func verifyFilesystemWrite(ctx context.Context, client *Client, args map[string]any) (*tool.Verification, error) {
	path, pathOK := args["path"].(string)
	content, contentOK := args["content"].(string)
	if !pathOK || path == "" || !contentOK {
		return nil, fmt.Errorf("filesystem write verification requires path and content strings")
	}
	result, err := client.CallTool(ctx, "read_text_file", map[string]any{"path": path})
	if err != nil {
		return nil, err
	}
	if result.IsError {
		return &tool.Verification{Retry: true, Evidence: "filesystem MCP could not read the current file; prior write remains uncertain"}, nil
	}
	observed, err := filesystemText(result)
	if err != nil {
		return nil, err
	}
	if observed != content {
		return &tool.Verification{Retry: true, Evidence: "filesystem MCP file content differs from the requested write; prior write remains uncertain"}, nil
	}
	sum := sha256.Sum256([]byte(observed))
	return &tool.Verification{
		Evidence: fmt.Sprintf("filesystem MCP write postcondition verified: path=%q bytes=%d sha256=%x; current full content matches requested content; historical execution is not established", path, len(observed), sum),
		Result:   fmt.Sprintf("filesystem MCP write recovery verified: current file content already matches requested content (%d bytes); no write performed", len(observed)),
	}, nil
}

// get_file_info in the filesystem server reports file type as text lines.
// Only an explicit directory result confirms create_directory's postcondition.
func verifyFilesystemDirectory(ctx context.Context, client *Client, args map[string]any) (*tool.Verification, error) {
	path, ok := args["path"].(string)
	if !ok || path == "" {
		return nil, fmt.Errorf("filesystem directory verification requires a path string")
	}
	result, err := client.CallTool(ctx, "get_file_info", map[string]any{"path": path})
	if err != nil {
		return nil, err
	}
	if result.IsError {
		return &tool.Verification{Retry: true, Evidence: "filesystem MCP could not inspect the directory; prior creation remains uncertain"}, nil
	}
	info, err := filesystemText(result)
	if err != nil {
		return nil, err
	}
	var directory, file bool
	var seenDirectory, seenFile bool
	for _, line := range strings.Split(info, "\n") {
		key, value, found := strings.Cut(line, ": ")
		if !found {
			continue
		}
		switch key {
		case "isDirectory":
			if seenDirectory || value != "true" && value != "false" {
				return nil, fmt.Errorf("filesystem MCP returned invalid directory type")
			}
			seenDirectory, directory = true, value == "true"
		case "isFile":
			if seenFile || value != "true" && value != "false" {
				return nil, fmt.Errorf("filesystem MCP returned invalid file type")
			}
			seenFile, file = true, value == "true"
		}
	}
	if !seenDirectory || !seenFile {
		return nil, fmt.Errorf("filesystem MCP file info has no complete type")
	}
	if !directory || file {
		return &tool.Verification{Retry: true, Evidence: "filesystem MCP path is not a directory; prior creation remains uncertain"}, nil
	}
	return &tool.Verification{
		Evidence: fmt.Sprintf("filesystem MCP create_directory postcondition verified: path=%q isDirectory=true isFile=false; historical execution is not established", path),
		Result:   "filesystem MCP create_directory recovery verified: target directory exists; no directory created",
	}, nil
}

func filesystemText(result CallToolResult) (string, error) {
	var observed string
	if len(result.StructuredContent) > 0 {
		var structured struct {
			Content *string `json:"content"`
		}
		if err := json.Unmarshal(result.StructuredContent, &structured); err != nil || structured.Content == nil {
			return "", fmt.Errorf("filesystem MCP read returned invalid structured content")
		}
		observed = *structured.Content
	}
	if len(result.Content) > 0 {
		if len(result.Content) != 1 {
			return "", fmt.Errorf("filesystem MCP read requires one text content block")
		}
		var block struct {
			Type string  `json:"type"`
			Text *string `json:"text"`
		}
		if err := json.Unmarshal(result.Content[0], &block); err != nil || block.Type != "text" || block.Text == nil {
			return "", fmt.Errorf("filesystem MCP read returned invalid text content")
		}
		if len(result.StructuredContent) > 0 && observed != *block.Text {
			return "", fmt.Errorf("filesystem MCP read returned conflicting content")
		}
		observed = *block.Text
	} else if len(result.StructuredContent) == 0 {
		return "", fmt.Errorf("filesystem MCP read returned no content")
	}
	return observed, nil
}
