package llm

import (
	"fmt"
	"os"
	"strings"
)

// ProviderConfig holds configuration for creating an LLM provider.
type ProviderConfig struct {
	// Type identifies the provider type: "anthropic", "openai", or
	// a third-party name (which uses the OpenAI-compatible protocol).
	Type string `json:"type"`

	// Model is the default model identifier (provider-specific).
	Model string `json:"model"`

	// APIKey for authentication. Can reference env vars with ${ENV_VAR}.
	APIKey string `json:"api_key,omitempty"`

	// BaseURL overrides the default API endpoint.
	BaseURL string `json:"base_url,omitempty"`

	// DisplayName is shown in the CLI (defaults to Type).
	DisplayName string `json:"display_name,omitempty"`

	// Models maps display-name → model details for models on this same endpoint.
	// Each entry is auto-expanded into its own provider at config load,
	// inheriting base_url, api_key, and type from this provider.
	//
	// The map key is the user-facing name (shown in CLI, used in /model command).
	// ModelAlias.ID is the actual model identifier sent to the API.
	// If ID is empty, the key itself is used as the API model ID.
	//
	// Example:
	//   "models": {
	//     "glm5": { "id": "6d3a57c3a6fb465e968b604783b89eda" },
	//     "gpt-5.4-mini": {}
	//   }
	Models map[string]ModelAlias `json:"models,omitempty"`
}

// ModelAlias defines a model entry within a provider's models map.
type ModelAlias struct {
	// ID is the actual model identifier sent to the API.
	// If empty, the map key (display name) is used as the model ID.
	ID string `json:"id,omitempty"`
}

// WellKnownProviders maps friendly names to their base URLs and default models.
var WellKnownProviders = map[string]ProviderConfig{
	"openai": {
		Type:    "openai",
		BaseURL: "https://api.openai.com/v1",
		Model:   "gpt-4o",
	},
	"anthropic": {
		Type:    "anthropic",
		BaseURL: "https://api.anthropic.com",
		Model:   "claude-sonnet-4-20250514",
	},
	"deepseek": {
		Type:        "openai",
		BaseURL:     "https://api.deepseek.com/v1",
		Model:       "deepseek-chat",
		DisplayName: "deepseek",
	},
	"moonshot": {
		Type:        "openai",
		BaseURL:     "https://api.moonshot.cn/v1",
		Model:       "moonshot-v1-128k",
		DisplayName: "moonshot",
	},
	"qwen": {
		Type:        "openai",
		BaseURL:     "https://dashscope.aliyuncs.com/compatible-mode/v1",
		Model:       "qwen-max",
		DisplayName: "qwen",
	},
	"siliconflow": {
		Type:        "openai",
		BaseURL:     "https://api.siliconflow.cn/v1",
		Model:       "deepseek-ai/DeepSeek-V3",
		DisplayName: "siliconflow",
	},
	"zhipu": {
		Type:        "openai",
		BaseURL:     "https://open.bigmodel.cn/api/paas/v4",
		Model:       "glm-4-plus",
		DisplayName: "zhipu",
	},
	"groq": {
		Type:        "openai",
		BaseURL:     "https://api.groq.com/openai/v1",
		Model:       "llama-3.3-70b-versatile",
		DisplayName: "groq",
	},
	"together": {
		Type:        "openai",
		BaseURL:     "https://api.together.xyz/v1",
		Model:       "meta-llama/Meta-Llama-3.1-70B-Instruct-Turbo",
		DisplayName: "together",
	},
	"ollama": {
		Type:        "openai",
		BaseURL:     "http://localhost:11434/v1",
		Model:       "llama3.2",
		DisplayName: "ollama",
	},
}

// ResolveProvider merges user config with well-known defaults.
func ResolveProvider(cfg ProviderConfig) ProviderConfig {
	if wellKnown, ok := WellKnownProviders[cfg.Type]; ok {
		if cfg.BaseURL == "" {
			cfg.BaseURL = wellKnown.BaseURL
		}
		if cfg.Model == "" {
			cfg.Model = wellKnown.Model
		}
		if cfg.DisplayName == "" {
			cfg.DisplayName = wellKnown.DisplayName
		}
		if wellKnown.Type != cfg.Type {
			cfg.Type = wellKnown.Type
		}
	}
	if cfg.DisplayName == "" {
		cfg.DisplayName = cfg.Type
	}
	return cfg
}

// ResolveAPIKey handles API key resolution.
// Supports: direct value, ${ENV_VAR} syntax, or empty.
func ResolveAPIKey(cfg ProviderConfig) string {
	key := cfg.APIKey
	if strings.HasPrefix(key, "${") && strings.HasSuffix(key, "}") {
		envName := key[2 : len(key)-1]
		return os.Getenv(envName)
	}
	return key
}

// ListWellKnownProviders returns all well-known provider names.
func ListWellKnownProviders() []string {
	names := make([]string, 0, len(WellKnownProviders))
	for name := range WellKnownProviders {
		names = append(names, name)
	}
	return names
}

// DescribeProvider returns a human-readable description.
func DescribeProvider(cfg ProviderConfig) string {
	return fmt.Sprintf("%s (type=%s, model=%s, base=%s)",
		cfg.DisplayName, cfg.Type, cfg.Model, cfg.BaseURL)
}
