package builtin

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/akria/gak/pkg/tool"
)

// ReadFileTool reads file contents from the filesystem.
type ReadFileTool struct{}

func NewReadFileTool() *ReadFileTool { return &ReadFileTool{} }

func (r *ReadFileTool) Name() string { return "read_file" }

func (r *ReadFileTool) Description() string {
	return "Read the contents of a file at the given path. Optionally specify start_line and end_line for partial reading."
}

func (r *ReadFileTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Absolute path to the file to read",
			},
			"start_line": map[string]any{
				"type":        "integer",
				"description": "Starting line number (1-indexed, optional)",
			},
			"end_line": map[string]any{
				"type":        "integer",
				"description": "Ending line number (1-indexed, inclusive, optional)",
			},
		},
		"required": []string{"path"},
	}
}

func (r *ReadFileTool) ValidateInput(input map[string]any) error {
	path, ok := input["path"].(string)
	if !ok || path == "" {
		return fmt.Errorf("'path' is required and must be a non-empty string")
	}
	if !filepath.IsAbs(path) {
		return fmt.Errorf("'path' must be an absolute path, got: %s", path)
	}
	return nil
}

func (r *ReadFileTool) Risk(_ map[string]any) tool.RiskLevel {
	return tool.RiskNone // Pure read operation
}

func (r *ReadFileTool) Execute(_ context.Context, input map[string]any) (tool.Result, error) {
	path, _ := input["path"].(string)

	data, err := os.ReadFile(path)
	if err != nil {
		return tool.NewErrorResult(err), nil
	}

	content := string(data)

	// Handle optional line range
	startLine := 0
	endLine := 0
	if v, ok := input["start_line"]; ok {
		startLine = toInt(v)
	}
	if v, ok := input["end_line"]; ok {
		endLine = toInt(v)
	}

	if startLine > 0 || endLine > 0 {
		lines := splitLines(content)
		if startLine < 1 {
			startLine = 1
		}
		if endLine < 1 || endLine > len(lines) {
			endLine = len(lines)
		}
		if startLine > len(lines) {
			return tool.NewErrorResultf("start_line %d exceeds file length %d", startLine, len(lines)), nil
		}
		// Convert to 0-indexed
		selected := lines[startLine-1 : endLine]
		content = ""
		for i, line := range selected {
			lineNum := startLine + i
			content += fmt.Sprintf("%d\t%s\n", lineNum, line)
		}
	}

	// Truncate very large files
	const maxOutput = 64000
	if len(content) > maxOutput {
		content = content[:maxOutput] + "\n... (content truncated)"
	}

	return tool.NewResult(content), nil
}

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

func toInt(v any) int {
	switch val := v.(type) {
	case int:
		return val
	case float64:
		return int(val)
	case string:
		n, _ := strconv.Atoi(val)
		return n
	default:
		return 0
	}
}
