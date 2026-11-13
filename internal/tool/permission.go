package tool

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

type PermissionAction string

const (
	Allow PermissionAction = "allow"
	Ask   PermissionAction = "ask"
	Deny  PermissionAction = "deny"
)

type PermissionRequest struct {
	Tool      string
	Operation string
	Arguments map[string]any
	Reason    string
}
type PermissionConfirmer func(context.Context, PermissionRequest) (bool, error)

var ErrPermissionDenied = errors.New("tool permission denied")

type permissionGate struct {
	mu      sync.RWMutex
	confirm PermissionConfirmer
	prompt  chan struct{}
}

// SetPermissionConfirmer also configures existing subsets of this registry.
func (r *ToolRegistry) SetPermissionConfirmer(confirm PermissionConfirmer) {
	r.permissions.mu.Lock()
	defer r.permissions.mu.Unlock()
	r.permissions.confirm = confirm
}

func permissionDecision(t Tool) PermissionAction {
	action := t.Permission
	if action == "" {
		action = Ask
	}
	return action
}

func (r *ToolRegistry) authorize(ctx context.Context, t Tool, args map[string]any, operation string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	action := permissionDecision(t)
	reason := "tool requires confirmation"
	if t.Permission == "" {
		reason = "tool has no explicit permission; confirmation is required"
	}
	switch action {
	case Allow:
		return ctx.Err()
	case Ask:
	default:
		return fmt.Errorf("%w: tool is denied by its permission setting", ErrPermissionDenied)
	}
	select {
	case r.permissions.prompt <- struct{}{}:
		defer func() { <-r.permissions.prompt }()
	case <-ctx.Done():
		return ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	r.permissions.mu.RLock()
	confirm := r.permissions.confirm
	r.permissions.mu.RUnlock()
	if confirm == nil {
		return fmt.Errorf("%w: no confirmation provider", ErrPermissionDenied)
	}
	approved, err := confirm(ctx, PermissionRequest{t.Definition.FunctionDefinition.Name, operation, copyPermissionArguments(args), reason})
	if err != nil {
		return fmt.Errorf("%w: %w", ErrPermissionDenied, err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !approved {
		return fmt.Errorf("%w: user declined", ErrPermissionDenied)
	}
	return ctx.Err()
}

func copyPermissionArguments(args map[string]any) map[string]any {
	result := make(map[string]any, len(args))
	for key, value := range args {
		result[key] = copyPermissionValue(value)
	}
	return result
}
func copyPermissionValue(value any) any {
	switch v := value.(type) {
	case map[string]any:
		return copyPermissionArguments(v)
	case []any:
		result := make([]any, len(v))
		for i := range v {
			result[i] = copyPermissionValue(v[i])
		}
		return result
	default:
		return value
	}
}

// AuthorizedCall binds one approval to an immutable argument snapshot. It has
// no exported fields, so callers cannot substitute a tool or new parameters.
type AuthorizedCall struct {
	tool Tool
	args map[string]any
	used bool
	mu   sync.Mutex
}

func (r *ToolRegistry) Authorize(ctx context.Context, name string, args map[string]any) (*AuthorizedCall, error) {
	t, ok := r.tools[name]
	if !ok {
		return nil, fmt.Errorf("tool not found: %s", name)
	}
	args = copyPermissionArguments(args)
	if err := r.authorize(ctx, t, args, "execute"); err != nil {
		return nil, err
	}
	return &AuthorizedCall{tool: t, args: args}, nil
}
func (c *AuthorizedCall) PrepareVerification(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if c == nil || c.tool.Handler == nil {
		return "", ErrPermissionDenied
	}
	if c.tool.PrepareVerification == nil {
		return "", nil
	}
	return c.tool.PrepareVerification(ctx, copyPermissionArguments(c.args))
}
func (c *AuthorizedCall) Execute(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if c == nil || c.tool.Handler == nil {
		return "", ErrPermissionDenied
	}
	c.mu.Lock()
	if c.used {
		c.mu.Unlock()
		return "", fmt.Errorf("%w: approval already consumed", ErrPermissionDenied)
	}
	c.used = true
	c.mu.Unlock()
	return c.tool.Handler(ctx, copyPermissionArguments(c.args))
}
