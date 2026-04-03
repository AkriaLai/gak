// Package openai implements an OpenAI-compatible LLM provider.
//
// This provider works with:
//   - OpenAI official API (api.openai.com)
//   - Third-party compatible APIs: DeepSeek, Moonshot, Qwen, SiliconFlow,
//     ZhiPu, Groq, Together, Ollama, vLLM, LiteLLM, etc.
//
// Any service that implements the OpenAI Chat Completions API with
// streaming can be used by setting the appropriate base_url and api_key.
package openai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/akria/gak/pkg/llm"
)

const (
	defaultBaseURL = "https://api.openai.com/v1"
	defaultModel   = "gpt-4o"
)

// Client implements llm.Provider for OpenAI-compatible APIs.
type Client struct {
	apiKey  string
	baseURL string
	model   string
	client  *http.Client

	// providerName identifies this provider instance for display.
	providerName string
}

// Option configures the OpenAI client.
type Option func(*Client)

// WithAPIKey sets the API key explicitly (instead of env variable).
func WithAPIKey(key string) Option {
	return func(c *Client) {
		c.apiKey = key
	}
}

// WithBaseURL sets a custom API base URL for third-party providers.
// Examples:
//   - DeepSeek:    "https://api.deepseek.com/v1"
//   - Moonshot:    "https://api.moonshot.cn/v1"
//   - Qwen:        "https://dashscope.aliyuncs.com/compatible-mode/v1"
//   - SiliconFlow: "https://api.siliconflow.cn/v1"
//   - ZhiPu:       "https://open.bigmodel.cn/api/paas/v4"
//   - Groq:        "https://api.groq.com/openai/v1"
//   - Together:    "https://api.together.xyz/v1"
//   - Ollama:      "http://localhost:11434/v1"
func WithBaseURL(url string) Option {
	return func(c *Client) {
		c.baseURL = strings.TrimRight(url, "/")
	}
}

// WithModel sets the model identifier.
// Model names vary by provider:
//   - OpenAI:      "gpt-4o", "gpt-4o-mini", "o1"
//   - DeepSeek:    "deepseek-chat", "deepseek-reasoner"
//   - Moonshot:    "moonshot-v1-128k"
//   - Qwen:        "qwen-max", "qwen-plus"
//   - ZhiPu:       "glm-4-plus"
func WithModel(model string) Option {
	return func(c *Client) {
		c.model = model
	}
}

// WithHTTPClient sets a custom HTTP client (for proxies, timeouts, etc.).
func WithHTTPClient(client *http.Client) Option {
	return func(c *Client) {
		c.client = client
	}
}

// WithProviderName sets a display name (e.g., "deepseek" instead of "openai").
func WithProviderName(name string) Option {
	return func(c *Client) {
		c.providerName = name
	}
}

// New creates a new OpenAI-compatible client.
//
// API key resolution order:
//  1. Explicit WithAPIKey option
//  2. OPENAI_API_KEY environment variable
//  3. Error (except for local providers like Ollama)
func New(opts ...Option) (*Client, error) {
	c := &Client{
		baseURL:      defaultBaseURL,
		model:        defaultModel,
		client:       http.DefaultClient,
		providerName: "openai",
	}

	for _, opt := range opts {
		opt(c)
	}

	// Try environment variable if no explicit key
	if c.apiKey == "" {
		c.apiKey = os.Getenv("OPENAI_API_KEY")
	}

	// Allow empty API key for local providers (Ollama, etc.)
	isLocal := strings.Contains(c.baseURL, "localhost") ||
		strings.Contains(c.baseURL, "127.0.0.1")

	if c.apiKey == "" && !isLocal {
		return nil, fmt.Errorf("API key required: set via WithAPIKey() or OPENAI_API_KEY env var")
	}

	return c, nil
}

// Name returns the provider display name.
func (c *Client) Name() string {
	return c.providerName
}

// Complete sends a completion request and returns streaming events via channel.
func (c *Client) Complete(ctx context.Context, req llm.CompletionRequest) (<-chan llm.StreamEvent, error) {
	body := c.buildRequestBody(req)

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshaling request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST",
		c.baseURL+"/chat/completions", bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	resp, err := c.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("executing request: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(bodyBytes))
	}

	events := make(chan llm.StreamEvent, 32)
	go func() {
		defer close(events)
		defer resp.Body.Close()
		c.parseSSE(ctx, resp.Body, events)
	}()

	return events, nil
}

// buildRequestBody constructs the OpenAI Chat Completions API request.
func (c *Client) buildRequestBody(req llm.CompletionRequest) map[string]any {
	body := map[string]any{
		"model":      c.model,
		"max_tokens": req.MaxTokens,
		"stream":     true,
	}

	if req.Temperature > 0 {
		body["temperature"] = req.Temperature
	}

	// Build messages array
	messages := make([]map[string]any, 0, len(req.Messages)+1)

	// System message
	if req.SystemPrompt != "" {
		messages = append(messages, map[string]any{
			"role":    "system",
			"content": req.SystemPrompt,
		})
	}

	// Conversation messages
	for _, msg := range req.Messages {
		messages = append(messages, convertMessage(msg))
	}
	body["messages"] = messages

	// Tools (OpenAI function calling format)
	if len(req.Tools) > 0 {
		tools := make([]map[string]any, 0, len(req.Tools))
		for _, t := range req.Tools {
			tools = append(tools, map[string]any{
				"type": "function",
				"function": map[string]any{
					"name":        t.Name,
					"description": t.Description,
					"parameters":  t.InputSchema,
				},
			})
		}
		body["tools"] = tools
	}

	return body
}

// convertMessage converts an llm.Message to OpenAI API format.
func convertMessage(msg llm.Message) map[string]any {
	result := map[string]any{
		"role": string(msg.Role),
	}

	// Simple text message
	if len(msg.Content) == 1 && msg.Content[0].Type == llm.ContentText {
		result["content"] = msg.Content[0].Text
		return result
	}

	// Multi-content or tool messages
	// Accumulate all tool calls into a single array to avoid overwriting
	var toolCalls []map[string]any

	for _, block := range msg.Content {
		switch block.Type {
		case llm.ContentText:
			result["content"] = block.Text

		case llm.ContentToolUse:
			// Assistant requesting a tool call (OpenAI format: tool_calls array)
			inputJSON, _ := json.Marshal(block.ToolInput)
			toolCalls = append(toolCalls, map[string]any{
				"id":   block.ToolUseID,
				"type": "function",
				"function": map[string]any{
					"name":      block.ToolName,
					"arguments": string(inputJSON),
				},
			})

		case llm.ContentToolResult:
			// Tool result message
			result["role"] = "tool"
			result["tool_call_id"] = block.ToolCallID
			result["content"] = block.Content
		}
	}

	if len(toolCalls) > 0 {
		result["tool_calls"] = toolCalls
	}

	return result
}

// parseSSE parses the OpenAI streaming response.
// It also handles non-streaming JSON responses as a fallback for models
// (e.g., some proxy services) that ignore the stream=true flag.
func (c *Client) parseSSE(ctx context.Context, body io.Reader, events chan<- llm.StreamEvent) {
	// Peek at the first byte to detect response format without consuming the stream
	br := bufio.NewReaderSize(body, 64*1024)
	firstByte, err := br.Peek(1)
	if err != nil {
		events <- llm.StreamEvent{Type: llm.StreamError, Error: fmt.Errorf("reading response: %w", err)}
		return
	}

	// Non-streaming fallback: response starts with '{' (JSON) instead of 'd' (data: SSE)
	if firstByte[0] == '{' {
		rawBytes, err := io.ReadAll(br)
		if err != nil {
			events <- llm.StreamEvent{Type: llm.StreamError, Error: fmt.Errorf("reading response body: %w", err)}
			return
		}
		c.parseNonStreaming(strings.TrimSpace(string(rawBytes)), events)
		return
	}

	// --- Standard SSE streaming path (reads directly from network, preserving real-time flow) ---
	scanner := bufio.NewScanner(br)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	// Accumulators for tool calls
	type toolCallAcc struct {
		id       string
		name     string
		argsJSON strings.Builder
	}
	toolCalls := make(map[int]*toolCallAcc)

	for scanner.Scan() {
		if ctx.Err() != nil {
			events <- llm.StreamEvent{Type: llm.StreamError, Error: ctx.Err()}
			return
		}

		line := scanner.Text()

		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			// Emit accumulated tool calls
			for _, tc := range toolCalls {
				var toolInput map[string]any
				argsStr := tc.argsJSON.String()
				if argsStr != "" {
					if err := json.Unmarshal([]byte(argsStr), &toolInput); err != nil {
						toolInput = map[string]any{"raw": argsStr}
					}
				} else {
					toolInput = map[string]any{}
				}

				events <- llm.StreamEvent{
					Type:       llm.StreamToolCall,
					ToolCallID: tc.id,
					ToolName:   tc.name,
					ToolInput:  toolInput,
				}
			}

			events <- llm.StreamEvent{Type: llm.StreamDone}
			return
		}

		var chunk map[string]any
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}

		choices, ok := chunk["choices"].([]any)
		if !ok || len(choices) == 0 {
			continue
		}

		choice, ok := choices[0].(map[string]any)
		if !ok {
			continue
		}

		// Support both streaming "delta" and non-streaming "message" keys
		delta, ok := choice["delta"].(map[string]any)
		if !ok {
			delta, ok = choice["message"].(map[string]any)
			if !ok {
				continue
			}
		}

		// Text content
		if content, ok := delta["content"].(string); ok && content != "" {
			events <- llm.StreamEvent{
				Type: llm.StreamTextDelta,
				Text: content,
			}
		}

		// Reasoning content (for models like DeepSeek-R1, GLM-5, o1)
		if reasoning, ok := delta["reasoning_content"].(string); ok && reasoning != "" {
			events <- llm.StreamEvent{
				Type: llm.StreamThinking,
				Text: reasoning,
			}
		}

		// Tool calls (streamed incrementally)
		if tcArr, ok := delta["tool_calls"].([]any); ok {
			for _, tcRaw := range tcArr {
				tc, ok := tcRaw.(map[string]any)
				if !ok {
					continue
				}

				idx := 0
				if idxFloat, ok := tc["index"].(float64); ok {
					idx = int(idxFloat)
				}

				if _, exists := toolCalls[idx]; !exists {
					toolCalls[idx] = &toolCallAcc{}
				}

				if id, ok := tc["id"].(string); ok && id != "" {
					toolCalls[idx].id = id
				}

				if fn, ok := tc["function"].(map[string]any); ok {
					if name, ok := fn["name"].(string); ok && name != "" {
						toolCalls[idx].name = name
					}
					if args, ok := fn["arguments"].(string); ok {
						toolCalls[idx].argsJSON.WriteString(args)
					}
				}
			}
		}
	}

	if err := scanner.Err(); err != nil {
		events <- llm.StreamEvent{
			Type:  llm.StreamError,
			Error: fmt.Errorf("reading stream: %w", err),
		}
	}
}

// parseNonStreaming handles a complete (non-streaming) JSON response.
// This is a fallback for APIs that ignore "stream":true and return a full response.
func (c *Client) parseNonStreaming(raw string, events chan<- llm.StreamEvent) {
	var resp map[string]any
	if err := json.Unmarshal([]byte(raw), &resp); err != nil {
		events <- llm.StreamEvent{Type: llm.StreamError, Error: fmt.Errorf("parsing non-streaming response: %w", err)}
		return
	}

	choices, ok := resp["choices"].([]any)
	if !ok || len(choices) == 0 {
		// Check for error response
		if errObj, ok := resp["error"].(map[string]any); ok {
			msg, _ := errObj["message"].(string)
			events <- llm.StreamEvent{Type: llm.StreamError, Error: fmt.Errorf("API error: %s", msg)}
		} else {
			events <- llm.StreamEvent{Type: llm.StreamError, Error: fmt.Errorf("no choices in response")}
		}
		return
	}

	choice, ok := choices[0].(map[string]any)
	if !ok {
		events <- llm.StreamEvent{Type: llm.StreamError, Error: fmt.Errorf("invalid choice format")}
		return
	}

	message, ok := choice["message"].(map[string]any)
	if !ok {
		events <- llm.StreamEvent{Type: llm.StreamError, Error: fmt.Errorf("no message in choice")}
		return
	}

	// Emit reasoning/thinking content first
	if reasoning, ok := message["reasoning_content"].(string); ok && reasoning != "" {
		events <- llm.StreamEvent{
			Type: llm.StreamThinking,
			Text: reasoning,
		}
	}

	// Emit main content
	if content, ok := message["content"].(string); ok && content != "" {
		events <- llm.StreamEvent{
			Type: llm.StreamTextDelta,
			Text: content,
		}
	}

	// Handle tool calls
	if tcArr, ok := message["tool_calls"].([]any); ok {
		for _, tcRaw := range tcArr {
			tc, ok := tcRaw.(map[string]any)
			if !ok {
				continue
			}
			id, _ := tc["id"].(string)
			fn, _ := tc["function"].(map[string]any)
			name, _ := fn["name"].(string)
			argsStr, _ := fn["arguments"].(string)

			var toolInput map[string]any
			if argsStr != "" {
				if err := json.Unmarshal([]byte(argsStr), &toolInput); err != nil {
					toolInput = map[string]any{"raw": argsStr}
				}
			} else {
				toolInput = map[string]any{}
			}

			events <- llm.StreamEvent{
				Type:       llm.StreamToolCall,
				ToolCallID: id,
				ToolName:   name,
				ToolInput:  toolInput,
			}
		}
	}

	events <- llm.StreamEvent{Type: llm.StreamDone}
}
