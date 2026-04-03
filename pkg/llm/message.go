// Package llm defines the LLM provider abstraction and message types.
package llm

// Role represents the sender of a message.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// ContentType identifies the kind of content in a content block.
type ContentType string

const (
	ContentText       ContentType = "text"
	ContentToolUse    ContentType = "tool_use"
	ContentToolResult ContentType = "tool_result"
)

// ContentBlock represents a single block of content within a message.
// A message can contain multiple content blocks (e.g., text + tool calls).
type ContentBlock struct {
	Type ContentType `json:"type"`

	// For text content
	Text string `json:"text,omitempty"`

	// For tool_use content (assistant requesting a tool call)
	ToolUseID string         `json:"id,omitempty"`
	ToolName  string         `json:"name,omitempty"`
	ToolInput map[string]any `json:"input,omitempty"`

	// For tool_result content (tool execution result)
	ToolCallID string `json:"tool_use_id,omitempty"`
	Content    string `json:"content,omitempty"`
	IsError    bool   `json:"is_error,omitempty"`
}

// Message represents a single message in the conversation history.
// Messages are treated as immutable once added to the history (Principle 3 & 5).
type Message struct {
	Role    Role           `json:"role"`
	Content []ContentBlock `json:"content"`
}

// NewTextMessage creates a simple text message.
func NewTextMessage(role Role, text string) Message {
	return Message{
		Role: role,
		Content: []ContentBlock{
			{Type: ContentText, Text: text},
		},
	}
}

// NewToolUseMessage creates an assistant message requesting a tool call.
func NewToolUseMessage(toolUseID, toolName string, toolInput map[string]any) Message {
	return Message{
		Role: RoleAssistant,
		Content: []ContentBlock{
			{
				Type:      ContentToolUse,
				ToolUseID: toolUseID,
				ToolName:  toolName,
				ToolInput: toolInput,
			},
		},
	}
}

// NewToolResultMessage creates a user message containing a tool execution result.
func NewToolResultMessage(toolCallID, content string, isError bool) Message {
	return Message{
		Role: RoleUser,
		Content: []ContentBlock{
			{
				Type:       ContentToolResult,
				ToolCallID: toolCallID,
				Content:    content,
				IsError:    isError,
			},
		},
	}
}

// GetText extracts the first text content from a message, if any.
func (m Message) GetText() string {
	for _, block := range m.Content {
		if block.Type == ContentText {
			return block.Text
		}
	}
	return ""
}

// GetToolCalls extracts all tool use blocks from a message.
func (m Message) GetToolCalls() []ContentBlock {
	var calls []ContentBlock
	for _, block := range m.Content {
		if block.Type == ContentToolUse {
			calls = append(calls, block)
		}
	}
	return calls
}

// HasToolCalls returns true if the message contains any tool use blocks.
func (m Message) HasToolCalls() bool {
	for _, block := range m.Content {
		if block.Type == ContentToolUse {
			return true
		}
	}
	return false
}
