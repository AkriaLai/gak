// Package skill implements the declarative Skill system.
//
// Skills are Principle 4, Level 2 — composite tools defined via
// Markdown + YAML frontmatter, accessible to advanced users without
// writing Go code.
//
// A skill file looks like:
//
//	---
//	name: k8s_check
//	description: "Check Kubernetes cluster health"
//	risk: medium
//	parameters:
//	  namespace:
//	    type: string
//	    description: "K8s namespace"
//	    default: "default"
//	---
//	## Steps
//	1. Check pod status
//	```bash
//	kubectl get pods -n {{.namespace}} --no-headers
//	```
//	2. Check node status
//	```bash
//	kubectl get nodes --no-headers
//	```
package skill

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"text/template"

	"github.com/akria/gak/pkg/tool"
)

// Definition is the parsed representation of a skill file.
type Definition struct {
	// Metadata from YAML frontmatter
	Name        string                       `yaml:"name"`
	Description string                       `yaml:"description"`
	Risk        string                       `yaml:"risk"`
	Parameters  map[string]ParameterDef      `yaml:"parameters"`

	// Steps extracted from markdown code blocks
	Steps []Step

	// Source file path
	SourcePath string
}

// ParameterDef describes a skill parameter.
type ParameterDef struct {
	Type        string `yaml:"type"`
	Description string `yaml:"description"`
	Default     string `yaml:"default"`
	Required    bool   `yaml:"required"`
}

// Step is a single executable step in a skill.
type Step struct {
	Description string // Text before the code block
	Language    string // Code block language (bash, python, etc.)
	Command     string // Code block content
}

// SkillTool wraps a skill Definition as a tool.Tool.
type SkillTool struct {
	def      Definition
	executor func(ctx context.Context, lang, command string) (string, error)
}

// NewSkillTool creates a tool from a skill definition.
// The executor function handles the actual command execution
// (typically delegating to the Bash tool or a language-specific runner).
func NewSkillTool(def Definition, executor func(ctx context.Context, lang, command string) (string, error)) *SkillTool {
	return &SkillTool{
		def:      def,
		executor: executor,
	}
}

func (s *SkillTool) Name() string        { return "skill_" + s.def.Name }
func (s *SkillTool) Description() string { return s.def.Description }

func (s *SkillTool) InputSchema() map[string]any {
	properties := make(map[string]any)
	required := make([]string, 0)

	for name, param := range s.def.Parameters {
		prop := map[string]any{
			"type":        param.Type,
			"description": param.Description,
		}
		if param.Default != "" {
			prop["default"] = param.Default
		}
		properties[name] = prop

		if param.Required {
			required = append(required, name)
		}
	}

	schema := map[string]any{
		"type":       "object",
		"properties": properties,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func (s *SkillTool) ValidateInput(input map[string]any) error {
	for name, param := range s.def.Parameters {
		if param.Required {
			if _, ok := input[name]; !ok {
				return fmt.Errorf("required parameter %q is missing", name)
			}
		}
	}
	return nil
}

func (s *SkillTool) Risk(_ map[string]any) tool.RiskLevel {
	switch s.def.Risk {
	case "high":
		return tool.RiskHigh
	case "medium":
		return tool.RiskMedium
	case "low":
		return tool.RiskLow
	default:
		return tool.RiskMedium // Default to medium for skills
	}
}

// Execute runs all steps in the skill sequentially, templating parameters.
func (s *SkillTool) Execute(ctx context.Context, input map[string]any) (tool.Result, error) {
	// Apply defaults for missing parameters
	params := make(map[string]any)
	for name, param := range s.def.Parameters {
		if v, ok := input[name]; ok {
			params[name] = v
		} else if param.Default != "" {
			params[name] = param.Default
		}
	}

	var output strings.Builder
	for i, step := range s.def.Steps {
		if ctx.Err() != nil {
			return tool.NewErrorResult(ctx.Err()), nil
		}

		// Template the command with parameters
		cmd, err := templateString(step.Command, params)
		if err != nil {
			return tool.NewErrorResultf("step %d template error: %v", i+1, err), nil
		}

		output.WriteString(fmt.Sprintf("### Step %d: %s\n", i+1, step.Description))

		result, err := s.executor(ctx, step.Language, cmd)
		if err != nil {
			output.WriteString(fmt.Sprintf("Error: %v\n", err))
			return tool.Result{Output: output.String(), IsError: true}, nil
		}

		output.WriteString(result)
		output.WriteString("\n\n")
	}

	return tool.NewResult(output.String()), nil
}

// --- Parsing ---

// ParseFile parses a skill definition from a markdown file.
func ParseFile(path string) (*Definition, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading skill file: %w", err)
	}

	def, err := Parse(string(data))
	if err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}

	def.SourcePath = path
	return def, nil
}

// Parse parses a skill definition from markdown content.
func Parse(content string) (*Definition, error) {
	def := &Definition{
		Parameters: make(map[string]ParameterDef),
	}

	// Split frontmatter and body
	frontmatter, body, err := splitFrontmatter(content)
	if err != nil {
		return nil, err
	}

	// Parse YAML frontmatter (simple key-value parser)
	if err := parseSimpleYAML(frontmatter, def); err != nil {
		return nil, fmt.Errorf("parsing frontmatter: %w", err)
	}

	// Parse steps from markdown body
	def.Steps = parseSteps(body)

	if def.Name == "" {
		return nil, fmt.Errorf("skill must have a 'name' in frontmatter")
	}

	return def, nil
}

// LoadDir loads all skill definitions from a directory.
func LoadDir(dir string) ([]*Definition, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var defs []*Definition
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		ext := filepath.Ext(entry.Name())
		if ext != ".md" && ext != ".markdown" {
			continue
		}

		def, err := ParseFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue // Skip invalid skill files
		}
		defs = append(defs, def)
	}

	return defs, nil
}

// --- Internal helpers ---

func splitFrontmatter(content string) (string, string, error) {
	lines := strings.SplitN(content, "\n", -1)
	if len(lines) < 3 || strings.TrimSpace(lines[0]) != "---" {
		return "", content, nil // No frontmatter
	}

	endIdx := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			endIdx = i
			break
		}
	}

	if endIdx == -1 {
		return "", content, fmt.Errorf("unterminated frontmatter")
	}

	fm := strings.Join(lines[1:endIdx], "\n")
	body := strings.Join(lines[endIdx+1:], "\n")
	return fm, body, nil
}

// parseSimpleYAML is a minimal YAML parser for skill frontmatter.
// Handles flat key-value pairs and nested parameter definitions.
func parseSimpleYAML(yaml string, def *Definition) error {
	scanner := bufio.NewScanner(strings.NewReader(yaml))
	var currentParam string

	for scanner.Scan() {
		line := scanner.Text()
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}

		indent := len(line) - len(strings.TrimLeft(line, " "))

		parts := strings.SplitN(trimmed, ":", 2)
		if len(parts) != 2 {
			continue
		}

		key := strings.TrimSpace(parts[0])
		value := strings.Trim(strings.TrimSpace(parts[1]), `"'`)

		if indent == 0 {
			switch key {
			case "name":
				def.Name = value
			case "description":
				def.Description = value
			case "risk":
				def.Risk = value
			case "parameters":
				// Container key, params follow
			}
			currentParam = ""
		} else if indent == 2 && value == "" {
			// New parameter definition
			currentParam = key
			if _, ok := def.Parameters[currentParam]; !ok {
				def.Parameters[currentParam] = ParameterDef{}
			}
		} else if indent >= 4 && currentParam != "" {
			// Parameter field
			p := def.Parameters[currentParam]
			switch key {
			case "type":
				p.Type = value
			case "description":
				p.Description = value
			case "default":
				p.Default = value
			case "required":
				p.Required = value == "true"
			}
			def.Parameters[currentParam] = p
		}
	}

	return nil
}

// codeBlockRe matches markdown fenced code blocks.
var codeBlockRe = regexp.MustCompile("(?s)```(\\w*)\\s*\\n(.*?)```")

func parseSteps(body string) []Step {
	matches := codeBlockRe.FindAllStringSubmatchIndex(body, -1)
	var steps []Step

	for _, loc := range matches {
		// loc[0:1] = full match, loc[2:3] = language, loc[4:5] = content
		lang := body[loc[2]:loc[3]]
		command := strings.TrimSpace(body[loc[4]:loc[5]])

		// Find description text before this code block
		desc := ""
		lineStart := strings.LastIndex(body[:loc[0]], "\n")
		if lineStart >= 0 {
			descLine := strings.TrimSpace(body[lineStart:loc[0]])
			// Strip markdown list markers and headers
			descLine = strings.TrimLeft(descLine, "0123456789.-#* ")
			desc = descLine
		}

		steps = append(steps, Step{
			Description: desc,
			Language:    lang,
			Command:     command,
		})
	}

	return steps
}

func templateString(tmpl string, data map[string]any) (string, error) {
	t, err := template.New("cmd").Parse(tmpl)
	if err != nil {
		return "", err
	}
	var buf strings.Builder
	if err := t.Execute(&buf, data); err != nil {
		return "", err
	}
	return buf.String(), nil
}
