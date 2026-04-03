package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
)

// Client communicates with a single MCP server through a Transport.
// It is transport-agnostic — works with both StdioTransport (local)
// and StreamableHTTPTransport (remote, current standard).
type Client struct {
	name      string
	transport Transport

	// Request tracking
	nextID    atomic.Int64
	pending   map[int64]chan Response
	pendingMu sync.Mutex

	// Server info (populated after initialize)
	serverInfo   ServerInfo
	capabilities Capabilities

	// Lifecycle
	done      chan struct{}
	closeOnce sync.Once
}

// NewClient creates a new MCP client with the given transport.
func NewClient(name string, transport Transport) *Client {
	return &Client{
		name:      name,
		transport: transport,
		pending:   make(map[int64]chan Response),
		done:      make(chan struct{}),
	}
}

// Connect starts the transport and performs the MCP initialization handshake.
func (c *Client) Connect(ctx context.Context) error {
	// Start transport
	if err := c.transport.Start(ctx); err != nil {
		return fmt.Errorf("starting transport: %w", err)
	}

	// Start routing incoming messages to pending requests
	go c.routeLoop()

	// Perform MCP initialization handshake
	if err := c.initialize(ctx); err != nil {
		c.Close()
		return fmt.Errorf("initialization failed for %q: %w", c.name, err)
	}

	return nil
}

// initialize performs the MCP initialization handshake.
func (c *Client) initialize(ctx context.Context) error {
	params := InitializeParams{
		ProtocolVersion: "2024-11-05",
		ClientInfo: ClientInfo{
			Name:    "gak",
			Version: "0.1.0",
		},
		Capabilities: Capabilities{},
	}

	paramsMap, err := structToMap(params)
	if err != nil {
		return fmt.Errorf("marshaling init params: %w", err)
	}

	resp, err := c.call(ctx, MethodInitialize, paramsMap)
	if err != nil {
		return err
	}

	var result InitializeResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return fmt.Errorf("parsing init result: %w", err)
	}

	c.serverInfo = result.ServerInfo
	c.capabilities = result.Capabilities

	// Send initialized notification
	if err := c.notify(ctx, MethodInitialized, nil); err != nil {
		return fmt.Errorf("sending initialized notification: %w", err)
	}

	return nil
}

// ListTools returns all tools provided by this MCP server.
func (c *Client) ListTools(ctx context.Context) ([]ToolInfo, error) {
	resp, err := c.call(ctx, MethodToolsList, nil)
	if err != nil {
		return nil, err
	}

	var result ToolsListResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return nil, fmt.Errorf("parsing tools list: %w", err)
	}

	return result.Tools, nil
}

// CallTool invokes a tool on the MCP server.
func (c *Client) CallTool(ctx context.Context, name string, arguments map[string]any) (*ToolCallResult, error) {
	params := map[string]any{
		"name":      name,
		"arguments": arguments,
	}

	resp, err := c.call(ctx, MethodToolsCall, params)
	if err != nil {
		return nil, err
	}

	var result ToolCallResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return nil, fmt.Errorf("parsing tool call result: %w", err)
	}

	return &result, nil
}

// ListResources returns all resources provided by this MCP server.
func (c *Client) ListResources(ctx context.Context) ([]ResourceInfo, error) {
	resp, err := c.call(ctx, MethodResourcesList, nil)
	if err != nil {
		return nil, err
	}

	var result ResourcesListResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return nil, fmt.Errorf("parsing resources list: %w", err)
	}

	return result.Resources, nil
}

// ServerName returns the server's declared name.
func (c *Client) ServerName() string {
	if c.serverInfo.Name != "" {
		return c.serverInfo.Name
	}
	return c.name
}

// Close terminates the MCP client and its transport.
func (c *Client) Close() error {
	var firstErr error
	c.closeOnce.Do(func() {
		close(c.done)

		// Cancel all pending requests
		c.pendingMu.Lock()
		for id, ch := range c.pending {
			close(ch)
			delete(c.pending, id)
		}
		c.pendingMu.Unlock()

		if err := c.transport.Close(); err != nil {
			firstErr = err
		}
	})
	return firstErr
}

// --- Internal methods ---

// call sends a JSON-RPC request and waits for the response.
func (c *Client) call(ctx context.Context, method string, params map[string]any) (*Response, error) {
	id := c.nextID.Add(1)

	req := NewRequest(id, method, params)

	// Register pending response channel
	respCh := make(chan Response, 1)
	c.pendingMu.Lock()
	c.pending[id] = respCh
	c.pendingMu.Unlock()

	defer func() {
		c.pendingMu.Lock()
		delete(c.pending, id)
		c.pendingMu.Unlock()
	}()

	// Send request via transport
	data, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshaling request: %w", err)
	}

	if err := c.transport.Send(ctx, data); err != nil {
		return nil, fmt.Errorf("sending request: %w", err)
	}

	// Wait for response
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.done:
		return nil, fmt.Errorf("MCP client closed")
	case resp, ok := <-respCh:
		if !ok {
			return nil, fmt.Errorf("response channel closed")
		}
		if resp.Error != nil {
			return nil, resp.Error
		}
		return &resp, nil
	}
}

// notify sends a JSON-RPC notification (no response expected).
func (c *Client) notify(ctx context.Context, method string, params map[string]any) error {
	notif := NewNotification(method, params)

	data, err := json.Marshal(notif)
	if err != nil {
		return fmt.Errorf("marshaling notification: %w", err)
	}

	return c.transport.Send(ctx, data)
}

// routeLoop reads incoming messages from transport and routes them to pending callers.
func (c *Client) routeLoop() {
	for {
		select {
		case <-c.done:
			return
		case msg, ok := <-c.transport.Receive():
			if !ok {
				return
			}

			var resp Response
			if err := json.Unmarshal(msg, &resp); err != nil {
				continue
			}

			// Skip notifications (no ID)
			if resp.ID == 0 && resp.Result == nil && resp.Error == nil {
				continue
			}

			c.pendingMu.Lock()
			if ch, ok := c.pending[resp.ID]; ok {
				select {
				case ch <- resp:
				default:
				}
			}
			c.pendingMu.Unlock()
		}
	}
}

// structToMap converts a struct to map[string]any via JSON roundtrip.
func structToMap(v any) (map[string]any, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return m, nil
}
