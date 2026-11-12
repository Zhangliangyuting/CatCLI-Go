package mcp

import "testing"

func TestMCPRecoveryReadOnlyClassification(t *testing.T) {
	for _, tc := range []struct {
		server, name string
		readOnly     bool
	}{
		{"filesystem", "read_text_file", false},
		{"filesystem", "list_directory", false},
		{"filesystem", "get_file_info", false},
		{"brave", "brave_web_search", true},
		{"fetch", "fetch", true},
		{"filesystem", "write_file", false},
		{"filesystem", "create_directory", false},
		{"filesystem", "edit_file", false},
		{"custom", "read_text_file", false},
	} {
		adapted := adaptTool(tc.server, ToolDescriptor{Name: tc.name}, nil)
		if adapted.ReadOnly != tc.readOnly {
			t.Errorf("%s/%s ReadOnly = %v, want %v", tc.server, tc.name, adapted.ReadOnly, tc.readOnly)
		}
	}
}
