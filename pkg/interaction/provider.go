// Package interaction defines the abstraction for user interaction.
// This is the "Provider" interface that separates the kernel from
// the concrete I/O medium (CLI terminal, WebSocket, etc.).
package interaction

import (
	"context"

	"github.com/akria/gak/pkg/tool"
)

// Provider abstracts user interaction for the kernel.
// CLI and Web implementations can provide different UIs
// while the kernel logic remains unchanged.
type Provider interface {
	// Confirm asks the user to approve a dangerous operation.
	// Implements the security.Authorizer interface.
	Confirm(ctx context.Context, toolName, description string, risk tool.RiskLevel) (bool, error)

	// Prompt asks the user for text input.
	Prompt(ctx context.Context, message string) (string, error)

	// Notify sends an informational message to the user (no response expected).
	Notify(ctx context.Context, message string) error
}
