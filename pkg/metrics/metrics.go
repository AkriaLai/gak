// Package metrics implements runtime monitoring for the agent kernel.
//
// Tracks:
//   - Token usage (input/output/cached) and estimated cost
//   - LLM call latency (per-turn and cumulative)
//   - Tool call frequency, success rate, and duration
//   - Turn counts and error rates
//   - Cache hit rates
package metrics

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Collector aggregates runtime metrics across the agent's lifetime.
// Thread-safe for concurrent access from multiple goroutines.
type Collector struct {
	mu sync.RWMutex

	// Token metrics
	inputTokens  atomic.Int64
	outputTokens atomic.Int64
	cachedTokens atomic.Int64

	// LLM call metrics
	llmCalls          atomic.Int64
	llmTotalLatency   atomic.Int64 // nanoseconds
	llmErrors         atomic.Int64

	// Tool metrics
	toolCalls   map[string]*ToolMetrics
	toolCallsMu sync.RWMutex

	// Turn metrics
	totalTurns    atomic.Int64
	completedRuns atomic.Int64
	erroredRuns   atomic.Int64

	// Timing
	startTime time.Time
}

// ToolMetrics tracks per-tool statistics.
type ToolMetrics struct {
	Name         string        `json:"name"`
	Calls        int64         `json:"calls"`
	Errors       int64         `json:"errors"`
	TotalLatency time.Duration `json:"total_latency"`
	MaxLatency   time.Duration `json:"max_latency"`
}

// NewCollector creates a new metrics collector.
func NewCollector() *Collector {
	return &Collector{
		toolCalls: make(map[string]*ToolMetrics),
		startTime: time.Now(),
	}
}

// --- Token tracking ---

// RecordTokens records token usage for an LLM call.
func (c *Collector) RecordTokens(input, output, cached int64) {
	c.inputTokens.Add(input)
	c.outputTokens.Add(output)
	c.cachedTokens.Add(cached)
}

// --- LLM call tracking ---

// RecordLLMCall records an LLM API call with its latency.
func (c *Collector) RecordLLMCall(latency time.Duration, err error) {
	c.llmCalls.Add(1)
	c.llmTotalLatency.Add(int64(latency))
	if err != nil {
		c.llmErrors.Add(1)
	}
}

// --- Tool tracking ---

// RecordToolCall records a tool invocation.
func (c *Collector) RecordToolCall(toolName string, latency time.Duration, isError bool) {
	c.toolCallsMu.Lock()
	defer c.toolCallsMu.Unlock()

	tm, ok := c.toolCalls[toolName]
	if !ok {
		tm = &ToolMetrics{Name: toolName}
		c.toolCalls[toolName] = tm
	}

	tm.Calls++
	tm.TotalLatency += latency
	if latency > tm.MaxLatency {
		tm.MaxLatency = latency
	}
	if isError {
		tm.Errors++
	}
}

// --- Turn tracking ---

// RecordTurn records a completed inference turn.
func (c *Collector) RecordTurn() {
	c.totalTurns.Add(1)
}

// RecordRunComplete records a completed agent run.
func (c *Collector) RecordRunComplete(hasError bool) {
	if hasError {
		c.erroredRuns.Add(1)
	} else {
		c.completedRuns.Add(1)
	}
}

// --- Snapshot ---

// Snapshot captures the current state of all metrics.
type Snapshot struct {
	// Uptime
	Uptime time.Duration `json:"uptime"`

	// Tokens
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
	CachedTokens int64 `json:"cached_tokens"`
	TotalTokens  int64 `json:"total_tokens"`

	// LLM
	LLMCalls       int64         `json:"llm_calls"`
	LLMErrors      int64         `json:"llm_errors"`
	LLMAvgLatency  time.Duration `json:"llm_avg_latency"`

	// Tools
	ToolStats []ToolMetrics `json:"tool_stats"`

	// Turns
	TotalTurns    int64   `json:"total_turns"`
	CompletedRuns int64   `json:"completed_runs"`
	ErroredRuns   int64   `json:"errored_runs"`
	ErrorRate     float64 `json:"error_rate"`

	// Cost estimate (approximate, based on Anthropic pricing)
	EstimatedCostUSD float64 `json:"estimated_cost_usd"`
}

// Snapshot captures the current metrics state.
func (c *Collector) Snapshot() Snapshot {
	inputTokens := c.inputTokens.Load()
	outputTokens := c.outputTokens.Load()
	cachedTokens := c.cachedTokens.Load()
	llmCalls := c.llmCalls.Load()
	llmErrors := c.llmErrors.Load()
	llmTotalLatency := c.llmTotalLatency.Load()
	completedRuns := c.completedRuns.Load()
	erroredRuns := c.erroredRuns.Load()

	var avgLatency time.Duration
	if llmCalls > 0 {
		avgLatency = time.Duration(llmTotalLatency / llmCalls)
	}

	totalRuns := completedRuns + erroredRuns
	var errorRate float64
	if totalRuns > 0 {
		errorRate = float64(erroredRuns) / float64(totalRuns) * 100
	}

	// Approximate cost (Anthropic Claude Sonnet pricing)
	// Input: $3/MTok, Output: $15/MTok, Cached: $0.30/MTok
	cost := float64(inputTokens-cachedTokens)*3.0/1_000_000 +
		float64(cachedTokens)*0.30/1_000_000 +
		float64(outputTokens)*15.0/1_000_000

	// Collect tool stats
	c.toolCallsMu.RLock()
	toolStats := make([]ToolMetrics, 0, len(c.toolCalls))
	for _, tm := range c.toolCalls {
		toolStats = append(toolStats, *tm)
	}
	c.toolCallsMu.RUnlock()

	return Snapshot{
		Uptime:           time.Since(c.startTime),
		InputTokens:      inputTokens,
		OutputTokens:     outputTokens,
		CachedTokens:     cachedTokens,
		TotalTokens:      inputTokens + outputTokens,
		LLMCalls:         llmCalls,
		LLMErrors:        llmErrors,
		LLMAvgLatency:    avgLatency,
		ToolStats:        toolStats,
		TotalTurns:       c.totalTurns.Load(),
		CompletedRuns:    completedRuns,
		ErroredRuns:      erroredRuns,
		ErrorRate:        errorRate,
		EstimatedCostUSD: cost,
	}
}

// String returns a human-readable summary.
func (s Snapshot) String() string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("  ⏱ Uptime:  %s\n", s.Uptime.Round(time.Second)))
	sb.WriteString(fmt.Sprintf("  📊 Tokens:  %d in / %d out / %d cached\n",
		s.InputTokens, s.OutputTokens, s.CachedTokens))
	sb.WriteString(fmt.Sprintf("  🤖 LLM:     %d calls (avg %s, %d errors)\n",
		s.LLMCalls, s.LLMAvgLatency.Round(time.Millisecond), s.LLMErrors))
	sb.WriteString(fmt.Sprintf("  🔧 Turns:   %d total, %d runs (%d ok, %d err, %.1f%% error rate)\n",
		s.TotalTurns, s.CompletedRuns+s.ErroredRuns, s.CompletedRuns, s.ErroredRuns, s.ErrorRate))
	sb.WriteString(fmt.Sprintf("  💰 Cost:    ~$%.4f\n", s.EstimatedCostUSD))

	if len(s.ToolStats) > 0 {
		sb.WriteString("  🛠 Tools:\n")
		for _, ts := range s.ToolStats {
			avg := time.Duration(0)
			if ts.Calls > 0 {
				avg = ts.TotalLatency / time.Duration(ts.Calls)
			}
			sb.WriteString(fmt.Sprintf("     %-20s %d calls (avg %s, max %s, %d errors)\n",
				ts.Name, ts.Calls, avg.Round(time.Millisecond), ts.MaxLatency.Round(time.Millisecond), ts.Errors))
		}
	}

	return sb.String()
}

// JSON returns the snapshot as JSON.
func (s Snapshot) JSON() string {
	data, _ := json.MarshalIndent(s, "", "  ")
	return string(data)
}
