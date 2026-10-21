package tool

import (
	"AgentCLI/internal/workspace"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestBuiltinProviderBindsFileToolsToWorkspace(t *testing.T) {
	root := t.TempDir()
	currentWorkspace, err := workspace.New(root)
	if err != nil {
		t.Fatalf("workspace.New() error = %v", err)
	}
	registry := NewToolRegistry()
	if err := registry.RegisterEnabledTools(
		[]Provider{NewBuiltinProvider(currentWorkspace)},
		[]string{"read_file", "write_file", "list_dir"},
	); err != nil {
		t.Fatalf("RegisterEnabledTools() error = %v", err)
	}

	if err := os.WriteFile(filepath.Join(root, "inside.txt"), []byte("inside"), 0644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	result, err := registry.Execute(context.Background(), "read_file", map[string]interface{}{"path": "inside.txt"})
	if err != nil || result != "inside" {
		t.Fatalf("read_file result/error = %q/%v", result, err)
	}

	if _, err := registry.Execute(context.Background(), "write_file", map[string]interface{}{
		"file_path": "generated.txt",
		"content":   "generated",
	}); err != nil {
		t.Fatalf("write_file error = %v", err)
	}
	if content, err := os.ReadFile(filepath.Join(root, "generated.txt")); err != nil || string(content) != "generated" {
		t.Fatalf("generated file content/error = %q/%v", content, err)
	}

	outside := filepath.Join(t.TempDir(), "outside.txt")
	if _, err := registry.Execute(context.Background(), "read_file", map[string]interface{}{"path": outside}); err == nil {
		t.Fatal("read_file outside workspace error = nil")
	}
	if _, err := registry.Execute(context.Background(), "write_file", map[string]interface{}{
		"file_path": outside,
		"content":   "outside",
	}); err == nil {
		t.Fatal("write_file outside workspace error = nil")
	}
}

func TestBuiltinProviderRunsCommandsFromWorkspaceRoot(t *testing.T) {
	root := t.TempDir()
	currentWorkspace, err := workspace.New(root)
	if err != nil {
		t.Fatalf("workspace.New() error = %v", err)
	}
	registry := NewToolRegistry()
	if err := registry.RegisterEnabledTools(
		[]Provider{NewBuiltinProvider(currentWorkspace)},
		[]string{"execute_command"},
	); err != nil {
		t.Fatalf("RegisterEnabledTools() error = %v", err)
	}

	result, err := registry.Execute(context.Background(), "execute_command", map[string]interface{}{"command": "pwd"})
	if err != nil {
		t.Fatalf("execute_command error = %v", err)
	}
	if strings.TrimSpace(result) != root {
		t.Fatalf("pwd = %q, want %q", strings.TrimSpace(result), root)
	}
}

func TestBuiltinProviderRejectsCreateProjectSymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation commonly requires elevated Windows privileges")
	}
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "outside-link")); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}
	currentWorkspace, err := workspace.New(root)
	if err != nil {
		t.Fatalf("workspace.New() error = %v", err)
	}
	registry := NewToolRegistry()
	if err := registry.RegisterEnabledTools(
		[]Provider{NewBuiltinProvider(currentWorkspace)},
		[]string{"create_project"},
	); err != nil {
		t.Fatalf("RegisterEnabledTools() error = %v", err)
	}

	_, err = registry.Execute(context.Background(), "create_project", map[string]interface{}{
		"root": ".",
		"files": []interface{}{map[string]interface{}{
			"path":    filepath.Join("outside-link", "escaped.txt"),
			"content": "escaped",
		}},
	})
	if err == nil {
		t.Fatal("create_project symlink escape error = nil")
	}
	if _, err := os.Stat(filepath.Join(outside, "escaped.txt")); !os.IsNotExist(err) {
		t.Fatalf("escaped file exists or stat failed unexpectedly: %v", err)
	}
}
