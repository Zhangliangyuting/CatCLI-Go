package tool

import (
	"context"
	"errors"
	"testing"
)

func TestToolRegistryPassesContextToHandler(t *testing.T) {
	registry := newApprovedTestRegistry()
	registry.RegisterTool(Definition{
		Type: "function",
		FunctionDefinition: FunctionDefinition{
			Name: "wait_for_cancel",
		},
	}, func(ctx context.Context, _ map[string]interface{}) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := registry.Execute(ctx, "wait_for_cancel", nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Execute() error = %v, want context.Canceled", err)
	}
}

func TestExecuteCommandUsesParentContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := ExecuteCommandHandler(ctx, map[string]interface{}{
		"command": "go test",
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ExecuteCommandHandler() error = %v, want context.Canceled", err)
	}
}

func TestToolRegistrySubsetRestrictsTools(t *testing.T) {
	registry := newApprovedTestRegistry()
	registry.RegisterTool(ReadFileDefinition(), ReadFileHandler)
	registry.RegisterTool(WriteFileDefinition(), WriteFileHandler)

	readOnly := registry.Subset("read_file", "list_dir")
	definitions := readOnly.ToolDefinitions()
	if len(definitions) != 1 || definitions[0].FunctionDefinition.Name != "read_file" {
		t.Fatalf("read-only definitions = %#v", definitions)
	}
	if _, err := readOnly.Execute(context.Background(), "write_file", nil); err == nil {
		t.Fatal("write_file remained available in read-only subset")
	}
}

func TestReadOnlyRecoveryClassificationUsesRegisteredImplementation(t *testing.T) {
	registry := newApprovedTestRegistry()
	registry.RegisterProvider(NewBuiltinProvider(nil))
	if !registry.IsReadOnly("read_file") || !registry.IsReadOnly("list_dir") {
		t.Fatal("built-in observations must be eligible for a fresh read")
	}
	for _, name := range []string{"write_file", "edit_file", "execute_command", "create_project", "missing"} {
		if registry.IsReadOnly(name) {
			t.Fatalf("%s was classified as a repeatable observation", name)
		}
	}
	custom := newApprovedTestRegistry()
	custom.RegisterTool(ReadFileDefinition(), ReadFileHandler)
	if custom.IsReadOnly("read_file") {
		t.Fatal("custom handler inherited built-in retry classification by name")
	}
}
