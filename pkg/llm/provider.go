package llm

import (
	"context"
)

// StreamEventType identifies the kind of streaming event from the LLM.
type StreamEventType string

const (
	StreamThinking StreamEventType = "thinking"
	StreamTextDelta StreamEventType = "text_delta"
	StreamTextDone  StreamEventType = "text_done"
	StreamToolCall  StreamEventType = "tool_call"
	StreamError     StreamEventType = "error"
	StreamDone      StreamEventType = "done"
)

// StreamEvent represents a single event in the LLM's streaming response.
type StreamEvent struct {
	Type StreamEventType

	// Text content for thinking/text events
	Text string

	// Tool call details (for StreamToolCall)
	ToolCallID string
	ToolName   string
	ToolInput  map[string]any

	// Error details (for StreamError)
	Error error
}

// ToolDefinition describes a tool that the LLM can invoke.
type ToolDefinition struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	InputSchema map[string]any         `json:"input_schema"`
}

// CompletionRequest contains all parameters for an LLM completion.
type CompletionRequest struct {
	// SystemPrompt is the system-level instruction.
	SystemPrompt string

	// Messages is the conversation history (immutable, append-only).
	Messages []Message

	// Tools is the list of available tools.
	Tools []ToolDefinition

	// MaxTokens limits the response length.
	MaxTokens int

	// Temperature controls randomness (0.0 = deterministic, 1.0 = creative).
	Temperature float64
}

// Provider abstracts the LLM API interaction.
// Implementations must support streaming via channels (Principle 1).
type Provider interface {
	// Complete sends a completion request and returns a channel of streaming events.
	// The channel is closed when the response is complete or the context is cancelled.
	// This is the Go equivalent of AsyncGenerator — the caller consumes events
	// with `for event := range ch` and can cancel via context.
	Complete(ctx context.Context, req CompletionRequest) (<-chan StreamEvent, error)

	// Name returns the provider's identifier (e.g., "anthropic", "openai").
	Name() string
}
