# 配置与认证

Chord 将行为配置与凭据配置分开管理。

- `~/.config/chord/config.yaml`：provider、模型、权限、扩展能力等行为配置
- `~/.config/chord/auth.yaml`：API key 或 OAuth 凭据
- `.chord/config.yaml`：项目级覆盖配置
- `~/.config/chord/agents/` / `.chord/agents/`：角色配置

## 配置层级

优先级从低到高：

1. 内置默认
2. 全局配置
3. 项目配置
4. Agent 级配置

兼顾用户习惯、项目差异和不同 Agent 的能力特化。

## 流式工具早执行

模型仍在流式输出响应、工具参数刚完整时，Chord 会提前执行一小批安全的只读工具，而不必等服务商完成最终确认（`CompleteStream()` 返回）后才开始。这能显著缩短最终确认阶段的体感等待时间。

- 始终启用，无 `early_tool_execution` 开关。
- 允许早执行的工具：`Read`、`Grep`、`Glob`，支持回滚的文件编辑工具（`Write`、`Edit`、`Delete`），以及 `Shell` 的保守只读子集（仅限单命令，不含管道/重定向/`&&`/`;` 等组合）：
  - `pwd`、`ls`、`cat`、`which`
  - `git status|log|diff|show|branch|rev-parse`
- 不允许早执行：非只读 `Shell`、交互/控制类工具，以及权限为 `ask` 的工具调用。
- 提前执行的文件变更会真实落盘，但运行时会先捕获变更前状态；若最终确认阶段丢弃了该调用，则自动回滚。同一回合内若多个提前执行的文件变更命中同一路径，后续冲突调用会跳过，留给正式路径处理。同一回合内只要有任意尚未提交的提前执行文件变更（不限路径），后续的读类早执行都会跳过——这避免了读取未提交状态，代价是读早执行会被进行中的写早执行短暂阻塞。
- 提前执行的结果可能提前显示在界面上，但只有在最终确认通过后才会追加进对话上下文；未通过的结果会被丢弃，界面显示为「推测执行，已丢弃（不属于上下文）」。

## 最小 provider 配置

### ModelScope

```yaml
providers:
  modelscope:
    type: chat-completions
    api_url: https://api-inference.modelscope.cn/v1/chat/completions
    models:
      Qwen/Qwen3.5-397B-A17B:
        limit:
          context: 262144
          input: 262144
          output: 65536
        modalities:
          input: [text, image]
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
          context: 400000
          input: 272000
          output: 128000
```

按这个顺序理解模型限制：

1. `limit.context` 是总窗口。对大多数模型，只要“输入 + 请求输出”放得进这个数字即可。
2. `limit.input` 只在 provider 还单独列出输入上限时才需要。部分 GPT 模型属于这种情况；如果省略，Chord 会从 `limit.context` 中预留有效请求输出后，推导可用输入预算。
3. `limit.output` 是模型的最大输出能力。Chord 默认 `max_output_tokens` 仍是 `32000`，所以实际发送请求时会取更小的输出上限，除非你主动调大。

`gpt-5.5` 示例使用 `context=400000`、`input=272000`、`output=128000`。provider 文档里有时会把这类配置叫作 split limits；见 [术语表](./glossary_CN.md)。

### OpenAI Codex preset

```yaml
providers:
  codex:
    preset: codex
    type: responses
    models:
      gpt-5.5:
        limit:
          context: 400000
          input: 272000
          output: 128000
```

### Google Gemini

```yaml
providers:
  gemini:
    api_url: https://generativelanguage.googleapis.com/v1beta/models
    models:
      gemini-3.1-pro-preview:
        limit:
          context: 1048576
          output: 65536
```

Gemini 的 `api_url` 应设为 `/models` 基础路径。Chord 根据 `/models` 后缀自动识别为 `type: generate-content`，可省略 `type`。不要在 URL 中包含模型名或 `:streamGenerateContent?alt=sse`，Chord 会自动追加 `/{model}:streamGenerateContent?alt=sse`。`models` 下的 key（如 `gemini-3.1-pro-preview`）即为发送给 Gemini 的模型 ID。

省略 `type` 时，Chord 按以下规则自动推断：

- `preset: codex` → `responses`
- `api_url` 以 `/responses` 结尾 → `responses`
- `api_url` 以 `/chat/completions` 结尾 → `chat-completions`
- `api_url` 以 `/messages` 结尾 → `messages`
- `api_url` 以 `/models` 结尾 → `generate-content`

不匹配以上规则时，需显式设置 `type`。

## auth.yaml

`auth.yaml` 的 key 名需与 `config.yaml` 中的 provider 名称对应：

```yaml
anthropic:
  - "$ANTHROPIC_API_KEY"

openai:
  - "$OPENAI_API_KEY"
```

可配置多个 key 作为轮换或备用。

对于 `preset: codex` 的 OAuth 凭据，Chord 还可能在 `auth.yaml` 中、同一个 provider key 下，持久化 provider 专用的软提示字段。例如当 `config.yaml` 中的 provider 名叫 `codex` 时：

```yaml
codex:
  - refresh: "..."
    access: "..."
    expires: 1774009702606
    account_id: acc-1
    codex_primary_reset_at: 1774013302000
    codex_secondary_reset_at: 1774600000000
```

这些 `codex_*_reset_at` 字段是跨重启保留的调度提示，不是硬封禁：

- 会影响启动后 / 首次选号时的优先级；
- **不会**仅凭字段本身就让账号绝对不可选；
- 真正的硬封禁仍来自已确认的请求失败，并只在运行时内存中跟踪。

## auth.yaml 中的环境变量

`auth.yaml` 的标量 API key 值支持环境变量展开：

```yaml
anthropic:
  - "$ANTHROPIC_API_KEY"

openai:
  - "${OPENAI_API_KEY}"
```

标量字符串以 `$` 开头时触发展开。未设置的环境变量会展开为空字符串并被过滤，除非 YAML 值本身就是字面空字符串。该展开仅适用于 `auth.yaml` 凭据，`config.yaml` 的字段不会自动展开。

确实需要空 API key 时，请显式写字面空字符串：

```yaml
local-provider:
  - ""
```

不要依赖未设置的环境变量来表示空 key——未设置的 `$ENV_VAR` 会被视为缺失凭据而过滤掉。

## OAuth 登录

当前仅配置了 `preset: codex` 的 provider 支持 OAuth。

对 Codex provider，`key_order` 额外支持：

- `sequential`
- `random`
- `smart`

当 `preset: codex` 且未显式配置 `key_order` 时，Chord 默认使用 `key_order: smart`；其它 provider 仍默认 `sequential`。

`smart` 不改变现有 `key_rotation` 语义，但会在可选的 Codex OAuth 账号之间优先：

- 绕开仍带有持久化 soft-cooling hint 的账号；
- 优先当前进程内尚未使用过的账号；
- 在已有 rate-limit snapshot 时优先剩余额度头寸更高的账号；
- 当没有更优候选时，仍允许回退尝试 soft-cooled 账号。

当 Codex client 变为活跃状态后，Chord 还可能在后台探测其他 OAuth slot，以刷新缓存的 headroom 快照。这个 warm-up 是 best-effort、低并发的，并且会在活跃 client 被替换时取消。

```bash
# 自动选择已配置的 codex provider
chord auth

# 显式指定 provider
chord auth codex

# 无桌面环境 / SSH
chord auth codex --device-code
```

## 模型池

Chord 通过命名模型池选择当前使用的模型。

模型池在 `config.yaml`（全局或项目级）中定义；agent 配置只能引用池名，不允许在 agent 中内联定义池。

### 在 config.yaml 中定义 model_pools

```yaml
# ~/.config/chord/config.yaml 或 .chord/config.yaml
model_pools:
  thinking:
    - anthropic/claude-opus-4.7
    - openai/gpt-5.5
  non-thinking:
    - anthropic/claude-sonnet-4
```

项目级 `.chord/config.yaml` 的 `model_pools` 会合并到全局配置中（同名覆盖）。

### 在 agent 中引用池名

```yaml
# ~/.config/chord/agents/builder.yaml 或 .chord/agents/builder.yaml
name: builder
mode: main
model_pools: [thinking, non-thinking]
```

```yaml
# .chord/agents/reviewer.yaml
name: reviewer
mode: subagent
model_pools: [thinking]
```

未显式选择池时，Chord 回退到该 agent `model_pools: [...]` 列表中的第一个池。

运行时通过 `/models` 切换当前视图对象的池（按项目持久化，重启后仍生效）：main 视图作用于当前主角色，SubAgent 视图作用于该 agent。切换池会更新后续 LLM 调用的整条 fallback 链；即使当前选中的 `provider/model` 同时存在于两个池中，也会按新池的顺序重新构建（已发起的 in-flight 请求仍使用其开始时快照到的 client）。也可通过 `/models --agent <name> <pool>` 直接设置指定 agent 的池。SubAgent 默认使用 `model_pools` 列表中的第一个池；想恢复默认时切回第一个池即可。

## 用 YAML anchor 复用模型模板

Chord 没有 `model_templates` 配置字段，但可用 YAML anchor 和 merge key 避免重复书写模型限制与 variants。下面顶层的 `model_templates` 仅作为锚点容器，Chord 会忽略其内容。

```yaml
model_templates:
  gpt-400k: &gpt-400k
    limit:
      context: 400000
      input: 272000
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
      context: 1050000
      input: 922000
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
      gpt-5.5: *gpt-400k

  openai:
    api_url: https://api.openai.com/v1/responses
    models:
      gpt-5.4: *gpt-1m
      gpt-5.5: *gpt-400k

  anthropic:
    type: messages
    api_url: https://api.anthropic.com/v1/messages
    models:
      claude-opus-4.7: *claude-1m
```

用到的模型字段含义：

- `limit.context`：已知时表示总请求窗口大小。
- `limit.input`：只在 provider 还单独公布了输入上限时才需要配置。Chord 用它判断何时在 prompt 过大前压缩，以及 provider 因请求过大而拒绝后如何重试。若省略，Chord 会按 `limit.context - 有效请求输出` 推导输入预算；有效请求输出来自 `max_output_tokens`，并受 `limit.output` 上限约束。它本身不会直接压低请求输出上限；输出裁剪遵循 `limit.output`、`max_output_tokens` 和已知的总窗口余量（`limit.context`）。
- `limit.output`：模型最大输出 token。实际请求还会受 `max_output_tokens` 限制，因此运行时会取两者里更小的值。
- `reasoning`：OpenAI reasoning 选项，主要用于 Responses 风格的 reasoning 模型。`summary` 控制推理摘要输出；variant 通常覆盖 `reasoning.effort`。
- `text.verbosity`：OpenAI 文本详细程度提示，取决于 provider/model 是否支持。
- `thinking`：Anthropic 扩展思考选项。`type: adaptive` 表示 Chord 根据 `effort` 推算合适的思考预算；variant 可覆盖 `thinking.effort`。
- `variants`：命名模型参数预设，可通过 `openai/gpt-5.5@high` 或 `anthropic/claude-opus-4.7@xhigh` 引用。
- `modalities.input`：模型支持的输入类型，可选 `text`、`image`、`pdf`。省略时默认 `[text, image]`。

只有 Chord 模型 schema 中定义的字段会被使用。`modalities.output` 当前不被运行时解释，示例中刻意省略。

## 项目级配置

项目需要特定默认值时，在项目根目录创建：

```text
.chord/config.yaml
```

常见用途：调整项目特有权限规则、配置该项目的 LSP / MCP / Hooks / Skills。

## Provider 请求压缩

Provider 级别的 `compress` 控制上游 HTTP 请求体的 gzip 压缩。它和上下文压缩是两回事——只影响请求传输编码，不会总结或移除对话历史。

```yaml
providers:
  openai:
    compress: true
```

启用后，Chord 仅在 gzip 能减小体积时才发送压缩请求。除非你的 provider 或网关明确受益于请求体压缩，否则无需配置。

## 输出 token 上限

`max_output_tokens` 设置全局输出 token 请求上限。实际请求上限仍受各模型 `limit.output` 和可用总上下文（已知时为 `limit.context`）限制，因此运行时会取适用限制中的最小值。

`limit.input` 是另一回事：只有当模型除了总上下文窗口外，还额外存在输入上限时才需要配置。降低 `max_output_tokens` 有助于控制成本、降低超长输出失败风险，但**不会**提升 provider 的输入上限，也不能替代 `limit.input`。

```yaml
max_output_tokens: 32000
```

## 本地 TUI 选项

以下选项影响本地 TUI，可写在全局配置中，也可由项目级 `.chord/config.yaml` 覆盖。

```yaml
desktop_notification: true
ime_switch_target: com.apple.keylayout.ABC
prevent_sleep: true
```

- `desktop_notification`：启用 OSC 9 终端通知（主要用于终端失焦场景），在权限确认、等待回答、agent 回到 idle 时发送通知。
- `ime_switch_target`：进入 Normal 模式时通过 `im-select`（Windows 为 `im-select.exe`）切换到指定输入法，回到 Insert 模式时恢复。常用于让快捷键在英文键盘布局下工作。
- `prevent_sleep`：任意 agent 活跃时阻止 macOS 空闲睡眠，仅本地 TUI 模式生效。

## WebFetch

`WebFetch` 默认使用类似浏览器的 `User-Agent`。某些站点需要不同请求头时，可在配置中覆盖：

```yaml
web_fetch:
  user_agent: Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/136.0.0.0 Safari/537.36
```

该配置同时支持全局和项目级，项目级优先。

也可为 WebFetch 请求单独配置代理：

```yaml
web_fetch:
  proxy: socks5://127.0.0.1:1080  # 支持 http, https, socks5
```

- `proxy: nil`（默认）—— 继承全局 `proxy` 配置
- `proxy: ""`（空字符串）—— 显式直连（不走代理）
- `proxy: "http://..."`、`"https://..."`、`"socks5://..."` —— 使用指定代理

`WebFetch` 保持轻量级静态 HTTP 读取，不运行本地浏览器。对 JS-heavy 页面，若返回的 HTML 只有应用空壳而非可读正文，结果会标记为 `Content-Quality: suspect-shell`。

## MCP

MCP server 可能暴露大量工具。通过 `allowed_tools` 只允许部分远端工具进入 Chord，避免把不必要的 tool schema 发送给模型：

```yaml
mcp:
  search:
    url: https://mcp.exa.ai/mcp
    allowed_tools:
      - web_search_exa
      - web_fetch_exa
```

被过滤的工具不会注册，也不会进入 LLM 工具列表。上例中 `search` 是用户自定义的 MCP server 名；Chord 只会注册 `mcp_search_web_search_exa` 和 `mcp_search_web_fetch_exa`。

### 手动（按需）启用 MCP

默认情况下，已配置的 MCP server 会自动启动，并成为默认 LLM 工具上下文的一部分。对于不是每轮对话都需要的 MCP，可设置 `manual: true`：启动时保持禁用，平时不连接该 server，也不把它的工具描述加入默认上下文，从而降低上下文开销；需要用到时再手动启用。

```yaml
mcp:
  exa:
    url: https://mcp.exa.ai/mcp
    manual: true
```

- `manual: true` 时，启动后该 server 处于禁用（灰色）状态，不会主动连接。
- 只有配置了 `manual: true` 的 server 才能在运行时通过 `/mcp` 修改状态。自动启动的 server 在 MCP 选择器中是只读的，也不会受 `/mcp enable|disable` 影响。
- 运行时可用 `/mcp`（TUI 菜单）或带参数命令启用/禁用：
  - `/mcp enable <server>`
  - `/mcp disable <server>`
  - `/mcp status`

### 启动一致性

自动启动的 MCP server 仍会在 TUI 启动后异步连接，但 **第一次 LLM 请求会等待**：每个自动启动的 server 要么连接成功，要么明确失败后才会继续发起请求，以避免工具描述不一致。

## Agent 配置

内置角色包括 `builder`、`planner`。可新增自定义 agent 或覆盖内置 agent。Agent 文件可放在：

- `~/.config/chord/agents/`
- `.chord/agents/`

支持的文件格式：

- `.md`：YAML frontmatter 加 Markdown 正文，正文作为 system prompt。
- `.yaml` / `.yml`：普通 YAML 文档，通过 `prompt` 或 `system_prompt` 配置 system prompt。

Markdown agent 示例：

```markdown
---
name: backend-coder
description: Backend developer
mode: subagent
model_pools: [default]
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
model_pools: [default]
permission:
  Write: ask
  Edit: ask
prompt: |
  你是一个专注于后端开发的 Agent。
```

常用字段：

- `name`：agent 名称。省略时使用不带扩展名的文件名。
- `description`：简短描述，在可委派给该 agent 时展示给 main agent。
- `mode`：`main` 表示 MainAgent 角色，`subagent` 表示 SubAgent。为空或其他值时按 `main` 处理；`sub_agent` 和 `sub` 也可作为 SubAgent 别名。
- `model_pools`：该 agent 的可用池名列表（有序）。池定义位于 `config.yaml` 顶层 `model_pools`。`openai/gpt-5.5@high` 这类 inline variant 写在池定义中。
- `variant`：model ref 未写 `@variant` 时的默认 variant。
- `permission`：该 agent 的逐工具权限策略。
- `mcp`：作用域限定在该 agent 的 MCP 配置。
- `delegation`：如 `max_children`、`max_depth`、`child_join` 等委派限制。
- `prompt` / `system_prompt`：纯 YAML agent 文件中的 system prompt。

## 上下文压缩

主会话接近模型上下文上限时，Chord 可自动执行持久化压缩。常见配置：

```yaml
context:
  auto_compact: true
  compact_threshold: 0.8
  compact_model: openai/gpt-5.4-mini
```

自动压缩阈值按**可用输入侧**预算计算：若模型配置了 `limit.input`，Chord 先从它出发；未配置时，按 `limit.context - effective_max_output` 推导，其中有效输出来自 `max_output_tokens`（未配置时使用运行时默认值）并受模型 `limit.output` 上限约束。如果设置了 `context.compaction.reserved`，Chord 会先减去这部分预留，再应用 `compact_threshold`。

可通过配置预留 headroom，用于 tokenizer 漂移、tool schema 开销和压缩/恢复安全余量：

```yaml
context:
  auto_compact: true
  compact_threshold: 0.8
  compaction:
    reserved: 16000
```

例如当模型配置为 `input: 272000` 时，以上配置会让自动压缩按 `256000` 的可用输入预算触发。

当 provider 同时公布“总上下文窗口”和“单独的输入上限”时，已知限制的话建议三个字段都写明：

```yaml
providers:
  openai:
    models:
      gpt-5.5:
        limit:
          context: 400000
          input: 272000
          output: 128000
```

原因是降低 `output` 并不会提高 provider 的硬输入上限。若所选模型输入预算较小，或 provider 明确区分 input/output limit，建议保持自动压缩开启。

## Provider 连通性自检

```bash
# 检查所有 provider
chord test-providers

# 只检查一个 provider
chord test-providers --provider openai
```

适合做认证与基础连通性的冒烟测试。

## 配置字段速查表

下面是 `config.yaml` 的全部顶层 key（同时适用于全局 `~/.config/chord/config.yaml` 和项目级 `.chord/config.yaml`）。除特别注明外，所有 key 均可选。

| Key                     | 类型                  | 默认值                          | 适用层级                 | 简述                                                                                                                  |
| ----------------------- | --------------------- | ------------------------------- | ------------------------ | --------------------------------------------------------------------------------------------------------------------- |
| `providers`             | `map[name]Provider`   | —                               | global / project         | 各 provider 的配置（`type`、`api_url`、`preset`、`models`、`compress`）。见 [最小 provider 配置](#最小-provider-配置)。 |
| `model_pools`           | `map[name][]ref`      | —                               | global / project         | 可复用的命名模型池，元素为 `provider/model`（或 `model@variant`）。见 [模型池](#模型池)。           |
| `context`               | object                | 见下文                          | global / project         | `auto_compact`、`compact_threshold`、`compact_model`、`compaction.reserved`。见 [上下文压缩](#上下文压缩)。                                  |
| `skills`                | object                | 空                              | global / project         | `paths: [...]` —— 在默认目录外追加 skill 目录。                                                                     |
| `confirm_timeout`       | int（秒）             | `0`（不超时）                   | global / project         | TUI 确认浮层超时；`0` 表示永远等。                                                                                    |
| `diff`                  | object                | `{inline_max_columns: 200}`     | global / project         | TUI diff 渲染。`inline_max_columns` 限制单行 inline diff 宽度。                                                    |
| `desktop_notification`  | bool                  | `false`                         | global / project         | 终端非聚焦时启用 OSC 9 idle 通知（仅本地 TUI）。                                                                    |
| `prevent_sleep`         | bool                  | `false`                         | global / project         | agent 活动时阻止 macOS idle sleep。仅 macOS 生效，其他平台 no-op。                                              |
| `keymap`                | `map[action][]key`    | 见 [快捷键 — Action 名速查](./keybindings_CN.md#action-名速查) | global / project | 覆盖键位绑定。Action 名采用 lower snake_case。                                                                       |
| `commands`              | `map[/cmd]text`       | 空                              | global / project         | 自定义 slash 命令；`"/cmd"` → 作为用户消息发送的文本。见 [扩展与定制 — 自定义 slash 命令](./customization_CN.md#自定义-slash-命令)。 |
| `ime_switch_target`     | string                | 空                              | global / project         | 进 Normal 模式时传给 `im-select` / `im-select.exe` 的 IM 标识。                           |
| `log_level`             | string                | `info`                          | global / project         | `debug` / `info` / `warn` / `error`。`debug` 输出较多。                                                              |
| `paths`                 | object                | XDG 默认值                      | 仅 global                | `state_dir`、`cache_dir`、`sessions_dir`、`logs_dir`。会被 CLI flag 与 `CHORD_*` 环境变量覆盖。                       |
| `maintenance`           | object                | 关闭                            | 仅 global                | `size_check_on_startup`、`size_check_interval_hours`、`warn_state_bytes`、`warn_cache_bytes`。                       |
| `lsp`                   | `map[name]Server`     | 空                              | global / project         | 各 language server 的配置。见 [扩展与定制 — LSP](./customization_CN.md#lsp)。                                      |
| `mcp`                   | `map[name]MCP`        | 空                              | global / project / agent | 各 MCP 服务器的配置。见 [MCP](#mcp)。                                                                              |
| `hooks`                 | object                | 空                              | global / project / agent | 按触发点分组的 hooks。见 [Hooks](./hooks_CN.md)。                                                                    |
| `max_output_tokens`     | int                   | 模型默认                        | global / project         | 全局输出 token 上限。实际请求还会受各模型 `limit.output` 限制；reasoning 请求同样遵守该上限。                      |
| `proxy`                 | string                | 空（用环境变量或直连）          | global / project         | 全局代理 URL。可通过 `web_fetch.proxy` 单独覆盖。                                                                    |
| `web_fetch`             | object                | 空                              | global / project         | `user_agent`、`proxy`（nil 继承全局；空字符串 = 显式直连）。见 [WebFetch](#webfetch)。                                |
| `worktree`              | object                | 空                              | global / project         | `chord --worktree` 与 `chord worktree …` 子命令的默认值。                                                            |

### Provider 字段参考

| 字段          | 类型   | 说明                                                                                                                                                |
| ------------- | ------ | --------------------------------------------------------------------------------------------------------------------------------------------------- |
| `type`        | string | `messages` / `chat-completions` / `responses` / `generate-content`。省略时按 `api_url` 或 `preset` 自动推断。                                       |
| `api_url`     | string | 接口地址。Gemini 用 `/models` 基础路径，Chord 自动附加 `/{model}:streamGenerateContent?alt=sse`。                                                |
| `preset`      | string | 当前可选 `codex`（OpenAI Codex / ChatGPT OAuth）。                                                                                                  |
| `compress`    | bool   | gzip 能减小体积时启用请求体压缩。默认关闭。                                                                                                      |
| `models`      | map    | model id → [模型配置](#模型字段参考)。                                                                                                              |

### 模型字段参考

| 字段              | 类型   | 说明                                                                                                              |
| ----------------- | ------ | ----------------------------------------------------------------------------------------------------------------- |
| `limit.context`   | int    | 已知时表示总请求窗口上限；未配置 `limit.input` 时，Chord 会从中扣除有效请求输出后推导输入预算。                                       |
| `limit.input`     | int    | provider 单独公布输入上限时填写。Chord 用它判断何时在 prompt 过大前压缩或恢复重试。                |
| `limit.output`    | int    | 输出 token 上限；运行时还会受 `max_output_tokens` 限制。                                                          |
| `context.compaction.reserved` | int | 可选的输入预算预留值。在应用 `compact_threshold` 前先扣除，适合为 tokenizer 误差、tool 开销和恢复安全余量留空间。 |
| `reasoning`       | object | OpenAI reasoning 选项（`summary`、`effort`）。variants 通常覆盖 `reasoning.effort`。                              |
| `text.verbosity`  | string | OpenAI 文本详细程度提示，支持的模型生效。                                                                      |
| `thinking`        | object | Anthropic 扩展思考选项。`type: adaptive` 让 Chord 按 `effort` 推算预算。                                          |
| `variants`        | map    | 命名参数预设。引用方式：`provider/model@variant`。                                                                |
| `modalities.input`| array  | `text` / `image` / `pdf` 的子集。默认 `[text, image]`。                                                           |

Agent 用法见 [扩展与定制 — Agent](./customization_CN.md#agent)；agent 完整 schema 见 [Agent 配置](#agent-配置)。

## 相关文档

- [快速开始](./quickstart_CN.md)
- [CLI](./cli_CN.md)
- [快捷键](./keybindings_CN.md)
- [目录与路径](./paths_CN.md)
- [环境变量](./environment_CN.md)
- [权限与安全](./permissions-and-safety_CN.md)
- [扩展与定制](./customization_CN.md)
- [Hooks](./hooks_CN.md)
- [常见问题排查](./troubleshooting_CN.md)
