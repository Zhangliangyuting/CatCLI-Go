package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

const maxMessageBytes = 16 << 20

var ErrClosed = errors.New("MCP connection closed")
var ErrSessionExpired = errors.New("MCP session expired; retry explicitly to start a new session")

type RPCError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *RPCError) Error() string { return fmt.Sprintf("JSON-RPC error %d: %s", e.Code, e.Message) }

type Message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

func decodeMessage(data []byte) (Message, error) {
	var message Message
	if err := json.Unmarshal(data, &message); err != nil {
		return message, fmt.Errorf("invalid MCP JSON message")
	}
	if message.JSONRPC != "2.0" {
		return message, fmt.Errorf("invalid JSON-RPC version")
	}
	if message.Method == "" && (len(message.ID) == 0 || (message.Error == nil && len(message.Result) == 0) || (message.Error != nil && len(message.Result) != 0)) {
		return message, fmt.Errorf("invalid JSON-RPC response")
	}
	return message, nil
}

// Start installs callbacks before any sends. Send may be called concurrently;
// receive callbacks must remain nonblocking. Close releases streams/processes.
type Transport interface {
	Start(receive func(Message), failed func(error)) error
	Send(context.Context, Message) error
	Close() error
}
