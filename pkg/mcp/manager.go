package mcp

import (
	"context"
	"fmt"
	"sync"

	"github.com/akria/gak/pkg/tool"
)

// TransportType identifies the transport mechanism.
type TransportType string

const (
	TransportStdio         TransportType = "stdio"
	TransportStreamableHTTP TransportType = "streamable-http"
)

// ServerConfig describes how to connect to an MCP server.
type ServerConfig struct {
	// Name is the human-readable identifier for this server.
	Name string `json:"name"`

	// Transport specifies the transport type: "stdio" or "streamable-http".
	// Default: "streamable-http" (current MCP standard).
	Transport TransportType `json:"transport"`

	// --- Stdio fields ---

	// Command is the executable to run (for stdio transport).
	Command string `json:"command,omitempty"`

	// Args are the command arguments (for stdio transport).
	Args []string `json:"args,omitempty"`

	// Env are additional environment variables (for stdio transport).
	Env []string `json:"env,omitempty"`

	// --- Streamable HTTP fields ---

	// URL is the MCP endpoint (for streamable-http transport).
	// e.g., "https://example.com/mcp"
	URL string `json:"url,omitempty"`

	// Headers are custom HTTP headers (for streamable-http transport).
	// e.g., {"Authorization": "Bearer xxx"}
	Headers map[string]string `json:"headers,omitempty"`

	// --- Common ---

	// Enabled controls whether this server is active.
	Enabled bool `json:"enabled"`
}

// Manager coordinates multiple MCP server connections.
// It handles lifecycle (connect, discover tools, disconnect) and
// provides a unified view of all MCP tools for the ToolRegistry.
type Manager struct {
	mu      sync.RWMutex
	clients map[string]*Client
	configs []ServerConfig
}

// NewManager creates a new MCP manager.
func NewManager() *Manager {
	return &Manager{
		clients: make(map[string]*Client),
	}
}

// AddServer adds a server configuration.
func (m *Manager) AddServer(config ServerConfig) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Default to streamable-http if not specified
	if config.Transport == "" {
		config.Transport = TransportStreamableHTTP
	}
	m.configs = append(m.configs, config)
}

// ConnectAll starts all enabled MCP servers and performs initialization.
func (m *Manager) ConnectAll(ctx context.Context) []error {
	m.mu.Lock()
	defer m.mu.Unlock()

	var errs []error
	for _, cfg := range m.configs {
		if !cfg.Enabled {
			continue
		}

		transport, err := m.createTransport(cfg)
		if err != nil {
			errs = append(errs, fmt.Errorf("MCP server %q: %w", cfg.Name, err))
			continue
		}

		client := NewClient(cfg.Name, transport)
		if err := client.Connect(ctx); err != nil {
			errs = append(errs, fmt.Errorf("MCP server %q: %w", cfg.Name, err))
			continue
		}

		m.clients[cfg.Name] = client
	}

	return errs
}

// createTransport builds the appropriate transport for the config.
func (m *Manager) createTransport(cfg ServerConfig) (Transport, error) {
	switch cfg.Transport {
	case TransportStdio:
		if cfg.Command == "" {
			return nil, fmt.Errorf("stdio transport requires 'command'")
		}
		return NewStdioTransport(cfg.Command, cfg.Args, cfg.Env), nil

	case TransportStreamableHTTP:
		if cfg.URL == "" {
			return nil, fmt.Errorf("streamable-http transport requires 'url'")
		}
		var opts []HTTPTransportOption
		for k, v := range cfg.Headers {
			opts = append(opts, WithHeader(k, v))
		}
		return NewStreamableHTTPTransport(cfg.URL, opts...), nil

	default:
		return nil, fmt.Errorf("unknown transport type: %q", cfg.Transport)
	}
}

// DiscoverAndRegister discovers tools from all connected MCP servers
// and registers them in the provided ToolRegistry.
func (m *Manager) DiscoverAndRegister(ctx context.Context, registry *tool.Registry) (int, []error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var errs []error
	total := 0

	for name, client := range m.clients {
		tools, err := client.ListTools(ctx)
		if err != nil {
			errs = append(errs, fmt.Errorf("listing tools from %q: %w", name, err))
			continue
		}

		prefix := fmt.Sprintf("mcp_%s_", name)
		for _, ti := range tools {
			adapter := NewToolAdapter(client, ti, prefix)
			if err := registry.Register(adapter); err != nil {
				errs = append(errs, fmt.Errorf("registering %q from %q: %w", ti.Name, name, err))
				continue
			}
			total++
		}
	}

	return total, errs
}

// CloseAll disconnects all MCP servers.
func (m *Manager) CloseAll() []error {
	m.mu.Lock()
	defer m.mu.Unlock()

	var errs []error
	for name, client := range m.clients {
		if err := client.Close(); err != nil {
			errs = append(errs, fmt.Errorf("closing %q: %w", name, err))
		}
		delete(m.clients, name)
	}

	return errs
}

// ConnectedServers returns the names of connected servers.
func (m *Manager) ConnectedServers() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	names := make([]string, 0, len(m.clients))
	for name := range m.clients {
		names = append(names, name)
	}
	return names
}

// ServerCount returns the number of connected servers.
func (m *Manager) ServerCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.clients)
}
