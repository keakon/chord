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

项目配置 `.chord/config.yaml` 会先按“无内置默认值注入”的方式加载，再覆盖到已加载的全局配置上。运行时命令把当前工作目录视为项目根，因此项目层配置读取的是启动 cwd 下的 `./.chord/config.yaml`，不会自动向父目录继续查找。因此：

- 项目里没写的字段会保持真正的未设置状态，不会意外遮蔽全局默认值；
- 项目配置写坏了会直接作为启动错误暴露，而不是被静默忽略；
- `paths.*`、`maintenance.*` 这类仅全局生效的字段，即使写进项目配置也会被忽略；
- 大多数标量值和对象值会按同一路径直接覆盖全局值；
- `model_pools` 按 pool 名称合并：项目里同名 pool 会覆盖全局定义；
- 追加型扩展点会保留全局条目并附加项目条目：当前包括 `skills.paths` 和 `hooks.*` 下各触发点的 hook 列表，它们是 append，不是 replace。

首次在交互式终端里运行 `chord` 且 `config.yaml` 缺失时，Chord 会启动一次性的初始化向导。它会写入最小可用的 `config.yaml`，必要时再写入 `auth.yaml`，如果已有匹配的 `auth.yaml` 凭据则尽量直接复用，并在结束时展示真实解析后的路径。stdin 被重定向本身不等于非交互；只要还能打开控制 TTY，向导仍会使用该 TTY。只有没有控制 TTY 时，它才会直接退出，不会等待输入。

## 流式工具早执行

模型仍在流式输出响应、工具参数刚完整时，Chord 会提前执行一小批安全的只读工具，而不必等服务商完成最终确认（`CompleteStream()` 返回）后才开始。这能显著缩短最终确认阶段的体感等待时间。

- 始终启用，无 `early_tool_execution` 开关。
- 允许早执行的工具：`Read`、`Grep`、`Glob`，支持回滚的文件编辑工具（`Write`、`ApplyPatch`、`Delete`），以及 `Shell` 的保守只读子集（仅限单命令，不含管道/重定向/`&&`/`;` 等组合）：
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

对 `type: responses`，Chord 使用与 Codex 一致的 Responses 请求形态：稳定 system prompt 会放在顶层 `instructions` 字段；对话消息仍保留为 typed `input` item，例如 `{"type":"message","role":"user",...}`。这样兼容网关不会在 `input` 里收到 system-role message。

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

Gemini 的 thinking 参数与其他 provider 一样统一放在 `thinking` 下（不使用 `gemini_thinking` 之类的专有键）：

- `thinking.budget` → `generationConfig.thinkingConfig.thinkingBudget`
  - Gemini：✅ 使用
  - Anthropic：⚠️ 仅在 `thinking.type: enabled` 时用于预算模式
  - OpenAI：❌ 忽略
- `thinking.include_thoughts` → `generationConfig.thinkingConfig.includeThoughts`
  - Gemini：✅ 使用
  - Anthropic / OpenAI：❌ 忽略
- `thinking.level` → `generationConfig.thinkingConfig.thinkingLevel`（`minimal|low|medium|high`，Gemini 3+；并非所有模型都支持 `minimal`）
  - Gemini 3+：✅ 使用
  - Gemini 2.x / Anthropic / OpenAI：❌ 忽略

示例：

```yaml
providers:
  gemini:
    api_url: https://generativelanguage.googleapis.com/v1beta/models
    models:
      gemini-2.5-flash:
        limit:
          context: 1048576
          output: 65536
        thinking:
          budget: -1
          include_thoughts: true
      gemini-3-pro:
        limit:
          context: 1048576
          output: 65536
        thinking:
          budget: -1
          level: high
```

省略 `type` 时，Chord 按以下规则自动推断：

- `preset: codex` → `responses`
- `api_url` 以 `/responses` 结尾 → `responses`
- `api_url` 以 `/chat/completions` 结尾 → `chat-completions`
- `api_url` 以 `/messages` 结尾 → `messages`
- `api_url` 以 `/models` 结尾 → `generate-content`

不匹配以上规则时，需显式设置 `type`。

## Thinking 内容双语附加翻译

如果你的模型会输出英文 thinking / reasoning，而你希望在界面中附加中文译文，可启用 `thinking_translation`：

```yaml
model_pools:
  translation:
    - openai/gpt-5.4-mini

thinking_translation:
  target_language: zh-Hans
  model_pool: translation
  max_chars: 1000
```

要点：

- 该能力只翻译 **thinking / reasoning**，不翻译 assistant 正文。
- 翻译走已配置的大模型 provider，`thinking_translation.model_pool` 必须指向一个顶层 `model_pools` 条目。
- `target_language` 和 `model_pool` 都是必填；缺失任一项时该能力不会启用。
- 建议为翻译单独配置低成本模型池。该池可包含多个 `provider/model[@variant]` ref；翻译会按模型池顺序执行**单轮 fallback**：若当前候选失败（含网络/5xx/超时等）会切到下一个候选继续尝试；此外，当返回为空、明显截断或不是目标语言的翻译结果时也会继续切换下一候选。
- `max_chars` 限制送去翻译的 thinking 预览长度，默认 `1000`；如需更低延迟/成本可设为 `500`，如希望译文更完整可调大。只翻译开头 `max_chars` 个字符，超出部分会被直接丢弃、不会出现在翻译卡片中。
- thinking 翻译层不再设置整体翻译超时，也不使用 circuit breaker。某个 thinking block 临时失败只会跳过该 block，不会阻塞后续 thinking 翻译，也不会影响主回答。
- provider 请求、header、流式空闲等底层传输超时仍然生效。默认辅助 client 使用一分钟级别的超时，因此卡住的模型 / key 可以失败切换，同时仍允许模型池有机会完整运行。
- 译文会附加到对应 thinking 卡片下方，使用中性的 `Translated · <target_language>` 分隔标题，并保留 Markdown / 代码高亮；不会写回模型上下文。
- 如果翻译模型误把内部 `<TRANSLATION>` 包装标记原样返回，Chord 会在持久化、恢复和渲染前移除该标记，避免它被 Markdown 当作 HTML block 解析而破坏后续格式。
- 译文以 UI 侧映射形式持久化到会话目录的 `thinking_translations.json`，按 `(message, block)` 定位、绑定内容 hash 校验；恢复同一 session 时会直接复用。同一个 thinking block 最多翻译一次：之后修改 `thinking_translation.target_language` 也不会重新翻译已存在的块。译文不会写回模型上下文。

更完整的字段说明见下文配置参考。

## auth.yaml

`auth.yaml` 的 key 名需与 `config.yaml` 中的 provider 名称对应：

首次向导可以帮你创建这个文件。它既支持字面 API key，也支持 `$ENV_VAR` 占位符。

```yaml
anthropic:
  - "$ANTHROPIC_API_KEY"

openai:
  - "$OPENAI_API_KEY"
```

可配置多个 key 作为轮换或备用。

对于 `preset: codex` 的 OAuth provider，Chord 会把高频变化的运行时状态（额度快照、重置时间、最近 warm-up 时间、共享 OAuth 状态缓存）写入 `auth.state.yaml`，而不是继续频繁改写 `auth.yaml`。

这样拆分是有意为之：

- `auth.yaml` 仍是用户可编辑的凭据与稳定 OAuth 字段来源，例如 `refresh`、`access`、`expires`、`account_id`、`email`；Chord 重写文件时会省略空 OAuth 字段，OAuth `status` 不属于 `auth.yaml`；
- `auth.state.yaml` 是机器维护的共享运行时状态。普通条目在每个 provider 下直接以 `account_id` 为 key；quota / reset 更新，以及 `expired`、`deactivated`、`invalidated` 这类账号状态，不会在用户可能同时编辑 `auth.yaml` 时频繁改写该文件。尚不知道账号的 refresh-only 凭据可临时使用 `refresh_sha256:<digest>` state 条目，直到首次成功 refresh 后回填 `account_id`。`chord auth state clean` 会移除没有对应 `auth.yaml` OAuth 凭据的孤儿状态，以及无法识别的旧 state key 格式。

对带 `access` 的 OAuth 凭据来说，access token 必须能解析出账号 ID。如果 `auth.yaml` 已有 `account_id`，token 中的账号 ID 必须一致；否则该 access token 会被视为账号不匹配而拒绝。Chord 也支持只包含 `refresh`、没有 `access` 的 OAuth 条目，并会在首次使用时刷新；成功刷新后会解析 `account_id` 并把运行时状态切换到账号 key。如果在账号未知前 refresh 发生不可恢复失败，Chord 会在 `refresh_sha256:<digest>` 下记录无效状态，之后由用户执行 `chord auth state clean` 清理该不可用凭据。既没有 `access` 也没有 `refresh` 的 OAuth 条目不可用。

`expires` 是 access token 的 Unix 毫秒过期时间。如果 `access` 是带 `exp` claim 的 JWT，Chord 会优先使用该值作为更准确的过期元数据，并可将得到的过期时间缓存到 `auth.state.yaml`，但不会在 state 文件里保存 access token。缺失或本地已过期的 `expires` 不会单独把 OAuth slot 标记为 `expired` 或不健康。Chord 仍会先尝试已有的 access token，只有在认证失败后，才会在可恢复时刷新凭据，或者在无法恢复时标记为 expired。

典型的 `auth.state.yaml` 条目形态如下：

```yaml
openai:
  acc-1:
    email: user@example.com
    expires: 1774009702606
    status: expired
    updated_at: 1774009702606
    last_warmup_at: 1774009702606
    codex_primary_used_pct: 12.5
    codex_primary_window_minutes: 60
    codex_primary_reset_at: 1774013302000
    codex_secondary_used_pct: 40
    codex_secondary_window_minutes: 10080
    codex_secondary_reset_at: 1774600000000
```

`status` 字段只在 `auth.state.yaml` 中权威生效。当 access token 已不可用且凭据无法刷新时，Chord 会写入 `expired`（包括 refresh token 缺失、无效、过期或已被复用），服务端报告账号停用 / 封禁时写入 `deactivated`，账号需要重新认证时写入 `invalidated`。任意非空状态都会让该 OAuth slot 不再被选择，直到清理或替换凭据。

这些 Codex 缓存字段是跨重启保留的调度与展示提示，不是硬封禁：

- 会帮助启动后 / 首次选号时优先选择更可能仍有额度的账号；
- 会让切 key 时先显示上次缓存的额度快照，再等待新 warm-up 覆盖；
- **不会**仅凭字段本身就让账号绝对不可选；
- 真正的硬封禁仍来自已确认的请求失败和运行时 cooldown 状态。

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

## Provider key 选择

Chord 支持多个 API key / OAuth 账号时的两层选择策略：`key_rotation` 决定何时重新选 key，`key_order` 决定在候选 key 中如何选择。

- `key_rotation: on_failure`（默认）：尽量固定使用当前 key，只有失败、冷却或不可用时才切换。
- `key_rotation: per_request`：每次请求前都重新选择 key，适合多个独立 key 做负载均衡。
- `key_order: sequential`（默认的非 Codex 行为）：按可用 key 的稳定顺序选择，通常接近“最久未使用优先”。
- `key_order: random`：在可用 key 中随机挑选。
- `key_order: smart`：仅 Codex provider 支持。会优先健康、额度更充足、reset 更近的 OAuth 账号。

`key_rotation` 只轮换 credential / API key，不会轮换模型；模型选择仍由 model pool 的 sticky cursor 和 fallback 逻辑控制。

在 loop 模式下，Chord 仍尊重用户显式配置的 `key_rotation` / `key_order`。如果希望 Codex 长任务保持 transport / cache 连续性，建议保留默认 `key_rotation: on_failure`；如果希望多账号分摊额度，则可显式启用 `per_request`。

## OAuth 登录

当前仅配置了 `preset: codex` 的 provider 支持 OAuth。

对 Codex provider，建议只写 `preset: codex` 和模型配置，不要手动覆盖 `api_url`、`token_url`、`client_id`、`type`、`store`、`responses_websocket` 或 `supported_service_tiers` 等由 preset 管理的字段。Codex preset 会自动选择官方 OAuth transport、Responses endpoint、WebSocket / cache 相关默认值和 service-tier 能力。需要显式 tier 矩阵时使用 `supported_service_tiers`。

Codex OAuth 账号的选择由 [Provider key 选择](#provider-key-选择) 中的 `key_rotation` / `key_order` 控制。Codex 默认使用 `key_order: smart`，会结合额度快照、soft cooldown 和 reset 时间选择更合适的账号。

`smart` 会在可选 Codex OAuth 账号之间优先：

- 缓存快照中没有任何受跟踪窗口达到 `100%` 使用率的账号；`100%` 窗口只会被放到最后尝试，不会仅凭快照硬封禁；
- 短窗口（例如 5h primary window）仍有剩余额度的账号，并优先选择 primary reset 更近的账号，以免快过期额度被浪费；
- 然后考虑长窗口（例如 1w secondary window）仍有剩余额度的账号，同样优先选择 secondary reset 更近的账号；
- 可比较窗口的 reset 时间相同时，再选择剩余额度更高的账号；
- 如果没有更优信息，仍会回退尝试未知或缓存较旧的账号。

当 Codex client 变为活跃状态后，Chord 还可能在后台探测其他 OAuth slot，以刷新缓存的 headroom 快照。这个 warm-up 是 best-effort、低并发的，会在活跃 client 被替换时取消，并且只刷新缓存额度状态；usage probe 的认证失败不会把 OAuth 凭据标记为不可用。

warm-up 本身也会参考共享状态优先级：

- 从未在共享状态里 warm-up 过的账号优先；
- 缓存较旧的账号优先于最近刚刷新的账号；
- warm-up 或轮询拿到更新后的快照后，会写回 `auth.state.yaml`，其他进程会在下次读取选 key 或额度状态时按需吸收这些更新。

```bash
# 自动选择已配置的 codex provider
chord auth

# 显式指定 provider
chord auth codex

# 无桌面环境 / SSH
chord auth codex --device-code
```

## 模型池

Chord 通过命名模型池选择当前使用的模型。每个池条目建议写成完整的 `provider/model[@variant]`，这样 provider endpoint、认证、协议以及 variant tuning 都是明确的。

模型池在 `config.yaml`（全局或项目级）中定义；agent 配置可以引用池名来限制可用池，但不允许在 agent 中内联定义池。

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

### 在 agent 中引用池名（可选）

Agent 可以不设置 `model_pools`。省略时，该 agent 可使用合并后的
`config.yaml` 顶层 `model_pools` 中的所有池，并按池名的字母顺序排序。只有在需要限制该 agent 可用池，或自定义 fallback 顺序时，才需要设置 `model_pools: [...]`。

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

未显式选择池时，Chord 会回退到该 agent 的第一个可用池：如果配置了 `model_pools: [...]`，使用列表中的第一个；否则使用按字母顺序排序后的第一个顶层池。

运行时通过 `/models` 切换当前视图对象的池（按项目持久化，重启后仍生效）：main 视图作用于当前主角色，SubAgent 视图作用于该 agent。切换池会更新后续 LLM 调用的整条 fallback 链；即使当前选中的 `provider/model` 同时存在于两个池中，也会按新池的顺序重新构建（已发起的 in-flight 请求仍使用其开始时快照到的 client）。也可通过 `/models --agent <name> <pool>` 直接设置指定 agent 的池。SubAgent 默认使用第一个可用池；想恢复默认时切回该池即可。

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
      # 推荐示例：当你希望 OpenAI Responses reasoning 模型返回可读的
      # reasoning 摘要时，可显式设置为 `auto`。这不是 Chord 的隐式默认值：
      # 留空 `summary` 表示不发送该字段，交给 provider/model 按自身行为处理。
      summary: auto
    # 可选：适用于 OpenAI GPT-5 / Responses API 模型。默认留空，使用
    # provider/model 自身默认值；需要更短的可见文本输出时设 low，明确需要
    # 更详细可见输出时再设 high。
    # text:
    #   verbosity: low
    variants:
      high:
        reasoning:
          effort: high
      xhigh:
        reasoning:
          effort: xhigh
    modalities:
      input: [text, image]
  # Chord 使用的 YAML 解析器不接受 anchor 名称里的点号：模型 key 可以有 .，
  # 但 anchor/alias 名请用 _。
  gpt-5.2: &gpt-5_2
    <<: *gpt-400k
    cost:
      input: 1.75
      output: 14
      cache_read: 0.175
      service_tier_multipliers:
        fast: 2
        slow: 0.5
      input_tiers:
        - above_input_tokens: 272000
          input: 3.5
          output: 28
          cache_read: 0.35

  gpt-5.5: &gpt-5_5
    <<: *gpt-400k
    cost:
      input: 5
      output: 30
      cache_read: 0.5
      service_tier_multipliers:
        fast: 2.5
        slow: 0.5
      input_tiers:
        - above_input_tokens: 272000
          input: 10
          output: 60
          cache_read: 1

  gpt-1m: &gpt-1m
    limit:
      context: 1050000
      input: 922000
      output: 128000
    reasoning:
      # 推荐示例：当你希望 OpenAI Responses reasoning 模型返回可读的
      # reasoning 摘要时，可显式设置为 `auto`。这不是 Chord 的隐式默认值：
      # 留空 `summary` 表示不发送该字段，交给 provider/model 按自身行为处理。
      summary: auto
    # 可选：适用于 OpenAI GPT-5 / Responses API 模型。默认留空，使用
    # provider/model 自身默认值；需要更短的可见文本输出时设 low，明确需要
    # 更详细可见输出时再设 high。
    # text:
    #   verbosity: low
    variants:
      high:
        reasoning:
          effort: high
      xhigh:
        reasoning:
          effort: xhigh
    modalities:
      input: [text, image]

  gpt-5.4: &gpt-5_4
    <<: *gpt-1m
    cost:
      input: 2.5
      output: 15
      cache_read: 0.25
      service_tier_multipliers:
        fast: 2
        slow: 0.5
      input_tiers:
        - above_input_tokens: 272000
          input: 5
          output: 30
          cache_read: 0.5

  claude-1m: &claude-1m
    limit:
      context: 1000000
      output: 65536
    cost:
      input: 5
      output: 25
      cache_read: 0.5
      cache_write: 6.25
      cache_write_1h: 10
      service_tier_multipliers:
        fast: 6
    thinking:
      type: adaptive
      effort: medium
      display: summarized
    variants:
      high:
        thinking:
          type: adaptive
          effort: high
          display: summarized
      xhigh:
        thinking:
          type: adaptive
          effort: xhigh
          display: summarized
    modalities:
      input: [text, image]

providers:
  codex:
    preset: codex
    # 可选：model 条目会继承这个 provider-level 默认值。
    # Codex preset 已默认 [fast, slow]；显式写出通常用于文档化或覆盖非 preset provider。
    supported_service_tiers: [fast, slow]
    models:
      gpt-5.2: *gpt-5_2
      gpt-5.5: *gpt-5_5

  openai:
    api_url: https://api.openai.com/v1/responses
    supported_service_tiers: [fast, slow]
    models:
      gpt-5.4: *gpt-5_4
      gpt-5.5: *gpt-5_5

  anthropic:
    type: messages
    api_url: https://api.anthropic.com/v1/messages
    supported_service_tiers: [fast]
    models:
      claude-opus-4.7: *claude-1m
```

用到的模型字段含义：

- `limit.context`：已知时表示总请求窗口大小。
- `limit.input`：只在 provider 还单独公布了输入上限时才需要配置。Chord 用它判断何时在 prompt 过大前压缩，以及 provider 因请求过大而拒绝后如何重试。若省略，Chord 会按 `limit.context - 有效请求输出` 推导输入预算；有效请求输出来自 `max_output_tokens`，并受 `limit.output` 上限约束。它本身不会直接压低请求输出上限；输出裁剪遵循 `limit.output`、`max_output_tokens` 和已知的总窗口余量（`limit.context`）。
- `limit.output`：模型最大输出 token。实际请求还会受 `max_output_tokens` 限制，因此运行时会取两者里更小的值。
- `reasoning`：OpenAI / OpenAI-compatible reasoning 选项。
  - `reasoning.effort` 用来控制 Chord 主动请求的 reasoning 深度/预算。Chord 当前透传 `low`、`medium`、`high`、`xhigh`；留空表示不发送该字段，使用 provider/model 自身默认值。日常编码一般可从 `medium` 起步；`high` / `xhigh` 更适合复杂规划、疑难调试或高难度综合任务，但通常也会增加延迟和 token 消耗。
  - `type: chat-completions` 会把 `reasoning.effort` 发送为顶层 `reasoning_effort`，并使用 `max_completion_tokens`。
  - `type: responses` 会把 `reasoning.effort` 和 `reasoning.summary` 放进 `reasoning` 对象。
  - `reasoning.summary` 用来控制 Chord 是否显式请求 Responses 输出中的可读 reasoning 摘要。Chord 当前支持 `auto`、`concise`、`detailed`；留空表示不发送该字段，也就是不由 Chord 显式请求 summary，具体默认行为由 provider/model 决定。
  - 需要 summary 时，推荐值是 `auto`。它会让 provider 选择当前模型支持的最佳摘要详细度，避免把模板绑定到某个固定 summarizer，也更贴近当前 OpenAI 的推荐用法。想减少 UI 噪音时用 `concise`；明确要更完整的调试/评估信息时用 `detailed`。
  - `reasoning.summary` 只对支持 Responses reasoning summary 的模型有意义。有些上游客户端/模型目录会用 `none` 表示“关闭 summary”；在 Chord 里，对应做法是直接留空 `reasoning.summary`。
- `text.verbosity`：可选的 OpenAI 文本详细程度提示，取决于 provider/model 是否支持。可复用模板里建议默认留空，除非你明确要覆盖 provider/model 默认值；需要更短的可见文本输出时用 `low`，明确需要详细可见输出时用 `high`。
- `thinking`：Anthropic 扩展思考选项。`type: adaptive` 表示 Chord 根据 `effort` 推算合适的思考预算；`display: summarized` 会请求 Claude 返回可展示的 summarized thinking block（仅在 `type: enabled` 或 `adaptive` 下有效，`disabled` 模式会被拒绝）；variant 可覆盖 `thinking.effort` 与 `thinking.display`。
- `variants`：命名模型参数预设，可通过 `openai/gpt-5.5@high` 或 `anthropic/claude-opus-4.7@xhigh` 引用。
- `cost`：估算价格，单位是 USD / 1M tokens。`input`、`output`、`cache_read`、`cache_write`、`cache_write_1h` 都是可选字段；配置后，Chord 会在 UI 和 `/usage` 输出中估算费用。`cache_write` 是默认 prompt-cache 写入价格，对 Anthropic 通常对应 5 分钟 TTL；当 provider 报告或配置为请求 1 小时 cache write 时，使用 `cache_write_1h`。缺少匹配的 cache-write 价格时，Chord 会按生效后的 `input` 价格估算 cache-write token。
  - `cost.service_tier_multipliers`：可选的 service tier 价格倍数，会在选中基础价格或匹配的 `input_tiers` 价格之后应用。用于表达 provider 的 service tier，例如 OpenAI priority（`fast`）或 flex（`slow`）。
  - `cost.input_tiers`：可选的长上下文价格覆盖。每项使用 `above_input_tokens` 作为严格阈值；当 billable input 大于该值时，Chord 会使用匹配阈值最高一项的 `input`、`output`、`cache_read` 和可选的 `cache_write`，然后再应用 service-tier 倍数。
- `modalities.input`：模型支持的输入类型，可选 `text`、`image`、`pdf`。省略时默认 `[text, image]`。
- `supported_service_tiers`：显式声明 provider 或 model 可接收的非 standard service tier，例如 OpenAI 可用 `[fast, slow]`，Anthropic 可用 `[fast]`。provider-level 值作为该 provider 下所有 model 的默认值；model-level 值会覆盖 provider 默认。两者都省略时使用 preset 默认值。定价需单独在 `cost.service_tier_multipliers`（service-tier 费率）和 `cost.input_tiers`（长上下文阈值）里配置。`standard` 始终可用，不需要写进 `supported_service_tiers`。手动切换只会选择当前 provider/model 支持的 tier：`Ctrl+R` 只在可用集合（`standard` 加上支持的非 standard tier）内循环；手动输入 `/tier fast` 或 `/tier slow` 但当前 provider/model 不支持时会显示错误提示。当切换 provider/model 后，之前请求的 `fast` 或 `slow` 不再受支持时，信息侧栏仍会显示用户请求的 `tier: fast` 或 `tier: slow`，并用灰色删除线表示该模式当前未实际生效。

只有 Chord 模型 schema 中定义的字段会被使用。`modalities.output` 当前不被运行时解释，示例中刻意省略。

## 项目级配置

项目需要特定默认值时，在项目根目录创建：

```text
.chord/config.yaml
```

常见用途：调整项目特有权限规则、配置该项目的 LSP / MCP / Hooks / Skills。

## Provider 请求压缩

Provider 级别的 `compress` 控制上游 HTTP 请求体的 gzip 压缩。它和上下文管理（compaction / reduction）是两回事——只影响请求传输编码，不会总结或移除对话历史。

```yaml
providers:
  openai:
    compress: true
```

启用后，Chord 仅在 gzip 能减小体积时才发送压缩请求。除非你的 provider 或网关明确受益于请求体压缩，否则无需配置。

Provider / 模型 HTTP 请求默认用 `User-Agent: chord/<version>` 标识客户端。仅当某个 provider 或网关要求特定值时，才配置 provider 级 `user_agent`。该配置只影响对应 provider 的普通模型 HTTP 请求：

```yaml
providers:
  gateway:
    user_agent: RequiredGatewayClient/1.0
```

该配置只影响对应 provider 的普通模型 HTTP 请求。WebFetch 使用独立的 `web_fetch.user_agent`；Codex OAuth、用量轮询和 WebSocket 请求继续使用 Chord 的协议专用 User-Agent。

## 输出 token 上限

`max_output_tokens` 设置全局输出 token 请求上限。实际请求上限仍受各模型 `limit.output` 和可用总上下文（已知时为 `limit.context`）限制，因此运行时会取适用限制中的最小值。

`limit.input` 是另一回事：只有当模型除了总上下文窗口外，还额外存在输入上限时才需要配置。降低 `max_output_tokens` 有助于控制成本、降低超长输出失败风险，但**不会**提升 provider 的输入上限，也不能替代 `limit.input`。

```yaml
max_output_tokens: 32000
```

## 流式重试上限

`stream_retry_rounds` 用来给公开 LLM 流式请求的“整轮重试”设置硬上限。
每一轮里仍会按正常顺序遍历当前模型池和 provider key；这个设置限制的是 `CompleteStream` 最多做多少轮完整重试。

这里的“一轮”指的是整个公开重试回合，而不是单次 provider/model 尝试。比如 `stream_retry_rounds: 2` 表示最多允许两次完整的路由遍历；一旦达到上限，即使是 all-keys-cooling、并发 429，或非官方兼容网关返回的可重试 HTTP 400 这类通常会等待后继续的错误，也会直接停止。

Provider HTTP 400 的处理是有意保守的：

- 官方 API 会把 400 视为终态 invalid-request 错误；
- 非官方兼容网关有时会把并发限制、上游容量不足等临时状态映射成 400。这类非请求形态的 400 会冷却当前 key、轮换到下一个 key，并在所有 key 都冷却时等待后继续；
- 请求参数/模型不兼容类 400 仍会停止，避免无限重试，例如 `invalid_request_error`、`missing required parameter`、`Store must be set to false` 或 `Stream must be set to true`。

- `0` 保持默认行为：一直重试，直到成功、被取消，或遇到终态失败；
- 正整数表示最多重试这么多轮，即使是 cooling / 并发 429 这类通常会继续等待的错误，也会在达到上限后停止；
- 这个选项更适合自动化或 headless 场景：用可预测时延换取更明确的退出边界。

```yaml
stream_retry_rounds: 3
```

## 本地 TUI 选项

以下选项影响本地 TUI，可写在全局配置中，也可由项目级 `.chord/config.yaml` 覆盖。

```yaml
desktop_notification: true
ime_switch_target: com.apple.keylayout.ABC
prevent_sleep: true
```

- `desktop_notification`: 启用本地 TUI 的终端通知，主要用于终端失焦场景。Chord 会按终端自动选择通知转义序列（OSC 9 或 OSC 777），并在权限确认、等待回答、agent 回到 idle 时发送通知；不支持的终端通常会忽略该序列。
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
- Agent 运行中也可以执行 `/mcp enable|disable`。当前正在进行的请求继续使用它启动时的工具表面；新的 MCP 工具和 MCP system-prompt block 会在下一次 LLM 请求（包括自动重试 / 恢复请求）时应用。
- 在 Codex loop 模式下，如果任一受跟踪的 Codex 额度窗口剩余不到 10%，Chord 会保留现有的 LLM 可见上下文表面，不重写工具描述或 system prompt。运行时权限与 MCP 工具执行状态仍会变化，但模型继续看到之前的工具列表 / prompt，以便低额度 loop 能在同一个 Codex 会话上继续推进，避免因为上下文形态变化而无法续用。
  这是有意的取舍。在额度恢复或构建新的会话 / 上下文表面之前，模型看到的状态可能会短暂不同于运行时状态：新启用的 MCP 工具可能不会被模型发现；已禁用的 MCP 工具可能仍看起来可调用，但执行时失败；权限从 `deny` / `ask` 改为 `allow` 时，prompt / 工具描述中可能仍未体现；权限从 `allow` 改为 `deny` / `ask` 时，模型可能以为可以调用，但实际调用时会被拦截。Chord 接受这种短期不一致，以避免 Codex loop 在额度窗口末尾因上下文形态变化而耗尽或无法继续复用当前会话。

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
permission:
  Write: ask
  ApplyPatch: ask
---

你是一个专注于后端开发的 Agent。
```

等价的 YAML agent 示例：

```yaml
name: backend-coder
description: Backend developer
mode: subagent
permission:
  Write: ask
  ApplyPatch: ask
prompt: |
  你是一个专注于后端开发的 Agent。
```

常用字段：

- `name`：agent 名称。省略时使用不带扩展名的文件名。
- `description`：简短描述，在可委派给该 agent 时展示给 main agent。
- `mode`：`main` 表示 MainAgent 角色，`subagent` 表示 SubAgent。为空或其他值时按 `main` 处理；`sub_agent` 和 `sub` 也可作为 SubAgent 别名。
- `model_pools`：可选的有序池名列表，用于限制该 agent 可使用的池。池定义位于 `config.yaml` 顶层 `model_pools`；省略时，该 agent 可使用所有顶层池并按池名排序。`openai/gpt-5.5@high` 这类 inline variant 写在池定义中。
- `variant`：model ref 未写 `@variant` 时的默认 variant。
- `permission`：该 agent 的逐工具权限策略。权限直接保存在 agent 配置文件中；确认弹窗里选择“记住规则”时，`project` 会更新当前项目的 `.chord/agents/<role>.yaml`，`global` 会更新用户配置目录的 `agents/<role>.yaml`（默认 `~/.config/chord/agents/<role>.yaml`），不会写入单独的 permissions 文件夹。部分编排工具有特殊语义（`Delegate` 也会联动控制委派工作相关能力，如 `Cancel`；`Handoff` 和 `Done` 的 `allow` / `ask` 都表示工作流可用，并由 Chord 自己的确认 gate 控制关键节点）。依赖精细控制工具规则前，请先阅读[权限与安全](./permissions-and-safety_CN.md#特殊权限语义)。
- `mcp`：作用域限定在该 agent 的 MCP 配置。
- `delegation`：如 `max_children`、`max_depth`、`child_join` 等委派限制。
- `prompt` / `system_prompt`：纯 YAML agent 文件中的 system prompt。

示例：

```yaml
name: builder
mode: main
model_pools: [default]
permission:
  "*": deny
  Read: allow
  Grep: allow
  Glob: allow
  WebFetch:
    "http://localhost:8000/*": ask
  Shell: allow
  ApplyPatch: ask
  Write: ask
```

## 上下文压缩与上下文剪裁

Chord 提供两层互补的上下文管理机制：**上下文压缩（Compaction）**调用 LLM 生成摘要并持久化改写会话历史，**上下文剪裁（Reduction）**在每次请求前用启发式规则裁剪 prompt。两者分别作用于持久化历史和单次请求，各司其职。

### 对比速览

| 特性 | 上下文压缩（Compaction） | 上下文剪裁（Reduction） |
|------|-------------------------|------------------------|
| 做了什么 | 调用 LLM 生成结构化摘要，归档旧历史，用摘要替代原文 | 按规则裁剪本次请求中过时的工具输出 |
| 是否落盘 | ✅ 改写 session 文件 | ❌ session 文件不变 |
| 是否调用模型 | ✅（可配置专用模型池） | ❌（纯启发式规则） |
| 触发时机 | 上下文超过阈值的百分比 / 手动 `/compact` / 异常恢复 | 每次 LLM 请求前自动执行 |
| 典型耗时 | 数秒到数十秒（需等待 LLM 回复） | 毫秒级（内存内规则匹配） |
| 用户感知 | TUI 显示"Compacting context..."进度 | 无感知（静默） |
| loop 模式 | 启用；自动和手动压缩仍可运行，让长会话在上下文额度用尽后继续推进 | 新增消息禁用；如果在请求进行中启用 `/loop on`，Chord 会复用该请求已剪裁过的前缀以保持 cache 稳定。切换 loop 模式不会重写稳定 system prompt。对于剩余额度不到 10% 的 Codex loop 会话，Chord 还会保持 LLM 可见的工具描述和 system prompt 不变；权限 / MCP 执行状态仍可变化。 |

**两者的关系**：Reduction 是轻量级的第一道防线——每次请求前自动裁剪过时的工具输出，减缓上下文膨胀速度。当 Reduction 仍不够、上下文持续增长到 Compaction 阈值时，Compaction 启动做深度压缩。大多数用户只需关注 Compaction 配置；Reduction 的默认值已经适配常见场景，通常无需调整。

### 上下文压缩（Compaction）

当主会话上下文使用量接近模型上限时，Chord 会自动触发上下文压缩。压缩过程调用 LLM 分析当前对话，生成结构化摘要（目标、进度、关键决策、文件证据等），归档旧消息，用摘要替换对话历史。压缩结果持久保存到磁盘，会话文件体积显著缩小。

**最小配置**（启用自动压缩）：

```yaml
context:
  compaction:
    threshold: 0.8
    model_pool: compact
```

**配置字段说明**：

| 字段 | 类型 | 默认 | 说明 |
|------|------|------|------|
| `threshold` | 浮点数 | `0.8` | 触发自动压缩的上下文使用率阈值。取值 `0` ~ `1`，例如 `0.8` 表示用量达到可用输入预算的 80% 时触发；设为 `0` 可关闭自动压缩。 |
| `model_pool` | 字符串 | 克隆当前 agent 模型池 | 执行压缩的专用模型池名。建议使用低成本/快速模型以节省开销。 |
| `reserved` | 整数 | `0` | 为 tokenizer 误差、工具 schema 开销、压缩恢复安全等预留的 token 余量。计算触发阈值时先从输入预算中扣除。 |
| `preset` | 字符串 | 自动检测 | 强制指定压缩实现方式，一般无需设置。 |
| `profile` | 字符串 | `auto` | 压缩策略，一般无需设置。 |

**触发阈值如何计算**：以**可用输入预算**为基准。若模型配置了 `limit.input`，以此为准；否则按 `limit.context - 有效请求输出`（其中有效输出取 `max_output_tokens` 与模型 `limit.output` 的较小值）推导。若设置了 `reserved`，再从预算中扣除。TUI 信息面板和底部栏的 `Context` 百分比使用同一输入预算基准，与自动压缩阈值保持对齐。对于会单独报告 prompt cache 写入的 provider，Chord 会把当前 prompt 侧用量按 `input_tokens + cache_write_tokens` 计算，因此新写入缓存的 prompt 片段也会计入显示的上下文负担。

**预留 headroom 示例**：

```yaml
context:
  compaction:
    threshold: 0.8
    reserved: 16000
```

以模型 `input: 272000` 为例，扣除 `reserved` 后可用预算为 `256000`，当上下文达到 `256000 × 0.8 = 204800` tokens 时触发自动压缩。设置合理的 `reserved` 可避免由于 tokenizer 计算误差或工具描述开销导致压缩触发偏晚。

除自动触发外，你也可通过 TUI 的 `/compact` 命令随时手动压缩。手动压缩与自动压缩使用同一套后台 worker：即使 agent 正在执行任务也可以启动，进度会显示在后台压缩状态槽位，并在下一个安全的 continuation/idle barrier 应用，而不是立刻打断当前 turn。也可使用 `/compact --no` 临时关闭当前会话的后续自动压缩。

当 provider 同时公布"总上下文窗口"和"单独的输入上限"时，已知限制则建议三个字段都写明：

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

降低 `output` 并不会提高 provider 的硬输入上限。若所选模型输入预算较小，或 provider 区分 input/output limit，建议保持自动压缩开启。

### 上下文剪裁（Reduction）

每次向 LLM 发送请求前，Chord 会用一套确定性规则检查对话中的工具输出，对过时的大段内容做裁剪。**这只影响本次请求的 prompt，不会改写磁盘上的会话文件。**

在 loop 模式下，新增消息不会再应用剪裁。如果你在某个 LLM 请求仍在进行时启用 `/loop on`，Chord 会冻结并复用该请求已经准备好的前缀，避免旧历史从“已剪裁形态”翻回完整原始工具输出，从而保持 prompt cache 前缀稳定；loop 期间产生的新消息会保持未剪裁，直到退出 loop 后再恢复普通剪裁策略。切换 loop 模式本身不会新增、删除或重写稳定的 system prompt 文本；否则即使任务上下文没有变化，也会导致 prompt cache 失效。

当当前主 Agent provider 使用 Codex rate-limit surface，且 5h 或 7d 额度窗口剩余不足 10% 时，Chord 会在连续自动 continuation 中临时冻结完整的 LLM-facing request surface。冻结范围包括请求级剪裁结果、已安装的系统提示词和可见工具定义。这样做是有意的：接近额度耗尽时，Codex 只有在上下文表面不变的情况下，才可能沿着 `stop_reason=tool_call` 链继续执行直到 `end_turn`；如果此时上下文形态变化，可能导致 Codex 在额度用尽后无法继续复用当前会话。冻结会在交互边界解除——例如 Agent 回到 idle，或用户发送真实的新消息——因此 MCP / YOLO 等显式用户切换可以在下一次请求重新构建 surface。如果 key 或运行模型发生变化，Chord 也会允许下一次请求重建 surface，因为之前冻结的 surface 已不再匹配当前 Codex 身份。

> **大多数用户不需要配置这一节。** 内置默认值偏保守，已适配常见场景。基于本地真实会话的统计，剪裁能带来可观节省，且没有系统性破坏 prompt cache 复用；如果你希望在节省上下文和缓存稳定性之间更均衡，可使用下面的推荐配置。

**推荐的默认配置**（保留中等大小的近期读取/日志，同时继续剪掉大型旧输出）：

```yaml
context:
  reduction:
    confirm_age_turns: 2
    error_age_turns: 3
    shell_success_age_turns: 2
    shell_success_bytes: 8000
    read_like_age_turns: 1
    read_like_output_bytes: 4000
    stale_age_turns: 4
    stale_output_bytes: 1500
    min_tool_results_prune: 8
```

当你重视 prompt cache 稳定性、且会在多个轮次中反复围绕同一批活跃文件工作时，默认配置是推荐选择。如果主要问题是工具密集型会话很快顶到上下文上限，可以下调字节阈值，例如 `shell_success_bytes: 4000`、`read_like_output_bytes: 2500`。

**剪裁规则**：按工具输出的类型和时效分五类处理，类别不同，裁剪激进程度也不同。

| 类别 | 典型场景 | 年龄阈值 | 大小阈值 | 设计意图 |
|------|----------|----------|----------|----------|
| 确认/权限 | 工具权限确认、用户授权结果 | `confirm_age_turns`（默认 2 轮后） | — | 权限决策很快过时，可较早裁剪 |
| 错误结果 | 工具执行失败的错误信息 | `error_age_turns`（默认 3 轮后） | — | 失败原因可能仍有参考价值，保留稍久 |
| Shell 成功 | `git`、`go test`、`npm run` 等命令输出 | `shell_success_age_turns`（默认 2 轮后） | `shell_success_bytes`（默认 8000 字节以上才剪） | 构建/测试输出有时是关键上下文，但通常可重新执行 |
| 读取/搜索 | `Read`、`Grep`、`Glob` 等工具输出 | `read_like_age_turns`（默认 1 轮后） | `read_like_output_bytes`（默认 4000 字节以上才剪） | 文件内容可随时重新读取，裁剪最激进 |
| 其他旧结果 | 不属于以上类别的旧工具输出 | `stale_age_turns`（默认 4 轮后） | `stale_output_bytes`（默认 1500 字节以上才剪） | 兜底规则，最保守，避免误删不易重建的内容 |

年龄参数说明：

- `*_age_turns` 统计的是工具结果之后又经过了多少个**用户轮次**（你发送消息的次数）。例如 `read_like_age_turns: 1` 表示大型读取结果从你下一次发言起就可能被裁剪。
- `*_bytes` 是该类别参与裁剪的**最小输出字节数**。小于此值的输出保留完整内容——短输出不需要裁剪。
- `min_tool_results_prune`（默认 8）是**安全门槛**：会话中至少有这么多条工具结果时，Chord 才启动剪裁，避免小会话被过早处理。

**调参思路**：

| 你遇到的情况 | 建议 |
|--------------|------|
| Prompt cache 复用良好，但中等大小的读取/日志仍然太容易改变请求前缀 | 进一步调高 `read_like_output_bytes` 和 `shell_success_bytes` |
| 每次对话很短但工具输出特别多 | 降低 `min_tool_results_prune`（如 `4`） |
| Prompt 中权限确认信息过多 | 降低 `confirm_age_turns`（如 `1`） |
| 构建/测试日志经常需要回头看 | 进一步调高 `shell_success_bytes`（如 `16000`） |
| 文件内容经常需要回头查阅 | 调高 `read_like_age_turns`（如 `3`）和 `read_like_output_bytes`（如 `8000`） |
| 工具输出都很重要不想丢 | 整体调高各 `*_age_turns` 和 `*_bytes` |

完整配置示例（同时展示所有默认值）：

```yaml
context:
  reduction:
    confirm_age_turns: 2
    error_age_turns: 3
    shell_success_age_turns: 2
    shell_success_bytes: 8000
    read_like_age_turns: 1
    read_like_output_bytes: 4000
    stale_age_turns: 4
    stale_output_bytes: 1500
    min_tool_results_prune: 8
```

未设置或非正数的字段使用默认值。项目级 `.chord/config.yaml` 可按字段覆盖全局配置。

## Provider/model 诊断

```bash
# 用代表模型冒烟测试所有 provider
chord doctor models

# 测试单个 provider 的代表模型
chord doctor models --provider openai

# 测试精确模型或 variant
chord doctor models --model openai/gpt-5.5@high
chord doctor models --provider openai --model gpt-5.5@high

# 独立审计模型池中的每个条目
chord doctor models --pool thinking
```

该命令适合做认证、endpoint、transport、模型存在性以及 variant tuning 的冒烟测试。它读取的配置视图与正常运行时一致：会先加载全局配置，再叠加项目级 provider / proxy / model 覆盖。Pool 诊断会逐项独立请求，不走正常 fallback 链。

## 配置字段速查表

下面是 `config.yaml` 的全部顶层 key（同时适用于全局 `~/.config/chord/config.yaml` 和项目级 `.chord/config.yaml`）。除特别注明外，所有 key 均可选。

| Key                     | 类型                  | 默认值                          | 适用层级                 | 简述                                                                                                                  |
| ----------------------- | --------------------- | ------------------------------- | ------------------------ | --------------------------------------------------------------------------------------------------------------------- |
| `providers`             | `map[name]Provider`   | —                               | global / project         | 各 provider 的配置（`type`、`api_url`、`preset`、`key_rotation`、`key_order`、`models`、`compress`）。见 [最小 provider 配置](#最小-provider-配置)。 |
| `model_pools`           | `map[name][]ref`      | —                               | global / project         | 可复用的命名模型池，元素为完整 `provider/model[@variant]` ref。见 [模型池](#模型池)。           |
| `thinking_translation`  | object                | 关闭（`max_chars: 1000`）        | global / project         | 可选的 thinking / reasoning 卡片附加翻译预览。需要 `target_language` 和 `model_pool`；失败只跳过受影响的 thinking block。 |
| `context`               | object                | 见下文                          | global / project         | `compaction`（上下文压缩）和 `reduction`（上下文剪裁）两项配置。见 [上下文压缩](#上下文压缩compaction) 和 [上下文剪裁](#上下文剪裁reduction)。 |
| `diagnostics`           | object                | 启用（Python LSP + Ruff 回退）  | global / project         | `ApplyPatch` / `Write` 完成后追加的诊断信息。`diagnostics.python.semantic_backend` 是主 LSP 服务（默认 `pyright`）；`diagnostics.python.quick_backend` 是一次性回退命令（默认 `ruff check`）。`diagnostics.python.large_file.{line_threshold, byte_threshold, strategy}` 决定大文件何时走 quick backend；`run_semantic_when_quick_unavailable: true` 在 quick backend 不可用时仍强制跑语义诊断。`diagnostics.python.output.{max_near_diagnostics, max_outside_diagnostics, max_total_diagnostics, near_range_before_lines, near_range_after_lines}` 控制追加文本的长度和裁剪窗口。诊断按严重级别优先展示（错误/警告优先，仍有名额时再显示 info/hint）。设 `diagnostics.enabled: false` 可整体关闭。 |
| `skills`                | object                | 空                              | global / project         | `paths: [...]` —— 在默认目录外追加 skill 目录。                                                                     |
| `confirm_timeout`       | int（秒）             | `0`（不超时）                   | global / project         | TUI 确认浮层超时；`0` 表示永远等。                                                                                    |
| `diff`                  | object                | `{inline_max_columns: 200}`     | global / project         | TUI diff 渲染。`inline_max_columns` 限制单行 inline diff 宽度。                                                    |
| `desktop_notification`  | bool                  | `false`                         | global / project         | 终端非聚焦时启用本地 TUI 终端通知；Chord 会按终端自动选择 OSC 9 或 OSC 777（不支持的终端通常会忽略该序列）。                                            |
| `prevent_sleep`         | bool                  | `false`                         | global / project         | agent 活动时阻止 macOS idle sleep。仅 macOS 生效，其他平台 no-op。                                              |
| `keymap`                | `map[action][]key`    | 见 [快捷键 — Action 名速查](./keybindings_CN.md#action-名速查) | global / project | 覆盖键位绑定。Action 名采用 lower snake_case。                                                                       |
| `commands`              | `map[/cmd]text`       | 空                              | global / project         | 自定义 slash 命令；`"/cmd"` → 作为用户消息发送的文本。见 [扩展与定制 — 自定义 slash 命令](./customization_CN.md#自定义-slash-命令)。 |
| `ime_switch_target`     | string                | 空                              | global / project         | 进 Normal 模式时传给 `im-select` / `im-select.exe` 的 IM 标识。                           |
| `log_level`             | string                | `info`                          | global / project         | `debug` / `info` / `warn` / `error`。`debug` 输出较多。                                                              |
| `paths`                 | object                | XDG 默认值                      | 仅 global                | `state_dir`、`cache_dir`、`sessions_dir`、`logs_dir`。会被 CLI flag 与 `CHORD_*` 环境变量覆盖。                       |
| `maintenance`           | object                | 关闭                            | 仅 global                | `size_check_on_startup`、`warn_state_bytes`、`warn_cache_bytes`。                       |
| `lsp`                   | `map[name]Server`     | 空                              | global / project         | 各 language server 的配置。见 [扩展与定制 — LSP](./customization_CN.md#lsp)。                                      |
| `mcp`                   | `map[name]MCP`        | 空                              | global / project / agent | 各 MCP 服务器的配置。见 [MCP](#mcp)。                                                                              |
| `hooks`                 | object                | 空                              | global / project / agent | 按触发点分组的 hooks。见 [Hooks](./hooks_CN.md)。                                                                    |
| `max_output_tokens`     | int                   | 模型默认                        | global / project         | 全局输出 token 上限。实际请求还会受各模型 `limit.output` 限制；reasoning 请求同样遵守该上限。                      |
| `stream_retry_rounds`   | int                   | `0`（重试直到成功/取消）       | global / project         | 公开 LLM 流式请求的整轮重试硬上限。`0` 表示一直重试，直到成功、取消或终态失败。                                       |
| `proxy`                 | string                | 空（用环境变量或直连）          | global / project         | 全局代理 URL。可通过 `web_fetch.proxy` 单独覆盖。                                                                    |
| `web_fetch`             | object                | 空                              | global / project         | `user_agent`、`proxy`（nil 继承全局；空字符串 = 显式直连）。见 [WebFetch](#webfetch)。                                |
| `worktree`              | object                | 空                              | global / project         | `chord --worktree` 与 `chord worktree …` 子命令的默认值。                                                            |

### Provider 字段参考

Chord 会把当前 Chord session id 自动传给 OpenAI 系 provider，作为缓存 / 路由亲和元数据：OpenAI Responses 请求会包含 `prompt_cache_key`，OpenAI Chat Completions / Responses HTTP 请求会在有 session id 时包含 `X-Session-Id` 和 `session-id` header。这些字段不能手动配置，会随当前 Chord session 自动切换 / 恢复。Anthropic prompt caching 由 `cache_control` block 驱动；它的可选 `metadata.user_id` 仍是稳定的匿名用户 / provider 标识，而不是按 session 变化的 id。对于 Anthropic 模型，你可以用 `prompt_cache.ttl: 1h` 请求按小时缓存：

```yaml
providers:
  anthropic:
    models:
      claude-sonnet-4-5:
        prompt_cache:
          mode: auto
          ttl: 1h
```

Gemini 在 Chord 当前的 `generateContent` transport 中没有简单的逐请求 session-id cache key；它的缓存信号来自 provider 专用 cached-content API / usage 字段，而不是 Chord session id header。

| 字段          | 类型   | 说明                                                                                                                                                |
| ------------- | ------ | --------------------------------------------------------------------------------------------------------------------------------------------------- |
| `type`        | string | `messages` / `chat-completions` / `responses` / `generate-content`。省略时按 `api_url` 或 `preset` 自动推断。                                       |
| `api_url`     | string | 接口地址。Gemini 用 `/models` 基础路径，Chord 自动附加 `/{model}:streamGenerateContent?alt=sse`。                                                |
| `preset`      | string | 当前可选 `codex`（OpenAI Codex / ChatGPT OAuth）。                                                                                                  |
| `official_api`| bool   | 将该 endpoint 视为官方 provider API；HTTP 400 通常表示请求无效，不应按临时网关错误重试。`preset: codex` 默认按官方 API 处理；聚合或代理网关可省略或设为 `false`。 |
| `key_rotation`| string | `on_failure`（默认）/ `per_request`。控制何时重新选择 credential / API key。                                                                    |
| `key_order`   | string | `sequential`（非 Codex 默认）/ `random` / `smart`（仅 Codex）。控制在候选 key 中如何选择。                                               |
| `compress`    | bool   | gzip 能减小体积时启用请求体压缩。默认关闭。                                                                                                      |
| `models`      | map    | model id → [模型配置](#模型字段参考)。                                                                                                              |

### 模型字段参考

| 字段              | 类型   | 说明                                                                                                              |
| ----------------- | ------ | ----------------------------------------------------------------------------------------------------------------- |
| `limit.context`   | int    | 已知时表示总请求窗口上限；未配置 `limit.input` 时，Chord 会从中扣除有效请求输出后推导输入预算。                                       |
| `limit.input`     | int    | provider 单独公布输入上限时填写。Chord 用它判断何时在 prompt 过大前压缩或恢复重试。                |
| `limit.output`    | int    | 输出 token 上限；运行时还会受 `max_output_tokens` 限制。                                                          |
| `context.compaction.reserved` | int | 可选的输入预算预留值。在应用 `compaction.threshold` 前先扣除，适合为 tokenizer 误差、tool 开销和恢复安全余量留空间。 |
| `reasoning`       | object | OpenAI reasoning 选项。Chord 当前支持的子字段有 `reasoning.effort`（`low` / `medium` / `high` / `xhigh`；留空 = 不发送，使用 provider/model 默认）以及 Responses 场景下的 `reasoning.summary`（`auto` / `concise` / `detailed`；留空 = 不发送 / 不显式请求 summary）。需要可读摘要时推荐 `auto`。 |
| `text.verbosity`  | string | 可选的 OpenAI 文本详细程度提示，支持的模型生效；除非明确要覆盖为 `low` / `medium` / `high`，否则建议留空使用 provider/model 默认值。 |
| `thinking`        | object | Anthropic 扩展思考选项。`type: adaptive` 让 Chord 按 `effort` 推算预算；`display: summarized` 启用 summarized thinking block（仅 `type: enabled` 或 `adaptive` 有效）。 |
| `variants`        | map    | 命名参数预设。引用方式：`provider/model@variant`。                                                                |
| `modalities.input`| array  | `text` / `image` / `pdf` 的子集。默认 `[text, image]`。                                                           |
| `supported_service_tiers` | 列表 | provider-level 默认值或 model-level 覆盖值，用于声明可接收的非 standard tier，例如 `[fast, slow]` 或 `[fast]`。省略时使用 preset 默认值。 |

服务层和 prompt cache 是 provider-specific 的：OpenAI 支持 priority/flex 风格的 tiering 以及 `prompt_cache_key` / `prompt_cache_retention`；Anthropic 支持 `cache_control`，并提供 5 分钟和 1 小时 TTL；Gemini 则使用它自己的路由 / thinking / cached-content 机制。Chord 会把用户侧的 tier 映射到当前 provider 能支持的最接近行为，而不是强行统一成同一种 wire 格式。

OpenAI 的 reasoning item 还可能通过 `reasoning.encrypted_content` 返回，用于无状态续传。这个字段是加密后的续传载荷，不适合直接在 UI 里展示；如果有可读摘要，应优先展示摘要而不是 encrypted payload。

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
