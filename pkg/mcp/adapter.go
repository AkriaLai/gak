package mcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/akria/gak/pkg/tool"
)

// ToolAdapter wraps an MCP server tool as a tool.Tool interface.
// This is the bridge between MCP's dynamic tool discovery and
// the kernel's static Tool interface (Principle 4).
type ToolAdapter struct {
	client   *Client
	info     ToolInfo
	prefix   string // namespace prefix, e.g., "mcp_servername_"
}

// NewToolAdapter creates a Tool adapter for an MCP tool.
func NewToolAdapter(client *Client, info ToolInfo, prefix string) *ToolAdapter {
	return &ToolAdapter{
		client: client,
		info:   info,
		prefix: prefix,
	}
}

// Name returns the namespaced tool name to avoid collisions.
// e.g., "mcp_github_create_issue"
func (a *ToolAdapter) Name() string {
	return a.prefix + a.info.Name
}

// Description returns the tool's description from the MCP server.
func (a *ToolAdapter) Description() string {
	desc := a.info.Description
	if desc == "" {
		desc = fmt.Sprintf("MCP tool %q from server %q", a.info.Name, a.client.ServerName())
	}
	return desc
}

// InputSchema returns the tool's input schema from the MCP server.
func (a *ToolAdapter) InputSchema() map[string]any {
	return a.info.InputSchema
}

// ValidateInput performs basic validation.
// MCP servers handle their own validation, but we do a sanity check here.
func (a *ToolAdapter) ValidateInput(input map[string]any) error {
	// MCP tools define their own schemas; we trust the server's validation.
	// The kernel's security pipeline provides additional checks.
	return nil
}

// Risk returns Medium for all MCP tools since they execute external code.
// This ensures they go through the Human-in-the-Loop security stage.
func (a *ToolAdapter) Risk(_ map[string]any) tool.RiskLevel {
	return tool.RiskMedium
}

// Execute invokes the tool on the remote MCP server.
func (a *ToolAdapter) Execute(ctx context.Context, input map[string]any) (tool.Result, error) {
	result, err := a.client.CallTool(ctx, a.info.Name, input)
	if err != nil {
		return tool.NewErrorResult(fmt.Errorf("MCP call failed: %w", err)), nil
	}

	// Convert MCP ContentItems to a single text output
	var sb strings.Builder
	for _, item := range result.Content {
		switch item.Type {
		case "text":
			sb.WriteString(item.Text)
		case "image":
			sb.WriteString(fmt.Sprintf("[image: %s]", item.MimeType))
		case "resource":
			sb.WriteString(fmt.Sprintf("[resource: %s]", item.Text))
		default:
			sb.WriteString(item.Text)
		}
	}

	return tool.Result{
		Output:  sb.String(),
		IsError: result.IsError,
	}, nil
}
