// Package state implements immutable state management.
// Core design principle: 不可变状态流转 (Principle 5)
// State_N + Event → State_N+1, no in-place mutation.
package state

import (
	"github.com/akria/gak/pkg/llm"
)

// Phase represents the current phase of the agent's FSM.
type Phase string

const (
	PhaseIdle       Phase = "idle"        // Waiting for user input
	PhaseThinking   Phase = "thinking"    // LLM is generating
	PhaseToolUse    Phase = "tool_use"    // Executing a tool call
	PhasePermission Phase = "permission"  // Waiting for user permission
	PhaseDone       Phase = "done"        // Run completed
	PhaseError      Phase = "error"       // Error state
)

// AgentState is the immutable state of the agent.
// Every state transition produces a NEW AgentState object.
// This guarantees:
//   - No concurrent mutation bugs
//   - Safe snapshot passing to sub-agents
//   - Trivial rollback support
type AgentState struct {
	// Phase is the current FSM phase.
	Phase Phase `json:"phase"`

	// Turn is the current inference turn number.
	Turn int `json:"turn"`

	// Messages is the immutable conversation history.
	// Only append via WithMessage / WithMessages.
	Messages []llm.Message `json:"messages"`

	// SystemPrompt is the current system prompt.
	SystemPrompt string `json:"system_prompt"`

	// Variables holds arbitrary key-value metadata.
	Variables map[string]any `json:"variables,omitempty"`
}

// NewState creates a fresh initial state.
func NewState(systemPrompt string) AgentState {
	return AgentState{
		Phase:        PhaseIdle,
		Turn:         0,
		Messages:     nil,
		SystemPrompt: systemPrompt,
		Variables:    make(map[string]any),
	}
}

// --- Immutable transition methods (return NEW state) ---

// WithPhase returns a new state with the given phase.
func (s AgentState) WithPhase(phase Phase) AgentState {
	s.Phase = phase
	return s
}

// WithTurn returns a new state with an incremented turn counter.
func (s AgentState) WithTurn(turn int) AgentState {
	s.Turn = turn
	return s
}

// WithMessage returns a new state with an appended message.
// The original messages slice is never modified (copy-on-append).
func (s AgentState) WithMessage(msg llm.Message) AgentState {
	newMsgs := make([]llm.Message, len(s.Messages), len(s.Messages)+1)
	copy(newMsgs, s.Messages)
	s.Messages = append(newMsgs, msg)
	return s
}

// WithMessages returns a new state with multiple appended messages.
func (s AgentState) WithMessages(msgs ...llm.Message) AgentState {
	newMsgs := make([]llm.Message, len(s.Messages), len(s.Messages)+len(msgs))
	copy(newMsgs, s.Messages)
	s.Messages = append(newMsgs, msgs...)
	return s
}

// WithVariable returns a new state with the given variable set.
func (s AgentState) WithVariable(key string, value any) AgentState {
	newVars := make(map[string]any, len(s.Variables)+1)
	for k, v := range s.Variables {
		newVars[k] = v
	}
	newVars[key] = value
	s.Variables = newVars
	return s
}

// GetVariable retrieves a variable value.
func (s AgentState) GetVariable(key string) (any, bool) {
	v, ok := s.Variables[key]
	return v, ok
}

// MessageCount returns the number of messages in history.
func (s AgentState) MessageCount() int {
	return len(s.Messages)
}

// LastAssistantText returns the text from the last assistant message.
func (s AgentState) LastAssistantText() string {
	for i := len(s.Messages) - 1; i >= 0; i-- {
		if s.Messages[i].Role == llm.RoleAssistant {
			return s.Messages[i].GetText()
		}
	}
	return ""
}
