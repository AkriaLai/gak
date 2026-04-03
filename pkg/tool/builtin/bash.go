// Package builtin provides commonly used tools built into the kernel.
package builtin

import (
	"context"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/akria/gak/pkg/tool"
)

// BashTool executes shell commands via /bin/bash.
// Risk is dynamically assessed based on the command content.
type BashTool struct {
	// Timeout is the maximum execution time for a command.
	Timeout time.Duration

	// DangerousPatterns are patterns that trigger RiskHigh.
	dangerousPatterns []*regexp.Regexp
}

// NewBashTool creates a new BashTool with default settings.
func NewBashTool() *BashTool {
	patterns := []string{
		`rm\s+-rf\s+/`,
		`rm\s+-rf\s+~`,
		`>\s*/dev/sd`,
		`mkfs`,
		`dd\s+if=`,
		`chmod\s+777`,
		`curl.*\|\s*sh`,
		`wget.*\|\s*sh`,
		`sudo\s+`,
	}

	compiled := make([]*regexp.Regexp, 0, len(patterns))
	for _, p := range patterns {
		if re, err := regexp.Compile(p); err == nil {
			compiled = append(compiled, re)
		}
	}

	return &BashTool{
		Timeout:           30 * time.Second,
		dangerousPatterns: compiled,
	}
}

func (b *BashTool) Name() string { return "bash" }

func (b *BashTool) Description() string {
	return "Execute a bash command. Use this to run shell commands, scripts, and system operations."
}

func (b *BashTool) InputSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"command": map[string]any{
				"type":        "string",
				"description": "The bash command to execute",
			},
		},
		"required": []string{"command"},
	}
}

func (b *BashTool) ValidateInput(input map[string]any) error {
	cmd, ok := input["command"].(string)
	if !ok || cmd == "" {
		return fmt.Errorf("'command' is required and must be a non-empty string")
	}
	return nil
}

// Risk dynamically assesses the risk level based on command content.
// This is the context-sensitive security check (Principle 2, Stage 3).
func (b *BashTool) Risk(input map[string]any) tool.RiskLevel {
	cmd, _ := input["command"].(string)

	// Check dangerous patterns
	for _, pattern := range b.dangerousPatterns {
		if pattern.MatchString(cmd) {
			return tool.RiskHigh
		}
	}

	// Write operations are medium risk
	writeIndicators := []string{"rm ", "mv ", "cp ", "> ", ">> ", "tee ", "sed -i", "chmod ", "chown "}
	for _, indicator := range writeIndicators {
		if strings.Contains(cmd, indicator) {
			return tool.RiskMedium
		}
	}

	// Read operations are low risk
	return tool.RiskLow
}

func (b *BashTool) Execute(ctx context.Context, input map[string]any) (tool.Result, error) {
	command, _ := input["command"].(string)

	// Apply timeout
	execCtx, cancel := context.WithTimeout(ctx, b.Timeout)
	defer cancel()

	cmd := exec.CommandContext(execCtx, "/bin/bash", "-c", command)

	output, err := cmd.CombinedOutput()
	outputStr := string(output)

	// Truncate very large output
	const maxOutput = 32000
	if len(outputStr) > maxOutput {
		outputStr = outputStr[:maxOutput] + "\n... (output truncated)"
	}

	if err != nil {
		if execCtx.Err() == context.DeadlineExceeded {
			return tool.NewErrorResultf("command timed out after %s\n%s", b.Timeout, outputStr), nil
		}
		// Command failed but produced output
		result := tool.Result{
			Output:  fmt.Sprintf("Exit code: %s\n%s", err.Error(), outputStr),
			IsError: true,
		}
		return result, nil
	}

	if outputStr == "" {
		outputStr = "(no output)"
	}

	return tool.NewResult(outputStr), nil
}
