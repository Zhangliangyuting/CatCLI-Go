package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
)

const ProtocolVersion = "2025-11-25"

// Raw content blocks preserve text, images, embedded resources and extensions.
type CallToolResult struct {
	Content           []json.RawMessage `json:"content"`
	StructuredContent json.RawMessage   `json:"structuredContent,omitempty"`
	IsError           bool              `json:"isError,omitempty"`
	Meta              json.RawMessage   `json:"_meta,omitempty"`
}

type InitializeResult struct {
	ProtocolVersion string                     `json:"protocolVersion"`
	Capabilities    map[string]json.RawMessage `json:"capabilities"`
	ServerInfo      struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"serverInfo"`
	Instructions string `json:"instructions,omitempty"`
}

type Client struct {
	rpc         *JSONRPCClient
	mu          sync.Mutex
	initialized bool
	info        InitializeResult
	initGate    chan struct{}
}

func NewClient(rpc *JSONRPCClient) *Client {
	return &Client{rpc: rpc, initGate: make(chan struct{}, 1)}
}

func (c *Client) Initialize(ctx context.Context) error {
	select {
	case c.initGate <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-c.initGate }()
	c.mu.Lock()
	defer c.mu.Unlock()
	if http, ok := c.rpc.transport.(*StreamableHTTPTransport); ok && http.SessionExpired() {
		c.initialized = false
	}
	if c.initialized {
		return nil
	}
	var result InitializeResult
	err := c.rpc.Call(ctx, "initialize", map[string]any{
		"protocolVersion": ProtocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]string{"name": "CatCLI", "version": "0.1.0"},
	}, &result)
	if err != nil {
		return err
	}
	switch result.ProtocolVersion {
	case ProtocolVersion, "2025-06-18", "2025-03-26":
	default:
		return fmt.Errorf("unsupported MCP protocol version %q", result.ProtocolVersion)
	}
	if result.ServerInfo.Name == "" {
		return fmt.Errorf("MCP initialize missing serverInfo.name")
	}
	if versioned, ok := c.rpc.transport.(interface{ SetProtocolVersion(string) }); ok {
		versioned.SetProtocolVersion(result.ProtocolVersion)
	}
	if err := c.rpc.Notify(ctx, "notifications/initialized", map[string]any{}); err != nil {
		return err
	}
	c.info, c.initialized = result, true
	return nil
}

func (c *Client) ListTools(ctx context.Context) ([]ToolDescriptor, error) {
	if err := c.Initialize(ctx); err != nil {
		return nil, err
	}
	c.mu.Lock()
	_, supportsTools := c.info.Capabilities["tools"]
	c.mu.Unlock()
	if !supportsTools {
		return nil, nil
	}
	var tools []ToolDescriptor
	seenNames, seenCursors := map[string]bool{}, map[string]bool{}
	cursor := ""
	for page := 0; page < 1000; page++ {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		var result struct {
			Tools      []ToolDescriptor `json:"tools"`
			NextCursor string           `json:"nextCursor"`
		}
		if err := c.rpc.Call(ctx, "tools/list", params, &result); err != nil {
			return nil, err
		}
		for _, descriptor := range result.Tools {
			if descriptor.Name == "" || seenNames[descriptor.Name] {
				return nil, fmt.Errorf("empty or duplicate MCP tool name")
			}
			if descriptor.InputSchema == nil || descriptor.InputSchema["type"] != "object" {
				return nil, fmt.Errorf("MCP tool %q requires an object inputSchema", descriptor.Name)
			}
			seenNames[descriptor.Name] = true
			tools = append(tools, descriptor)
		}
		if result.NextCursor == "" {
			return tools, nil
		}
		if seenCursors[result.NextCursor] {
			return nil, fmt.Errorf("MCP tools/list repeated cursor")
		}
		cursor = result.NextCursor
		seenCursors[cursor] = true
	}
	return nil, fmt.Errorf("MCP tools/list exceeded page limit")
}

func (c *Client) CallTool(ctx context.Context, name string, arguments map[string]any) (CallToolResult, error) {
	if err := c.Initialize(ctx); err != nil {
		return CallToolResult{}, err
	}
	if arguments == nil {
		arguments = map[string]any{}
	}
	var result CallToolResult
	// tools/call may have side effects. Neither transport errors nor session
	// expiration trigger a replay; a later explicit invocation can reconnect.
	err := c.rpc.Call(ctx, "tools/call", map[string]any{"name": name, "arguments": arguments}, &result)
	return result, err
}

func (c *Client) Close() error { return c.rpc.Close() }
