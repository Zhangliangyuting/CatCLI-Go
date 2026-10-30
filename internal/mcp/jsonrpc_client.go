package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

type rpcReply struct {
	message Message
	err     error
}

type JSONRPCClient struct {
	transport Transport
	timeout   time.Duration
	sequence  atomic.Uint64
	mu        sync.Mutex
	pending   map[string]chan rpcReply
	failed    error
	ctx       context.Context
	cancel    context.CancelFunc
}

func NewJSONRPCClient(transport Transport, timeout time.Duration) (*JSONRPCClient, error) {
	if transport == nil {
		return nil, fmt.Errorf("MCP transport is required")
	}
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	ctx, cancel := context.WithCancel(context.Background())
	client := &JSONRPCClient{transport: transport, timeout: timeout, pending: make(map[string]chan rpcReply), ctx: ctx, cancel: cancel}
	if err := transport.Start(client.receive, client.fail); err != nil {
		cancel()
		_ = transport.Close()
		return nil, err
	}
	return client, nil
}

func (c *JSONRPCClient) receive(message Message) {
	if message.Method != "" {
		if len(message.ID) != 0 {
			go func() {
				ctx, cancel := context.WithTimeout(c.ctx, c.timeout)
				defer cancel()
				reply := Message{JSONRPC: "2.0", ID: message.ID}
				if message.Method == "ping" {
					reply.Result = json.RawMessage(`{}`)
				} else {
					reply.Error = &RPCError{Code: -32601, Message: "client method not supported"}
				}
				_ = c.transport.Send(ctx, reply)
			}()
		}
		// No optional client capability is advertised. Tool-list changes are
		// picked up on restart, rather than mutating the active tool registry.
		return
	}
	c.mu.Lock()
	channel := c.pending[string(message.ID)]
	if channel != nil {
		delete(c.pending, string(message.ID))
		channel <- rpcReply{message: message}
	}
	c.mu.Unlock()
}

func (c *JSONRPCClient) fail(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failed != nil {
		return
	}
	c.failed = err
	for key, channel := range c.pending {
		channel <- rpcReply{err: err}
		delete(c.pending, key)
	}
}

func (c *JSONRPCClient) Call(ctx context.Context, method string, params any, result any) error {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	stop := context.AfterFunc(c.ctx, cancel)
	defer stop()
	if err := ctx.Err(); err != nil {
		return err
	}
	encoded, err := json.Marshal(params)
	if err != nil {
		return err
	}
	id := strconv.FormatUint(c.sequence.Add(1), 10)
	replies := make(chan rpcReply, 1)
	c.mu.Lock()
	if c.failed != nil {
		err := c.failed
		c.mu.Unlock()
		return err
	}
	c.pending[id] = replies
	c.mu.Unlock()
	defer func() { c.mu.Lock(); delete(c.pending, id); c.mu.Unlock() }()
	defer func() {
		if ctx.Err() != nil && method != "initialize" && c.ctx.Err() == nil {
			go func() {
				ctx, cancel := context.WithTimeout(c.ctx, time.Second)
				defer cancel()
				_ = c.Notify(ctx, "notifications/cancelled", map[string]any{"requestId": json.RawMessage(id), "reason": "request cancelled"})
			}()
		}
	}()
	message := Message{JSONRPC: "2.0", ID: json.RawMessage(id), Method: method, Params: encoded}
	if err := c.transport.Send(ctx, message); err != nil {
		return fmt.Errorf("MCP %s: %w", method, err)
	}
	select {
	case reply := <-replies:
		if reply.err != nil {
			return reply.err
		}
		if reply.message.Error != nil {
			return reply.message.Error
		}
		if result == nil {
			return nil
		}
		if err := json.Unmarshal(reply.message.Result, result); err != nil {
			return fmt.Errorf("decode MCP %s result: %w", method, err)
		}
		return nil
	case <-ctx.Done():
		return fmt.Errorf("MCP %s: %w", method, ctx.Err())
	}
}

func (c *JSONRPCClient) Notify(ctx context.Context, method string, params any) error {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	stop := context.AfterFunc(c.ctx, cancel)
	defer stop()
	data, err := json.Marshal(params)
	if err != nil {
		return err
	}
	return c.transport.Send(ctx, Message{JSONRPC: "2.0", Method: method, Params: data})
}

func (c *JSONRPCClient) Close() error { c.cancel(); c.fail(ErrClosed); return c.transport.Close() }
