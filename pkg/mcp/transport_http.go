package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
)

// StreamableHTTPTransport implements the MCP Streamable HTTP transport.
//
// This is the current standard (spec 2025-03-26) for remote MCP servers:
//   - Single endpoint (e.g., https://example.com/mcp)
//   - POST for client→server messages (may return SSE stream or JSON)
//   - GET for server→client streaming (optional SSE)
//   - Session management via Mcp-Session-Id header
type StreamableHTTPTransport struct {
	endpoint  string
	client    *http.Client
	headers   map[string]string

	sessionID string       // Assigned by server after initialize
	sessionMu sync.RWMutex

	incoming  chan []byte
	done      chan struct{}
	closeOnce sync.Once
}

// NewStreamableHTTPTransport creates a transport for a remote MCP server.
func NewStreamableHTTPTransport(endpoint string, opts ...HTTPTransportOption) *StreamableHTTPTransport {
	t := &StreamableHTTPTransport{
		endpoint: endpoint,
		client:   http.DefaultClient,
		headers:  make(map[string]string),
		incoming: make(chan []byte, 64),
		done:     make(chan struct{}),
	}
	for _, opt := range opts {
		opt(t)
	}
	return t
}

// HTTPTransportOption configures the Streamable HTTP transport.
type HTTPTransportOption func(*StreamableHTTPTransport)

// WithHTTPClient sets a custom HTTP client.
func WithHTTPClient(client *http.Client) HTTPTransportOption {
	return func(t *StreamableHTTPTransport) {
		t.client = client
	}
}

// WithHeader adds a custom header to all requests.
func WithHeader(key, value string) HTTPTransportOption {
	return func(t *StreamableHTTPTransport) {
		t.headers[key] = value
	}
}

func (t *StreamableHTTPTransport) Start(_ context.Context) error {
	// No explicit start needed for HTTP — connection is per-request.
	// Optionally start a GET SSE listener for server-initiated messages.
	go t.listenSSE()
	return nil
}

// Send sends a JSON-RPC message via POST to the MCP endpoint.
// The response may be:
//   - A single JSON-RPC response (Content-Type: application/json)
//   - An SSE stream of responses (Content-Type: text/event-stream)
func (t *StreamableHTTPTransport) Send(ctx context.Context, data []byte) error {
	req, err := http.NewRequestWithContext(ctx, "POST", t.endpoint, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	t.applyHeaders(req)

	resp, err := t.client.Do(req)
	if err != nil {
		return fmt.Errorf("POST request: %w", err)
	}

	// Capture session ID from response
	if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
		t.sessionMu.Lock()
		t.sessionID = sid
		t.sessionMu.Unlock()
	}

	// Handle response based on content type
	contentType := resp.Header.Get("Content-Type")

	switch {
	case resp.StatusCode == http.StatusAccepted:
		// 202 Accepted — notification acknowledged, no body
		resp.Body.Close()
		return nil

	case strings.HasPrefix(contentType, "text/event-stream"):
		// SSE stream — parse events and forward to incoming channel
		go t.consumeSSE(resp.Body)
		return nil

	case strings.HasPrefix(contentType, "application/json"):
		// Single JSON response
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("reading response: %w", err)
		}

		// Could be a single response or a batch (JSON array)
		if len(body) > 0 && body[0] == '[' {
			// Batch response — split into individual messages
			var batch []json.RawMessage
			if err := json.Unmarshal(body, &batch); err == nil {
				for _, msg := range batch {
					select {
					case t.incoming <- []byte(msg):
					case <-t.done:
						return nil
					}
				}
				return nil
			}
		}

		select {
		case t.incoming <- body:
		case <-t.done:
		}
		return nil

	default:
		resp.Body.Close()
		if resp.StatusCode >= 400 {
			return fmt.Errorf("HTTP %d: %s", resp.StatusCode, contentType)
		}
		return nil
	}
}

func (t *StreamableHTTPTransport) Receive() <-chan []byte {
	return t.incoming
}

func (t *StreamableHTTPTransport) Close() error {
	t.closeOnce.Do(func() {
		close(t.done)

		// Send DELETE to terminate session if we have one
		t.sessionMu.RLock()
		sid := t.sessionID
		t.sessionMu.RUnlock()

		if sid != "" {
			req, err := http.NewRequest("DELETE", t.endpoint, nil)
			if err == nil {
				t.applyHeaders(req)
				resp, err := t.client.Do(req)
				if err == nil {
					resp.Body.Close()
				}
			}
		}
	})
	return nil
}

// applyHeaders adds session ID and custom headers to a request.
func (t *StreamableHTTPTransport) applyHeaders(req *http.Request) {
	t.sessionMu.RLock()
	if t.sessionID != "" {
		req.Header.Set("Mcp-Session-Id", t.sessionID)
	}
	t.sessionMu.RUnlock()

	for k, v := range t.headers {
		req.Header.Set(k, v)
	}
}

// listenSSE opens a GET SSE connection for server-initiated messages.
func (t *StreamableHTTPTransport) listenSSE() {
	req, err := http.NewRequest("GET", t.endpoint, nil)
	if err != nil {
		return
	}

	req.Header.Set("Accept", "text/event-stream")
	t.applyHeaders(req)

	resp, err := t.client.Do(req)
	if err != nil {
		return
	}

	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		resp.Body.Close()
		return
	}

	// Capture session ID
	if sid := resp.Header.Get("Mcp-Session-Id"); sid != "" {
		t.sessionMu.Lock()
		t.sessionID = sid
		t.sessionMu.Unlock()
	}

	t.consumeSSE(resp.Body)
}

// consumeSSE parses an SSE stream and forwards data events to the incoming channel.
func (t *StreamableHTTPTransport) consumeSSE(body io.ReadCloser) {
	defer body.Close()

	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 256*1024), 1024*1024)

	for scanner.Scan() {
		select {
		case <-t.done:
			return
		default:
		}

		line := scanner.Text()

		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		data := strings.TrimPrefix(line, "data: ")
		if data == "" || data == "[DONE]" {
			continue
		}

		msg := []byte(data)
		select {
		case t.incoming <- msg:
		case <-t.done:
			return
		}
	}
}
