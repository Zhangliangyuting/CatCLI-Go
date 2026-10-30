package mcp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigExpandsDecodedStringsAndResolvesDirectory(t *testing.T) {
	t.Setenv("MCP_TEST_TOKEN", "secret-with-\"quotes")
	directory := t.TempDir()
	path := filepath.Join(directory, "mcp.json")
	data := `{"mcpServers":{"local":{"transport":"stdio","command":"server","args":["${MCP_TEST_TOKEN}"],"env":{"KEY":"${MCP_TEST_TOKEN}"},"cwd":".."},"remote":{"transport":"streamable_http","url":"http://localhost/mcp","headers":{"Authorization":"Bearer ${MCP_TEST_TOKEN}"}}}}`
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Servers["local"].Args[0] != `secret-with-"quotes` || cfg.Servers["local"].Directory != filepath.Dir(directory) {
		t.Fatalf("expansion or directory resolution failed")
	}
	if cfg.Servers["remote"].Headers["Authorization"] != `Bearer secret-with-"quotes` {
		t.Fatal("header expansion failed")
	}
}

func TestConfigRejectsMissingVariablesAndInvalidSettings(t *testing.T) {
	for _, data := range []string{
		`{"mcpServers":{"a":{"transport":"stdio","command":"${CATCLI_NONEXISTENT_MCP_TEST_VARIABLE}"}}}`,
		`{"mcpServers":{"a":{"transport":"stdio","command":"server","timeout":"0s"}}}`,
		`{"mcpServers":{"a":{"transport":"streamable_http","url":"file:///tmp/mcp"}}}`,
		`{"mcpServers":{"a":{"transport":"streamable_http","url":"http://localhost/mcp","headers":{"Mcp-Session-Id":"override"}}}}`,
		`{"mcpServers":{"a":{"transport":"stdio","command":"server","unknown":true}}}`,
		`{"mcpServers":{}} {}`,
		`{}`,
	} {
		path := filepath.Join(t.TempDir(), "mcp.json")
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadConfig(path); err == nil {
			t.Errorf("accepted invalid config %s", data)
		}
	}
}

func TestToolNamesAreStableBoundedAndUnambiguous(t *testing.T) {
	if got := ToolName("local", "search"); got != "mcp__local__search" {
		t.Fatal(got)
	}
	seen := map[string]bool{}
	for _, pair := range [][2]string{{"a__b", "c"}, {"a", "b__c"}, {"a", "b/c"}, {"a", "b?c"}, {"a", strings.Repeat("long", 40)}} {
		name := ToolName(pair[0], pair[1])
		if len(name) > 64 || unsafeToolCharacters.MatchString(name) || seen[name] || name != ToolName(pair[0], pair[1]) {
			t.Fatalf("bad name %s", name)
		}
		seen[name] = true
	}
}
