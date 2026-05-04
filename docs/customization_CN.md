# 扩展与定制

Chord 支持多种可选扩展能力。建议先把基础使用跑通，再逐步添加这些能力。

## 自定义 Agents

你可以覆盖或新增角色配置：

- 全局：`~/.config/chord/agents/`
- 项目级：`.chord/agents/`

支持的文件格式包括 `.md`（YAML frontmatter 加 Markdown prompt 正文）以及 `.yaml` / `.yml`（plain YAML，通过 `prompt` 或 `system_prompt` 配置 prompt）。

常见用途：

- 为不同角色设置不同模型链
- 为不同角色设置不同权限
- 增加专门的 reviewer、backend、frontend、docs 等角色

完整的 Agent 配置字段、示例和 delegation 选项见 [配置与认证 — Agent 配置](./configuration_CN.md#agent-配置)。

## Skills

Chord 默认会从以下目录发现 Skills：

- `.chord/skills/`
- `.agents/skills/`
- `~/.config/chord/skills/`
- `skills.paths` 中配置的额外目录

运行时不会把所有 skill 正文预先注入 system prompt；只有任务明显匹配时，模型才会调用 `Skill` 工具按需加载对应内容。

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

Hooks 适合做这些事情：

- 通知
- 审计
- 自动化检查
- 工具结果清洗或拦截

常见触发点包括：

- `on_tool_call`
- `on_before_tool_result_append`
- `on_after_llm_call`
- `on_idle`
- `on_tool_batch_complete`

示例：

```yaml
hooks:
  on_idle:
    - name: notify-idle
      command: ["osascript", "-e", "display notification \"Chord 已空闲\" with title \"Chord\""]
```

## LSP

LSP 可以在你写文件后返回语义级诊断，并提供 `definition` / `references` / `implementation` 等能力。

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

是否可用取决于本机是否已安装对应语言服务器。对于 Pyright，如果未配置 Python 解释器，Chord 会自动使用 LSP root 下的项目本地虚拟环境，并按当前运行平台探测对应布局：类 Unix 系统（包括 WSL）查找 `.venv/bin/python`、`venv/bin/python` 和 `env/bin/python`；Windows 查找 `.venv\Scripts\python.exe`、`venv\Scripts\python.exe` 和 `env\Scripts\python.exe`。WSL 自动发现会有意避开 Windows 虚拟环境中的 `Scripts\python.exe`；建议在 WSL 内创建 Linux venv，确需自定义解释器时再显式配置 `python.pythonPath`。

如果某个语言服务器只应该在包含特定项目标记的目录内运行，可以配置 `root_markers`。如果省略它，则只由 `file_types` 决定该 server 是否处理某个文件。

对 Python 来说，通常更建议不要默认配置 `root_markers`。在 Chord 当前的 LSP 模型里，`root_markers` 只决定 Pyright 是否为某个文件启动；它不会把 Pyright 的工作区根目录重定向到最近的 `pyproject.toml` 或 `pyrightconfig.json`。因此，把 Python root markers 作为默认推荐配置，往往只会让合法的独立脚本或轻量项目无法启用 Pyright，却不能改善 workspace root 的选择。如果你确实需要更严格的项目范围控制，再按仓库实际情况显式添加 `root_markers`。

通常不需要手动设置 `python.pythonPath`。当未显式配置解释器时，Chord 已经会在 LSP root 下自动发现项目本地的 `.venv`、`venv` 或 `env`。只有当你需要覆盖这套自动发现逻辑、改用自定义解释器路径时，才需要设置 `python.pythonPath`。同样，`python.analysis` 也是按需启用的 Pyright 行为调优项，例如调整类型检查严格度。对于这类配置，请使用嵌套的 `options` section，例如：

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

如果你确实需要显式覆盖解释器，再在同样的嵌套 `options` 结构下添加 `python.pythonPath`：

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

MCP 适合把外部工具或远端数据源接入 Chord。

```yaml
mcp:
  exa:
    url: https://mcp.exa.ai/mcp
```

可以用 `allowed_tools` 只暴露部分工具，减少 token 开销。详见 [配置与认证](./configuration_CN.md#mcp)。

在本地模式下，MCP 会在 TUI 启动后异步连接；刚启动时短暂 unavailable 不一定代表配置错误。

## 自定义 slash commands

你可以在 `config.yaml` 中定义项目级或全局级 slash commands，把常用模板或操作包装成快捷入口。

```yaml
commands:
  /review: "请审查当前 diff 中的代码变更，关注正确性和安全性。"
  /commit: "请根据当前 staged 变更生成一条简洁的 commit message。"
```

输入 `/review` 后按 `Enter`，Chord 会把对应文本作为用户消息发送给模型。自定义命令也会出现在 `/` 自动补全列表中。

适合的场景：

- 统一代码审查提示词
- 统一提交说明模板
- 团队常用工作流入口

## 通知

你可以通过 Hooks 或桌面通知配置，在以下场景提醒自己：

- 权限确认
- 问题等待输入
- agent 完全停止

## 使用建议

- 先加 LSP，再考虑 Hooks / MCP
- 先做最小可用集成，再补复杂自动化
- 对每个扩展都明确权限与失败时行为

## 相关文档

- [配置与认证](./configuration_CN.md)
- [权限与安全](./permissions-and-safety_CN.md)
- [常见问题排查](./troubleshooting_CN.md)
