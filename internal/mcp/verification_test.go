package mcp

import (
	"AgentCLI/internal/tool"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestFilesystemMCPWriteVerification(t *testing.T) {
	var content = "expected\n"
	var readCalls, writeCalls int
	var readError, malformed bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		var request Message
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		switch request.Method {
		case "initialize":
			sendTestReply(w, request, initializePayload())
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			sendTestReply(w, request, map[string]any{"tools": []ToolDescriptor{descriptor("read_text_file"), descriptor("write_file")}})
		case "tools/call":
			var params struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			}
			if err := json.Unmarshal(request.Params, &params); err != nil {
				t.Error(err)
				return
			}
			if params.Name != "read_text_file" {
				writeCalls++
				t.Errorf("verification dispatched %q", params.Name)
			}
			if len(params.Arguments) != 1 || params.Arguments["path"] != "note.txt" {
				t.Errorf("unexpected read arguments: %#v", params.Arguments)
			}
			readCalls++
			if malformed {
				sendTestReply(w, request, map[string]any{"content": []any{map[string]any{"type": "image", "data": "x"}}})
			} else {
				sendTestReply(w, request, map[string]any{"content": []any{map[string]any{"type": "text", "text": content}}, "structuredContent": map[string]any{"content": content}, "isError": readError})
			}
		default:
			t.Errorf("unexpected MCP method %q", request.Method)
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer server.Close()
	manager := NewServerManager(Config{Servers: map[string]ServerConfig{"filesystem": {
		Transport: "streamable_http", URL: server.URL, Tools: []string{"read_text_file", "write_file"},
	}}}, nil, nil)
	defer manager.Close()
	if statuses := manager.Start(t.Context()); len(statuses) != 1 || statuses[0].Err != nil {
		t.Fatalf("start MCP manager: %+v", statuses)
	}
	registry := tool.NewToolRegistry()
	registry.RegisterProvider(manager)
	args := map[string]any{"path": "note.txt", "content": "expected\n"}
	check, err := registry.Verify(t.Context(), "mcp__filesystem__write_file", args, "")
	if err != nil || check == nil || check.Retry || check.Result == "" {
		t.Fatalf("matching write: check=%+v err=%v", check, err)
	}
	content = "different"
	check, err = registry.Verify(t.Context(), "mcp__filesystem__write_file", args, "")
	if err != nil || check == nil || !check.Retry {
		t.Fatalf("mismatching write: check=%+v err=%v", check, err)
	}
	readError = true
	check, err = registry.Verify(t.Context(), "mcp__filesystem__write_file", args, "")
	if err != nil || check == nil || !check.Retry {
		t.Fatalf("unreadable write: check=%+v err=%v", check, err)
	}
	readError, malformed = false, true
	if check, err = registry.Verify(t.Context(), "mcp__filesystem__write_file", args, ""); err == nil || check != nil {
		t.Fatalf("malformed read: check=%+v err=%v", check, err)
	}
	if readCalls != 4 || writeCalls != 0 {
		t.Fatalf("read calls=%d write calls=%d", readCalls, writeCalls)
	}
	if check, err = registry.Verify(t.Context(), "mcp__filesystem__read_text_file", map[string]any{"path": "note.txt"}, ""); err != nil || check != nil {
		t.Fatalf("read tool unexpectedly verifies: check=%+v err=%v", check, err)
	}
}

func TestFilesystemMCPVerificationNeedsEnabledReadTools(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		var request Message
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		switch request.Method {
		case "initialize":
			sendTestReply(w, request, initializePayload())
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			sendTestReply(w, request, map[string]any{"tools": []ToolDescriptor{descriptor("read_text_file"), descriptor("write_file"), descriptor("get_file_info"), descriptor("create_directory")}})
		default:
			t.Errorf("unexpected method %q", request.Method)
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer server.Close()
	manager := NewServerManager(Config{Servers: map[string]ServerConfig{"filesystem": {
		Transport: "streamable_http", URL: server.URL, Tools: []string{"write_file", "create_directory"},
	}}}, nil, nil)
	defer manager.Close()
	if statuses := manager.Start(t.Context()); len(statuses) != 1 || statuses[0].Err != nil {
		t.Fatalf("start MCP manager: %+v", statuses)
	}
	registry := tool.NewToolRegistry()
	registry.RegisterProvider(manager)
	check, err := registry.Verify(t.Context(), "mcp__filesystem__write_file", map[string]any{"path": "note.txt", "content": "expected"}, "")
	if err != nil || check != nil {
		t.Fatalf("write verified without enabled read tool: check=%+v err=%v", check, err)
	}
	check, err = registry.Verify(t.Context(), "mcp__filesystem__create_directory", map[string]any{"path": "output"}, "")
	if err != nil || check != nil {
		t.Fatalf("directory verified without enabled info tool: check=%+v err=%v", check, err)
	}
}

func TestFilesystemMCPDirectoryVerification(t *testing.T) {
	info := "isDirectory: true\nisFile: false\nsize: 0"
	readError := false
	var infoCalls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		var request Message
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		switch request.Method {
		case "initialize":
			sendTestReply(w, request, initializePayload())
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			sendTestReply(w, request, map[string]any{"tools": []ToolDescriptor{descriptor("get_file_info"), descriptor("create_directory"), descriptor("edit_file")}})
		case "tools/call":
			var params struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			}
			if err := json.Unmarshal(request.Params, &params); err != nil {
				t.Error(err)
				return
			}
			if params.Name != "get_file_info" || len(params.Arguments) != 1 || params.Arguments["path"] != "output" {
				t.Errorf("unexpected verification call: %+v", params)
			}
			infoCalls++
			sendTestReply(w, request, map[string]any{"content": []any{map[string]any{"type": "text", "text": info}}, "structuredContent": map[string]any{"content": info}, "isError": readError})
		default:
			t.Errorf("unexpected MCP method %q", request.Method)
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer server.Close()
	manager := NewServerManager(Config{Servers: map[string]ServerConfig{"filesystem": {
		Transport: "streamable_http", URL: server.URL, Tools: []string{"get_file_info", "create_directory", "edit_file"},
	}}}, nil, nil)
	defer manager.Close()
	if statuses := manager.Start(t.Context()); len(statuses) != 1 || statuses[0].Err != nil {
		t.Fatalf("start MCP manager: %+v", statuses)
	}
	registry := tool.NewToolRegistry()
	registry.RegisterProvider(manager)
	args := map[string]any{"path": "output"}
	check, err := registry.Verify(t.Context(), "mcp__filesystem__create_directory", args, "")
	if err != nil || check == nil || check.Retry || check.Result == "" {
		t.Fatalf("existing directory: check=%+v err=%v", check, err)
	}
	info = "isDirectory: false\nisFile: true"
	check, err = registry.Verify(t.Context(), "mcp__filesystem__create_directory", args, "")
	if err != nil || check == nil || !check.Retry {
		t.Fatalf("file instead of directory: check=%+v err=%v", check, err)
	}
	readError = true
	check, err = registry.Verify(t.Context(), "mcp__filesystem__create_directory", args, "")
	if err != nil || check == nil || !check.Retry {
		t.Fatalf("missing directory: check=%+v err=%v", check, err)
	}
	readError, info = false, "size: 0"
	if check, err = registry.Verify(t.Context(), "mcp__filesystem__create_directory", args, ""); err == nil || check != nil {
		t.Fatalf("missing type: check=%+v err=%v", check, err)
	}
	if check, err = registry.Verify(t.Context(), "mcp__filesystem__edit_file", args, ""); err != nil || check != nil {
		t.Fatalf("edit_file unexpectedly verifies: check=%+v err=%v", check, err)
	}
	if infoCalls != 4 {
		t.Fatalf("get_file_info calls=%d", infoCalls)
	}
}
