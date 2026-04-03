// Package cache implements Prompt caching optimization.
//
// Core design principle: 缓存感知设计 (Principle 3)
//
// LLM API providers (Anthropic, OpenAI) support Prompt Caching —
// if the prefix of the prompt matches a previous call, the cached
// portion is served at reduced cost and latency.
//
// This package ensures cache-friendliness by:
//   1. Stabilizing system prompt construction (deterministic byte content)
//   2. Making message history append-only (never modifying sent messages)
//   3. Managing cache control breakpoints for optimal cache hits
package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/akria/gak/pkg/llm"
)

// PromptBuilder constructs system prompts in a cache-friendly manner.
// The key insight: even trivial changes (tool list ordering, whitespace)
// can invalidate the entire prompt cache. This builder ensures deterministic output.
type PromptBuilder struct {
	// sections are ordered, named prompt sections.
	sections []section
}

type section struct {
	name     string
	content  string
	priority int // Lower = earlier in prompt
}

// NewPromptBuilder creates a new builder.
func NewPromptBuilder() *PromptBuilder {
	return &PromptBuilder{}
}

// AddSection adds a named section to the prompt.
// Sections are always rendered in priority order for determinism.
func (b *PromptBuilder) AddSection(name, content string, priority int) {
	b.sections = append(b.sections, section{
		name:     name,
		content:  content,
		priority: priority,
	})
}

// Build generates the final system prompt with deterministic ordering.
// The returned hash can be used to verify cache stability across calls.
func (b *PromptBuilder) Build() (prompt string, hash string) {
	// Sort by priority for deterministic output
	sorted := make([]section, len(b.sections))
	copy(sorted, b.sections)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].priority != sorted[j].priority {
			return sorted[i].priority < sorted[j].priority
		}
		return sorted[i].name < sorted[j].name
	})

	var sb strings.Builder
	for _, sec := range sorted {
		sb.WriteString(sec.content)
		sb.WriteString("\n\n")
	}

	prompt = strings.TrimSpace(sb.String())

	// Generate hash for cache tracking
	h := sha256.Sum256([]byte(prompt))
	hash = hex.EncodeToString(h[:8]) // Short hash is sufficient

	return prompt, hash
}

// BuildToolSection creates a deterministic tool list section.
// Tools are sorted by name to ensure consistent ordering.
func BuildToolSection(tools []llm.ToolDefinition) string {
	// Sort tools by name for deterministic output
	sorted := make([]llm.ToolDefinition, len(tools))
	copy(sorted, tools)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Name < sorted[j].Name
	})

	var sb strings.Builder
	sb.WriteString("Available tools:\n")
	for _, t := range sorted {
		sb.WriteString(fmt.Sprintf("- %s: %s\n", t.Name, t.Description))
	}

	return sb.String()
}

// CacheBreakpoints manages cache control breakpoints for Anthropic API.
// By placing cache_control markers at strategic points in the message array,
// we maximize cache hit rates across multi-turn conversations.
type CacheBreakpoints struct {
	// SystemPromptCached tracks whether the system prompt has been cached.
	SystemPromptHash string

	// LastCachedMessageIndex tracks the last message index that was marked
	// for caching. New messages after this index are the only "new" content.
	LastCachedMessageIndex int
}

// NewCacheBreakpoints creates a new cache breakpoint tracker.
func NewCacheBreakpoints() *CacheBreakpoints {
	return &CacheBreakpoints{
		LastCachedMessageIndex: -1,
	}
}

// ShouldCacheSystemPrompt returns true if the system prompt has changed
// and needs a new cache control marker.
func (cb *CacheBreakpoints) ShouldCacheSystemPrompt(hash string) bool {
	if cb.SystemPromptHash == hash {
		return false
	}
	cb.SystemPromptHash = hash
	return true
}

// UpdateMessageBreakpoint advances the cache breakpoint to the given index.
// Messages before this index are considered "cached" and don't need reprocessing.
func (cb *CacheBreakpoints) UpdateMessageBreakpoint(index int) {
	if index > cb.LastCachedMessageIndex {
		cb.LastCachedMessageIndex = index
	}
}

// NewMessagesFrom returns the index from which messages are "new" (uncached).
func (cb *CacheBreakpoints) NewMessagesFrom() int {
	return cb.LastCachedMessageIndex + 1
}

// Stats holds cache performance statistics.
type Stats struct {
	TotalCalls      int64   `json:"total_calls"`
	CacheHits       int64   `json:"cache_hits"`
	CacheMisses     int64   `json:"cache_misses"`
	CachedTokens    int64   `json:"cached_tokens"`
	UncachedTokens  int64   `json:"uncached_tokens"`
	EstimatedSaving float64 `json:"estimated_saving_usd"`
}

// HitRate returns the cache hit rate as a percentage.
func (s Stats) HitRate() float64 {
	if s.TotalCalls == 0 {
		return 0
	}
	return float64(s.CacheHits) / float64(s.TotalCalls) * 100
}

// RecordHit records a cache hit with the number of cached tokens.
func (s *Stats) RecordHit(cachedTokens int64) {
	s.TotalCalls++
	s.CacheHits++
	s.CachedTokens += cachedTokens
}

// RecordMiss records a cache miss.
func (s *Stats) RecordMiss(tokens int64) {
	s.TotalCalls++
	s.CacheMisses++
	s.UncachedTokens += tokens
}

func (s Stats) String() string {
	return fmt.Sprintf("Cache: %d/%d hits (%.1f%%), %d cached tokens, ~$%.4f saved",
		s.CacheHits, s.TotalCalls, s.HitRate(), s.CachedTokens, s.EstimatedSaving)
}
