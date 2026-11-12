package mcp

import (
	"AgentCLI/internal/tool"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"sync"
)

type Server struct {
	Name   string
	Client *Client
	Tools  []tool.Tool
}

type ServerStatus struct {
	Name  string
	Tools int
	Err   error
}

// ServerManager owns the connections and immutable discovered tool snapshot.
// Start finishes registration before any agent requests may execute.
type ServerManager struct {
	config  Config
	stderr  io.Writer
	http    *http.Client
	servers []*Server
	tools   []tool.Tool
	started bool
}

func NewServerManager(config Config, stderr io.Writer, httpClient *http.Client) *ServerManager {
	return &ServerManager{config: config, stderr: stderr, http: httpClient}
}

func (m *ServerManager) Start(ctx context.Context) []ServerStatus {
	if m.started {
		return []ServerStatus{{Err: fmt.Errorf("MCP manager already started")}}
	}
	m.started = true
	var names []string
	for name, cfg := range m.config.Servers {
		if !cfg.Disabled {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	statuses := make([]ServerStatus, len(names))
	servers := make([]*Server, len(names))
	var wait sync.WaitGroup
	semaphore := make(chan struct{}, 4)
	// Third-party stderr writes can occur concurrently across subprocesses.
	stderr := &lockedWriter{writer: m.stderr}
	for index, name := range names {
		wait.Go(func() {
			statuses[index].Name = name
			select {
			case semaphore <- struct{}{}:
			case <-ctx.Done():
				statuses[index].Err = ctx.Err()
				return
			}
			defer func() { <-semaphore }()
			cfg := m.config.Servers[name]
			if err := cfg.validate(name); err != nil {
				statuses[index].Err = err
				return
			}
			startupCtx, cancel := context.WithTimeout(ctx, cfg.requestTimeout())
			defer cancel()
			var transport Transport
			if cfg.Transport == "stdio" {
				transport = NewStdioTransport(cfg, stderr)
			} else {
				transport = NewStreamableHTTPTransport(cfg, m.http)
			}
			rpc, err := NewJSONRPCClient(transport, cfg.requestTimeout())
			if err != nil {
				statuses[index].Err = err
				return
			}
			client := NewClient(rpc)
			descriptors, err := client.ListTools(startupCtx)
			if err != nil {
				_ = client.Close()
				statuses[index].Err = err
				return
			}
			allowed := map[string]bool{}
			for _, name := range cfg.Tools {
				allowed[name] = true
			}
			server := &Server{Name: name, Client: client}
			for _, descriptor := range descriptors {
				if cfg.Tools == nil || allowed[descriptor.Name] {
					server.Tools = append(server.Tools, adaptTool(name, descriptor, client))
					delete(allowed, descriptor.Name)
				}
			}
			if len(allowed) > 0 {
				_ = client.Close()
				statuses[index].Err = fmt.Errorf("configured MCP tools not found on server")
				return
			}
			servers[index] = server
			statuses[index].Tools = len(server.Tools)
		})
	}
	wait.Wait()
	seen := map[string]bool{}
	for index, server := range servers {
		if server == nil {
			continue
		}
		collision := false
		localNames := map[string]bool{}
		for _, entry := range server.Tools {
			name := entry.Definition.FunctionDefinition.Name
			if seen[name] || localNames[name] {
				collision = true
			}
			localNames[name] = true
		}
		if collision {
			_ = server.Client.Close()
			statuses[index].Err = fmt.Errorf("MCP tool namespace collision")
			statuses[index].Tools = 0
			continue
		}
		for _, entry := range server.Tools {
			seen[entry.Definition.FunctionDefinition.Name] = true
		}
		m.servers = append(m.servers, server)
		m.tools = append(m.tools, server.Tools...)
	}
	return statuses
}

func (m *ServerManager) Tools() []tool.Tool { return append([]tool.Tool(nil), m.tools...) }

func (m *ServerManager) Close() error {
	var wait sync.WaitGroup
	errorsByServer := make([]error, len(m.servers))
	for index, server := range m.servers {
		wait.Go(func() { errorsByServer[index] = server.Client.Close() })
	}
	wait.Wait()
	return errors.Join(errorsByServer...)
}

type lockedWriter struct {
	mu     sync.Mutex
	writer io.Writer
}

func (w *lockedWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.writer == nil {
		return len(data), nil
	}
	return w.writer.Write(data)
}
