// Package kernel implements the core agent inference loop.
// 核心设计原则：异步流式优先 (Principle 1)
// 所有的思考、工具调用、进度更新都封装为 Event 结构体，
// 通过 <-chan Event 实现流式输出。
package kernel

import (
	"time"

	"github.com/akria/gak/pkg/llm"
	"github.com/akria/gak/pkg/tool"
)

// EventType defines the type of an event emitted by the kernel.
type EventType string

const (
	// EventThinking indicates the LLM is generating reasoning tokens.
	EventThinking EventType = "thinking"

	// EventTextDelta indicates a streaming text token from the LLM.
	EventTextDelta EventType = "text_delta"

	// EventTextDone indicates the LLM has finished generating text.
	EventTextDone EventType = "text_done"

	// EventToolUseStart indicates the LLM has requested a tool call.
	EventToolUseStart EventType = "tool_use_start"

	// EventToolUseResult indicates a tool call has completed.
	EventToolUseResult EventType = "tool_use_result"

	// EventPermissionRequest indicates a tool needs user approval.
	EventPermissionRequest EventType = "permission_request"

	// EventPermissionResponse indicates the user has responded to a permission request.
	EventPermissionResponse EventType = "permission_response"

	// EventStateTransition indicates a state machine transition.
	EventStateTransition EventType = "state_transition"

	// EventError indicates an error occurred.
	EventError EventType = "error"

	// EventDone indicates the agent run has completed.
	EventDone EventType = "done"
)

// Event is the fundamental unit of the streaming event system.
// Every interaction between kernel components produces Events
// that flow through the <-chan Event pipeline.
type Event struct {
	// Type categorizes this event for consumers.
	Type EventType `json:"type"`

	// Timestamp records when this event was created.
	Timestamp time.Time `json:"timestamp"`

	// Turn identifies which inference turn produced this event.
	Turn int `json:"turn"`

	// --- Payload fields (only one set per event type) ---

	// Text carries content for TextDelta/TextDone/Thinking events.
	Text string `json:"text,omitempty"`

	// ToolCall carries details for ToolUseStart events.
	ToolCall *ToolCallEvent `json:"tool_call,omitempty"`

	// ToolResult carries details for ToolUseResult events.
	ToolResult *ToolResultEvent `json:"tool_result,omitempty"`

	// Permission carries details for PermissionRequest/PermissionResponse events.
	Permission *PermissionEvent `json:"permission,omitempty"`

	// Transition carries details for StateTransition events.
	Transition *TransitionEvent `json:"transition,omitempty"`

	// Error carries the error for Error events.
	Error error `json:"error,omitempty"`
}

// ToolCallEvent describes an LLM-requested tool invocation.
type ToolCallEvent struct {
	ID    string         `json:"id"`
	Name  string         `json:"name"`
	Input map[string]any `json:"input"`
}

// ToolResultEvent describes the outcome of a tool execution.
type ToolResultEvent struct {
	CallID  string          `json:"call_id"`
	Name    string          `json:"name"`
	Result  tool.Result     `json:"result"`
	Elapsed time.Duration   `json:"elapsed"`
}

// PermissionEvent describes a permission request or response.
type PermissionEvent struct {
	ToolName    string `json:"tool_name"`
	Description string `json:"description"`
	RiskLevel   string `json:"risk_level"`
	Approved    bool   `json:"approved,omitempty"`
}

// TransitionEvent describes a state machine transition.
type TransitionEvent struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// NewEvent creates a new Event with the given type and current timestamp.
func NewEvent(eventType EventType, turn int) Event {
	return Event{
		Type:      eventType,
		Timestamp: time.Now(),
		Turn:      turn,
	}
}

// NewTextEvent creates a text delta event.
func NewTextEvent(text string, turn int) Event {
	e := NewEvent(EventTextDelta, turn)
	e.Text = text
	return e
}

// NewThinkingEvent creates a thinking event.
func NewThinkingEvent(text string, turn int) Event {
	e := NewEvent(EventThinking, turn)
	e.Text = text
	return e
}

// NewToolCallEvent creates a tool use start event.
func NewToolCallEvent(id, name string, input map[string]any, turn int) Event {
	e := NewEvent(EventToolUseStart, turn)
	e.ToolCall = &ToolCallEvent{
		ID:    id,
		Name:  name,
		Input: input,
	}
	return e
}

// NewToolResultEvent creates a tool result event.
func NewToolResultEvent(callID, name string, result tool.Result, elapsed time.Duration, turn int) Event {
	e := NewEvent(EventToolUseResult, turn)
	e.ToolResult = &ToolResultEvent{
		CallID:  callID,
		Name:    name,
		Result:  result,
		Elapsed: elapsed,
	}
	return e
}

// NewErrorEvent creates an error event.
func NewErrorEvent(err error, turn int) Event {
	e := NewEvent(EventError, turn)
	e.Error = err
	return e
}

// NewDoneEvent creates a done event.
func NewDoneEvent(turn int) Event {
	return NewEvent(EventDone, turn)
}

// NewPermissionRequestEvent creates a permission request event.
func NewPermissionRequestEvent(toolName, description, riskLevel string, turn int) Event {
	e := NewEvent(EventPermissionRequest, turn)
	e.Permission = &PermissionEvent{
		ToolName:    toolName,
		Description: description,
		RiskLevel:   riskLevel,
	}
	return e
}

// NewTransitionEvent creates a state transition event.
func NewTransitionEvent(from, to string, turn int) Event {
	e := NewEvent(EventStateTransition, turn)
	e.Transition = &TransitionEvent{
		From: from,
		To:   to,
	}
	return e
}

// --- Convenience: convert llm.StreamEvent to kernel Event ---

// FromStreamEvent converts an LLM stream event to a kernel event.
func FromStreamEvent(se llm.StreamEvent, turn int) Event {
	switch se.Type {
	case llm.StreamThinking:
		return NewThinkingEvent(se.Text, turn)
	case llm.StreamTextDelta:
		return NewTextEvent(se.Text, turn)
	case llm.StreamTextDone:
		e := NewEvent(EventTextDone, turn)
		e.Text = se.Text
		return e
	case llm.StreamToolCall:
		return NewToolCallEvent(se.ToolCallID, se.ToolName, se.ToolInput, turn)
	case llm.StreamError:
		return NewErrorEvent(se.Error, turn)
	default:
		return NewEvent(EventTextDelta, turn)
	}
}
