// Package config provides unified configuration for the GAK agent.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/akria/gak/pkg/llm"
	"github.com/akria/gak/pkg/mcp"
)

// Config is the top-level configuration for GAK.
type Config struct {
	// LLM configures the language model provider.
	LLM LLMConfig `json:"llm"`

	// MCP configures external MCP servers.
	MCP MCPConfig `json:"mcp"`

	// Skills configures the skill system.
	Skills SkillsConfig `json:"skills"`

	// Plugins configures the plugin system.
	Plugins PluginsConfig `json:"plugins"`

	// Security configures the security pipeline.
	Security SecurityConfig `json:"security"`

	// Agent configures the kernel runner.
	Agent AgentConfig `json:"agent"`
}

// LLMConfig configures the LLM provider(s).
type LLMConfig struct {
	// Provider is the primary provider type.
	// Can be a well-known name (openai, anthropic, deepseek, moonshot, qwen, etc.)
	// or a generic type (openai, anthropic) with custom base_url.
	Provider string `json:"provider"`

	// Model is the model identifier.
	Model string `json:"model,omitempty"`

	// APIKey is the API key. Supports ${ENV_VAR} syntax.
	APIKey string `json:"api_key,omitempty"`

	// BaseURL is an optional custom API base URL.
	BaseURL string `json:"base_url,omitempty"`

	// Providers defines named providers that can be switched at runtime.
	// Key is a friendly name, value is the provider config.
	//
	// If a provider has a "models" array, each model is auto-expanded
	// into its own provider entry inheriting base_url, api_key, and type.
	// This avoids repeating credentials when one API serves multiple models.
	//
	// Example:
	//   "windhub": {
	//     "type": "openai",
	//     "base_url": "https://windhub.cc/v1",
	//     "api_key": "sk-xxx",
	//     "model": "gpt-5.4",
	//     "display_name": "WindHub",
	//     "models": ["gpt-5.4", "gpt-5.4-mini"]
	//   }
	// → expands to providers: "gpt-5.4" and "gpt-5.4-mini"
	Providers map[string]llm.ProviderConfig `json:"providers,omitempty"`
}

// flattenModels expands ProviderConfig.Models into individual provider entries.
// Called automatically during LoadFile.
func (l *LLMConfig) flattenModels() {
	if len(l.Providers) == 0 {
		return
	}

	expanded := make(map[string]llm.ProviderConfig)

	for name, p := range l.Providers {
		if len(p.Models) == 0 {
			// No models list — keep as-is
			expanded[name] = p
			continue
		}

		// Expand each model into its own provider entry
		// Use "provider/model" as key to avoid conflicts between
		// different providers serving the same model name.
		// e.g., "windhub/gpt-5.4" vs "api-test/gpt-5.4"
		for modelAlias, alias := range p.Models {
			// Resolve actual API model ID
			apiModelID := alias.ID
			if apiModelID == "" {
				apiModelID = modelAlias
			}

			// Namespaced key: "provider/model"
			key := name + "/" + modelAlias

			expanded[key] = llm.ProviderConfig{
				Type:        p.Type,
				BaseURL:     p.BaseURL,
				APIKey:      p.APIKey,
				Model:       apiModelID,
				DisplayName: modelAlias + " (" + name + ")",
			}
		}
	}

	l.Providers = expanded
}

// MCPConfig configures MCP server connections.
type MCPConfig struct {
	Servers []mcp.ServerConfig `json:"servers"`
}

// SkillsConfig configures the skill system.
type SkillsConfig struct {
	Dirs []string `json:"dirs"`
}

// PluginsConfig configures the plugin system.
type PluginsConfig struct {
	// Configs maps plugin name → plugin-specific config values.
	Configs map[string]map[string]any `json:"configs,omitempty"`
}

// SecurityConfig configures the security pipeline.
type SecurityConfig struct {
	AutoApproveRisk string   `json:"auto_approve_risk"`
	DeniedTools     []string `json:"denied_tools,omitempty"`
}

// AgentConfig configures the kernel runner.
type AgentConfig struct {
	MaxTurns     int     `json:"max_turns"`
	MaxTokens    int     `json:"max_tokens"`
	Temperature  float64 `json:"temperature"`
	SystemPrompt string  `json:"system_prompt,omitempty"`
}

// DefaultConfig returns a sensible default configuration.
func DefaultConfig() Config {
	return Config{
		LLM: LLMConfig{
			Provider: "anthropic",
			Model:    "claude-sonnet-4-20250514",
		},
		MCP: MCPConfig{
			Servers: nil,
		},
		Skills: SkillsConfig{
			Dirs: []string{".gak/skills", "~/.gak/skills"},
		},
		Plugins: PluginsConfig{
			Configs: make(map[string]map[string]any),
		},
		Security: SecurityConfig{
			AutoApproveRisk: "low",
		},
		Agent: AgentConfig{
			MaxTurns:    25,
			MaxTokens:   8192,
			Temperature: 0.7,
		},
	}
}

// PrimaryProviderConfig returns the ProviderConfig for the primary LLM.
func (c Config) PrimaryProviderConfig() llm.ProviderConfig {
	return llm.ProviderConfig{
		Type:    c.LLM.Provider,
		Model:   c.LLM.Model,
		APIKey:  c.LLM.APIKey,
		BaseURL: c.LLM.BaseURL,
	}
}

// LoadFile loads configuration from a JSON file.
func LoadFile(path string) (Config, error) {
	cfg := DefaultConfig()

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return cfg, fmt.Errorf("reading config: %w", err)
	}

	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("parsing config: %w", err)
	}

	// Expand models arrays into individual provider entries
	cfg.LLM.flattenModels()

	return cfg, nil
}

// Save writes the configuration to a JSON file.
func (c Config) Save(path string) error {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling config: %w", err)
	}

	return os.WriteFile(path, data, 0644)
}

// ResolveWorkspace returns the active GAK workspace directory.
// It prioritizes ./.gak in the current directory.
// If absent, it falls back to ~/.gak.
func ResolveWorkspace() string {
	local := ".gak"
	if info, err := os.Stat(local); err == nil && info.IsDir() {
		return local
	}

	home, err := os.UserHomeDir()
	if err == nil {
		return filepath.Join(home, ".gak")
	}
	return local // fallback to local if home is unavailable
}

// AutoLoad resolves the workspace and loads the config from there.
// Returns the loaded config and the resolved workspace directory path.
func AutoLoad() (Config, string, error) {
	ws := ResolveWorkspace()
	cfgPath := filepath.Join(ws, "config.json")
	cfg, err := LoadFile(cfgPath)
	return cfg, ws, err
}
