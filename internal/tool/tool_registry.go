package tool

import (
	"context"
	"fmt"
)

type Definition struct {
	Type               string             `json:"type"`
	FunctionDefinition FunctionDefinition `json:"function"`
}

type FunctionDefinition struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Parameters  map[string]interface{} `json:"parameters"`
}

type Handler func(
	ctx context.Context,
	args map[string]interface{},
) (string, error)

type Tool struct {
	Definition Definition
	Handler    Handler
}

type ToolRegistry struct {
	tools map[string]Tool
}

func NewToolRegistry() *ToolRegistry {
	return &ToolRegistry{
		tools: make(map[string]Tool),
	}
}

func (r *ToolRegistry) RegisterTool(definition Definition, handler Handler) {
	//检查空名字和重复注册
	if definition.FunctionDefinition.Name == "" {
		panic("tool name cannot be empty")
	}
	if _, exists := r.tools[definition.FunctionDefinition.Name]; exists {
		panic(fmt.Sprintf("tool already registered: %s", definition.FunctionDefinition.Name))
	}
	r.tools[definition.FunctionDefinition.Name] = Tool{
		Definition: definition,
		Handler:    handler,
	}
}

func (r *ToolRegistry) Execute(
	ctx context.Context,
	name string,
	args map[string]interface{},
) (string, error) {
	toolDef, exists := r.tools[name]
	if !exists {
		return "", fmt.Errorf("tool not found: %s", name)
	}

	return toolDef.Handler(ctx, args)
}

func (r *ToolRegistry) ToolDefinitions() []Definition {
	definitions := make([]Definition, 0, len(r.tools))
	for _, tool := range r.tools {
		definitions = append(definitions, tool.Definition)
	}

	return definitions
}

// Subset returns an independent registry containing only names that already
// exist in this registry. Handlers are shared, while registration remains
// isolated, making it suitable for role-specific least-privilege tool access.
func (r *ToolRegistry) Subset(names ...string) *ToolRegistry {
	subset := NewToolRegistry()
	if r == nil {
		return subset
	}
	for _, name := range names {
		if registered, exists := r.tools[name]; exists {
			subset.tools[name] = registered
		}
	}
	return subset
}

func (r *ToolRegistry) RegisterProvider(provider Provider) {
	for _, tool := range provider.Tools() {
		r.RegisterTool(tool.Definition, tool.Handler)
	}
}

func (r *ToolRegistry) RegisterEnabledTools(providers []Provider, enabled []string) error {
	enabledSet := make(map[string]bool)
	for _, name := range enabled {
		enabledSet[name] = true
	}

	registeredSet := make(map[string]bool)

	for _, provider := range providers {
		for _, t := range provider.Tools() {
			name := t.Definition.FunctionDefinition.Name

			if !enabledSet[name] {
				continue
			}

			r.RegisterTool(t.Definition, t.Handler)
			registeredSet[name] = true
		}
	}

	for _, name := range enabled {
		if !registeredSet[name] {
			return fmt.Errorf("enabled tool not found: %s", name)
		}
	}

	return nil
}
