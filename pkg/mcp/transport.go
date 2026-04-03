package mcp

import (
	"context"
)

// Transport abstracts the underlying communication mechanism for MCP.
// Two implementations are provided:
//   - StdioTransport:          for local subprocess MCP servers
//   - StreamableHTTPTransport: for remote HTTP MCP servers (current standard)
type Transport interface {
	// Start initializes the transport connection.
	Start(ctx context.Context) error

	// Send sends a JSON-RPC message (request or notification) to the server.
	Send(ctx context.Context, data []byte) error

	// Receive returns a channel that delivers incoming JSON-RPC messages.
	// The channel is closed when the transport is closed or errors.
	Receive() <-chan []byte

	// Close terminates the transport.
	Close() error
}
