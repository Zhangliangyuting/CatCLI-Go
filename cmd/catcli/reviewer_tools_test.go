package main

import (
	"AgentCLI/internal/tool"
	"context"
	"reflect"
	"sort"
	"testing"
)

func TestSelectReviewerTools(t *testing.T) {
	for _, tc := range []struct {
		name            string
		available, want []string
	}{
		{"builtin fallback", []string{"list_dir", "read_file", "edit_file", "execute_command"}, []string{"list_dir", "read_file"}},
		{"MCP only", []string{"mcp__filesystem__list_directory", "mcp__filesystem__read_text_file", "mcp__filesystem__write_file", "mcp__shell__shell_execute"}, []string{"mcp__filesystem__list_directory", "mcp__filesystem__read_text_file"}},
		{"prefer MCP", []string{"list_dir", "read_file", "mcp__filesystem__list_directory", "mcp__filesystem__read_text_file"}, []string{"mcp__filesystem__list_directory", "mcp__filesystem__read_text_file"}},
		{"partial MCP", []string{"list_dir", "read_file", "mcp__filesystem__read_text_file", "mcp__other__read_text_file"}, []string{"list_dir", "mcp__filesystem__read_text_file"}},
		{"empty", nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			registry := tool.NewToolRegistry()
			for _, name := range tc.available {
				registry.RegisterTool(tool.Definition{FunctionDefinition: tool.FunctionDefinition{Name: name}}, func(context.Context, map[string]any) (string, error) { return "ok", nil })
			}
			selected := selectReviewerTools(registry)
			var got []string
			for _, definition := range selected.ToolDefinitions() {
				name := definition.FunctionDefinition.Name
				got = append(got, name)
				if _, err := selected.Execute(context.Background(), name, nil); err != nil {
					t.Fatal(err)
				}
			}
			sort.Strings(got)
			sort.Strings(tc.want)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}
