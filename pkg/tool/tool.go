// Package tool defines the tool abstraction and registry.
// Tools are the atomic units of capability extension (Principle 4, Level 1).
package tool

import (
	"context"
	"fmt"
)

// RiskLevel classifies the danger level of a tool operation.
type RiskLevel string

const (
	RiskNone   RiskLevel = "none"     // Pure read, no side effects
	RiskLow    RiskLevel = "low"      // Minor side effects (e.g., create file)
	RiskMedium RiskLevel = "medium"   // Moderate side effects (e.g., modify file)
	RiskHigh   RiskLevel = "high"     // Dangerous (e.g., delete, network, exec)
)

// Result represents the outcome of a tool execution.
type Result struct {
	// Output is the textual content returned to the LLM.
	Output string `json:"output"`

	// IsError indicates whether the tool execution failed.
	IsError bool `json:"is_error"`

	// Metadata carries optional structured data (not sent to LLM).
	Metadata map[string]any `json:"metadata,omitempty"`
}

// NewResult creates a successful result.
func NewResult(output string) Result {
	return Result{Output: output}
}

// NewErrorResult creates an error result.
func NewErrorResult(err error) Result {
	return Result{
		Output:  err.Error(),
		IsError: true,
	}
}

// NewErrorResultf creates a formatted error result.
func NewErrorResultf(format string, args ...any) Result {
	return Result{
		Output:  fmt.Sprintf(format, args...),
		IsError: true,
	}
}

// Tool defines the interface that all tools must implement.
// This is the foundational abstraction for Principle 4 (Progressive Capability).
type Tool interface {
	// Name returns the unique identifier for this tool.
	Name() string

	// Description returns a human-readable description for the LLM.
	Description() string

	// InputSchema returns the JSON Schema describing the tool's parameters.
	InputSchema() map[string]any

	// ValidateInput checks if the provided input is valid.
	// This is phase 2 of the security pipeline (Principle 2).
	ValidateInput(input map[string]any) error

	// Risk returns the risk level of this tool for the given input.
	// This enables context-sensitive security decisions.
	Risk(input map[string]any) RiskLevel

	// Execute runs the tool with the given input.
	// The context enables cancellation (hard interrupt via Ctrl+C).
	Execute(ctx context.Context, input map[string]any) (Result, error)
}
