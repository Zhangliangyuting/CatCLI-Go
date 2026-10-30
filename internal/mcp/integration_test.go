package mcp

import (
	"AgentCLI/internal/tool"
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func initializePayload() any {
	return map[string]any{"protocolVersion": ProtocolVersion, "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]string{"name": "fixture", "version": "1"}}
}
func descriptor(name string) ToolDescriptor {
	return ToolDescriptor{Name: name, Description: "Test tool", InputSchema: map[string]any{"type": "object", "properties": map[string]any{}}}
}
func sendTestReply(w http.ResponseWriter, request Message, result any) {
	w.Header().Set("Content-Type", "application/json")
	data, _ := json.Marshal(result)
	_ = json.NewEncoder(w).Encode(Message{JSONRPC: "2.0", ID: request.ID, Result: data})
}

func TestStreamableHTTPHandshakePaginationSSEAndToolResults(t *testing.T) {
	var initialized atomic.Bool
	var deleted atomic.Bool
	pingReply := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test" {
			t.Error("missing configured header")
		}
		if r.Method == http.MethodDelete {
			if r.Header.Get("Mcp-Session-Id") != "test-session" {
				t.Error("missing delete session")
			}
			deleted.Store(true)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.Method != http.MethodPost {
			t.Errorf("method=%s", r.Method)
			w.WriteHeader(405)
			return
		}
		if r.Header.Get("Accept") != "application/json, text/event-stream" {
			t.Error("missing Accept header")
		}
		var request Message
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		if request.Method != "initialize" {
			if r.Header.Get("Mcp-Session-Id") != "test-session" || r.Header.Get("Mcp-Protocol-Version") != ProtocolVersion {
				t.Error("missing session or negotiated version")
			}
		}
		switch request.Method {
		case "initialize":
			var params struct {
				ProtocolVersion string         `json:"protocolVersion"`
				Capabilities    map[string]any `json:"capabilities"`
			}
			if err := json.Unmarshal(request.Params, &params); err != nil || params.ProtocolVersion != ProtocolVersion || len(params.Capabilities) != 0 {
				t.Errorf("initialize params=%s", request.Params)
			}
			w.Header().Set("Mcp-Session-Id", "test-session")
			sendTestReply(w, request, initializePayload())
		case "notifications/initialized":
			initialized.Store(true)
			w.WriteHeader(202)
		case "tools/list":
			if !initialized.Load() {
				t.Error("tools/list before initialized")
			}
			var params map[string]any
			_ = json.Unmarshal(request.Params, &params)
			if params["cursor"] == nil {
				sendTestReply(w, request, map[string]any{"tools": []ToolDescriptor{descriptor("echo")}, "nextCursor": "page2"})
				return
			}
			if params["cursor"] != "page2" {
				t.Error("wrong page cursor")
			}
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, ": heartbeat\n\nevent: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/tools/list_changed\"}\n\n")
			fmt.Fprint(w, "data: {\"jsonrpc\":\"2.0\",\"id\":\"server-ping\",\"method\":\"ping\"}\n\n")
			w.(http.Flusher).Flush()
			select {
			case <-pingReply:
			case <-time.After(time.Second):
				t.Error("server ping not answered")
			}
			result, _ := json.Marshal(map[string]any{"tools": []ToolDescriptor{descriptor("fail")}})
			reply, _ := json.Marshal(Message{JSONRPC: "2.0", ID: request.ID, Result: result})
			// SSE supports multi-line data fields.
			fmt.Fprintf(w, "event: message\ndata: %s\ndata: \n\n", reply)
		case "tools/call":
			var params struct {
				Name      string         `json:"name"`
				Arguments map[string]any `json:"arguments"`
			}
			_ = json.Unmarshal(request.Params, &params)
			sendTestReply(w, request, map[string]any{"content": []any{map[string]any{"type": "text", "text": "hello"}, map[string]any{"type": "image", "data": "aGVsbG8=", "mimeType": "image/png"}}, "structuredContent": map[string]any{"value": 42}, "isError": params.Name == "fail"})
		case "":
			if string(request.ID) != `"server-ping"` || string(request.Result) != "{}" {
				t.Errorf("bad server ping reply=%+v", request)
			}
			pingReply <- struct{}{}
			w.WriteHeader(202)
		default:
			t.Errorf("unexpected method %s", request.Method)
			w.WriteHeader(400)
		}
	}))
	defer server.Close()
	cfg := Config{Servers: map[string]ServerConfig{"remote": {Transport: "streamable_http", URL: server.URL, Headers: map[string]string{"Authorization": "Bearer test"}, Timeout: "2s"}}}
	manager := NewServerManager(cfg, nil, nil)
	defer manager.Close()
	statuses := manager.Start(context.Background())
	if len(statuses) != 1 || statuses[0].Err != nil || statuses[0].Tools != 2 {
		t.Fatalf("statuses=%+v", statuses)
	}
	registry := tool.NewToolRegistry()
	registry.RegisterProvider(manager)
	result, err := registry.Execute(context.Background(), "mcp__remote__echo", map[string]any{})
	if err != nil || !strings.Contains(result, `"type":"image"`) || !strings.Contains(result, `"value":42`) {
		t.Fatalf("result=%s err=%v", result, err)
	}
	result, err = registry.Execute(context.Background(), "mcp__remote__fail", nil)
	if err == nil || !strings.Contains(result, `"isError":true`) {
		t.Fatalf("tool failure not preserved: %s %v", result, err)
	}
	_ = manager.Close()
	if !deleted.Load() {
		t.Error("session not deleted")
	}
}

func TestHTTPSessionExpirationDoesNotReplayToolCall(t *testing.T) {
	var initializations, calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			w.WriteHeader(405)
			return
		}
		var request Message
		_ = json.NewDecoder(r.Body).Decode(&request)
		switch request.Method {
		case "initialize":
			initializations.Add(1)
			w.Header().Set("Mcp-Session-Id", fmt.Sprint(initializations.Load()))
			sendTestReply(w, request, initializePayload())
		case "notifications/initialized":
			w.WriteHeader(202)
		case "tools/call":
			if calls.Add(1) == 1 {
				w.WriteHeader(404)
				return
			}
			sendTestReply(w, request, map[string]any{"content": []any{}})
		default:
			w.WriteHeader(400)
		}
	}))
	defer server.Close()
	transport := NewStreamableHTTPTransport(ServerConfig{URL: server.URL}, nil)
	rpc, err := NewJSONRPCClient(transport, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	client := NewClient(rpc)
	defer client.Close()
	if _, err := client.CallTool(context.Background(), "write", nil); !errors.Is(err, ErrSessionExpired) {
		t.Fatalf("err=%v", err)
	}
	if calls.Load() != 1 || initializations.Load() != 1 {
		t.Fatal("call was replayed")
	}
	if _, err := client.CallTool(context.Background(), "write", nil); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 || initializations.Load() != 2 {
		t.Fatal("explicit retry did not reinitialize")
	}
}

func TestHTTPTimeoutCancelsWithoutReplay(t *testing.T) {
	var calls atomic.Int32
	cancelled := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request Message
		_ = json.NewDecoder(r.Body).Decode(&request)
		if request.Method == "notifications/cancelled" {
			cancelled <- struct{}{}
			w.WriteHeader(202)
			return
		}
		calls.Add(1)
		<-r.Context().Done()
	}))
	defer server.Close()
	rpc, _ := NewJSONRPCClient(NewStreamableHTTPTransport(ServerConfig{URL: server.URL}, nil), 30*time.Millisecond)
	defer rpc.Close()
	if err := rpc.Call(context.Background(), "tools/call", map[string]any{}, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v", err)
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("missing cancellation notification")
	}
	if calls.Load() != 1 {
		t.Fatal("request replayed")
	}
}

// A real child process fixture exercises newline framing, environment passing,
// initialization ordering and cleanup without requiring Node/Python or network.
func TestMCPStdioHelper(t *testing.T) {
	if os.Getenv("CATCLI_MCP_HELPER") != "1" {
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	initialized := false
	fmt.Fprintln(os.Stderr, "fixture started")
	for scanner.Scan() {
		var request Message
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
			os.Exit(2)
		}
		reply := Message{JSONRPC: "2.0", ID: request.ID}
		var result any
		switch request.Method {
		case "initialize":
			if os.Getenv("CATCLI_MCP_HANG") == "1" {
				continue
			}
			result = initializePayload()
		case "notifications/initialized":
			initialized = true
			continue
		case "notifications/cancelled":
			continue
		case "tools/list":
			if !initialized {
				os.Exit(3)
			}
			result = map[string]any{"tools": []ToolDescriptor{descriptor("echo"), descriptor("hidden")}}
		case "tools/call":
			var params map[string]any
			_ = json.Unmarshal(request.Params, &params)
			text, _ := json.Marshal(params["arguments"])
			result = map[string]any{"content": []any{map[string]any{"type": "text", "text": string(text)}}}
		default:
			os.Exit(4)
		}
		reply.Result, _ = json.Marshal(result)
		if err := encoder.Encode(reply); err != nil {
			os.Exit(5)
		}
	}
	os.Exit(0)
}

func helperConfig(t *testing.T) ServerConfig {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return ServerConfig{Transport: "stdio", Command: executable, Args: []string{"-test.run=^TestMCPStdioHelper$"}, Env: map[string]string{"CATCLI_MCP_HELPER": "1"}, Timeout: "2s", Tools: []string{"echo"}}
}

func TestManagerStdioFailureIsolationAndShutdown(t *testing.T) {
	local := helperConfig(t)
	hang := helperConfig(t)
	hang.Env["CATCLI_MCP_HANG"] = "1"
	hang.Timeout = "40ms"
	cfg := Config{Servers: map[string]ServerConfig{
		"a": local, "b": local, "broken": {Transport: "stdio", Command: "/nonexistent/catcli-mcp"}, "hang": hang, "disabled": {Disabled: true},
	}}
	var stderr bytes.Buffer
	manager := NewServerManager(cfg, &stderr, nil)
	defer manager.Close()
	statuses := manager.Start(context.Background())
	if len(statuses) != 4 || statuses[0].Err != nil || statuses[1].Err != nil || statuses[2].Err == nil || !errors.Is(statuses[3].Err, context.DeadlineExceeded) {
		t.Fatalf("statuses=%+v", statuses)
	}
	registry := tool.NewToolRegistry()
	registry.RegisterProvider(manager)
	var wait sync.WaitGroup
	for _, name := range []string{"mcp__a__echo", "mcp__b__echo"} {
		wait.Go(func() {
			result, err := registry.Execute(context.Background(), name, map[string]any{"message": "hi"})
			if err != nil || !strings.Contains(result, "hi") {
				t.Errorf("result=%s err=%v", result, err)
			}
		})
	}
	wait.Wait()
	if len(registry.ToolDefinitions()) != 2 {
		t.Fatal("tool allowlist not enforced")
	}
	_ = manager.Close()
	for _, server := range manager.servers {
		transport := server.Client.rpc.transport.(*StdioTransport)
		select {
		case <-transport.done:
		default:
			t.Error("subprocess was not reaped")
		}
	}
	if !strings.Contains(stderr.String(), "fixture started") {
		t.Fatal("stderr was not forwarded")
	}
}

func TestManagerStartsServersConcurrently(t *testing.T) {
	arrivals := make(chan struct{}, 2)
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request Message
		_ = json.NewDecoder(r.Body).Decode(&request)
		switch request.Method {
		case "initialize":
			arrivals <- struct{}{}
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
			sendTestReply(w, request, initializePayload())
		case "notifications/initialized":
			w.WriteHeader(202)
		case "tools/list":
			sendTestReply(w, request, map[string]any{"tools": []any{}})
		}
	}))
	defer server.Close()
	cfg := ServerConfig{Transport: "streamable_http", URL: server.URL, Timeout: "2s"}
	manager := NewServerManager(Config{Servers: map[string]ServerConfig{"a": cfg, "b": cfg}}, nil, nil)
	defer manager.Close()
	done := make(chan []ServerStatus, 1)
	go func() { done <- manager.Start(context.Background()) }()
	for range 2 {
		select {
		case <-arrivals:
		case <-time.After(time.Second):
			close(release)
			<-done
			t.Fatal("startup was serialized")
		}
	}
	close(release)
	for _, status := range <-done {
		if status.Err != nil {
			t.Fatal(status.Err)
		}
	}
}

func TestLateExpiredResponseDoesNotInvalidateNewSession(t *testing.T) {
	var initializations atomic.Int32
	slowStarted, releaseSlow := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			w.WriteHeader(204)
			return
		}
		var request Message
		_ = json.NewDecoder(r.Body).Decode(&request)
		switch request.Method {
		case "initialize":
			session := initializations.Add(1)
			w.Header().Set("Mcp-Session-Id", fmt.Sprint(session))
			sendTestReply(w, request, initializePayload())
		case "notifications/initialized":
			w.WriteHeader(202)
		case "tools/call":
			var params struct {
				Name string `json:"name"`
			}
			_ = json.Unmarshal(request.Params, &params)
			if params.Name == "slow" {
				close(slowStarted)
				select {
				case <-releaseSlow:
				case <-r.Context().Done():
					return
				}
			}
			if r.Header.Get("Mcp-Session-Id") == "1" {
				w.WriteHeader(404)
				return
			}
			sendTestReply(w, request, map[string]any{"content": []any{}})
		}
	}))
	defer server.Close()
	rpc, _ := NewJSONRPCClient(NewStreamableHTTPTransport(ServerConfig{URL: server.URL}, nil), 2*time.Second)
	client := NewClient(rpc)
	defer client.Close()
	if err := client.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := client.CallTool(context.Background(), "slow", nil); done <- err }()
	<-slowStarted
	if _, err := client.CallTool(context.Background(), "fast", nil); !errors.Is(err, ErrSessionExpired) {
		close(releaseSlow)
		t.Fatal(err)
	}
	if _, err := client.CallTool(context.Background(), "fast", nil); err != nil {
		close(releaseSlow)
		t.Fatal(err)
	}
	close(releaseSlow)
	if err := <-done; !errors.Is(err, ErrSessionExpired) {
		t.Fatal(err)
	}
	if _, err := client.CallTool(context.Background(), "fast", nil); err != nil {
		t.Fatal(err)
	}
	if initializations.Load() != 2 {
		t.Fatalf("late response reset a new session: initializations=%d", initializations.Load())
	}
}

func TestInvalidHandshakeAndRepeatedPaginationAreRejected(t *testing.T) {
	for _, scenario := range []string{"unsupported-version", "repeated-cursor", "missing-allowlisted-tool"} {
		t.Run(scenario, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request Message
				_ = json.NewDecoder(r.Body).Decode(&request)
				switch request.Method {
				case "initialize":
					result := initializePayload().(map[string]any)
					if scenario == "unsupported-version" {
						result["protocolVersion"] = "2099-01-01"
					}
					sendTestReply(w, request, result)
				case "notifications/initialized":
					w.WriteHeader(202)
				case "tools/list":
					cursor := ""
					if scenario == "repeated-cursor" {
						cursor = "same"
					}
					sendTestReply(w, request, map[string]any{"tools": []any{}, "nextCursor": cursor})
				}
			}))
			defer server.Close()
			cfg := ServerConfig{Transport: "streamable_http", URL: server.URL, Timeout: "1s"}
			if scenario == "missing-allowlisted-tool" {
				cfg.Tools = []string{"nonexistent"}
			}
			manager := NewServerManager(Config{Servers: map[string]ServerConfig{"test": cfg}}, nil, nil)
			defer manager.Close()
			statuses := manager.Start(context.Background())
			if len(statuses) != 1 || statuses[0].Err == nil || len(manager.Tools()) != 0 {
				t.Fatalf("accepted invalid server: %+v", statuses)
			}
		})
	}
}
