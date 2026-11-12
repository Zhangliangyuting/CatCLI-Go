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
	// ReadOnly permits a fresh observation after recovery. It does not make a
	// previous result reusable, because the external state may have changed.
	ReadOnly bool
	// Verify checks the current postcondition without executing the tool.
	Verify func(context.Context, map[string]any, string) (*Verification, error)
	// PrepareVerification captures evidence before the start event, never executes.
	PrepareVerification func(context.Context, map[string]any) (string, error)
}

// IsReadOnly reports an explicit property of the registered implementation.
// An unregistered or custom tool is never assumed to be safe to repeat.
func (r *ToolRegistry) IsReadOnly(name string) bool {
	if r == nil {
		return false
	}
	t, ok := r.tools[name]
	return ok && t.ReadOnly
}

// Verification confirms a current postcondition or requests a fresh attempt.
// Retry does not mean that repeating a mutation is safe. Nil is inconclusive.
type Verification struct {
	Evidence string
	Result   string
	Retry    bool // Requires a new observation/attempt instead of result reuse.
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
	r.register(Tool{Definition: definition, Handler: handler})
}

func (r *ToolRegistry) register(t Tool) {
	definition := t.Definition
	//检查空名字和重复注册
	if definition.FunctionDefinition.Name == "" {
		panic("tool name cannot be empty")
	}
	if _, exists := r.tools[definition.FunctionDefinition.Name]; exists {
		panic(fmt.Sprintf("tool already registered: %s", definition.FunctionDefinition.Name))
	}
	r.tools[definition.FunctionDefinition.Name] = t
}

// Verify uses only the verifier attached to the registered tool. Names alone
// never grant built-in verification to MCP or custom handlers.
func (r *ToolRegistry) Verify(ctx context.Context, name string, args map[string]any, data string) (*Verification, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if r == nil {
		return nil, nil
	}
	t, ok := r.tools[name]
	if !ok || t.Verify == nil {
		return nil, nil
	}
	return t.Verify(ctx, args, data)
}

func (r *ToolRegistry) PrepareVerification(ctx context.Context, name string, args map[string]any) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if r == nil {
		return "", nil
	}
	t, ok := r.tools[name]
	if !ok || t.PrepareVerification == nil {
		return "", nil
	}
	return t.PrepareVerification(ctx, args)
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
		r.register(tool)
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

			r.register(t)
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
