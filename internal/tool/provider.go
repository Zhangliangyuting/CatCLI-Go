package tool

import "fmt"

type Provider interface {
	Tools() []Tool
}

func NewProvider(name string) (Provider, error) {
	switch name {
	case "builtin":
		return NewBuiltinProvider(), nil
	default:
		return nil, fmt.Errorf("unknown provider: %s", name)
	}
}

func Providers(names []string) ([]Provider, error) {
	providers := make([]Provider, 0, len(names))
	for _, name := range names {
		provider, err := NewProvider(name)
		if err != nil {
			return nil, err
		}
		providers = append(providers, provider)
	}
	return providers, nil
}

type BuiltinProvider struct{}

func NewBuiltinProvider() BuiltinProvider {
	return BuiltinProvider{}
}

func (p BuiltinProvider) Tools() []Tool {
	return []Tool{
		{
			Definition: ListDirDefinition(),
			Handler:    ListDirHandler,
		},
		{
			Definition: ReadFileDefinition(),
			Handler:    ReadFileHandler,
		},
		{
			Definition: EditFileDefinition(),
			Handler:    EditFileHandler,
		},
		{
			Definition: WriteFileDefinition(),
			Handler:    WriteFileHandler,
		},
		{
			Definition: ExecuteCommandDefinition(),
			Handler:    ExecuteCommandHandler,
		},
		{
			Definition: CreateProjectDefinition(),
			Handler:    CreateProjectHandler,
		},
	}
}
