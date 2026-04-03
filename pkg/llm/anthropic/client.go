// Package anthropic implements the Anthropic Claude API provider.
package anthropic

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
	defaultBaseURL = "https://api.anthropic.com"
	apiVersion     = "2023-06-01"
	defaultModel   = "claude-sonnet-4-20250514"
)

// Client implements llm.Provider for the Anthropic Claude API.
type Client struct {
	apiKey  string
	baseURL string
	model   string
	client  *http.Client
}

// Option configures the Anthropic client.
type Option func(*Client)

// WithBaseURL sets a custom API base URL (useful for proxies).
func WithBaseURL(url string) Option {
	return func(c *Client) {
		c.baseURL = url
	}
}

// WithModel sets the model to use.
func WithModel(model string) Option {
	return func(c *Client) {
		c.model = model
	}
}

// WithHTTPClient sets a custom HTTP client.
func WithHTTPClient(client *http.Client) Option {
	return func(c *Client) {
		c.client = client
	}
}

// New creates a new Anthropic client.
// API key is read from ANTHROPIC_API_KEY environment variable if not provided.
func New(opts ...Option) (*Client, error) {
	c := &Client{
		apiKey:  os.Getenv("ANTHROPIC_API_KEY"),
		baseURL: defaultBaseURL,
		model:   defaultModel,
		client:  http.DefaultClient,
	}

	for _, opt := range opts {
		opt(c)
	}

	if c.apiKey == "" {
		return nil, fmt.Errorf("ANTHROPIC_API_KEY environment variable is required")
	}

	return c, nil
}

// Name returns the provider identifier.
func (c *Client) Name() string {
	return "anthropic"
}

// Complete sends a completion request and returns streaming events via channel.
// This implements llm.Provider — the Go equivalent of AsyncGenerator.
func (c *Client) Complete(ctx context.Context, req llm.CompletionRequest) (<-chan llm.StreamEvent, error) {
	// Build API request body
	body := c.buildRequestBody(req)

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshaling request: %w", err)
	}

	// Create HTTP request
	httpReq, err := http.NewRequestWithContext(ctx, "POST",
		c.baseURL+"/v1/messages", bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", c.apiKey)
	httpReq.Header.Set("anthropic-version", apiVersion)

	// Execute request
	resp, err := c.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("executing request: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		bodyBytes, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API error (status %d): %s", resp.StatusCode, string(bodyBytes))
	}

	// Parse streaming response in a goroutine
	events := make(chan llm.StreamEvent, 32)
	go func() {
		defer close(events)
		defer resp.Body.Close()
		c.parseSSE(ctx, resp.Body, events)
	}()

	return events, nil
}

// buildRequestBody constructs the Anthropic API request.
func (c *Client) buildRequestBody(req llm.CompletionRequest) map[string]any {
	body := map[string]any{
		"model":      c.model,
		"max_tokens": req.MaxTokens,
		"stream":     true,
	}

	if req.Temperature > 0 {
		body["temperature"] = req.Temperature
	}

	if req.SystemPrompt != "" {
		body["system"] = req.SystemPrompt
	}

	// Convert messages
	messages := make([]map[string]any, 0, len(req.Messages))
	for _, msg := range req.Messages {
		messages = append(messages, convertMessage(msg))
	}
	body["messages"] = messages

	// Convert tools
	if len(req.Tools) > 0 {
		tools := make([]map[string]any, 0, len(req.Tools))
		for _, t := range req.Tools {
			tools = append(tools, map[string]any{
				"name":         t.Name,
				"description":  t.Description,
				"input_schema": t.InputSchema,
			})
		}
		body["tools"] = tools
	}

	return body
}

// convertMessage converts an llm.Message to the Anthropic API format.
func convertMessage(msg llm.Message) map[string]any {
	result := map[string]any{
		"role": string(msg.Role),
	}

	if len(msg.Content) == 1 && msg.Content[0].Type == llm.ContentText {
		result["content"] = msg.Content[0].Text
	} else {
		content := make([]map[string]any, 0, len(msg.Content))
		for _, block := range msg.Content {
			switch block.Type {
			case llm.ContentText:
				content = append(content, map[string]any{
					"type": "text",
					"text": block.Text,
				})
			case llm.ContentToolUse:
				content = append(content, map[string]any{
					"type":  "tool_use",
					"id":    block.ToolUseID,
					"name":  block.ToolName,
					"input": block.ToolInput,
				})
			case llm.ContentToolResult:
				content = append(content, map[string]any{
					"type":       "tool_result",
					"tool_use_id": block.ToolCallID,
					"content":    block.Content,
					"is_error":   block.IsError,
				})
			}
		}
		result["content"] = content
	}

	return result
}

// parseSSE parses Server-Sent Events from the Anthropic streaming response.
func (c *Client) parseSSE(ctx context.Context, body io.Reader, events chan<- llm.StreamEvent) {
	scanner := bufio.NewScanner(body)
	// Increase scanner buffer for large events
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var currentToolCallID string
	var currentToolName string
	var toolInputAccumulator strings.Builder

	for scanner.Scan() {
		if ctx.Err() != nil {
			events <- llm.StreamEvent{Type: llm.StreamError, Error: ctx.Err()}
			return
		}

		line := scanner.Text()

		// SSE format: "event: <type>" followed by "data: <json>"
		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			return
		}

		var event map[string]any
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			continue
		}

		eventType, _ := event["type"].(string)

		switch eventType {
		case "content_block_start":
			if cb, ok := event["content_block"].(map[string]any); ok {
				if cbType, _ := cb["type"].(string); cbType == "tool_use" {
					currentToolCallID, _ = cb["id"].(string)
					currentToolName, _ = cb["name"].(string)
					toolInputAccumulator.Reset()
				}
			}

		case "content_block_delta":
			if delta, ok := event["delta"].(map[string]any); ok {
				deltaType, _ := delta["type"].(string)
				switch deltaType {
				case "text_delta":
					text, _ := delta["text"].(string)
					events <- llm.StreamEvent{
						Type: llm.StreamTextDelta,
						Text: text,
					}
				case "thinking_delta":
					text, _ := delta["thinking"].(string)
					events <- llm.StreamEvent{
						Type: llm.StreamThinking,
						Text: text,
					}
				case "input_json_delta":
					partial, _ := delta["partial_json"].(string)
					toolInputAccumulator.WriteString(partial)
				}
			}

		case "content_block_stop":
			// If we were accumulating tool input, emit the tool call
			if currentToolCallID != "" {
				var toolInput map[string]any
				inputStr := toolInputAccumulator.String()
				if inputStr != "" {
					if err := json.Unmarshal([]byte(inputStr), &toolInput); err != nil {
						toolInput = map[string]any{"raw": inputStr}
					}
				} else {
					toolInput = map[string]any{}
				}

				events <- llm.StreamEvent{
					Type:       llm.StreamToolCall,
					ToolCallID: currentToolCallID,
					ToolName:   currentToolName,
					ToolInput:  toolInput,
				}

				currentToolCallID = ""
				currentToolName = ""
				toolInputAccumulator.Reset()
			}

		case "message_stop":
			events <- llm.StreamEvent{Type: llm.StreamDone}
			return

		case "error":
			errData, _ := event["error"].(map[string]any)
			errMsg, _ := errData["message"].(string)
			events <- llm.StreamEvent{
				Type:  llm.StreamError,
				Error: fmt.Errorf("API error: %s", errMsg),
			}
			return
		}
	}

	if err := scanner.Err(); err != nil {
		events <- llm.StreamEvent{
			Type:  llm.StreamError,
			Error: fmt.Errorf("reading stream: %w", err),
		}
	}
}
