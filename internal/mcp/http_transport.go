package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"sync"
	"time"
)

// StreamableHTTPTransport handles JSON and POST SSE responses. Optional
// unsolicited GET streams and SSE replay are deliberately not enabled.
type StreamableHTTPTransport struct {
	config           ServerConfig
	http             *http.Client
	receive          func(Message)
	mu               sync.Mutex
	session, version string
	expired, closed  bool
	ctx              context.Context
	cancel           context.CancelFunc
}

func NewStreamableHTTPTransport(config ServerConfig, client *http.Client) *StreamableHTTPTransport {
	if client == nil {
		client = &http.Client{}
	}
	copyClient := *client
	// Redirecting an authenticated MCP request could leak configured headers.
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	ctx, cancel := context.WithCancel(context.Background())
	return &StreamableHTTPTransport{config: config, http: &copyClient, ctx: ctx, cancel: cancel}
}

func (t *StreamableHTTPTransport) Start(receive func(Message), _ func(error)) error {
	t.receive = receive
	return nil
}

func (t *StreamableHTTPTransport) SetProtocolVersion(version string) {
	t.mu.Lock()
	t.version = version
	t.mu.Unlock()
}
func (t *StreamableHTTPTransport) SessionExpired() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.expired
}

func (t *StreamableHTTPTransport) request(ctx context.Context, method string, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, t.config.URL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("invalid MCP HTTP request")
	}
	for key, value := range t.config.Headers {
		req.Header.Set(key, value)
	}
	req.Header.Set("Accept", "application/json, text/event-stream")
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
	}
	t.mu.Lock()
	session, version := t.session, t.version
	t.mu.Unlock()
	if session != "" {
		req.Header.Set("Mcp-Session-Id", session)
	}
	if version != "" {
		req.Header.Set("Mcp-Protocol-Version", version)
	}
	response, err := t.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		// Do not include URLs, headers or server bodies in diagnostics.
		return nil, fmt.Errorf("MCP HTTP transport failed")
	}
	return response, nil
}

func (t *StreamableHTTPTransport) Send(ctx context.Context, message Message) error {
	t.mu.Lock()
	closed := t.closed
	t.mu.Unlock()
	if closed {
		return ErrClosed
	}
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(t.ctx, cancel)
	defer stop()
	defer cancel()
	data, err := json.Marshal(message)
	if err != nil {
		return err
	}
	response, err := t.request(ctx, http.MethodPost, data)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound && message.Method != "initialize" {
		requestSession := response.Request.Header.Get("Mcp-Session-Id")
		t.mu.Lock()
		// A late response from an old session must not invalidate a new one.
		if requestSession != "" && t.session == requestSession {
			t.session, t.expired = "", true
		}
		t.mu.Unlock()
		if requestSession != "" {
			return ErrSessionExpired
		}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("MCP HTTP status %d", response.StatusCode)
	}
	if message.Method == "initialize" {
		session := response.Header.Get("Mcp-Session-Id")
		for _, char := range session {
			if char < 0x21 || char > 0x7e {
				return fmt.Errorf("invalid MCP session header")
			}
		}
		t.mu.Lock()
		t.session, t.expired = session, false
		t.mu.Unlock()
	}
	if len(message.ID) == 0 || message.Method == "" {
		if response.StatusCode != http.StatusAccepted {
			return fmt.Errorf("MCP notification/response requires HTTP 202")
		}
		return nil
	}
	contentType, _, _ := mime.ParseMediaType(response.Header.Get("Content-Type"))
	switch contentType {
	case "application/json":
		data, err := io.ReadAll(io.LimitReader(response.Body, maxMessageBytes+1))
		if err != nil {
			return err
		}
		if len(data) > maxMessageBytes {
			return fmt.Errorf("MCP response too large")
		}
		reply, err := decodeMessage(data)
		if err != nil {
			return err
		}
		if !bytes.Equal(reply.ID, message.ID) || reply.Method != "" {
			return fmt.Errorf("MCP HTTP response ID mismatch")
		}
		t.receive(reply)
		return nil
	case "text/event-stream":
		return t.readSSE(response.Body, message.ID)
	default:
		return fmt.Errorf("unsupported MCP HTTP response content type")
	}
}

func (t *StreamableHTTPTransport) readSSE(reader io.Reader, id json.RawMessage) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), maxMessageBytes)
	var data strings.Builder
	flush := func() (bool, error) {
		if data.Len() == 0 {
			return false, nil
		}
		payload := strings.TrimSuffix(data.String(), "\n")
		data.Reset()
		if payload == "" {
			return false, nil
		}
		message, err := decodeMessage([]byte(payload))
		if err != nil {
			return false, err
		}
		t.receive(message)
		return message.Method == "" && bytes.Equal(message.ID, id), nil
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			done, err := flush()
			if done || err != nil {
				return err
			}
		} else if line == "data" || strings.HasPrefix(line, "data:") {
			value := strings.TrimPrefix(line, "data")
			value = strings.TrimPrefix(value, ":")
			value = strings.TrimPrefix(value, " ")
			if data.Len()+len(value)+1 > maxMessageBytes {
				return fmt.Errorf("MCP SSE event too large")
			}
			data.WriteString(value)
			data.WriteByte('\n')
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read MCP SSE: %w", err)
	}
	return fmt.Errorf("MCP SSE disconnected before response; request was not replayed")
}

func (t *StreamableHTTPTransport) Close() error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil
	}
	t.closed = true
	hasSession := t.session != ""
	t.mu.Unlock()
	t.cancel()
	if !hasSession {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	response, err := t.request(ctx, http.MethodDelete, nil)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode >= 300 && response.StatusCode != http.StatusMethodNotAllowed && response.StatusCode != http.StatusNotFound {
		return fmt.Errorf("MCP session DELETE status %d", response.StatusCode)
	}
	return nil
}
