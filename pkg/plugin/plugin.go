// Package plugin implements the Plugin system.
//
// Plugins are Principle 4, Level 3 — tool packages with lifecycle management.
// Unlike atomic Tools (Level 1) or declarative Skills (Level 2), Plugins are
// Go packages that can:
//   - Bundle multiple related tools together
//   - Manage shared state across tools (e.g., database connections)
//   - Hook into kernel lifecycle events (init, start, stop)
//   - Declare configuration requirements
//
// Plugin lifecycle:
//   Init() → Start(ctx) → [tools available] → Stop()
package plugin

import (
	"context"
	"fmt"
	"sync"

	"github.com/akria/gak/pkg/tool"
)

// Plugin is the interface that all plugins must implement.
type Plugin interface {
	// Manifest returns metadata about the plugin.
	Manifest() Manifest

	// Init is called once during kernel bootstrap to validate config
	// and set up the plugin's internal state. No tools are available yet.
	Init(cfg map[string]any) error

	// Start is called after Init to activate the plugin.
	// This is where plugins should open connections, start workers, etc.
	// The provided context is cancelled when the kernel shuts down.
	Start(ctx context.Context) error

	// Tools returns the tools provided by this plugin.
	// Called after Start() — the plugin should return fully initialized tools.
	Tools() []tool.Tool

	// Stop is called during kernel shutdown for cleanup.
	// Plugins should close connections, flush buffers, etc.
	Stop() error
}

// Manifest describes a plugin's identity, version, and requirements.
type Manifest struct {
	// Name is the unique identifier (e.g., "kubernetes", "database").
	Name string `json:"name"`

	// Version follows semver (e.g., "1.0.0").
	Version string `json:"version"`

	// Description is human-readable.
	Description string `json:"description"`

	// Author is the plugin creator.
	Author string `json:"author,omitempty"`

	// RequiredConfig lists configuration keys the plugin needs.
	RequiredConfig []string `json:"required_config,omitempty"`

	// ToolPrefix is prepended to all tool names (e.g., "k8s_").
	// If empty, plugin name + "_" is used.
	ToolPrefix string `json:"tool_prefix,omitempty"`
}

// ToolPrefix returns the effective tool name prefix.
func (m Manifest) GetToolPrefix() string {
	if m.ToolPrefix != "" {
		return m.ToolPrefix
	}
	return m.Name + "_"
}

// State represents the lifecycle state of a plugin.
type State string

const (
	StateUnloaded    State = "unloaded"
	StateInitialized State = "initialized"
	StateRunning     State = "running"
	StateStopped     State = "stopped"
	StateError       State = "error"
)

// entry tracks a plugin and its lifecycle state.
type entry struct {
	plugin Plugin
	state  State
	err    error
}

// Manager manages the lifecycle of all registered plugins.
type Manager struct {
	mu      sync.RWMutex
	plugins map[string]*entry
	order   []string // Insertion order for deterministic startup/shutdown
}

// NewManager creates a new plugin manager.
func NewManager() *Manager {
	return &Manager{
		plugins: make(map[string]*entry),
	}
}

// Register adds a plugin. Must be called before InitAll.
func (m *Manager) Register(p Plugin) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	manifest := p.Manifest()
	if _, exists := m.plugins[manifest.Name]; exists {
		return fmt.Errorf("plugin %q already registered", manifest.Name)
	}

	m.plugins[manifest.Name] = &entry{
		plugin: p,
		state:  StateUnloaded,
	}
	m.order = append(m.order, manifest.Name)
	return nil
}

// InitAll initializes all registered plugins with their configuration.
// pluginConfigs maps plugin name → config values.
func (m *Manager) InitAll(pluginConfigs map[string]map[string]any) []error {
	m.mu.Lock()
	defer m.mu.Unlock()

	var errs []error
	for _, name := range m.order {
		e := m.plugins[name]
		if e.state != StateUnloaded {
			continue
		}

		cfg := pluginConfigs[name]
		if cfg == nil {
			cfg = make(map[string]any)
		}

		// Validate required config
		manifest := e.plugin.Manifest()
		for _, key := range manifest.RequiredConfig {
			if _, ok := cfg[key]; !ok {
				err := fmt.Errorf("plugin %q requires config key %q", name, key)
				e.state = StateError
				e.err = err
				errs = append(errs, err)
				continue
			}
		}

		if err := e.plugin.Init(cfg); err != nil {
			e.state = StateError
			e.err = err
			errs = append(errs, fmt.Errorf("plugin %q init: %w", name, err))
			continue
		}

		e.state = StateInitialized
	}

	return errs
}

// StartAll starts all initialized plugins.
func (m *Manager) StartAll(ctx context.Context) []error {
	m.mu.Lock()
	defer m.mu.Unlock()

	var errs []error
	for _, name := range m.order {
		e := m.plugins[name]
		if e.state != StateInitialized {
			continue
		}

		if err := e.plugin.Start(ctx); err != nil {
			e.state = StateError
			e.err = err
			errs = append(errs, fmt.Errorf("plugin %q start: %w", name, err))
			continue
		}

		e.state = StateRunning
	}

	return errs
}

// RegisterTools registers all tools from running plugins into the ToolRegistry.
func (m *Manager) RegisterTools(registry *tool.Registry) (int, []error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var errs []error
	count := 0

	for _, name := range m.order {
		e := m.plugins[name]
		if e.state != StateRunning {
			continue
		}

		for _, t := range e.plugin.Tools() {
			if err := registry.Register(t); err != nil {
				errs = append(errs, fmt.Errorf("plugin %q tool %q: %w", name, t.Name(), err))
				continue
			}
			count++
		}
	}

	return count, errs
}

// StopAll stops all running plugins in reverse order.
func (m *Manager) StopAll() []error {
	m.mu.Lock()
	defer m.mu.Unlock()

	var errs []error
	// Stop in reverse order (LIFO)
	for i := len(m.order) - 1; i >= 0; i-- {
		name := m.order[i]
		e := m.plugins[name]
		if e.state != StateRunning {
			continue
		}

		if err := e.plugin.Stop(); err != nil {
			errs = append(errs, fmt.Errorf("plugin %q stop: %w", name, err))
		}
		e.state = StateStopped
	}

	return errs
}

// Status returns the lifecycle state of all plugins.
func (m *Manager) Status() map[string]State {
	m.mu.RLock()
	defer m.mu.RUnlock()

	status := make(map[string]State, len(m.plugins))
	for name, e := range m.plugins {
		status[name] = e.state
	}
	return status
}

// Count returns the number of registered plugins.
func (m *Manager) Count() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.plugins)
}

// RunningCount returns the number of running plugins.
func (m *Manager) RunningCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()

	count := 0
	for _, e := range m.plugins {
		if e.state == StateRunning {
			count++
		}
	}
	return count
}
