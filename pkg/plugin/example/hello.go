package example

import (
	"context"
	"fmt"

	"github.com/akria/gak/pkg/plugin"
	"github.com/akria/gak/pkg/tool"
)

// HelloPlugin is a minimal example plugin.
type HelloPlugin struct{}

func New() *HelloPlugin {
	return &HelloPlugin{}
}

func (p *HelloPlugin) Manifest() plugin.Manifest {
	return plugin.Manifest{
		Name:        "example",
		Version:     "1.0.0",
		Description: "A simple example plugin",
	}
}

func (p *HelloPlugin) Init(cfg map[string]any) error {
	// No config requirements
	return nil
}

func (p *HelloPlugin) Start(ctx context.Context) error {
	return nil
}

func (p *HelloPlugin) Stop() error {
	return nil
}

func (p *HelloPlugin) Tools() []tool.Tool {
	return []tool.Tool{&helloTool{}}
}

// helloTool is a simple tool provided by the plugin.
type helloTool struct{}

func (t *helloTool) Name() string {
	return "example_hello"
}

func (t *helloTool) Description() string {
	return "Returns a friendly greeting. Use this whenever the user asks for a plugin greeting."
}

func (t *helloTool) InputSchema() map[string]any {
	return map[string]any{
		"type":       "object",
		"properties": map[string]any{
			"name": map[string]any{
				"type": "string",
				"description": "Name of the person to greet",
			},
		},
		"required": []string{"name"},
	}
}

func (t *helloTool) ValidateInput(input map[string]any) error {
	if _, ok := input["name"].(string); !ok {
		return fmt.Errorf("name string is required")
	}
	return nil
}

func (t *helloTool) Risk(input map[string]any) tool.RiskLevel {
	return tool.RiskNone
}

func (t *helloTool) Execute(ctx context.Context, input map[string]any) (tool.Result, error) {
	name := input["name"].(string)
	return tool.NewResult(fmt.Sprintf("Hello %s from the Example Plugin!", name)), nil
}
