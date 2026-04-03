// GAK (Go-Agent-Kernel) CLI entry point.
//
// This is the main executable that wires together all kernel components
// and provides a terminal-based interactive agent experience.
//
// Usage:
//
//	export ANTHROPIC_API_KEY=sk-ant-...
//	go run ./cmd/gak/
//
// Third-party providers:
//
//	# DeepSeek
//	export DEEPSEEK_API_KEY=sk-...
//	# In .gak/config.json set provider: "deepseek"
//
//	# Any OpenAI-compatible API
//	# In .gak/config.json set provider: "openai", base_url: "...", api_key: "${MY_KEY}"
package main

import (
	"context"
	"encoding/json"
	"io"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/akria/gak/pkg/cache"
	"github.com/akria/gak/pkg/config"
	"github.com/akria/gak/pkg/interaction"
	"github.com/akria/gak/pkg/kernel"
	"github.com/akria/gak/pkg/llm"
	"github.com/akria/gak/pkg/llm/anthropic"
	"github.com/akria/gak/pkg/llm/openai"
	"github.com/akria/gak/pkg/logging"
	"github.com/akria/gak/pkg/mcp"
	"github.com/c-bata/go-prompt"
	"github.com/manifoldco/promptui"
	"github.com/akria/gak/pkg/metrics"
	"github.com/akria/gak/pkg/plugin"
	"github.com/akria/gak/pkg/plugin/example"
	"github.com/akria/gak/pkg/security"
	"github.com/akria/gak/pkg/session"
	"github.com/akria/gak/pkg/skill"
	"github.com/akria/gak/pkg/state"
	"github.com/akria/gak/pkg/tool"
	"github.com/akria/gak/pkg/tool/builtin"
)

const banner = `
   ██████╗  █████╗ ██╗  ██╗
  ██╔════╝ ██╔══██╗██║ ██╔╝
  ██║  ███╗███████║█████╔╝ 
  ██║   ██║██╔══██║██╔═██╗ 
  ╚██████╔╝██║  ██║██║  ██╗
   ╚═════╝ ╚═╝  ╚═╝╚═╝  ╚═╝
  Go-Agent-Kernel v0.1.0
`

const defaultSystemPrompt = `You are GAK, a powerful AI coding assistant built on the Go-Agent-Kernel.
You help users with coding tasks by reading files, executing commands, and writing code.

Guidelines:
- Always use absolute paths for file operations.
- When executing commands, explain what you're doing first.
- Be concise but thorough in your responses.
- If a command might be dangerous, explain the risks.
`

func main() {
	fmt.Print(banner)

	// --- Load configuration ---
	cfg, ws, err := config.AutoLoad()
	if err != nil {
		fmt.Fprintf(os.Stderr, "  Warning: config load: %v (using defaults)\n", err)
		cfg = config.DefaultConfig()
	}

	// --- Initialize Logger (Phase 3) ---
	os.MkdirAll(ws, 0755)
	logFile, _ := os.OpenFile(filepath.Join(ws, "agent.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	var logWriter = os.Stderr
	if logFile != nil {
		logWriter = logFile
		defer logFile.Close()
	}
	logger := logging.New(
		logging.WithLevel(logging.LevelInfo),
		logging.WithFormat(logging.FormatJSON),
		logging.WithOutput(logWriter),
		logging.WithField("component", "gak"),
	)
	logger.Info("GAK starting")

	// --- Initialize Metrics (Phase 3) ---
	collector := metrics.NewCollector()

	// --- Initialize LLM Provider via Factory ---
	providerCfg := cfg.PrimaryProviderConfig()
	resolvedCfg := llm.ResolveProvider(providerCfg)

	llmProvider, err := createProvider(resolvedCfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "  Error: %v\n", err)

		// Helpful hint based on provider type
		switch resolvedCfg.Type {
		case "anthropic":
			fmt.Fprintf(os.Stderr, "  Set ANTHROPIC_API_KEY environment variable.\n")
		case "openai":
			if resolvedCfg.DisplayName != "openai" {
				fmt.Fprintf(os.Stderr, "  Set API key via config or environment variable.\n")
				fmt.Fprintf(os.Stderr, "  Config: \"api_key\": \"${YOUR_ENV_VAR}\"\n")
			} else {
				fmt.Fprintf(os.Stderr, "  Set OPENAI_API_KEY environment variable.\n")
			}
		}
		os.Exit(1)
	}
	fmt.Printf("  Provider: %s (%s)\n", llmProvider.Name(), resolvedCfg.Model)

	// Show available alternative providers
	if len(cfg.LLM.Providers) > 0 {
		names := make([]string, 0, len(cfg.LLM.Providers))
		for name := range cfg.LLM.Providers {
			names = append(names, name)
		}
		fmt.Printf("  Alts:     %s\n", strings.Join(names, ", "))
	}

	// --- Initialize Tool Registry (Principle 4: Progressive Capability) ---
	registry := tool.NewRegistry()

	// Level 1: Atomic builtin tools
	registry.MustRegister(builtin.NewBashTool())
	registry.MustRegister(builtin.NewReadFileTool())
	registry.MustRegister(builtin.NewWriteFileTool())
	registry.MustRegister(builtin.NewListDirTool())
	fmt.Printf("  Tools:    %d builtin\n", registry.Count())

	// --- Setup signal handling (hard interrupt) ---
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		fmt.Println("\n\n  Interrupted. Shutting down...")
		cancel()
	}()

	// --- Level 2: Load Skills ---
	skillCount := 0
	bashTool := builtin.NewBashTool()
	executor := func(ctx context.Context, lang, command string) (string, error) {
		result, err := bashTool.Execute(ctx, map[string]any{"command": command})
		if err != nil {
			return "", err
		}
		return result.Output, nil
	}

	for _, dir := range cfg.Skills.Dirs {
		if strings.HasPrefix(dir, "~") {
			home, _ := os.UserHomeDir()
			dir = filepath.Join(home, dir[1:])
		}
		defs, err := skill.LoadDir(dir)
		if err != nil {
			continue
		}
		for _, def := range defs {
			st := skill.NewSkillTool(*def, executor)
			if err := registry.Register(st); err == nil {
				skillCount++
			}
		}
	}
	if skillCount > 0 {
		fmt.Printf("  Skills:   %d loaded\n", skillCount)
	}

	// --- Level 3: Initialize Plugins ---
	pluginMgr := plugin.NewManager()
	// Plugins are registered programmatically. Example:
	pluginMgr.Register(example.New())
	if pluginMgr.Count() > 0 {
		errs := pluginMgr.InitAll(cfg.Plugins.Configs)
		for _, e := range errs {
			logger.Warn("plugin init error", "error", e)
		}
		errs = pluginMgr.StartAll(ctx)
		for _, e := range errs {
			logger.Warn("plugin start error", "error", e)
		}
		pluginToolCount, errs := pluginMgr.RegisterTools(registry)
		for _, e := range errs {
			logger.Warn("plugin tool registration error", "error", e)
		}
		fmt.Printf("  Plugins:  %d running, %d tools\n", pluginMgr.RunningCount(), pluginToolCount)
		defer pluginMgr.StopAll()
	}

	// --- Level 4: Connect MCP servers ---
	mcpManager := mcp.NewManager()
	for _, serverCfg := range cfg.MCP.Servers {
		mcpManager.AddServer(serverCfg)
	}

	if len(cfg.MCP.Servers) > 0 {
		fmt.Print("  MCP:      connecting...")
		errs := mcpManager.ConnectAll(ctx)
		for _, e := range errs {
			fmt.Fprintf(os.Stderr, "\n  Warning: %v", e)
			logger.Warn("MCP connection error", "error", e)
		}
		mcpToolCount, errs := mcpManager.DiscoverAndRegister(ctx, registry)
		for _, e := range errs {
			fmt.Fprintf(os.Stderr, "\n  Warning: %v", e)
		}
		servers := mcpManager.ConnectedServers()
		fmt.Printf("\r  MCP:      %d servers, %d tools (%s)\n",
			len(servers), mcpToolCount, strings.Join(servers, ", "))
		defer mcpManager.CloseAll()
	}

	fmt.Printf("  Total:    %d tools available\n", registry.Count())

	// --- Initialize Session Manager (Phase 3) ---
	sessionID := fmt.Sprintf("session_%d", time.Now().UnixMilli())
	sessMgr, err := session.NewManager(filepath.Join(ws, "sessions"), sessionID)
	if err != nil {
		logger.Warn("session manager init failed", "error", err)
		sessMgr = nil
	} else {
		fmt.Printf("  Session:  %s\n", sessionID)
	}

	// --- Initialize Interaction Provider ---
	interactionProvider := interaction.NewCLIProvider()

	// --- Initialize Security Pipeline (Principle 2) ---
	policy := security.DefaultPolicy()
	pipeline := security.NewPipeline(policy, interactionProvider)

	// --- Initialize State Store (Principle 5) + Cache-Friendly Prompt (Principle 3) ---
	basePrompt := defaultSystemPrompt
	if cfg.Agent.SystemPrompt != "" {
		basePrompt = cfg.Agent.SystemPrompt
	}

	// Use PromptBuilder for deterministic, cache-friendly prompt construction
	promptBuilder := cache.NewPromptBuilder()
	promptBuilder.AddSection("system", basePrompt, 0)
	promptBuilder.AddSection("tools", cache.BuildToolSection(registry.Definitions()), 10)
	systemPrompt, promptHash := promptBuilder.Build()
	logger.Info("system prompt built", "hash", promptHash, "length", len(systemPrompt))

	initialState := state.NewState(systemPrompt)
	store := state.NewStore(initialState)

	// --- Initialize Kernel Runner ---
	runnerOpts := []kernel.Option{
		kernel.WithMaxTurns(cfg.Agent.MaxTurns),
		kernel.WithMaxTokens(cfg.Agent.MaxTokens),
		kernel.WithTemperature(cfg.Agent.Temperature),
		kernel.WithMetrics(collector),
		kernel.WithLogger(logger),
	}
	if sessMgr != nil {
		runnerOpts = append(runnerOpts, kernel.WithSession(sessMgr))
	}

	runner := kernel.New(llmProvider, registry, pipeline, store, runnerOpts...)

	// Track current model info for display
	activeModel := resolvedCfg.Model
	activeCfg := resolvedCfg

	fmt.Print("\n  Commands: /models /model <name> /stats /thinking /rollback /quit")
	fmt.Print("\n  Type your message and press Enter. Use @ to mention files/tools.\n\n")

	// 存储上一次的思维链内容
	var lastThinking strings.Builder

	// --- Main interaction loop ---
	completer := func(d prompt.Document) []prompt.Suggest {
		text := d.TextBeforeCursor()
		
		// 子命令补全：当用户敲入 /model 空格后，给出当前系统所有的可用模型选择
		if strings.HasPrefix(text, "/model ") {
			var s []prompt.Suggest
			for name := range cfg.LLM.Providers {
				s = append(s, prompt.Suggest{Text: name, Description: "Local config"})
			}
			for _, name := range llm.ListWellKnownProviders() {
				s = append(s, prompt.Suggest{Text: name, Description: "Built-in known model"})
			}
			return prompt.FilterHasPrefix(s, d.GetWordBeforeCursor(), true)
		}

		word := d.GetWordBeforeCursor()

		// '@' 上下文注入符：拦截文件与工具
		if strings.HasPrefix(word, "@") {
			var s []prompt.Suggest

			// 1. 工具与技能
			for _, tName := range registry.List() {
				s = append(s, prompt.Suggest{Text: "@" + tName, Description: "Tool/Skill"})
			}

			// 2. 当前目录文件
			if entries, err := os.ReadDir("."); err == nil {
				for _, entry := range entries {
					if !entry.IsDir() && !strings.HasPrefix(entry.Name(), ".") {
						s = append(s, prompt.Suggest{Text: "@" + entry.Name(), Description: "File"})
					}
				}
			}
			
			// 强制按字母顺序稳定排序，避免 Go Map 乱序遍历导致上下键选择跳转闪烁
			sort.Slice(s, func(i, j int) bool {
				return s[i].Text < s[j].Text
			})
			
			return prompt.FilterHasPrefix(s, word, true)
		}

		// '/' 系统核心指令控制符
		if strings.HasPrefix(word, "/") {
			var s []prompt.Suggest

			if !strings.Contains(text, " ") {
				s = append(s,
					prompt.Suggest{Text: "/models", Description: "Fetch models"},
					prompt.Suggest{Text: "/model ", Description: "Switch active model"},
					prompt.Suggest{Text: "/providers", Description: "List providers"},
					prompt.Suggest{Text: "/checkpoints", Description: "List state checkpoints"},
					prompt.Suggest{Text: "/rollback", Description: "Revert to previous checkpoint"},
					prompt.Suggest{Text: "/stats", Description: "Show runtime stats"},
					prompt.Suggest{Text: "/quit", Description: "Exit GAK"},
				)
			}
			return prompt.FilterHasPrefix(s, word, true)
		}

		return []prompt.Suggest{}
	}

	for {
		prefix := fmt.Sprintf("❯ [%s] ", activeModel)
		input := prompt.Input(prefix, completer,
			// 输入框高亮
			prompt.OptionPrefixTextColor(prompt.Turquoise),
			prompt.OptionPreviewSuggestionTextColor(prompt.DarkGreen),
			
			// 被选中的那一栏（整行微弱高亮 + 湖蓝色主字 + 墨绿色描述字）
			prompt.OptionSelectedSuggestionBGColor(prompt.DarkGray),
			prompt.OptionSelectedSuggestionTextColor(prompt.Turquoise),
			prompt.OptionSelectedDescriptionBGColor(prompt.DarkGray),
			prompt.OptionSelectedDescriptionTextColor(prompt.Green),
			
			// 未选中的栏目（全透明背景色融入终端 + 高级灰白字）
			prompt.OptionSuggestionBGColor(prompt.DefaultColor),
			prompt.OptionSuggestionTextColor(prompt.LightGray),
			prompt.OptionDescriptionBGColor(prompt.DefaultColor),
			prompt.OptionDescriptionTextColor(prompt.DarkGray),
			
			// 杂项
			prompt.OptionMaxSuggestion(10),
			prompt.OptionScrollbarBGColor(prompt.DefaultColor),
			prompt.OptionScrollbarThumbColor(prompt.DarkGray),
			prompt.OptionCompletionOnDown(),
		)
		input = strings.TrimSpace(input)

		if input == "" {
			continue
		}

		switch {
		case input == "exit" || input == "quit" || input == "/quit":
			fmt.Println("  Goodbye! 👋")
			printStats(collector)
			return

		case input == "/stats":
			printStats(collector)
			continue

		case input == "/thinking":
			if lastThinking.Len() == 0 {
				fmt.Println("\033[90m  No thinking content available.\033[0m")
			} else {
				fmt.Printf("\n\033[90m  ╭─ Last Thinking ────────────────────────\033[0m\n")
				fmt.Printf("\033[90m%s\033[0m", lastThinking.String())
				fmt.Printf("\n\033[90m  ╰──────────────────────────────────────────\033[0m\n")
			}
			continue

		case input == "/rollback":
			handleRollback(sessMgr, store)
			continue

		case input == "/checkpoints":
			handleListCheckpoints(sessMgr)
			continue

		case input == "/providers":
			handleListProviders(cfg)
			continue

		case input == "/models":
			handleListModels(activeCfg, cfg)
			continue

		case input == "/model":
			// 如果用户选了 /model 但没有输参数直接回车，弹出一个选择层作为救场保障
			var items []string
			for name := range cfg.LLM.Providers {
				items = append(items, name)
			}
			for _, name := range llm.ListWellKnownProviders() {
				items = append(items, name)
			}
			sort.Strings(items)
			
			p := promptui.Select{
				Label: "Select Model Parameter",
				Items: items,
			}
			// 光标上移去覆盖刚才的提示符
			fmt.Print("\033[1A\033[2K")
			
			_, mName, err := p.Run()
			if err != nil {
				continue
			}
			input = "/model " + mName
			// 回显拼接完整的命令
			fmt.Printf("\033[36m❯ \033[90m[%s]\033[0m %s\n", activeModel, input)
			fallthrough

		case strings.HasPrefix(input, "/model "):
			modelName := strings.TrimSpace(strings.TrimPrefix(input, "/model "))
			if newProvider, newCfg, err := handleSwitchModel(modelName, activeCfg, cfg); err != nil {
				fmt.Printf("\033[31m  ✗ %v\033[0m\n", err)
			} else {
				runner.SetProvider(newProvider)
				activeModel = newCfg.Model
				activeCfg = newCfg
				fmt.Printf("\033[32m  ✓ Switched to %s (%s)\033[0m\n", newCfg.DisplayName, newCfg.Model)
			}
			continue

		default:
			// --- @ 上下文隐式注入器 (Context Mention Pre-processor) ---
			re := regexp.MustCompile(`@([^\s]+)`)
			matches := re.FindAllStringSubmatch(input, -1)
			
			enhancedInput := input
			var contextBlocks []string
			
			for _, match := range matches {
				if len(match) > 1 {
					m := match[1]
					
					// 1. 检查是否点名了工具
					if _, exists := registry.Get(m); exists {
						contextBlocks = append(contextBlocks, fmt.Sprintf("[System Note: The user explicitly mentioned the tool %q. Please prioritize executing it.]", m))
						continue
					}
					
					// 2. 检查是否点名了本地文件，直接隐式附夹读取文件内容
					if data, err := os.ReadFile(m); err == nil {
						content := string(data)
						if len(content) > 16000 {
							content = content[:16000] + "\n...(truncated)"
						}
						contextBlocks = append(contextBlocks, fmt.Sprintf("```%s\n%s\n```", m, content))
					}
				}
			}
			
			if len(contextBlocks) > 0 {
				enhancedInput = "Attached Context:\n" + strings.Join(contextBlocks, "\n\n") + "\n\n" + input
			}

			events := runner.Run(ctx, enhancedInput)
			lastThinking.Reset()
			renderEvents(events, &lastThinking)
			fmt.Println()
		}
	}
}

// printStats displays current metrics.
func printStats(collector *metrics.Collector) {
	snap := collector.Snapshot()
	fmt.Printf("\n\033[90m── Metrics ──────────────────────────────\033[0m\n")
	fmt.Print(snap.String())
	fmt.Printf("\033[90m─────────────────────────────────────────\033[0m\n\n")
}

// handleRollback rolls back to the last checkpoint.
func handleRollback(sessMgr *session.Manager, store *state.Store) {
	if sessMgr == nil {
		fmt.Println("  Session manager not available.")
		return
	}

	checkpoints, err := sessMgr.List()
	if err != nil || len(checkpoints) == 0 {
		fmt.Println("  No checkpoints available.")
		return
	}

	if len(checkpoints) < 2 {
		fmt.Println("  Only one checkpoint — nothing to roll back to.")
		return
	}

	target := checkpoints[len(checkpoints)-2]
	restored, err := sessMgr.Rollback(target.ID)
	if err != nil {
		fmt.Printf("  Rollback failed: %v\n", err)
		return
	}

	store.Update(func(_ state.AgentState) state.AgentState {
		return *restored
	})
	fmt.Printf("  ↩ Rolled back to checkpoint %s (turn %d)\n", target.ID, target.Turn)
}

// handleListCheckpoints shows available checkpoints.
func handleListCheckpoints(sessMgr *session.Manager) {
	if sessMgr == nil {
		fmt.Println("  Session manager not available.")
		return
	}

	checkpoints, err := sessMgr.List()
	if err != nil {
		fmt.Printf("  Error: %v\n", err)
		return
	}

	if len(checkpoints) == 0 {
		fmt.Println("  No checkpoints.")
		return
	}

	fmt.Printf("  Checkpoints (%d):\n", len(checkpoints))
	for _, cp := range checkpoints {
		fmt.Printf("    %s  turn=%d  messages=%d  %s\n",
			cp.ID, cp.Turn, len(cp.State.Messages),
			cp.Timestamp.Format("15:04:05"))
	}
}

// handleListProviders shows available providers.
func handleListProviders(cfg config.Config) {
	fmt.Println("\n  Well-known providers:")
	for _, name := range llm.ListWellKnownProviders() {
		wk := llm.WellKnownProviders[name]
		fmt.Printf("    %-14s model=%-30s url=%s\n", name, wk.Model, wk.BaseURL)
	}

	if len(cfg.LLM.Providers) > 0 {
		fmt.Println("\n  Configured providers:")
		for name, p := range cfg.LLM.Providers {
			fmt.Printf("    %-14s %s\n", name, llm.DescribeProvider(p))
		}
	}

	fmt.Printf("\n  Active: %s (model=%s)\n\n", cfg.LLM.Provider, cfg.LLM.Model)
}

// renderEvents consumes and renders kernel events to the terminal.
func renderEvents(events <-chan kernel.Event, thinkBuf *strings.Builder) {
	inText := false
	inThinking := false

	for event := range events {
		switch event.Type {
		case kernel.EventThinking:
			if event.Text != "" {
				if !inThinking {
					fmt.Print("\n\033[2m\033[90m  💭 ")
					inThinking = true
				}
				// 以极淡的样式直接展示，视觉上自然"折叠"
				fmt.Printf("\033[2m\033[90m%s\033[0m", event.Text)
				thinkBuf.WriteString(event.Text)
			}

		case kernel.EventTextDelta:
			if inThinking {
				fmt.Print("\033[0m\n")
				inThinking = false
			}
			if !inText {
				fmt.Print("\n\033[37m")
				inText = true
			}
			fmt.Print(event.Text)

		case kernel.EventTextDone:
			if inThinking {
				fmt.Print("\n")
				inThinking = false
			}
			if inText {
				fmt.Print("\033[0m\n")
				inText = false
			}

		case kernel.EventToolUseStart:
			if inText {
				fmt.Print("\033[0m\n")
				inText = false
			}
			if event.ToolCall != nil {
				fmt.Printf("\n\033[33m⚡ %s\033[0m", event.ToolCall.Name)
				if cmd, ok := event.ToolCall.Input["command"].(string); ok {
					fmt.Printf(" → %s", truncate(cmd, 80))
				} else if path, ok := event.ToolCall.Input["path"].(string); ok {
					fmt.Printf(" → %s", path)
				}
				fmt.Println()
			}

		case kernel.EventToolUseResult:
			if event.ToolResult != nil {
				elapsed := event.ToolResult.Elapsed.Round(time.Millisecond)
				if event.ToolResult.Result.IsError {
					fmt.Printf("\033[31m  ✗ Error (%s): %s\033[0m\n",
						elapsed, truncate(event.ToolResult.Result.Output, 200))
				} else {
					output := event.ToolResult.Result.Output
					if len(output) > 500 {
						output = output[:500] + "..."
					}
					fmt.Printf("\033[32m  ✓ Done (%s)\033[0m\n", elapsed)
					if output != "(no output)" {
						lines := strings.Split(output, "\n")
						maxLines := 10
						if len(lines) > maxLines {
							for _, line := range lines[:maxLines] {
								fmt.Printf("  │ %s\n", line)
							}
							fmt.Printf("  │ ... (%d more lines)\n", len(lines)-maxLines)
						} else {
							for _, line := range lines {
								fmt.Printf("  │ %s\n", line)
							}
						}
					}
				}
			}

		case kernel.EventPermissionRequest:
			if event.Permission != nil {
				fmt.Printf("\033[33m🔒 Permission required for %s (risk: %s)\033[0m\n",
					event.Permission.ToolName, event.Permission.RiskLevel)
			}

		case kernel.EventStateTransition:
			// Silent

		case kernel.EventError:
			if inText {
				fmt.Print("\033[0m\n")
				inText = false
			}
			fmt.Printf("\033[31m✗ Error: %v\033[0m\n", event.Error)

		case kernel.EventDone:
			// Run complete
		}
	}

	if inText {
		fmt.Print("\033[0m\n")
	}
}

func truncate(s string, maxLen int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > maxLen {
		return s[:maxLen] + "..."
	}
	return s
}

// createProvider is the local factory function that creates an LLM provider
// from a resolved config. This lives in cmd/ to avoid import cycles between
// llm/ and its sub-packages (llm/anthropic, llm/openai).
func createProvider(cfg llm.ProviderConfig) (llm.Provider, error) {
	// Resolve API key from ${ENV_VAR} syntax
	apiKey := llm.ResolveAPIKey(cfg)

	switch cfg.Type {
	case "anthropic":
		var opts []anthropic.Option
		if cfg.Model != "" {
			opts = append(opts, anthropic.WithModel(cfg.Model))
		}
		if cfg.BaseURL != "" {
			opts = append(opts, anthropic.WithBaseURL(cfg.BaseURL))
		}
		return anthropic.New(opts...)

	case "openai":
		var opts []openai.Option
		if apiKey != "" {
			opts = append(opts, openai.WithAPIKey(apiKey))
		}
		if cfg.BaseURL != "" {
			opts = append(opts, openai.WithBaseURL(cfg.BaseURL))
		}
		if cfg.Model != "" {
			opts = append(opts, openai.WithModel(cfg.Model))
		}
		if cfg.DisplayName != "" {
			opts = append(opts, openai.WithProviderName(cfg.DisplayName))
		}
		return openai.New(opts...)

	default:
		return nil, fmt.Errorf("unknown provider type: %q (available: anthropic, openai, or well-known: %s)",
			cfg.Type, strings.Join(llm.ListWellKnownProviders(), ", "))
	}
}

// handleListModels queries the API's /models endpoint and combines with configured models.
func handleListModels(activeCfg llm.ProviderConfig, appCfg config.Config) {
	fmt.Printf("\n  \033[36mActive:\033[0m %s (%s)\n", activeCfg.DisplayName, activeCfg.Model)

	// List configured models from config
	if len(appCfg.LLM.Providers) > 0 {
		fmt.Println("\n  \033[33mConfigured models:\033[0m")
		for name, p := range appCfg.LLM.Providers {
			resolved := llm.ResolveProvider(p)
			marker := "  "
			if resolved.Model == activeCfg.Model && resolved.BaseURL == activeCfg.BaseURL {
				marker = "→ "
			}
			fmt.Printf("    %s%-18s %s\n", marker, name, resolved.Model)
		}
	}

	// Query remote API for available models
	apiKey := llm.ResolveAPIKey(activeCfg)
	baseURL := activeCfg.BaseURL
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}

	fmt.Printf("\n  \033[33mQuerying %s/models ...\033[0m\n", baseURL)

	req, err := http.NewRequest("GET", baseURL+"/models", nil)
	if err != nil {
		fmt.Printf("  \033[31m✗ %v\033[0m\n\n", err)
		return
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Printf("  \033[31m✗ %v\033[0m\n\n", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		fmt.Printf("  \033[31m✗ HTTP %d: %s\033[0m\n\n", resp.StatusCode, truncate(string(body), 200))
		return
	}

	var result struct {
		Data []struct {
			ID      string `json:"id"`
			Created int64  `json:"created"`
			OwnedBy string `json:"owned_by"`
		} `json:"data"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		fmt.Printf("  \033[31m✗ parse error: %v\033[0m\n\n", err)
		return
	}

	if len(result.Data) == 0 {
		fmt.Println("  (no models returned)")
	} else {
		// Sort by ID
		sort.Slice(result.Data, func(i, j int) bool {
			return result.Data[i].ID < result.Data[j].ID
		})

		fmt.Printf("  \033[33mRemote models (%d):\033[0m\n", len(result.Data))
		for _, m := range result.Data {
			marker := "  "
			if m.ID == activeCfg.Model {
				marker = "→ "
			}
			fmt.Printf("    %s%s\n", marker, m.ID)
		}
	}
	fmt.Println()
}

// handleSwitchModel creates a new provider for the given model name.
// It first checks configured providers, then tries as a direct model name
// with the active provider's base URL and API key.
func handleSwitchModel(modelName string, activeCfg llm.ProviderConfig, appCfg config.Config) (llm.Provider, llm.ProviderConfig, error) {
	// 1. Check if it matches a configured provider name
	if p, ok := appCfg.LLM.Providers[modelName]; ok {
		resolved := llm.ResolveProvider(p)
		// Inherit API key from active config if not set
		if resolved.APIKey == "" && activeCfg.APIKey != "" {
			resolved.APIKey = activeCfg.APIKey
		}
		provider, err := createProvider(resolved)
		if err != nil {
			return nil, llm.ProviderConfig{}, fmt.Errorf("creating provider %q: %w", modelName, err)
		}
		return provider, resolved, nil
	}

	// 2. Treat as a direct model name — reuse active provider's base URL, API key, type
	newCfg := llm.ProviderConfig{
		Type:        activeCfg.Type,
		BaseURL:     activeCfg.BaseURL,
		APIKey:      activeCfg.APIKey,
		Model:       modelName,
		DisplayName: modelName,
	}

	provider, err := createProvider(newCfg)
	if err != nil {
		return nil, llm.ProviderConfig{}, fmt.Errorf("creating provider for model %q: %w", modelName, err)
	}
	return provider, newCfg, nil
}

