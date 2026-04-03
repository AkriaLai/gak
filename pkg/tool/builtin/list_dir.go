package builtin

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/akria/gak/pkg/tool"
)

// ListDirTool lists the contents of a directory.
type ListDirTool struct{}

func NewListDirTool() *ListDirTool { return &ListDirTool{} }

func (l *ListDirTool) Name() string { return "list_dir" }

func (l *ListDirTool) Description() string {
	return "List the contents of a directory, showing files and subdirectories with their sizes."
}

func (l *ListDirTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "Absolute path to the directory",
			},
		},
		"required": []string{"path"},
	}
}

func (l *ListDirTool) ValidateInput(input map[string]any) error {
	path, ok := input["path"].(string)
	if !ok || path == "" {
		return fmt.Errorf("'path' is required and must be a non-empty string")
	}
	if !filepath.IsAbs(path) {
		return fmt.Errorf("'path' must be an absolute path, got: %s", path)
	}
	return nil
}

func (l *ListDirTool) Risk(_ map[string]any) tool.RiskLevel {
	return tool.RiskNone
}

func (l *ListDirTool) Execute(_ context.Context, input map[string]any) (tool.Result, error) {
	path, _ := input["path"].(string)

	entries, err := os.ReadDir(path)
	if err != nil {
		return tool.NewErrorResult(err), nil
	}

	var sb strings.Builder
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			continue
		}

		if entry.IsDir() {
			sb.WriteString(fmt.Sprintf("[dir]  %s/\n", entry.Name()))
		} else {
			sb.WriteString(fmt.Sprintf("[file] %s (%s)\n", entry.Name(), humanSize(info.Size())))
		}
	}

	if sb.Len() == 0 {
		return tool.NewResult("(empty directory)"), nil
	}

	return tool.NewResult(sb.String()), nil
}

func humanSize(bytes int64) string {
	const (
		KB = 1024
		MB = KB * 1024
		GB = MB * 1024
	)
	switch {
	case bytes >= GB:
		return fmt.Sprintf("%.1fGB", float64(bytes)/float64(GB))
	case bytes >= MB:
		return fmt.Sprintf("%.1fMB", float64(bytes)/float64(MB))
	case bytes >= KB:
		return fmt.Sprintf("%.1fKB", float64(bytes)/float64(KB))
	default:
		return fmt.Sprintf("%dB", bytes)
	}
}
