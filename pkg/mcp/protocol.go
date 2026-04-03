// Package mcp implements the Model Context Protocol (MCP) client.
//
// MCP is a standardized protocol that allows AI agents to dynamically
// discover and invoke tools from external servers. This implements
// Principle 4 (Progressive Capability), Level 4 — the outermost
// extensibility layer that enables third-party tool ecosystems.
//
// Protocol: JSON-RPC 2.0 over stdio (stdin/stdout)
//
// References:
//   - https://modelcontextprotocol.io/
//   - https://spec.modelcontextprotocol.io/
package mcp

import "encoding/json"

// --- JSON-RPC 2.0 Base Types ---

// Request is a JSON-RPC 2.0 request.
type Request struct {
	JSONRPC string         `json:"jsonrpc"`
	ID      int64          `json:"id"`
	Method  string         `json:"method"`
	Params  map[string]any `json:"params,omitempty"`
}

// Response is a JSON-RPC 2.0 response.
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// Notification is a JSON-RPC 2.0 notification (no ID, no response expected).
type Notification struct {
	JSONRPC string         `json:"jsonrpc"`
	Method  string         `json:"method"`
	Params  map[string]any `json:"params,omitempty"`
}

// RPCError is a JSON-RPC 2.0 error object.
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func (e *RPCError) Error() string {
	return e.Message
}

// --- MCP Protocol Methods ---

const (
	MethodInitialize    = "initialize"
	MethodInitialized   = "notifications/initialized"
	MethodToolsList     = "tools/list"
	MethodToolsCall     = "tools/call"
	MethodResourcesList = "resources/list"
	MethodResourcesRead = "resources/read"
	MethodPromptsList   = "prompts/list"
	MethodPromptsGet    = "prompts/get"
	MethodPing          = "ping"
)

// --- MCP Data Types ---

// ServerInfo describes an MCP server's identity.
type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// ClientInfo describes the MCP client's identity.
type ClientInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// Capabilities describes what an MCP server supports.
type Capabilities struct {
	Tools     *ToolsCapability     `json:"tools,omitempty"`
	Resources *ResourcesCapability `json:"resources,omitempty"`
	Prompts   *PromptsCapability   `json:"prompts,omitempty"`
}

// ToolsCapability indicates the server supports tools.
type ToolsCapability struct {
	ListChanged bool `json:"listChanged,omitempty"`
}

// ResourcesCapability indicates the server supports resources.
type ResourcesCapability struct {
	Subscribe   bool `json:"subscribe,omitempty"`
	ListChanged bool `json:"listChanged,omitempty"`
}

// PromptsCapability indicates the server supports prompts.
type PromptsCapability struct {
	ListChanged bool `json:"listChanged,omitempty"`
}

// InitializeParams are sent to the server during initialization.
type InitializeParams struct {
	ProtocolVersion string       `json:"protocolVersion"`
	ClientInfo      ClientInfo   `json:"clientInfo"`
	Capabilities    Capabilities `json:"capabilities"`
}

// InitializeResult is the server's response to initialize.
type InitializeResult struct {
	ProtocolVersion string       `json:"protocolVersion"`
	ServerInfo      ServerInfo   `json:"serverInfo"`
	Capabilities    Capabilities `json:"capabilities"`
}

// ToolInfo describes a tool exposed by an MCP server.
type ToolInfo struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"inputSchema"`
}

// ToolsListResult is the response to tools/list.
type ToolsListResult struct {
	Tools []ToolInfo `json:"tools"`
}

// ToolCallParams are sent to invoke a tool.
type ToolCallParams struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments,omitempty"`
}

// ToolCallResult is the response from a tool invocation.
type ToolCallResult struct {
	Content []ContentItem `json:"content"`
	IsError bool          `json:"isError,omitempty"`
}

// ContentItem is a single piece of content in a tool result.
type ContentItem struct {
	Type     string `json:"type"` // "text", "image", "resource"
	Text     string `json:"text,omitempty"`
	MimeType string `json:"mimeType,omitempty"`
	Data     string `json:"data,omitempty"` // base64 for images
}

// ResourceInfo describes a resource exposed by an MCP server.
type ResourceInfo struct {
	URI         string `json:"uri"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	MimeType    string `json:"mimeType,omitempty"`
}

// ResourcesListResult is the response to resources/list.
type ResourcesListResult struct {
	Resources []ResourceInfo `json:"resources"`
}

// --- Helper constructors ---

// NewRequest creates a new JSON-RPC request.
func NewRequest(id int64, method string, params map[string]any) Request {
	return Request{
		JSONRPC: "2.0",
		ID:      id,
		Method:  method,
		Params:  params,
	}
}

// NewNotification creates a new JSON-RPC notification.
func NewNotification(method string, params map[string]any) Notification {
	return Notification{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
	}
}
