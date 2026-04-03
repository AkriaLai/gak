// Package kernel implements the core agent inference loop.
//
// The Runner is the heart of GAK — it orchestrates the LLM Provider,
// Tool Registry, Security Pipeline, and State Machine into a coherent
// event-driven loop.
//
// Design principles applied:
//   - Principle 1: Run() returns <-chan Event (AsyncGenerator equivalent)
//   - Principle 2: Every tool call passes through the security pipeline
//   - Principle 3: System prompt and message history are cache-friendly
//   - Principle 5: State transitions produce new immutable states
package kernel

import (
	"context"
	"fmt"
	"time"

	"github.com/akria/gak/pkg/llm"
	"github.com/akria/gak/pkg/logging"
	"github.com/akria/gak/pkg/metrics"
	"github.com/akria/gak/pkg/security"
	"github.com/akria/gak/pkg/session"
	"github.com/akria/gak/pkg/state"
	"github.com/akria/gak/pkg/tool"
)

// Runner is the core agent kernel.
type Runner struct {
	provider  llm.Provider
	registry  *tool.Registry
	pipeline  *security.Pipeline
	store     *state.Store

	// Optional subsystems (Phase 3)
	metrics   *metrics.Collector
	logger    *logging.Logger
	session   *session.Manager

	// Configuration
	maxTurns        int
	maxTokens       int
	temperature     float64
	eventBufferSize int
}

// New creates a new kernel Runner.
func New(
	provider llm.Provider,
	registry *tool.Registry,
	pipeline *security.Pipeline,
	store *state.Store,
	opts ...Option,
) *Runner {
	r := &Runner{
		provider:        provider,
		registry:        registry,
		pipeline:        pipeline,
		store:           store,
		maxTurns:        25,
		maxTokens:       8192,
		temperature:     0.7,
		eventBufferSize: 64,
	}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// Run starts the agent inference loop for a single user message.
// Returns a read-only channel of Events (Go's AsyncGenerator equivalent).
//
// The consumer uses:
//
//	for event := range kernel.Run(ctx, "user message") {
//	    // render event
//	}
//
// Cancellation: close the context to stop the loop immediately.
// Backpressure: if the consumer is slow, the goroutine blocks on channel send.
func (r *Runner) Run(ctx context.Context, userInput string) <-chan Event {
	events := make(chan Event, r.eventBufferSize)

	go func() {
		defer close(events)
		r.runLoop(ctx, userInput, events)
	}()

	return events
}

// runLoop is the main inference loop.
func (r *Runner) runLoop(ctx context.Context, userInput string, events chan<- Event) {
	// Append user message to state (immutable transition)
	currentState := r.store.Update(func(prev state.AgentState) state.AgentState {
		return prev.
			WithMessage(llm.NewTextMessage(llm.RoleUser, userInput)).
			WithPhase(state.PhaseThinking)
	})

	hasError := false
	defer func() {
		if r.metrics != nil {
			r.metrics.RecordRunComplete(hasError)
		}
	}()

	for turn := 1; turn <= r.maxTurns; turn++ {
		// Check for cancellation
		if ctx.Err() != nil {
			r.emit(events, NewErrorEvent(ctx.Err(), turn))
			hasError = true
			return
		}

		// Update turn in state
		currentState = r.store.Update(func(prev state.AgentState) state.AgentState {
			return prev.WithTurn(turn).WithPhase(state.PhaseThinking)
		})

		r.emit(events, NewTransitionEvent(string(state.PhaseIdle), string(state.PhaseThinking), turn))

		if r.logger != nil {
			r.logger.WithTurn(turn).Info("starting inference turn",
				"messages", len(currentState.Messages))
		}

		// Build completion request (cache-friendly: system prompt stable, messages append-only)
		req := llm.CompletionRequest{
			SystemPrompt: currentState.SystemPrompt,
			Messages:     currentState.Messages,
			Tools:        r.registry.DefinitionsFiltered(r.pipeline.ExcludedTools()),
			MaxTokens:    r.maxTokens,
			Temperature:  r.temperature,
		}

		// Call LLM provider (returns streaming channel)
		llmStart := time.Now()
		streamCh, err := r.provider.Complete(ctx, req)
		if err != nil {
			r.emit(events, NewErrorEvent(fmt.Errorf("LLM error: %w", err), turn))
			r.store.Update(func(prev state.AgentState) state.AgentState {
				return prev.WithPhase(state.PhaseError)
			})
			if r.metrics != nil {
				r.metrics.RecordLLMCall(time.Since(llmStart), err)
			}
			hasError = true
			return
		}

		// Consume streaming events from LLM
		var assistantText string
		var toolCalls []llm.ContentBlock
		hasToolCalls := false

		for streamEvent := range streamCh {
			if ctx.Err() != nil {
				r.emit(events, NewErrorEvent(ctx.Err(), turn))
				hasError = true
				return
			}

			// Convert and forward to consumer
			kernelEvent := FromStreamEvent(streamEvent, turn)
			r.emit(events, kernelEvent)

			// Accumulate response
			switch streamEvent.Type {
			case llm.StreamTextDelta:
				assistantText += streamEvent.Text
			case llm.StreamToolCall:
				hasToolCalls = true
				toolCalls = append(toolCalls, llm.ContentBlock{
					Type:      llm.ContentToolUse,
					ToolUseID: streamEvent.ToolCallID,
					ToolName:  streamEvent.ToolName,
					ToolInput: streamEvent.ToolInput,
				})
			}
		}

		llmLatency := time.Since(llmStart)
		if r.metrics != nil {
			r.metrics.RecordLLMCall(llmLatency, nil)
			r.metrics.RecordTurn()
		}
		if r.logger != nil {
			r.logger.WithTurn(turn).Info("LLM response received",
				"latency", llmLatency.Round(time.Millisecond),
				"text_len", len(assistantText),
				"tool_calls", len(toolCalls))
		}

		// Build assistant message with all content blocks
		var contentBlocks []llm.ContentBlock
		if assistantText != "" {
			contentBlocks = append(contentBlocks, llm.ContentBlock{
				Type: llm.ContentText,
				Text: assistantText,
			})
		}
		contentBlocks = append(contentBlocks, toolCalls...)

		if len(contentBlocks) > 0 {
			assistantMsg := llm.Message{
				Role:    llm.RoleAssistant,
				Content: contentBlocks,
			}
			currentState = r.store.Update(func(prev state.AgentState) state.AgentState {
				return prev.WithMessage(assistantMsg)
			})
		}

		// If no tool calls, the LLM has provided its final answer
		if !hasToolCalls {
			r.store.Update(func(prev state.AgentState) state.AgentState {
				return prev.WithPhase(state.PhaseDone)
			})
			r.emit(events, NewDoneEvent(turn))

			// Auto-save checkpoint
			r.autoCheckpoint(currentState, turn)
			return
		}

		// Execute tool calls
		r.emit(events, NewTransitionEvent(string(state.PhaseThinking), string(state.PhaseToolUse), turn))

		for _, tc := range toolCalls {
			if ctx.Err() != nil {
				r.emit(events, NewErrorEvent(ctx.Err(), turn))
				hasError = true
				return
			}

			result := r.executeTool(ctx, tc, turn, events)

			// Append tool result to state
			currentState = r.store.Update(func(prev state.AgentState) state.AgentState {
				return prev.WithMessage(llm.NewToolResultMessage(
					tc.ToolUseID,
					result.Output,
					result.IsError,
				))
			})
		}

		// Auto-save checkpoint after tool execution
		r.autoCheckpoint(currentState, turn)

		// Continue to next turn (LLM will see tool results)
	}

	// Max turns exceeded
	r.emit(events, NewErrorEvent(fmt.Errorf("max turns (%d) exceeded", r.maxTurns), r.maxTurns))
	r.store.Update(func(prev state.AgentState) state.AgentState {
		return prev.WithPhase(state.PhaseError)
	})
	hasError = true
}

// executeTool runs a single tool call through the security pipeline.
func (r *Runner) executeTool(ctx context.Context, tc llm.ContentBlock, turn int, events chan<- Event) tool.Result {
	t, ok := r.registry.Get(tc.ToolName)
	if !ok {
		errResult := tool.NewErrorResultf("unknown tool: %s", tc.ToolName)
		r.emit(events, NewToolResultEvent(tc.ToolUseID, tc.ToolName, errResult, 0, turn))
		return errResult
	}

	if r.logger != nil {
		r.logger.WithTurn(turn).Tool(tc.ToolName).Info("security check starting")
	}

	// Security pipeline check (Principle 2: four-stage pipeline)
	checkResult, err := r.pipeline.Check(ctx, t, tc.ToolInput)
	if err != nil {
		errResult := tool.NewErrorResult(fmt.Errorf("security check error: %w", err))
		r.emit(events, NewToolResultEvent(tc.ToolUseID, tc.ToolName, errResult, 0, turn))
		return errResult
	}

	if checkResult.Decision == security.DecisionDeny {
		if r.logger != nil {
			r.logger.WithTurn(turn).Tool(tc.ToolName).Info("tool denied",
				"reason", checkResult.Reason, "stage", checkResult.Stage)
		}
		errResult := tool.NewErrorResultf("permission denied: %s", checkResult.Reason)
		r.emit(events, NewToolResultEvent(tc.ToolUseID, tc.ToolName, errResult, 0, turn))
		return errResult
	}

	// Execute the tool
	r.emit(events, NewToolCallEvent(tc.ToolUseID, tc.ToolName, tc.ToolInput, turn))

	start := time.Now()
	result, err := t.Execute(ctx, tc.ToolInput)
	elapsed := time.Since(start)

	if err != nil {
		result = tool.NewErrorResult(err)
	}

	// Record metrics
	if r.metrics != nil {
		r.metrics.RecordToolCall(tc.ToolName, elapsed, result.IsError)
	}
	if r.logger != nil {
		r.logger.WithTurn(turn).Tool(tc.ToolName).Info("tool executed",
			"elapsed", elapsed.Round(time.Millisecond),
			"is_error", result.IsError)
	}

	r.emit(events, NewToolResultEvent(tc.ToolUseID, tc.ToolName, result, elapsed, turn))
	return result
}

// autoCheckpoint saves a checkpoint if session manager is configured.
func (r *Runner) autoCheckpoint(st state.AgentState, turn int) {
	if r.session == nil || !r.session.ShouldAutoSave() {
		return
	}

	meta := map[string]string{
		"turn":     fmt.Sprintf("%d", turn),
		"provider": r.provider.Name(),
	}

	if _, err := r.session.Save(st, meta); err != nil {
		if r.logger != nil {
			r.logger.Warn("checkpoint save failed", "error", err)
		}
	}
}

// emit safely sends an event to the channel, respecting context cancellation.
func (r *Runner) emit(events chan<- Event, event Event) {
	select {
	case events <- event:
	default:
		// Channel full — block until space available
		events <- event
	}
}

// GetState returns the current agent state snapshot.
func (r *Runner) GetState() state.AgentState {
	return r.store.Get()
}

// GetMetrics returns the metrics collector, or nil if not configured.
func (r *Runner) GetMetrics() *metrics.Collector {
	return r.metrics
}

// SetProvider swaps the LLM provider at runtime (for model switching).
func (r *Runner) SetProvider(p llm.Provider) {
	r.provider = p
}

// ProviderName returns the current provider's display name.
func (r *Runner) ProviderName() string {
	return r.provider.Name()
}
