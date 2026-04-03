# GAK (Go-Agent-Kernel) 🚀

> 用 Go 的工程严谨性约束 LLM 的不确定性，打造工业级 AI Agent 架构体系。

GAK 是一套采用云原生思路构建的生产级 AI Agent 代理内核基座。秉承“状态机驱动”与“高度可定制化扩展”的设计哲学，将复杂的 Prompt 推理链与现实世界的工具执行进行严格且安全的编排。

## ✨ 核心特征

- 🔄 **事件驱动范式**：核心采用 `<-chan Event` 进行非阻塞的状态流转通信。
- 🧩 **四级护城河扩展模型**：
  - **Tool (内建)**：自带文件操作、系统 Bash 等安全基座功能。
  - **Skill (技能)**：零代码纯 YAML 工作流编排配置系统，热插拔，无缝热更新。
  - **Plugin (插件)**：Go 原生类型静态注入扩展结构，适应高性能/重依赖逻辑。
  - **MCP (标准化应用上下文)**：完美实现官方 Model Context Protocol，兼容 StreamableHTTP 及 Stdio 本地子进程无缝扩展。
- 🧱 **弹性模型架构层 (模型隔离)**：
  - 基于虚拟化 `models` 和命名空间的厂商映射，同一配置一次即可驱动类似 WindHub、CTYun 下多个兼容 OpenAI / Anthropic 协议的模型节点。
- 🔐 **严苛企业级安全风控**：内置四阶段安全审查管道（Static Filter → Validate → Pattern Check → Human-in-Loop 人工介入截断）。
- 💾 **无损不变量引擎**：结合 Checkpoint 的 Copy-on-append 数据流转引擎，支持 Agent 随时回滚（Rollback）、中止及暂停重载。

## 🚀 快速跳伞开始

### 安装与启动

1. 获取代码：
```bash
git clone https://github.com/AkriaLai/gak.git
cd gak
```

2. 准备配置及密匙：
配置具备层级覆盖特性：优先寻找当前木目录 `./.gak/` ，若不存在则应用全局配置 `~/.gak/`。

```bash
# 我们强烈建议作为全局工具使用：
mkdir -p ~/.gak
# 复制我们给您准备的模板配置
cp config.json.example ~/.gak/config.json

# 随后编辑 ~/.gak/config.json 给模型或者 MCP 填充你的真实 Token：
vim ~/.gak/config.json
```

3. 运行：
```bash
go run ./cmd/gak/
# 或构建后放入系统路径
go build -o gak ./cmd/gak/
```

### 命令交互

启动后可直接自由在终端输入需求给 Agent，或使用斜杠命令管理内核：
- `/models` — 查看所有可用模型节点。
- `/model windhub/gpt-5.4` — 热切换 Agent 思考引擎。
- `/stats` — 查看流式统计状态、费用和运行期 Token 损耗。
- `/rollback` — 将状态机回滚上一帧的错误对话或工具调用。
- `/quit` 或 `/exit` — 安全退出并封存 Checkpoint 记忆档案。

## 📂 项目结构

```text
gak/
├── cmd/gak/main.go          # CLI 执行入口及核心组装层
├── pkg/                     # (核心类库)
│   ├── kernel/              # 高性能异步推理流转枢纽引擎 + 核心事件系统
│   ├── config/              # 多级自动回滚配置处理器
│   ├── state/               # 带有 Publish-Subscribe 机制的不可变状态切片
│   ├── llm/                 # 统一异构厂商的多模型协议接口抽象
│   ├── tool/                # 底层工具沙盒及权限审计拦截门
│   ├── mcp/                 # Model Context Protocol 标准端点
│   ├── security/            # 基于拦截和风控正则的 Security Pipeline 实体
│   ├── plugin/              # Go 原生静态二次开发框架接入口
│   ├── skill/               # YMAL 描述热加载执行工作流脚本解析器
│   ├── session/             # 带有冷冻存储功能的会话封存器
│   └── interaction/         # 终端输入捕捉渲染控制器 (CLI)
├── .gak/                    # 本地调试存放目录（技能及节点）
├── config.json.example      # 安全配置文件实例
└── README.md
```

## ⚖️ License

MIT License.
