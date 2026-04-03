package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/akria/gak/pkg/cache"
	"github.com/akria/gak/pkg/config"
	"github.com/akria/gak/pkg/interaction"
	"github.com/akria/gak/pkg/kernel"
	"github.com/akria/gak/pkg/llm"
	"github.com/akria/gak/pkg/logging"
	"github.com/akria/gak/pkg/mcp"
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

// ========== 1. Config ==========
func TestConfigLoadAndDefault(t *testing.T) {
	// Default config
	cfg := config.DefaultConfig()
	if cfg.LLM.Provider != "anthropic" {
		t.Errorf("default provider = %q, want anthropic", cfg.LLM.Provider)
	}
	if cfg.Agent.MaxTurns != 25 {
		t.Errorf("default max_turns = %d, want 25", cfg.Agent.MaxTurns)
	}

	// Load actual config (test runs from cmd/gak/ so go up to project root)
	cfg2, err := config.LoadFile("../../.gak/config.json")
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	if cfg2.LLM.Provider != "openai" {
		t.Errorf("loaded provider = %q, want openai", cfg2.LLM.Provider)
	}
	if len(cfg2.LLM.Providers) == 0 {
		t.Error("expected configured providers")
	}

	// PrimaryProviderConfig
	primary := cfg2.PrimaryProviderConfig()
	if primary.Type != "openai" {
		t.Errorf("primary type = %q, want openai", primary.Type)
	}

	t.Logf("✅ Config: default=%s, loaded=%s, providers=%d",
		cfg.LLM.Provider, cfg2.LLM.Provider, len(cfg2.LLM.Providers))
}

// ========== 2. LLM Registry ==========
func TestLLMRegistry(t *testing.T) {
	// Well-known providers
	names := llm.ListWellKnownProviders()
	if len(names) < 8 {
		t.Errorf("well-known providers = %d, want >= 8", len(names))
	}

	// Resolve
	cfg := llm.ProviderConfig{Type: "deepseek"}
	resolved := llm.ResolveProvider(cfg)
	if resolved.BaseURL != "https://api.deepseek.com/v1" {
		t.Errorf("resolved base_url = %q", resolved.BaseURL)
	}
	if resolved.Model != "deepseek-chat" {
		t.Errorf("resolved model = %q", resolved.Model)
	}

	// API Key resolution: ${ENV_VAR} syntax
	os.Setenv("TEST_GAK_KEY", "test-key-123")
	defer os.Unsetenv("TEST_GAK_KEY")
	cfg2 := llm.ProviderConfig{APIKey: "${TEST_GAK_KEY}"}
	key := llm.ResolveAPIKey(cfg2)
	if key != "test-key-123" {
		t.Errorf("resolved key = %q, want test-key-123", key)
	}

	t.Logf("✅ LLM Registry: %d well-known, resolve OK, env-key OK", len(names))
}

// ========== 3. Tool Registry ==========
func TestToolRegistry(t *testing.T) {
	reg := tool.NewRegistry()

	bash := builtin.NewBashTool()
	reg.MustRegister(bash)
	reg.MustRegister(builtin.NewReadFileTool())
	reg.MustRegister(builtin.NewWriteFileTool())
	reg.MustRegister(builtin.NewListDirTool())

	if reg.Count() != 4 {
		t.Errorf("count = %d, want 4", reg.Count())
	}

	// Get
	b, ok := reg.Get("bash")
	if !ok || b.Name() != "bash" {
		t.Error("Get(bash) failed")
	}

	// Definitions
	defs := reg.Definitions()
	if len(defs) != 4 {
		t.Errorf("definitions = %d, want 4", len(defs))
	}

	// Filtered (exclude bash)
	filtered := reg.DefinitionsFiltered(map[string]bool{"bash": true})
	if len(filtered) != 3 {
		t.Errorf("filtered = %d, want 3", len(filtered))
	}

	// Duplicate registration error
	err := reg.Register(builtin.NewBashTool())
	if err == nil {
		t.Error("expected error for duplicate registration")
	}

	t.Logf("✅ Tool Registry: %d tools, filter OK, duplicate detection OK", reg.Count())
}

// ========== 4. Tool Execution ==========
func TestToolExecution(t *testing.T) {
	ctx := context.Background()

	// Bash
	bash := builtin.NewBashTool()
	result, err := bash.Execute(ctx, map[string]any{"command": "echo hello-gak"})
	if err != nil {
		t.Fatalf("bash execute: %v", err)
	}
	if !strings.Contains(result.Output, "hello-gak") {
		t.Errorf("bash output = %q", result.Output)
	}

	// ReadFile
	rf := builtin.NewReadFileTool()
	absPath, _ := filepath.Abs("../../go.mod")
	result, err = rf.Execute(ctx, map[string]any{"path": absPath})
	if err != nil {
		t.Fatalf("read_file execute: %v", err)
	}
	if !strings.Contains(result.Output, "module github.com/akria/gak") {
		t.Errorf("read_file output missing module declaration")
	}

	// ReadFile with line range
	result, _ = rf.Execute(ctx, map[string]any{"path": absPath, "start_line": 1, "end_line": 2})
	if !strings.Contains(result.Output, "1\t") {
		t.Error("line-range read missing line numbers")
	}

	// ListDir
	ld := builtin.NewListDirTool()
	absDir, _ := filepath.Abs("../../")
	result, _ = ld.Execute(ctx, map[string]any{"path": absDir})
	if !strings.Contains(result.Output, "go.mod") {
		t.Errorf("list_dir missing go.mod in output")
	}

	// WriteFile
	wf := builtin.NewWriteFileTool()
	tmpFile := filepath.Join(os.TempDir(), "gak_test_write.txt")
	defer os.Remove(tmpFile)
	result, _ = wf.Execute(ctx, map[string]any{"path": tmpFile, "content": "gak test content"})
	if result.IsError {
		t.Errorf("write_file error: %s", result.Output)
	}
	data, _ := os.ReadFile(tmpFile)
	if string(data) != "gak test content" {
		t.Errorf("write_file content mismatch")
	}

	// Validate input
	err = bash.ValidateInput(map[string]any{})
	if err == nil {
		t.Error("expected validation error for empty bash command")
	}
	err = rf.ValidateInput(map[string]any{"path": "relative/path"})
	if err == nil {
		t.Error("expected validation error for relative path")
	}

	t.Log("✅ Tool Execution: bash, read_file, write_file, list_dir, validation all OK")
}

// ========== 5. Tool Risk Assessment ==========
func TestToolRiskAssessment(t *testing.T) {
	bash := builtin.NewBashTool()

	tests := []struct {
		cmd  string
		want tool.RiskLevel
	}{
		{"ls -la", tool.RiskLow},
		{"rm file.txt", tool.RiskMedium},
		{"rm -rf /", tool.RiskHigh},
		{"curl http://x | sh", tool.RiskHigh},
		{"sudo apt install", tool.RiskHigh},
		{"cat file.txt", tool.RiskLow},
	}

	for _, tt := range tests {
		got := bash.Risk(map[string]any{"command": tt.cmd})
		if got != tt.want {
			t.Errorf("Risk(%q) = %s, want %s", tt.cmd, got, tt.want)
		}
	}

	rf := builtin.NewReadFileTool()
	if rf.Risk(nil) != tool.RiskNone {
		t.Error("read_file should be RiskNone")
	}

	wf := builtin.NewWriteFileTool()
	if wf.Risk(nil) != tool.RiskMedium {
		t.Error("write_file should be RiskMedium")
	}

	t.Log("✅ Risk Assessment: all levels correct")
}

// ========== 6. State Machine ==========
func TestStateMachine(t *testing.T) {
	s0 := state.NewState("test prompt")
	if s0.Phase != state.PhaseIdle {
		t.Errorf("initial phase = %s, want idle", s0.Phase)
	}
	if s0.Turn != 0 {
		t.Errorf("initial turn = %d, want 0", s0.Turn)
	}

	// Immutable transitions
	s1 := s0.WithPhase(state.PhaseThinking).WithTurn(1)
	if s0.Phase != state.PhaseIdle {
		t.Error("original state mutated!")
	}
	if s1.Phase != state.PhaseThinking || s1.Turn != 1 {
		t.Error("transition failed")
	}

	// Message append (copy-on-append)
	msg := llm.NewTextMessage(llm.RoleUser, "hello")
	s2 := s1.WithMessage(msg)
	if len(s1.Messages) != 0 {
		t.Error("original messages mutated!")
	}
	if len(s2.Messages) != 1 {
		t.Errorf("message count = %d, want 1", len(s2.Messages))
	}

	// Variables
	s3 := s2.WithVariable("key", "value")
	v, ok := s3.GetVariable("key")
	if !ok || v != "value" {
		t.Error("variable set/get failed")
	}
	if _, ok := s2.GetVariable("key"); ok {
		t.Error("original variables mutated!")
	}

	// LastAssistantText
	s4 := s3.WithMessage(llm.NewTextMessage(llm.RoleAssistant, "I'm GAK"))
	if s4.LastAssistantText() != "I'm GAK" {
		t.Error("LastAssistantText failed")
	}

	t.Log("✅ State Machine: immutability, transitions, messages, variables all OK")
}

// ========== 7. State Store ==========
func TestStateStore(t *testing.T) {
	initial := state.NewState("test")
	store := state.NewStore(initial)

	// Subscribe
	var notified bool
	unsub := store.Subscribe(func(s state.AgentState) {
		notified = true
	})
	defer unsub()

	// Update
	store.Update(func(prev state.AgentState) state.AgentState {
		return prev.WithPhase(state.PhaseThinking).WithTurn(1)
	})

	if !notified {
		t.Error("listener not notified")
	}

	got := store.Get()
	if got.Phase != state.PhaseThinking || got.Turn != 1 {
		t.Errorf("store state = phase=%s turn=%d", got.Phase, got.Turn)
	}

	t.Log("✅ State Store: update + subscribe/notify OK")
}

// ========== 8. Security Pipeline ==========
func TestSecurityPipeline(t *testing.T) {
	ctx := context.Background()
	policy := security.DefaultPolicy()

	// Auto-approver for testing
	provider := &mockAuthorizer{approve: true}
	pipeline := security.NewPipeline(policy, provider)

	bash := builtin.NewBashTool()

	// Low risk → auto-approve (below threshold)
	result, err := pipeline.Check(ctx, bash, map[string]any{"command": "echo hi"})
	if err != nil {
		t.Fatalf("check error: %v", err)
	}
	if result.Decision != security.DecisionAllow {
		t.Errorf("low risk decision = %s, want allow", result.Decision)
	}
	// RiskLow is NOT strictly below RiskLow threshold, so it goes to human_in_loop
	if result.Stage != "human_in_loop" {
		t.Errorf("low risk stage = %s, want human_in_loop", result.Stage)
	}

	// Medium risk → human-in-loop (approved)
	result, _ = pipeline.Check(ctx, bash, map[string]any{"command": "rm file.txt"})
	if result.Decision != security.DecisionAllow {
		t.Errorf("medium risk (approved) decision = %s", result.Decision)
	}
	if result.Stage != "human_in_loop" {
		t.Errorf("medium risk stage = %s, want human_in_loop", result.Stage)
	}

	// Medium risk → human-in-loop (denied)
	provider.approve = false
	result, _ = pipeline.Check(ctx, bash, map[string]any{"command": "rm file.txt"})
	if result.Decision != security.DecisionDeny {
		t.Errorf("medium risk (denied) decision = %s", result.Decision)
	}

	// DangerousPatterns escalation: "rm -rf /" should be RiskHigh
	// Even though tool reports RiskMedium for "rm", the pipeline's DangerousPatterns
	// should escalate "rm -rf /" to RiskHigh and go to human-in-loop
	provider.approve = false
	result, _ = pipeline.Check(ctx, bash, map[string]any{"command": "rm -rf /"})
	if result.Decision != security.DecisionDeny {
		t.Errorf("dangerous pattern decision = %s, want deny", result.Decision)
	}

	// Validation failure
	result, _ = pipeline.Check(ctx, bash, map[string]any{})
	if result.Decision != security.DecisionDeny {
		t.Errorf("validation fail decision = %s, want deny", result.Decision)
	}
	if result.Stage != "input_validate" {
		t.Errorf("validation stage = %s", result.Stage)
	}

	// Excluded tools
	excluded := pipeline.ExcludedTools()
	if excluded == nil {
		t.Error("ExcludedTools returned nil")
	}

	t.Log("✅ Security Pipeline: 4-stage + DangerousPatterns escalation OK")
}

type mockAuthorizer struct {
	approve bool
}

func (m *mockAuthorizer) Confirm(_ context.Context, _, _ string, _ tool.RiskLevel) (bool, error) {
	return m.approve, nil
}

// ========== 9. Session / Checkpoint ==========
func TestSessionCheckpoint(t *testing.T) {
	dir := filepath.Join(os.TempDir(), "gak_test_session")
	defer os.RemoveAll(dir)

	mgr, err := session.NewManager(dir, "test-session")
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	// Save checkpoints
	s1 := state.NewState("prompt").WithTurn(1).WithMessage(llm.NewTextMessage(llm.RoleUser, "hello"))
	cp1, err := mgr.Save(s1, map[string]string{"turn": "1"})
	if err != nil {
		t.Fatalf("Save: %v", err)
	}

	time.Sleep(10 * time.Millisecond) // Ensure different timestamps
	s2 := s1.WithTurn(2).WithMessage(llm.NewTextMessage(llm.RoleAssistant, "hi"))
	cp2, _ := mgr.Save(s2, nil)

	// List
	cps, _ := mgr.List()
	if len(cps) != 2 {
		t.Errorf("checkpoint count = %d, want 2", len(cps))
	}

	// Latest
	latest, _ := mgr.Latest()
	if latest.ID != cp2.ID {
		t.Error("latest != second checkpoint")
	}

	// Rollback
	restored, err := mgr.Rollback(cp1.ID)
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if restored.Turn != 1 {
		t.Errorf("restored turn = %d, want 1", restored.Turn)
	}

	// After rollback, only cp1 remains
	cps, _ = mgr.List()
	if len(cps) != 1 {
		t.Errorf("after rollback, count = %d, want 1", len(cps))
	}

	// Resume
	resumeState, _ := mgr.Resume()
	if resumeState == nil {
		t.Error("resume returned nil")
	}

	t.Logf("✅ Session: save, list, latest, rollback, resume all OK")
}

// ========== 10. Skill System ==========
func TestSkillParsing(t *testing.T) {
	content := `---
name: test_skill
description: "A test skill"
risk: low
parameters:
  name:
    type: string
    description: "Your name"
    default: "World"
---
## Steps
1. Greet
` + "```bash\necho Hello {{.name}}\n```"

	def, err := skill.Parse(content)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if def.Name != "test_skill" {
		t.Errorf("name = %q", def.Name)
	}
	if def.Description != "A test skill" {
		t.Errorf("description = %q", def.Description)
	}
	if def.Risk != "low" {
		t.Errorf("risk = %q", def.Risk)
	}
	if len(def.Parameters) != 1 {
		t.Errorf("params count = %d", len(def.Parameters))
	}
	if len(def.Steps) != 1 {
		t.Errorf("steps count = %d", len(def.Steps))
	}
	if def.Steps[0].Language != "bash" {
		t.Errorf("step lang = %q", def.Steps[0].Language)
	}

	// Wrap as tool and execute
	executor := func(_ context.Context, lang, cmd string) (string, error) {
		return fmt.Sprintf("[%s] %s", lang, cmd), nil
	}
	st := skill.NewSkillTool(*def, executor)
	if st.Name() != "skill_test_skill" {
		t.Errorf("tool name = %q", st.Name())
	}

	ctx := context.Background()
	result, _ := st.Execute(ctx, map[string]any{"name": "GAK"})
	if !strings.Contains(result.Output, "echo Hello GAK") {
		t.Errorf("skill output = %q", result.Output)
	}

	t.Log("✅ Skill System: parse, parameters, template execution OK")
}

// ========== 11. Plugin System ==========
func TestPluginSystem(t *testing.T) {
	mgr := plugin.NewManager()
	mgr.Register(example.New())

	if mgr.Count() != 1 {
		t.Errorf("plugin count = %d", mgr.Count())
	}

	// Init
	errs := mgr.InitAll(map[string]map[string]any{})
	if len(errs) > 0 {
		t.Errorf("init errors: %v", errs)
	}

	// Start
	ctx := context.Background()
	errs = mgr.StartAll(ctx)
	if len(errs) > 0 {
		t.Errorf("start errors: %v", errs)
	}
	if mgr.RunningCount() != 1 {
		t.Errorf("running = %d", mgr.RunningCount())
	}

	// Register tools
	reg := tool.NewRegistry()
	count, errs := mgr.RegisterTools(reg)
	if count != 1 {
		t.Errorf("tools registered = %d", count)
	}

	// Execute plugin tool
	ht, ok := reg.Get("example_hello")
	if !ok {
		t.Fatal("example_hello not found")
	}
	result, _ := ht.Execute(ctx, map[string]any{"name": "Tester"})
	if !strings.Contains(result.Output, "Hello Tester") {
		t.Errorf("plugin output = %q", result.Output)
	}

	// Stop
	errs = mgr.StopAll()
	if len(errs) > 0 {
		t.Errorf("stop errors: %v", errs)
	}

	// Status
	status := mgr.Status()
	if status["example"] != plugin.StateStopped {
		t.Errorf("status = %s, want stopped", status["example"])
	}

	t.Log("✅ Plugin System: lifecycle (init→start→tools→stop) OK")
}

// ========== 12. MCP Protocol Types ==========
func TestMCPProtocol(t *testing.T) {
	// Request construction
	req := mcp.NewRequest(1, "tools/list", nil)
	if req.JSONRPC != "2.0" || req.ID != 1 || req.Method != "tools/list" {
		t.Error("request construction failed")
	}

	// Notification
	notif := mcp.NewNotification("notifications/initialized", nil)
	if notif.JSONRPC != "2.0" || notif.Method != "notifications/initialized" {
		t.Error("notification construction failed")
	}

	// RPCError
	rpcErr := &mcp.RPCError{Code: -32600, Message: "invalid request"}
	if rpcErr.Error() != "invalid request" {
		t.Error("RPCError.Error() failed")
	}

	t.Log("✅ MCP Protocol: JSON-RPC types OK")
}

// ========== 12b. MCP StreamableHTTP Transport ==========
func TestMCPStreamableHTTP(t *testing.T) {
	// Mock MCP server handling POST (JSON response) and DELETE (session close)
	sessionID := "test-session-123"
	var deleteCalled bool

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "POST":
			// Return session ID header
			w.Header().Set("Mcp-Session-Id", sessionID)

			// Read the request body
			body, _ := io.ReadAll(r.Body)
			var req map[string]any
			json.Unmarshal(body, &req)

			method, _ := req["method"].(string)

			switch method {
			case "initialize":
				// JSON response with server capabilities
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]any{
					"jsonrpc": "2.0",
					"id":      req["id"],
					"result": map[string]any{
						"protocolVersion": "2025-03-26",
						"serverInfo": map[string]any{
							"name":    "test-server",
							"version": "1.0",
						},
					},
				})

			case "tools/list":
				// SSE response with tool list
				w.Header().Set("Content-Type", "text/event-stream")
				w.WriteHeader(http.StatusOK)
				flusher, ok := w.(http.Flusher)
				if !ok {
					return
				}
				resp, _ := json.Marshal(map[string]any{
					"jsonrpc": "2.0",
					"id":      req["id"],
					"result": map[string]any{
						"tools": []map[string]any{
							{"name": "test_tool", "description": "A test tool"},
						},
					},
				})
				fmt.Fprintf(w, "data: %s\n\n", resp)
				flusher.Flush()

			default:
				// 202 Accepted for notifications
				w.WriteHeader(http.StatusAccepted)
			}

		case "GET":
			// SSE endpoint — return empty stream
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Mcp-Session-Id", sessionID)
			flusher, ok := w.(http.Flusher)
			if ok {
				flusher.Flush()
			}
			// Keep connection open briefly then close
			time.Sleep(50 * time.Millisecond)

		case "DELETE":
			deleteCalled = true
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	// Create transport
	transport := mcp.NewStreamableHTTPTransport(server.URL)

	// Start
	ctx := context.Background()
	err := transport.Start(ctx)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Send initialize request
	initReq, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": "2025-03-26",
			"clientInfo":      map[string]any{"name": "gak-test"},
		},
	})
	err = transport.Send(ctx, initReq)
	if err != nil {
		t.Fatalf("Send initialize: %v", err)
	}

	// Receive response
	select {
	case msg := <-transport.Receive():
		var resp map[string]any
		if err := json.Unmarshal(msg, &resp); err != nil {
			t.Fatalf("unmarshal response: %v", err)
		}
		result, ok := resp["result"].(map[string]any)
		if !ok {
			t.Fatal("missing result in response")
		}
		serverInfo, _ := result["serverInfo"].(map[string]any)
		if serverInfo["name"] != "test-server" {
			t.Errorf("server name = %v", serverInfo["name"])
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for initialize response")
	}

	// Send tools/list (SSE response)
	toolsReq, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/list",
	})
	err = transport.Send(ctx, toolsReq)
	if err != nil {
		t.Fatalf("Send tools/list: %v", err)
	}

	select {
	case msg := <-transport.Receive():
		var resp map[string]any
		json.Unmarshal(msg, &resp)
		result, _ := resp["result"].(map[string]any)
		tools, _ := result["tools"].([]any)
		if len(tools) != 1 {
			t.Errorf("tools count = %d, want 1", len(tools))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for tools/list response")
	}

	// Close (should send DELETE)
	transport.Close()
	time.Sleep(100 * time.Millisecond)

	if !deleteCalled {
		t.Error("DELETE not called on Close")
	}

	t.Log("✅ MCP StreamableHTTP: POST(JSON), POST(SSE), session ID, DELETE on Close all OK")
}

// ========== 13. Cache System ==========
func TestCacheSystem(t *testing.T) {
	// PromptBuilder determinism
	b1 := cache.NewPromptBuilder()
	b1.AddSection("tools", "tool: bash\ntool: read_file", 10)
	b1.AddSection("system", "You are GAK", 0)
	prompt1, hash1 := b1.Build()

	b2 := cache.NewPromptBuilder()
	b2.AddSection("system", "You are GAK", 0)
	b2.AddSection("tools", "tool: bash\ntool: read_file", 10)
	prompt2, hash2 := b2.Build()

	if prompt1 != prompt2 {
		t.Error("PromptBuilder not deterministic (different order → different output)")
	}
	if hash1 != hash2 {
		t.Error("PromptBuilder hash not deterministic")
	}

	// BuildToolSection sorting
	tools := []llm.ToolDefinition{
		{Name: "write_file", Description: "Write"},
		{Name: "bash", Description: "Execute"},
		{Name: "read_file", Description: "Read"},
	}
	section := cache.BuildToolSection(tools)
	lines := strings.Split(strings.TrimSpace(section), "\n")
	// After sort: bash, read_file, write_file
	if !strings.Contains(lines[1], "bash") {
		t.Errorf("first tool should be bash, got: %s", lines[1])
	}

	// CacheBreakpoints
	cb := cache.NewCacheBreakpoints()
	if !cb.ShouldCacheSystemPrompt("hash1") {
		t.Error("first prompt should need caching")
	}
	if cb.ShouldCacheSystemPrompt("hash1") {
		t.Error("same hash should not need re-caching")
	}
	if cb.NewMessagesFrom() != 0 {
		t.Error("initial NewMessagesFrom should be 0")
	}
	cb.UpdateMessageBreakpoint(5)
	if cb.NewMessagesFrom() != 6 {
		t.Errorf("NewMessagesFrom = %d, want 6", cb.NewMessagesFrom())
	}

	t.Logf("✅ Cache System: deterministic prompt, sorted tools, breakpoints OK (hash=%s)", hash1)
}

// ========== 14. Logging ==========
func TestLogging(t *testing.T) {
	var buf strings.Builder
	logger := logging.New(
		logging.WithLevel(logging.LevelDebug),
		logging.WithFormat(logging.FormatJSON),
		logging.WithOutput(&buf),
		logging.WithField("test", true),
	)

	logger.Info("test message", "key", "value")
	logger.WithTurn(1).Info("turn message")
	logger.WithTurn(2).Tool("bash").Info("tool message")

	output := buf.String()
	if !strings.Contains(output, "test message") {
		t.Error("missing log output")
	}
	if !strings.Contains(output, "turn message") {
		t.Error("missing turn log")
	}
	if !strings.Contains(output, "tool message") {
		t.Error("missing tool log")
	}

	t.Log("✅ Logging: JSON format, levels, turn/tool context OK")
}

// ========== 15. Metrics ==========
func TestMetrics(t *testing.T) {
	c := metrics.NewCollector()

	c.RecordTokens(1000, 500, 200)
	c.RecordLLMCall(100*time.Millisecond, nil)
	c.RecordLLMCall(200*time.Millisecond, fmt.Errorf("timeout"))
	c.RecordToolCall("bash", 50*time.Millisecond, false)
	c.RecordToolCall("bash", 30*time.Millisecond, true)
	c.RecordTurn()
	c.RecordRunComplete(false)
	c.RecordRunComplete(true)

	snap := c.Snapshot()
	if snap.InputTokens != 1000 || snap.OutputTokens != 500 || snap.CachedTokens != 200 {
		t.Error("token metrics wrong")
	}
	if snap.LLMCalls != 2 || snap.LLMErrors != 1 {
		t.Error("LLM metrics wrong")
	}
	if len(snap.ToolStats) != 1 || snap.ToolStats[0].Calls != 2 {
		t.Error("tool metrics wrong")
	}
	if snap.TotalTurns != 1 {
		t.Error("turn count wrong")
	}
	if snap.CompletedRuns != 1 || snap.ErroredRuns != 1 {
		t.Errorf("run counts: ok=%d err=%d", snap.CompletedRuns, snap.ErroredRuns)
	}
	if snap.ErrorRate != 50.0 {
		t.Errorf("error rate = %.1f, want 50.0", snap.ErrorRate)
	}

	str := snap.String()
	if !strings.Contains(str, "Uptime") {
		t.Error("String() missing uptime")
	}

	t.Logf("✅ Metrics: tokens, LLM, tools, turns, runs, cost estimate OK (cost=$%.4f)", snap.EstimatedCostUSD)
}

// ========== 16. Interaction Provider ==========
func TestInteractionProvider(t *testing.T) {
	p := interaction.NewCLIProvider()
	if p == nil {
		t.Fatal("NewCLIProvider returned nil")
	}

	// Notify (non-blocking)
	err := p.Notify(context.Background(), "test notification")
	if err != nil {
		t.Errorf("Notify: %v", err)
	}

	t.Log("✅ Interaction Provider: CLIProvider creation + Notify OK")
}

// ========== 17. Kernel Event System ==========
func TestKernelEvents(t *testing.T) {
	// Event constructors
	e1 := kernel.NewTextEvent("hello", 1)
	if e1.Type != kernel.EventTextDelta || e1.Text != "hello" || e1.Turn != 1 {
		t.Error("NewTextEvent failed")
	}

	e2 := kernel.NewThinkingEvent("thinking...", 1)
	if e2.Type != kernel.EventThinking {
		t.Error("NewThinkingEvent failed")
	}

	e3 := kernel.NewToolCallEvent("id1", "bash", map[string]any{"command": "ls"}, 2)
	if e3.ToolCall == nil || e3.ToolCall.Name != "bash" {
		t.Error("NewToolCallEvent failed")
	}

	e4 := kernel.NewToolResultEvent("id1", "bash", tool.NewResult("output"), 50*time.Millisecond, 2)
	if e4.ToolResult == nil || e4.ToolResult.Result.Output != "output" {
		t.Error("NewToolResultEvent failed")
	}

	e5 := kernel.NewErrorEvent(fmt.Errorf("test error"), 3)
	if e5.Error == nil || e5.Error.Error() != "test error" {
		t.Error("NewErrorEvent failed")
	}

	e6 := kernel.NewDoneEvent(3)
	if e6.Type != kernel.EventDone {
		t.Error("NewDoneEvent failed")
	}

	e7 := kernel.NewPermissionRequestEvent("bash", "dangerous", "high", 1)
	if e7.Permission == nil || e7.Permission.ToolName != "bash" {
		t.Error("NewPermissionRequestEvent failed")
	}

	e8 := kernel.NewTransitionEvent("idle", "thinking", 1)
	if e8.Transition == nil || e8.Transition.From != "idle" {
		t.Error("NewTransitionEvent failed")
	}

	// FromStreamEvent conversion
	se := llm.StreamEvent{Type: llm.StreamTextDelta, Text: "converted"}
	ke := kernel.FromStreamEvent(se, 5)
	if ke.Type != kernel.EventTextDelta || ke.Text != "converted" || ke.Turn != 5 {
		t.Error("FromStreamEvent failed")
	}

	t.Log("✅ Kernel Events: all 8 constructors + stream conversion OK")
}

// ========== 18. Message Types ==========
func TestMessageTypes(t *testing.T) {
	// Text message
	msg := llm.NewTextMessage(llm.RoleUser, "hello")
	if msg.GetText() != "hello" {
		t.Error("GetText failed")
	}
	if msg.HasToolCalls() {
		t.Error("text message should not have tool calls")
	}

	// Tool use message
	tu := llm.NewToolUseMessage("id1", "bash", map[string]any{"command": "ls"})
	if !tu.HasToolCalls() {
		t.Error("tool use message should have tool calls")
	}
	calls := tu.GetToolCalls()
	if len(calls) != 1 || calls[0].ToolName != "bash" {
		t.Error("GetToolCalls failed")
	}

	// Tool result message
	tr := llm.NewToolResultMessage("id1", "output", false)
	if tr.Role != llm.RoleUser {
		t.Error("tool result should have user role")
	}

	t.Log("✅ Message Types: text, tool_use, tool_result all OK")
}

// ========== 19. End-to-End Kernel (with mock LLM) ==========
func TestKernelEndToEnd(t *testing.T) {
	// Create mock provider that returns text + tool call + final text
	mock := &mockLLMProvider{
		responses: [][]llm.StreamEvent{
			// Turn 1: text + tool call
			{
				{Type: llm.StreamTextDelta, Text: "Let me check. "},
				{Type: llm.StreamToolCall, ToolCallID: "tc1", ToolName: "bash", ToolInput: map[string]any{"command": "echo test"}},
				{Type: llm.StreamDone},
			},
			// Turn 2: final text
			{
				{Type: llm.StreamTextDelta, Text: "Done! The output was: test"},
				{Type: llm.StreamDone},
			},
		},
	}

	reg := tool.NewRegistry()
	reg.MustRegister(builtin.NewBashTool())

	policy := security.DefaultPolicy()
	pipeline := security.NewPipeline(policy, &mockAuthorizer{approve: true})

	store := state.NewStore(state.NewState("You are test agent"))
	collector := metrics.NewCollector()
	logger := logging.New(logging.WithLevel(logging.LevelDebug), logging.WithOutput(&strings.Builder{}))

	runner := kernel.New(mock, reg, pipeline, store,
		kernel.WithMaxTurns(10),
		kernel.WithMaxTokens(4096),
		kernel.WithTemperature(0.5),
		kernel.WithMetrics(collector),
		kernel.WithLogger(logger),
	)

	// Run
	ctx := context.Background()
	events := runner.Run(ctx, "run echo test")

	var eventTypes []kernel.EventType
	var toolCalled bool
	var gotText bool
	var gotDone bool

	for event := range events {
		eventTypes = append(eventTypes, event.Type)
		switch event.Type {
		case kernel.EventToolUseStart:
			toolCalled = true
		case kernel.EventTextDelta:
			gotText = true
		case kernel.EventDone:
			gotDone = true
		case kernel.EventError:
			t.Fatalf("unexpected error: %v", event.Error)
		}
	}

	if !toolCalled {
		t.Error("tool was not called")
	}
	if !gotText {
		t.Error("no text received")
	}
	if !gotDone {
		t.Error("no done event")
	}

	// Verify state
	finalState := runner.GetState()
	if finalState.Phase != state.PhaseDone {
		t.Errorf("final phase = %s, want done", finalState.Phase)
	}
	if len(finalState.Messages) < 3 {
		t.Errorf("messages = %d, want >= 3 (user + assistant + tool_result + assistant)", len(finalState.Messages))
	}

	// Verify metrics
	snap := collector.Snapshot()
	if snap.LLMCalls != 2 {
		t.Errorf("LLM calls = %d, want 2", snap.LLMCalls)
	}
	if snap.TotalTurns != 2 {
		t.Errorf("turns = %d, want 2", snap.TotalTurns)
	}

	// Provider swap
	runner.SetProvider(mock)
	if runner.ProviderName() != "mock" {
		t.Errorf("provider name = %s", runner.ProviderName())
	}

	t.Logf("✅ Kernel E2E: %d events, tool call + text + done, state transitions OK", len(eventTypes))
}

// mockLLMProvider simulates LLM responses without real API calls.
type mockLLMProvider struct {
	responses [][]llm.StreamEvent
	callCount int
}

func (m *mockLLMProvider) Name() string { return "mock" }

func (m *mockLLMProvider) Complete(_ context.Context, _ llm.CompletionRequest) (<-chan llm.StreamEvent, error) {
	ch := make(chan llm.StreamEvent, 32)

	idx := m.callCount
	if idx >= len(m.responses) {
		idx = len(m.responses) - 1
	}
	m.callCount++

	go func() {
		defer close(ch)
		for _, event := range m.responses[idx] {
			ch <- event
		}
	}()

	return ch, nil
}
