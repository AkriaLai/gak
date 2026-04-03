package builtin

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/akria/gak/pkg/tool"
)

// WriteFileTool writes content to a file.
type WriteFileTool struct{}

func NewWriteFileTool() *WriteFileTool { return &WriteFileTool{} }

func (w *WriteFileTool) Name() string { return "write_file" }

func (w *WriteFileTool) Description() string {
	return "Write content to a file at the given path. Creates parent directories if needed. Overwrites existing content."
}

func (w *WriteFileTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Absolute path to the file to write",
			},
			"content": map[string]any{
				"type":        "string",
				"description": "Content to write to the file",
			},
		},
		"required": []string{"path", "content"},
	}
}

func (w *WriteFileTool) ValidateInput(input map[string]any) error {
	path, ok := input["path"].(string)
	if !ok || path == "" {
		return fmt.Errorf("'path' is required and must be a non-empty string")
	}
	if !filepath.IsAbs(path) {
		return fmt.Errorf("'path' must be an absolute path, got: %s", path)
	}
	if _, ok := input["content"].(string); !ok {
		return fmt.Errorf("'content' is required and must be a string")
	}
	return nil
}

func (w *WriteFileTool) Risk(_ map[string]any) tool.RiskLevel {
	return tool.RiskMedium // File modification
}

func (w *WriteFileTool) Execute(_ context.Context, input map[string]any) (tool.Result, error) {
	path, _ := input["path"].(string)
	content, _ := input["content"].(string)

	// Create parent directories
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return tool.NewErrorResult(fmt.Errorf("creating directories: %w", err)), nil
	}

	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		return tool.NewErrorResult(err), nil
	}

	return tool.NewResult(fmt.Sprintf("Successfully wrote %d bytes to %s", len(content), path)), nil
}
