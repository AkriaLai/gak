# GAK — Go-Agent-Kernel

> 用 Go 的工程严谨性约束 LLM 的不确定性

GAK 是一个基于 [Claude Code Agent 设计原则](https://github.com/anthropics/claude-code) 构建的 AI Agent 内核，用 Go 语言实现。

## 设计原则

| 原则 | Go 实现 |
|---|---|
| 🔄 异步流式优先 | `<-chan Event` + `context.Context` 取消 |
| 🔒 安全边界内嵌 | 四阶段管线：Filter → Validate → Authorize → Confirm |
| 💾 缓存感知设计 | System Prompt 稳定构建 + 消息追加不修改 |
| 🧩 渐进式能力扩展 | Tool → Skill → Plugin → MCP 四级模型 |
| 📦 不可变状态流转 | `State_N + Event → State_N+1`，DeepCopy 保证 |

## 快速开始

```bash
# 设置 API Key
export ANTHROPIC_API_KEY=sk-ant-...

# 运行
go run ./cmd/gak/
```

## 架构概览

```
┌─────────────────────────────────────────────┐
│                  CLI / Web                   │
│              (Event Consumer)                │
├─────────────────────────────────────────────┤
│              Kernel Runner                   │
│  ┌──────────┬──────────┬──────────────────┐ │
│  │  State   │   LLM    │   Tool Registry  │ │
│  │ Machine  │ Provider │  ┌────────────┐  │ │
│  │          │          │  │  Bash      │  │ │
│  │ S_N→S_N+1│ Anthropic│  │  ReadFile  │  │ │
│  │          │  OpenAI  │  │  WriteFile │  │ │
│  │          │          │  │  ListDir   │  │ │
│  └──────────┴──────────┤  │  (MCP...)  │  │ │
│                        │  └────────────┘  │ │
│  ┌─────────────────────┴──────────────────┐ │
│  │         Security Pipeline              │ │
│  │  Static → Validate → Check → Confirm   │ │
│  └────────────────────────────────────────┘ │
└─────────────────────────────────────────────┘
```

## 目录结构

```
gak/
├── cmd/gak/main.go          # CLI 入口
├── pkg/
│   ├── kernel/              # 核心推理循环 + 事件系统
│   ├── state/               # 不可变状态机 + 响应式 Store
│   ├── llm/                 # LLM Provider 抽象 + Anthropic 实现
│   ├── tool/                # Tool 接口 + 注册中心 + 内建工具
│   ├── security/            # 四阶段安全管线
│   ├── interaction/         # 用户交互抽象 (CLI/Web)
│   └── mcp/                 # MCP 协议 (TODO)
├── go.mod
└── README.md
```

## 核心概念

### 事件流 (Event Stream)

所有交互都通过 `<-chan kernel.Event` 传递：

```go
events := runner.Run(ctx, "帮我列出当前目录")
for event := range events {
    switch event.Type {
    case kernel.EventTextDelta:
        fmt.Print(event.Text)
    case kernel.EventToolUseStart:
        fmt.Printf("调用工具: %s\n", event.ToolCall.Name)
    case kernel.EventDone:
        fmt.Println("完成")
    }
}
```

### 不可变状态

每次状态变化都生成新对象：

```go
newState := oldState.
    WithMessage(msg).
    WithPhase(state.PhaseThinking).
    WithTurn(2)
// oldState 未被修改
```

### 安全管线

工具调用经过四阶段检查：

```go
// 1. Static Filter:   禁用工具对 LLM 不可见
// 2. Input Validate:  参数格式校验
// 3. Dynamic Check:   上下文相关的风险评估
// 4. Human-in-Loop:   高危操作需用户确认
result, _ := pipeline.Check(ctx, tool, input)
```

## License

MIT
