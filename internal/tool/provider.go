package tool

import (
	"AgentCLI/internal/workspace"
	"context"
	"fmt"
	"path/filepath"
)

type Provider interface {
	Tools() []Tool
}

func NewProvider(name string, workspace *workspace.Workspace) (Provider, error) {
	switch name {
	case "builtin":
		return NewBuiltinProvider(workspace), nil
	default:
		return nil, fmt.Errorf("unknown provider: %s", name)
	}
}

func Providers(names []string, workspace *workspace.Workspace) ([]Provider, error) {
	providers := make([]Provider, 0, len(names))
	for _, name := range names {
		provider, err := NewProvider(name, workspace)
		if err != nil {
			return nil, err
		}
		providers = append(providers, provider)
	}
	return providers, nil
}

type BuiltinProvider struct {
	workspace *workspace.Workspace
}

func NewBuiltinProvider(workspace *workspace.Workspace) BuiltinProvider {
	return BuiltinProvider{workspace: workspace}
}

func (p BuiltinProvider) Tools() []Tool {
	return []Tool{
		{
			Definition: ListDirDefinition(),
			Handler:    p.pathHandler("path", ListDirHandler),
		},
		{
			Definition: ReadFileDefinition(),
			Handler:    p.pathHandler("path", ReadFileHandler),
		},
		{
			Definition: EditFileDefinition(),
			Handler:    p.pathHandler("file_path", EditFileHandler),
		},
		{
			Definition: WriteFileDefinition(),
			Handler:    p.pathHandler("file_path", WriteFileHandler),
		},
		{
			Definition: ExecuteCommandDefinition(),
			Handler:    p.commandHandler(),
		},
		{
			Definition: CreateProjectDefinition(),
			Handler:    p.createProjectHandler(),
		},
	}
}

func (p BuiltinProvider) pathHandler(argumentName string, handler Handler) Handler {
	return func(ctx context.Context, arguments map[string]interface{}) (string, error) {
		if p.workspace == nil {
			return "", fmt.Errorf("builtin tool workspace is nil")
		}
		path, ok := arguments[argumentName].(string)
		if !ok || path == "" {
			return handler(ctx, arguments)
		}
		resolved, err := p.workspace.Resolve(path)
		if err != nil {
			return "", err
		}
		bound := cloneArguments(arguments)
		bound[argumentName] = resolved
		return handler(ctx, bound)
	}
}

func (p BuiltinProvider) commandHandler() Handler {
	return func(ctx context.Context, arguments map[string]interface{}) (string, error) {
		if p.workspace == nil {
			return "", fmt.Errorf("builtin tool workspace is nil")
		}
		return executeCommandHandler(ctx, arguments, p.workspace.Root())
	}
}

func (p BuiltinProvider) createProjectHandler() Handler {
	return func(ctx context.Context, arguments map[string]interface{}) (string, error) {
		if p.workspace == nil {
			return "", fmt.Errorf("builtin tool workspace is nil")
		}
		root, ok := arguments["root"].(string)
		if !ok || root == "" {
			return CreateProjectHandler(ctx, arguments)
		}
		resolvedRoot, err := p.workspace.Resolve(root)
		if err != nil {
			return "", err
		}
		if files, ok := arguments["files"].([]interface{}); ok {
			for _, rawFile := range files {
				file, ok := rawFile.(map[string]interface{})
				if !ok {
					continue
				}
				relativePath, ok := file["path"].(string)
				if !ok || relativePath == "" || filepath.IsAbs(relativePath) {
					continue
				}
				if _, err := p.workspace.Resolve(filepath.Join(resolvedRoot, relativePath)); err != nil {
					return "", err
				}
			}
		}
		bound := cloneArguments(arguments)
		bound["root"] = resolvedRoot
		return CreateProjectHandler(ctx, bound)
	}
}

func cloneArguments(arguments map[string]interface{}) map[string]interface{} {
	cloned := make(map[string]interface{}, len(arguments))
	for key, value := range arguments {
		cloned[key] = value
	}
	return cloned
}
