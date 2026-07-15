# 扩展与定制

Chord 支持多种可选扩展能力。建议先把基础使用跑通，再逐步添加。

## 仓库指令

项目需要给自动化 agent 提供长期有效的规则时，可以添加 `AGENTS.md`，例如编码规范、验证命令、安全要求或仓库专属审查准则。

会话开始时，Chord 会从 session working directory 向上探索到项目根目录，发现适用的 `AGENTS.md` 文件；随后按“项目根目录 → session working directory”的顺序注入每个非空文件的完整内容，并用相对 session working directory 的路径作为段落标题（例如从嵌套目录运行时会显示 `## ../../AGENTS.md`、`## ../AGENTS.md` 和 `## AGENTS.md`）。如果 session working directory 就是项目根目录，则只加载根目录的 `AGENTS.md`。这些指令会作为内部 user-role 消息注入 LLM 请求，位置在第一条真实用户消息之前。AGENTS.md 内容会以自识别的头部交付：首行为 `# AGENTS.md instructions`，随后是 `<INSTRUCTIONS> ... </INSTRUCTIONS>` 块。这个 meta message 可能不会显示在可见对话记录里，但 main agent 与 sub-agent 都会把它当作持久工作区指导来遵守，除非它与更高优先级的 system、developer 或 user 指令冲突。

Chord 也会为当前会话探测一个 Python 虚拟环境：从 session working directory 向上探索到项目根目录，并在每一层按 `.venv`、`venv`、`env` 的顺序检查。找到第一个有效环境后，prompt 会用相对 session working directory 的路径提示该环境，并要求 agent 运行 Python 命令时优先使用其中的解释器。

系统提示词是完全静态的：它只包含身份、准则和能力描述，不含任何动态字段。工作目录、平台、当前日期和探测到的虚拟环境路径通过同一个 session-context meta user 消息交付（与 AGENTS.md 内容相同的注入机制），注入在第一条真实用户消息之前，而不是嵌入在可缓存的系统前缀中。这样系统提示词在不同会话、不同日期、不同工作目录下都完全相同，最大化前缀缓存复用。上下文压缩后该 meta 消息会重新注入，因此环境信息不会丢失。

## 自定义 Agents

可覆盖或新增角色配置：

- 全局：`~/.config/chord/agents/`
- 项目级：`.chord/agents/`

支持 `.md`（YAML frontmatter 加 Markdown prompt 正文）以及 `.yaml` / `.yml`（纯 YAML，通过 `prompt` 或 `system_prompt` 配置 prompt）。

常见用途：为不同角色设置不同模型链和权限，或增加专门的 reviewer、backend、frontend、docs 等角色。

完整 Agent 配置字段、示例和委派选项见 [配置与认证 — Agent 配置](./configuration_CN.md#agent-配置)。

## Skills

Chord 默认从以下目录发现 Skills：

- `.chord/skills/`
- `.agents/skills/`
- `~/.config/chord/skills/`
- `skills.paths` 中配置的额外目录

运行时不会把所有 skill 正文预先注入 system prompt；任务明显匹配时，模型才会调用 `skill` 工具按需加载。

TUI 侧边栏的 **SKILLS** 区块只显示当前已发现的 skills。`skill` 工具成功加载某个 skill 后，该 skill 以绿色显示为已调用；加载失败不会标记，未发现/不存在的 skill 也不显示（直至被发现）。

Skill discovery 的结果是工作区级 catalog，但这不表示 MainAgent 与 SubAgent 共享相同权限或调用状态：

- 每个 Agent 都用自己的最新 Agent 配置和 permission rules 过滤 catalog，决定哪些 skills 可见、可由 `skill` 工具加载；
- MainAgent 与每个 SubAgent 分别记录 invoked 状态。一个 Agent 成功加载 skill，不会让其他 Agent 的同名 skill 变成“已调用”；
- parked SubAgent 恢复后，会从 durable task 状态及该任务的历史 transcript 恢复 invoked 名称，再按当前工作区 catalog 和最新 Agent 权限过滤展示；
- 当前正在进行的模型请求继续使用它开始时冻结的 prompt/tool surface；TUI 和后续 `skill` 工具解析使用当前 catalog 与当前 Agent 配置。

因此，工作区 catalog 可以安全复用，但 Agent 的可见列表、权限判断和 invoked 状态不能直接复用 MainAgent 的结果。

最小结构示例：

```text
.chord/skills/
└── go-expert/
    └── SKILL.md
```

`SKILL.md` 示例：

```markdown
---
name: go-expert
description: Go language development expert
---

遵循 Effective Go 和 Go Code Review Comments。
```

## Hooks

Hooks 让你在运行时的明确节点（工具调用前、LLM 调用后、idle 时、工具批量完成后等）运行外部命令，用途包括通知、审计、自动化检查、工具结果清洗。

简单示例——agent idle 时弹桌面通知：

```yaml
hooks:
  on_idle:
    - name: notify-idle
      command: ["osascript", "-e", "display notification \"Chord 已空闲\" with title \"Chord\""]
```

完整 14 个触发点列表、JSON envelope 协议、sync / automation / observer 三类差异及更多示例，见专门的 [Hooks](./hooks_CN.md) 页面。

## LSP

LSP 可在写文件后返回语义级诊断，并提供 `definition` / `references` / `implementation` 等能力。

典型配置：

```yaml
lsp:
  gopls:
    command: gopls
    file_types: [".go"]
    root_markers: ["go.work", "go.mod", ".git"]
  pyright:
    command: pyright-langserver
    args: ["--stdio"]
    file_types: [".py", ".pyi"]
  typescript:
    command: typescript-language-server
    args: ["--stdio"]
    file_types: [".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs"]
    root_markers: ["tsconfig.json", "jsconfig.json", "package.json", ".git"]
  rust-analyzer:
    command: rust-analyzer
    file_types: [".rs"]
    root_markers: ["Cargo.toml", "rust-project.json"]
```

需要先在本机安装对应语言服务器才能使用。对于 Pyright，未配置 Python 解释器时，Chord 会自动使用 LSP root 下的项目本地虚拟环境，并按当前运行平台探测对应布局：类 Unix（含 WSL）查找 `.venv/bin/python`、`venv/bin/python` 和 `env/bin/python`；Windows 查找 `.venv\Scripts\python.exe`、`venv\Scripts\python.exe` 和 `env\Scripts\python.exe`。WSL 自动发现有意识避开 Windows 虚拟环境中的 `Scripts\python.exe`；建议在 WSL 内创建 Linux venv，确需自定义解释器时再显式配置 `python.pythonPath`。

需要限制语言服务器的生效范围时，可配置 `root_markers`；省略时仅由 `file_types` 决定是否处理某文件。

对 Python 来说，通常不建议默认配置 `root_markers`。在 Chord 当前的 LSP 模型中，`root_markers` 只决定 Pyright 是否为某个文件启动，而不会将工作区根目录重定向到最近的 `pyproject.toml` 或 `pyrightconfig.json`。默认配置 Python root markers 往往只会让合法的独立脚本或轻量项目无法启用 Pyright，却不能改善 workspace root 的选择。需要更严格的项目范围控制时，再按仓库实际情况显式添加 `root_markers`。

通常无需手动设置 `python.pythonPath`。未显式配置解释器时，Chord 已在 LSP root 下自动发现项目本地的 `.venv`、`venv` 或 `env`。仅当需覆盖自动发现逻辑、改用自定义解释器路径时，才需设置 `python.pythonPath`。`python.analysis` 也是按需启用的 Pyright 行为调优项，如调整类型检查严格度。这类配置请使用嵌套 `options`：

```yaml
lsp:
  pyright:
    command: pyright-langserver
    args: ["--stdio"]
    file_types: [".py", ".pyi"]
    options:
      python.analysis:
        typeCheckingMode: strict
```

确需显式覆盖解释器时，在同样的嵌套 `options` 下添加 `python.pythonPath`：

```yaml
lsp:
  pyright:
    command: pyright-langserver
    args: ["--stdio"]
    file_types: [".py", ".pyi"]
    options:
      python:
        pythonPath: .venv/bin/python
```

## MCP

MCP 适合将外部工具或远端数据源接入 Chord。

```yaml
mcp:
  exa:
    url: https://mcp.exa.ai/mcp
```

可通过 `allowed_tools` 只暴露部分工具，减少 token 开销。详见 [配置与认证](./configuration_CN.md#mcp)。

本地模式下 MCP 会在 TUI 启动后异步连接。自动启动的 server 仍在后台连接，但第一次 LLM 请求会等待：每个自动启动的 server 要么连接成功，要么明确失败后才会继续。

对于不是每轮对话都需要的 MCP，建议设置 `manual: true`：启动时保持禁用，不连接该 server，也不把它的工具描述加入默认 LLM 工具上下文，从而降低平时的上下文开销。需要使用时，再通过 `/mcp`（菜单）或 `/mcp enable <server>` 手动启用。

在 TUI 中，按 `Ctrl+O` 可打开 MCP 选择器。Agent 运行中也可以打开它查看 server 状态并切换手动 server。运行中做出的变更会在下一次模型请求生效，因此当前正在进行的请求会继续使用它启动时的工具表面和 prompt。

只有 `manual: true` 的 server 才能在运行时修改状态。自动启动的 server 会作为默认工具上下文的一部分保持只读，不受 `/mcp enable|disable` 影响。

## 自定义 slash commands

可在 `config.yaml` 中定义项目级或全局级 slash commands，将常用模板或操作包装为快捷入口。

```yaml
commands:
  /review: "请审查当前 diff 中的代码变更，关注正确性和安全性。"
  /commit: "请根据当前 staged 变更生成一条简洁的 commit message。"
```

输入 `/review` 后，如果出现自动补全列表，先用 `Tab` 或 `Enter` 接受补全，再按 `Enter`；Chord 会将对应文本作为用户消息发送给模型。自定义命令也会出现在 `/` 自动补全列表中。

适合：统一代码审查提示词、统一提交说明模板、团队常用工作流入口。

## 通知

可通过 Hooks 或桌面通知配置，在以下场景提醒自己：权限确认、问题等待输入、agent 完全停止。

## 使用建议

- 先加 LSP，再考虑 Hooks / MCP
- 先做最小可用集成，再补复杂自动化
- 对每个扩展明确权限及失败时行为

## 相关文档

- [配置与认证](./configuration_CN.md)
- [权限与安全](./permissions-and-safety_CN.md)
- [常见问题排查](./troubleshooting_CN.md)
