# 扩展与定制

先选择你想改变的行为，不需要一次配置所有扩展：

| 想做什么 | 使用哪项扩展 |
| --- | --- |
| 让 Agent 遵守项目规则 | [仓库指令](#仓库指令) |
| 给不同任务分配模型和权限 | [自定义 Agents](#自定义-agents) |
| 按需加载专业知识或操作步骤 | [Skills](#skills) |
| 自动通知、检查或处理工具结果 | [Hooks](#hooks) |
| 获取代码诊断、定义和引用 | [LSP](#lsp) |
| 接入外部工具 | [MCP](#mcp) |
| 保存常用提示词 | [自定义命令](#自定义-slash-commands) |

## 仓库指令

项目需要给自动化 agent 提供长期有效的规则时，可以添加 `AGENTS.md`，例如编码规范、验证命令、安全要求或仓库专属审查准则。

会话开始时，Chord 从当前工作目录向上查找，直到项目根目录，再按从根目录到当前目录的顺序加载 `AGENTS.md`。从项目根启动时，只加载根目录的文件。主 Agent 和子 Agent 都会收到这些规则；它们不覆盖更高优先级的指令。

Python 项目中，Chord 会沿同一路径寻找最近的有效虚拟环境，每层依次检查 `.venv`、`venv`、`env`，并提示 Agent 优先使用其中的解释器。工作目录、平台和虚拟环境信息会在上下文压缩后继续提供，无需重复说明。

## 自定义 Agents

可覆盖或新增角色配置：

- 全局：`~/.config/chord/agents/`
- 项目级：`.chord/agents/`

支持 `.md`（YAML frontmatter 加 Markdown prompt 正文）以及 `.yaml` / `.yml`（纯 YAML，通过 `prompt` 或 `system_prompt` 配置 prompt）。

常见用途：为不同角色设置不同模型链和权限，或增加专门的 reviewer、backend、frontend、docs 等角色；也可以通过 `prompt_preset` 让自定义角色复用内置角色 prompt 块，并用 `prompt_append` 在其基础上补充而非替换。

完整 Agent 配置字段、示例和委派选项见 [配置与认证：Agent 配置](./configuration_CN.md#agent-配置)。

## Skills

Chord 默认从以下目录发现 Skills：

- `.chord/skills/`
- `.agents/skills/`
- `~/.config/chord/skills/`
- `skills.paths` 中配置的额外目录

运行时不会把所有 skill 正文预先注入 system prompt；任务明显匹配时，模型才会调用 `skill` 工具按需加载。

TUI 侧边栏的 **SKILLS** 区块只显示当前已发现的 skills。`skill` 工具成功加载某个 skill 后，该 skill 以绿色显示为已调用；加载失败不会标记，未发现/不存在的 skill 也不显示（直至被发现）。

每个 Agent 只能看到和加载自己权限允许的技能。加载状态分别记录：主 Agent 加载过某个技能，不代表子 Agent 也已加载。恢复子任务时，Chord 会恢复其加载记录，并按当前权限显示。

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

agent idle 时弹桌面通知的简单示例：

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
    options:
      staticcheck: true
      analyses:
        minmax: true
        rangeint: true
        slicescontains: true
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

`options` 用于语言服务器的 workspace settings。对 gopls，应把 `staticcheck`、`analyses` 等设置放在这里；`init_options` 只作为 LSP 初始化元数据发送，并不是 gopls settings 的正确位置。可用的 analyzer 名称及其默认值取决于本机安装的 gopls 版本。较新的 gopls 已默认启用大多数 `modernize` analyzer；显式设为 `true` 可以记录并保留项目依赖的检查，设为 `false` 则可关闭单项检查。修改 Go 文件后，Chord 会透传 gopls 的 information 和 hint 诊断，但在默认最多 10 条的输出额度内，error 和 warning 会优先展示。

这种 LSP 反馈是编辑后的增量检查，不能替代 CI 中的全仓门禁。若项目要在 CI 中采用独立的 `modernize` 命令，应先清理并审查现有发现，再固定命令版本，而不是使用 `@latest`；部分建议修复（例如把 `omitempty` 改为 `omitzero`）会有意改变序列化行为，必须人工审查。

需要先在本机安装对应语言服务器才能使用。对于 Pyright，未配置 Python 解释器时，Chord 会从 LSP workspace root 向上寻找最近的有效虚拟环境，不越过项目根；类 Unix 查找 `.venv/bin/python`、`venv/bin/python` 和 `env/bin/python`，Windows 查找对应的 `Scripts\python.exe`。同一 workspace root 的发现结果会随 LSP client 缓存，避免重复探测。

`file_types` 决定语言服务器处理哪些文件，`root_markers` 决定工作区根目录。省略 `root_markers` 时，TypeScript/JavaScript 使用 `tsconfig.json`、`jsconfig.json` 和 `package.json`，Pyright 使用 `pyrightconfig.json`、`pyproject.toml` 和 `requirements.txt`，两类服务器不会把另一种语言的标记当作项目边界。其他服务器回退到 Chord 项目根；显式配置 `root_markers` 会覆盖默认标记。

Chord 根据配置中的服务器名或可执行文件名识别类型：`typescript` / `typescript-language-server`、`pyright` / `pyright-langserver`、`basedpyright` / `basedpyright-langserver`，也支持 Windows 可执行文件后缀。使用自定义包装脚本时，保留这些服务器名，或显式设置 `root_markers`。目录查找始终限制在项目根内。

对匹配的文件，Chord 会按以下规则确定该语言服务器的 workspace root：从文件所在目录向上，取最近一个包含任一 `root_markers` 的目录（不越过项目根）；没有匹配则回退到项目根。发现是按文件进行的，所以不同文件可能落在不同的根上，同一个服务器名也能按根各起一个实例。这样嵌套前端工程（例如仓库根本身是后端项目、前端在 `frontend/` 子目录）就能得到 root 定位到该子包的语言服务器，直接在包内找它的 `node_modules`、`tsconfig.json` 等包级配置。单个服务器名最多保留 8 个存活实例；monorepo 中标记目录超出这个数量时，最久未使用的实例会被关闭，下次读取其根下的文件时再重启。

Python、TypeScript 和 JavaScript 都会按文件发现最近的 workspace root；同一服务器名可以为不同根目录缓存独立实例。

通常无需手动设置 `python.pythonPath`。只有需覆盖自动发现逻辑或改用自定义解释器路径，才需设置它。`python.analysis` 也是按需启用的 Pyright 行为调优项，如调整类型检查严格度。这类配置请使用嵌套 `options`：

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

MCP 服务器把外部工具或远端数据源暴露给模型。

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

## 通知

Hooks 或桌面通知配置可以在以下场景提醒你：权限确认、问题等待输入、agent 完全停止。

## 相关文档

- [配置与认证](./configuration_CN.md)
- [权限与安全](./permissions-and-safety_CN.md)
- [常见问题排查](./troubleshooting_CN.md)
