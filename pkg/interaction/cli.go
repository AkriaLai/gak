package interaction

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/manifoldco/promptui"
	"github.com/akria/gak/pkg/tool"
)

// CLIProvider implements the Provider interface for terminal interaction.
type CLIProvider struct {
	reader *bufio.Reader
}

// NewCLIProvider creates a new CLI interaction provider.
func NewCLIProvider() *CLIProvider {
	return &CLIProvider{
		reader: bufio.NewReader(os.Stdin),
	}
}

// Confirm asks the user to approve a dangerous operation via terminal.
func (c *CLIProvider) Confirm(ctx context.Context, toolName, description string, risk tool.RiskLevel) (bool, error) {
	riskColor := riskColorCode(risk)
	fmt.Printf("\n%s⚠ PERMISSION REQUEST%s\n", riskColor, colorReset)
	fmt.Printf("  Tool:  %s\n", toolName)
	fmt.Printf("  Risk:  %s%s%s\n", riskColor, risk, colorReset)
	fmt.Printf("  Info:  %s\n", description)

	// Check context before blocking on input
	select {
	case <-ctx.Done():
		fmt.Println()
		return false, ctx.Err()
	default:
	}

	prompt := promptui.Select{
		Label: "Action",
		Items: []string{"Accept", "Reject"},
	}

	_, result, err := prompt.Run()
	if err != nil {
		if err == promptui.ErrInterrupt {
			return false, fmt.Errorf("action interrupted")
		}
		return false, fmt.Errorf("prompt failed: %w", err)
	}

	return result == "Accept", nil
}

// Prompt asks the user for text input via terminal.
func (c *CLIProvider) Prompt(ctx context.Context, message string) (string, error) {
	fmt.Printf("%s: ", message)

	select {
	case <-ctx.Done():
		fmt.Println()
		return "", ctx.Err()
	default:
	}

	line, err := c.reader.ReadString('\n')
	if err != nil {
		return "", fmt.Errorf("reading input: %w", err)
	}

	return strings.TrimSpace(line), nil
}

// Notify sends an informational message to the terminal.
func (c *CLIProvider) Notify(_ context.Context, message string) error {
	fmt.Printf("%s\n", message)
	return nil
}

// --- ANSI color codes ---

const (
	colorReset  = "\033[0m"
	colorYellow = "\033[33m"
	colorRed    = "\033[31m"
	colorOrange = "\033[38;5;208m"
)

func riskColorCode(risk tool.RiskLevel) string {
	switch risk {
	case tool.RiskHigh:
		return colorRed
	case tool.RiskMedium:
		return colorOrange
	case tool.RiskLow:
		return colorYellow
	default:
		return colorReset
	}
}
