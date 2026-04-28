# 配置与认证

Chord 把“行为配置”和“凭据配置”分开管理。

- `~/.config/chord/config.yaml`：provider、model、权限、扩展能力等行为配置
- `~/.config/chord/auth.yaml`：API keys 或 OAuth 凭据
- `.chord/config.yaml`：项目级覆盖配置
- `~/.config/chord/agents/` / `.chord/agents/`：角色配置

## 配置层级

常见优先级可以理解为：

1. 内置默认
2. 全局配置
3. 项目配置
4. Agent 级配置

这样可以同时满足用户习惯、项目差异和不同 Agent 的能力特化。

## 最小 provider 配置

### Anthropic

```yaml
providers:
  anthropic:
    type: messages
    api_url: https://api.anthropic.com/v1/messages
    models:
      claude-opus-4.7:
        limit:
          context: 1000000
          output: 128000
```

### OpenAI Chat Completions

```yaml
providers:
  bigmodel:
    type: chat-completions
    api_url: https://open.bigmodel.cn/api/paas/v4/chat/completions
    models:
      glm-5.1:
        limit:
          context: 200000
          output: 131072
```

### OpenAI Responses

```yaml
providers:
  openai:
    type: responses
    api_url: https://api.openai.com/v1/responses
    models:
      gpt-5.5:
        limit:
          context: 1000000
          output: 128000
```

### OpenAI Codex preset

```yaml
providers:
  codex:
    preset: codex
    type: responses
    models:
      gpt-5.4:
        limit:
          context: 1000000
          output: 128000
```

如果省略 `type`，Chord 会根据 provider 配置自动识别：

- `preset: codex` → `responses`
- `api_url` 以 `/responses` 结尾 → `responses`
- `api_url` 以 `/chat/completions` 结尾 → `chat-completions`
- `api_url` 以 `/messages` 结尾 → `messages`

如果不匹配这些规则，需要显式设置 `type`。

## auth.yaml

`auth.yaml` 的 key 名需要与 `config.yaml` 中的 provider 名称对应：

```yaml
anthropic:
  - "$ANTHROPIC_API_KEY"

openai:
  - "$OPENAI_API_KEY"
```

也可以配置多个 key 作为轮换或备用来源。

## auth.yaml 中的环境变量

`auth.yaml` 的标量 API key 值支持环境变量展开：

```yaml
anthropic:
  - "$ANTHROPIC_API_KEY"

openai:
  - "${OPENAI_API_KEY}"
```

当标量字符串以 `$` 开头时会进行展开。未设置的环境变量会展开为空字符串并被过滤掉，除非 YAML 值本身就是字面量空字符串。这个展开能力适用于 `auth.yaml` 凭据，不代表 `config.yaml` 的所有字段都会自动展开环境变量。

如果你确实需要空 API key，请显式写字面量空字符串：

```yaml
local-provider:
  - ""
```

不要依赖未设置的环境变量来表达空 key。未设置的 `$ENV_VAR` 会被视为缺失凭据并被过滤掉。

## OAuth 登录

当前只有配置了 `preset: codex` 的 provider 才会被视为 OAuth provider。

```bash
# 自动选择已配置的 codex provider
chord auth

# 显式指定 provider
chord auth codex

# 无桌面环境 / SSH
chord auth codex --device-code
```

## 指定 provider 与 model

主 agent 的初始 provider/model 来自当前 `builder` agent 配置中的 `models`。如果没有配置
`builder` 模型，Chord 会按字母序从已配置的 provider/model 中选择第一个作为兜底。

```yaml
# ~/.config/chord/agents/builder.yaml 或 .chord/agents/builder.yaml
models:
  - anthropic/claude-opus-4.7
  - openai/gpt-5.5
```

这个有序 `models` 列表同时也是该 agent 运行时的 fallback 模型池。

## 使用 YAML anchor 复用模型模板

Chord 没有专门的 `model_templates` 配置字段，但可以使用 YAML anchor 和 merge key 来避免重复写模型限制与 variants。下面顶层的 `model_templates` 只是一个会被忽略的锚点容器。

```yaml
model_templates:
  gpt-400k: &gpt-400k
    limit:
      context: 400000
      output: 128000
    reasoning:
      summary: auto
    text:
      verbosity: medium
    variants:
      high:
        reasoning:
          effort: high
      xhigh:
        reasoning:
          effort: xhigh
    modalities:
      input: [text, image]

  gpt-1m: &gpt-1m
    limit:
      context: 1000000
      output: 128000
    reasoning:
      summary: auto
    text:
      verbosity: medium
    variants:
      high:
        reasoning:
          effort: high
      xhigh:
        reasoning:
          effort: xhigh
    modalities:
      input: [text, image]

  claude-1m: &claude-1m
    limit:
      context: 1000000
      output: 65536
    thinking:
      type: adaptive
      effort: medium
    variants:
      high:
        thinking:
          type: adaptive
          effort: high
      xhigh:
        thinking:
          type: adaptive
          effort: xhigh
    modalities:
      input: [text, image]

providers:
  codex:
    preset: codex
    models:
      gpt-5.2: *gpt-400k
      gpt-5.4: *gpt-1m

  openai:
    api_url: https://api.openai.com/v1/responses
    models:
      gpt-5.2: *gpt-400k
      gpt-5.4: *gpt-1m
      gpt-5.5: *gpt-1m

  anthropic:
    type: messages
    api_url: https://api.anthropic.com/v1/messages
    models:
      claude-opus-4.7: *claude-1m
```

示例中用到的模型字段含义：

- `limit.context`：模型上下文窗口大小。
- `limit.output`：模型自身最大输出 token 能力。运行时请求还会受到 `max_output_tokens` 限制。
- `reasoning`：OpenAI reasoning 选项，主要用于 Responses 风格的 reasoning 模型。`summary` 控制 reasoning summary 输出；variant 通常覆盖 `reasoning.effort`。
- `text.verbosity`：OpenAI text verbosity hint，取决于 provider/model 是否支持。
- `thinking`：Anthropic extended thinking 选项。`type: adaptive` 表示由 Chord 根据 `effort` 推导合适的 thinking budget；variant 可以覆盖 `thinking.effort`。
- `variants`：命名模型参数预设。可以通过 `openai/gpt-5.5@high` 或 `anthropic/claude-opus-4.7@xhigh` 这样的 model ref 选择。
- `modalities.input`：模型支持的输入模态。支持值包括 `text`、`image`、`pdf`。如果省略，Chord 为了向后兼容默认认为支持 `text` 和 `image`。

只有 Chord 模型 schema 中定义的字段会被使用。`modalities.output` 当前不会被运行时解释，因此示例中刻意省略。

## 项目级配置

如果某个项目需要特定默认值，可以在项目根目录创建：

```text
.chord/config.yaml
```

常见用途：

- 调整项目特有的权限规则
- 配置该项目的 LSP / MCP / Hooks / Skills

## Provider 请求压缩

Provider 级别的 `compress` 控制上游 HTTP 请求体的 gzip 压缩。它和上下文压缩不同：只影响 HTTP 请求传输编码，不会总结或移除对话历史。

```yaml
providers:
  openai:
    compress: true
```

启用后，Chord 只会在 gzip 后体积更小时发送压缩请求；否则仍发送未压缩请求。除非你的 provider 或网关明确受益于请求体压缩，否则可以不配置。

## 输出 token 上限

使用 `max_output_tokens` 设置全局输出 token 请求上限。实际请求上限仍会被每个模型的 `limit.output` 和可用上下文限制。

```yaml
max_output_tokens: 32000
```

## 本地 TUI 选项

这些选项影响本地 TUI。它们可以写在全局配置里，也可以在合适时由项目级 `.chord/config.yaml` 覆盖。

```yaml
desktop_notification: true
ime_switch_target: com.apple.keylayout.ABC
prevent_sleep: true
```

- `desktop_notification`：在本地 TUI 中启用 OSC 9 终端通知，主要用于终端失焦场景。Chord 会在权限确认、等待用户回答、agent 回到 idle 等场景发送通知。
- `ime_switch_target`：通过 `im-select`（Windows 为 `im-select.exe`）在进入 Normal 模式时切换到指定输入法，并在回到 Insert 模式时恢复之前的输入法。常用于让快捷键使用英文键盘布局。
- `prevent_sleep`：在任意 agent 活跃时阻止 macOS 进入空闲睡眠。只在本地 TUI 模式生效。

## WebFetch

`WebFetch` 默认使用内置的 browser-like `User-Agent`。如果某些站点需要不同请求头，可以在配置中覆盖：

```yaml
web_fetch:
  user_agent: Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/136.0.0.0 Safari/537.36
```

这个配置同时支持全局配置和项目级 `.chord/config.yaml`，且项目级配置优先。

`WebFetch` 会保持轻量的静态 HTTP 读取定位，不运行本地浏览器。对于 JS-heavy 页面，如果返回的 HTML 像应用空壳而不是可读正文，结果会标记为 `Content-Quality: suspect-shell`。

## MCP

MCP server 可能暴露很多工具。可以用 `allowed_tools` 只允许部分远端工具进入 Chord，从而避免把不用的 tool schema 发送给模型：

```yaml
mcp:
  search:
    url: https://mcp.exa.ai/mcp
    allowed_tools:
      - web_search_exa
      - web_fetch_exa
```

被过滤的工具不会注册，也不会进入 LLM tool surface。上例中的 `search` 是用户自定义 MCP server 名；Chord 只会注册 `mcp_search_web_search_exa` 和 `mcp_search_web_fetch_exa`。

## Agent 配置

内置角色包括 `builder`、`planner`。你也可以新增自定义 agent 或覆盖内置 agent。Agent 文件可以放在：

- `~/.config/chord/agents/`
- `.chord/agents/`

支持的文件格式：

- `.md`：YAML frontmatter 加 Markdown 正文，正文会作为 system prompt。
- `.yaml` / `.yml`：普通 YAML 文档，通过 `prompt` 或 `system_prompt` 配置 system prompt。

Markdown agent 示例：

```markdown
---
name: backend-coder
description: Backend developer
mode: subagent
models:
  - anthropic/claude-opus-4.7
permission:
  Write: ask
  Edit: ask
---

你是一个专注于后端开发的 Agent。
```

等价的 YAML agent 示例：

```yaml
name: backend-coder
description: Backend developer
mode: subagent
models:
  - anthropic/claude-opus-4.7
permission:
  Write: ask
  Edit: ask
prompt: |
  你是一个专注于后端开发的 Agent。
```

常用字段包括：

- `name`：agent 名称。省略时使用不带扩展名的文件名。
- `description`：简短描述，会在 delegation 可用时展示给 main agent。
- `mode`：`subagent` 或其他角色模式；为空时默认按 subagent 行为处理。
- `models`：该 agent 的有序模型池，同时决定 fallback 顺序。支持 `openai/gpt-5.5@high` 这样的 inline variant。
- `variant`：当 model ref 没有写 `@variant` 时使用的默认 variant。
- `permission`：该 agent 的 per-tool 权限策略。
- `mcp`：作用域限定在该 agent 的 MCP 配置。
- `delegation`：例如 `max_children`、`max_depth`、`child_join` 等 delegation 限制。
- `prompt` / `system_prompt`：plain YAML agent 文件中的 system prompt。

## 上下文压缩

当主会话接近模型上下文上限时，Chord 可以自动执行 durable compaction。常见相关配置：

```yaml
context:
  auto_compact: true
  compact_threshold: 0.8
  compact_model: openai/gpt-5.4-mini
```

如果你的模型上下文较小，建议保持自动压缩开启。

## Provider 连通性自检

```bash
# 检查所有 provider
chord test-providers

# 只检查一个 provider
chord test-providers --provider openai
```

这个命令适合做认证与基础连通性 smoke test。

## 相关文档

- [快速开始](./quickstart_CN.md)
- [权限与安全](./permissions-and-safety_CN.md)
- [扩展与定制](./customization_CN.md)
- [常见问题排查](./troubleshooting_CN.md)
