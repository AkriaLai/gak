// Package security implements the four-stage security pipeline.
// Core design principle: 安全边界内嵌 (Principle 2) — Defense in Depth.
//
// Pipeline stages:
//   1. Static Filter   → Based on identity/role, remove tools from visibility
//   2. Input Validate  → Check parameter format/safety before permission check
//   3. Dynamic Check   → Context-sensitive risk analysis (same tool, different risk)
//   4. Human-in-Loop   → Dangerous operations require explicit user approval
package security

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/akria/gak/pkg/tool"
)

// Decision represents the outcome of a security check.
type Decision string

const (
	DecisionAllow Decision = "allow"  // Safe to proceed
	DecisionDeny  Decision = "deny"   // Blocked by policy
	DecisionAsk   Decision = "ask"    // Requires user confirmation
)

// CheckResult contains the full result of a security pipeline check.
type CheckResult struct {
	Decision    Decision `json:"decision"`
	Reason      string   `json:"reason,omitempty"`
	Stage       string   `json:"stage"` // Which stage made the decision
}

// Authorizer is the callback interface for user interaction during security checks.
// The kernel's interaction provider implements this.
type Authorizer interface {
	// Confirm asks the user to approve a dangerous operation.
	// Returns true if approved, false if denied.
	// The context allows cancellation (e.g., user hits Ctrl+C).
	Confirm(ctx context.Context, toolName, description string, risk tool.RiskLevel) (bool, error)
}

// Rule defines a static permission rule.
type Rule struct {
	// ToolPattern is a glob pattern matching tool names.
	ToolPattern string `json:"tool_pattern"`

	// Decision is the action to take when matched.
	Decision Decision `json:"decision"`

	// Reason explains why this rule exists.
	Reason string `json:"reason,omitempty"`
}

// Policy holds the complete security configuration.
type Policy struct {
	// Rules are evaluated in order; first match wins.
	Rules []Rule `json:"rules"`

	// AutoApproveRiskBelow is the threshold below which operations
	// are automatically approved without user confirmation.
	AutoApproveRiskBelow tool.RiskLevel `json:"auto_approve_risk_below"`

	// DangerousPatterns are regex patterns that trigger RiskHigh
	// regardless of the tool's self-reported risk level.
	DangerousPatterns []string `json:"dangerous_patterns,omitempty"`
}

// DefaultPolicy returns a sensible default security policy.
func DefaultPolicy() Policy {
	return Policy{
		AutoApproveRiskBelow: tool.RiskLow,
		DangerousPatterns: []string{
			`rm\s+-rf\s+/`,       // Delete root
			`rm\s+-rf\s+~`,       // Delete home
			`>\s*/dev/sd`,        // Write to raw disk
			`mkfs`,              // Format filesystem
			`dd\s+if=`,          // Disk copy
			`chmod\s+777`,       // World-writable
			`curl.*\|\s*sh`,     // Pipe to shell
			`wget.*\|\s*sh`,     // Pipe to shell
		},
	}
}

// Pipeline is the four-stage security pipeline.
type Pipeline struct {
	policy            Policy
	authorizer        Authorizer
	dangerousPatterns []*regexp.Regexp // Compiled from Policy.DangerousPatterns
}

// NewPipeline creates a new security pipeline with the given policy and authorizer.
func NewPipeline(policy Policy, authorizer Authorizer) *Pipeline {
	// Pre-compile dangerous patterns for Stage 3 dynamic checks
	compiled := make([]*regexp.Regexp, 0, len(policy.DangerousPatterns))
	for _, pattern := range policy.DangerousPatterns {
		if re, err := regexp.Compile(pattern); err == nil {
			compiled = append(compiled, re)
		}
	}

	return &Pipeline{
		policy:            policy,
		authorizer:        authorizer,
		dangerousPatterns: compiled,
	}
}

// ExcludedTools returns the set of tool names that should be hidden from the LLM.
// (Stage 1: Static Filter — tools the model cannot even "see")
func (p *Pipeline) ExcludedTools() map[string]bool {
	excluded := make(map[string]bool)
	for _, rule := range p.policy.Rules {
		if rule.Decision == DecisionDeny {
			// For simplicity, treat exact tool names as patterns.
			// TODO: Add glob matching support.
			excluded[rule.ToolPattern] = true
		}
	}
	return excluded
}

// Check runs the full security pipeline for a tool call.
// Returns a CheckResult indicating whether the tool should be allowed to execute.
func (p *Pipeline) Check(ctx context.Context, t tool.Tool, input map[string]any) (CheckResult, error) {
	// Stage 1: Static filter (already applied at tool visibility level)
	for _, rule := range p.policy.Rules {
		if rule.ToolPattern == t.Name() {
			if rule.Decision == DecisionDeny {
				return CheckResult{
					Decision: DecisionDeny,
					Reason:   fmt.Sprintf("blocked by rule: %s", rule.Reason),
					Stage:    "static_filter",
				}, nil
			}
			if rule.Decision == DecisionAllow {
				return CheckResult{
					Decision: DecisionAllow,
					Reason:   fmt.Sprintf("allowed by rule: %s", rule.Reason),
					Stage:    "static_filter",
				}, nil
			}
		}
	}

	// Stage 2: Input validation (delegated to the tool itself)
	if err := t.ValidateInput(input); err != nil {
		return CheckResult{
			Decision: DecisionDeny,
			Reason:   fmt.Sprintf("input validation failed: %v", err),
			Stage:    "input_validate",
		}, nil
	}

	// Stage 3: Dynamic risk check
	risk := t.Risk(input)

	// Check DangerousPatterns against tool input (e.g., bash command)
	// This escalates risk to High regardless of the tool's self-reported level
	if len(p.dangerousPatterns) > 0 {
		inputStr := stringifyInput(input)
		for _, re := range p.dangerousPatterns {
			if re.MatchString(inputStr) {
				risk = tool.RiskHigh
				break
			}
		}
	}

	// Auto-approve low-risk operations
	if riskBelow(risk, p.policy.AutoApproveRiskBelow) {
		return CheckResult{
			Decision: DecisionAllow,
			Reason:   fmt.Sprintf("auto-approved: risk=%s below threshold=%s", risk, p.policy.AutoApproveRiskBelow),
			Stage:    "dynamic_check",
		}, nil
	}

	// Stage 4: Human-in-the-loop for medium/high risk
	if p.authorizer != nil {
		desc := fmt.Sprintf("Tool %q wants to execute with risk level %s", t.Name(), risk)
		approved, err := p.authorizer.Confirm(ctx, t.Name(), desc, risk)
		if err != nil {
			return CheckResult{
				Decision: DecisionDeny,
				Reason:   fmt.Sprintf("authorization error: %v", err),
				Stage:    "human_in_loop",
			}, err
		}
		if !approved {
			return CheckResult{
				Decision: DecisionDeny,
				Reason:   "denied by user",
				Stage:    "human_in_loop",
			}, nil
		}
		return CheckResult{
			Decision: DecisionAllow,
			Reason:   "approved by user",
			Stage:    "human_in_loop",
		}, nil
	}

	// No authorizer available, deny by default for high risk
	if risk == tool.RiskHigh {
		return CheckResult{
			Decision: DecisionDeny,
			Reason:   "high risk operation with no authorizer",
			Stage:    "dynamic_check",
		}, nil
	}

	return CheckResult{
		Decision: DecisionAllow,
		Reason:   "medium risk auto-approved (no authorizer)",
		Stage:    "dynamic_check",
	}, nil
}

// riskBelow returns true if actual risk is strictly below threshold.
func riskBelow(actual, threshold tool.RiskLevel) bool {
	order := map[tool.RiskLevel]int{
		tool.RiskNone:   0,
		tool.RiskLow:    1,
		tool.RiskMedium: 2,
		tool.RiskHigh:   3,
	}
	return order[actual] < order[threshold]
}

// stringifyInput concatenates all string values in the input map
// for pattern matching against DangerousPatterns.
func stringifyInput(input map[string]any) string {
	var parts []string
	for _, v := range input {
		if s, ok := v.(string); ok {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, " ")
}

