# 模型配置速查

<!-- description: 可直接复制的 provider 与模型池配置：OpenAI、Anthropic、Codex OAuth 和 OpenAI 兼容网关。 -->

当你已经确定要用哪一类 provider / model，只想要一段可复制的起始配置时，用这一页。还没选定渠道时，先看[按工作选模型](./model-choice_CN.md)。字段语义和完整 schema 仍以[配置与认证](./configuration_CN.md)为准；完整的多文件工作站 / 团队布局示例见[配置示例](./examples/index_CN.md)。

## 本页怎么读

先选接入方式，再复制对应片段。第一次配置可以先保留默认的上下文设置，等模型连接正常后再调优。需要 API key 的 provider，先把凭据写进 `~/.config/chord/auth.yaml`；下面各节只列对应的条目片段。

| 想接入什么 | 配方 |
| --- | --- |
| OpenAI API / Responses 兼容接口 | [OpenAI GPT](#openai-gptresponses-兼容接口) |
| Codex OAuth | [Codex 登录配置](#codex-oauth-preset) |
| Anthropic API | [Claude](#anthropic-claude) |
| Google API | [Gemini](#google-gemini) |
| 其他模型 | [GLM](#glm--bigmodel-coding-plan) · [DeepSeek](#deepseek) · [Qwen](#qwen-保留历史思考) · [Kimi](#kimi) · [Grok](#grokxai) · [MiniMax](#minimaxopenai-兼容接口) · [MiMo](#小米-mimoopenai-兼容接口) · [Muse Spark](#meta-muse-spark) |
| Chat Completions 网关的思考设置 | [网关配置](#走-chat-completions-网关的-thinking) |

复制后按[验证步骤](#如何验证任意一份配置)检查配置和连接。需要长期运行或控制上下文成本时，再看文末的[按模型调压缩](#按模型调压缩)。

## 共用模型模板

下面各 provider 片段反复用到同一组窗口和输入模态。把它们收进本节一次，各片段
再用 merge key 组合；`model_templates` 只是 YAML anchor 命名空间，不会作为
模型定义生效。anchor 只在同一份文件内有效，所以先把本节放进 `config.yaml`
顶部，再粘贴需要的片段。

```yaml
model_templates:
  # 窗口：按模型公布档位收敛，跨 provider 复用
  window-1050k-128k: &window-1050k-128k   # GPT-5.x / GPT-6 官方 API：不写 input
    limit: {context: 1050000, output: 128000}
  window-codex-1050k-128k: &window-codex-1050k-128k   # 同档位，Codex 暴露独立输入上限
    limit: {context: 1050000, input: 922000, output: 128000}
  window-400k-128k: &window-400k-128k     # GPT-5.5 及仍在旧档位的账号/中转
    limit: {context: 400000, input: 272000, output: 128000}
  window-1m-128k: &window-1m-128k         # Claude 5、GLM-5.x
    limit: {context: 1000000, output: 128000}
  window-1m-64k: &window-1m-64k           # DeepSeek V4.1
    limit: {context: 1000000, output: 64000}
  window-1m-65k: &window-1m-65k           # Qwen
    limit: {context: 1000000, output: 65536}
  window-1049k-64k: &window-1049k-64k     # Gemini 3.x
    limit: {context: 1048576, output: 65536}
  window-1049k-131k: &window-1049k-131k   # Kimi K3、MiMo、Muse Spark
    limit: {context: 1048576, output: 131072}
  window-256k-32k: &window-256k-32k       # Kimi K2.x
    limit: {context: 262144, output: 32768}
  window-200k-64k: &window-200k-64k       # 网关后的 Claude / GLM chat
    limit: {context: 200000, output: 64000}
  # 输入模态：模型未声明 modalities 时只接受文本
  vision-pdf: &vision-pdf
    modalities: {input: [text, image, pdf]}
  vision: &vision
    modalities: {input: [text, image]}
  text-only: &text-only
    modalities: {input: [text]}
  # GPT-5.x / GPT-6 的 Responses reasoning（API 与 Codex 通用）
  gpt-reasoning-full: &gpt-reasoning-full
    reasoning: {effort: medium, summary: auto}
    variants:
      low: {reasoning: {effort: low}}
      medium: {reasoning: {effort: medium}}
      high: {reasoning: {effort: high}}
      xhigh: {reasoning: {effort: xhigh}}
      max: {reasoning: {effort: max}}
  gpt-reasoning-basic: &gpt-reasoning-basic
    reasoning: {summary: auto}
    variants:
      high: {reasoning: {effort: high}}
      xhigh: {reasoning: {effort: xhigh}}
```

各片段只需要写自己的 provider `type`、`api_url`、认证、`cost` 与 `compat`；
窗口和模态都来自上面的锚点。想单独调整某个模型时，在它的条目里按整块覆盖
对应字段即可。

本节之外还有几条各 provider 通用的约定：

- 未声明 `modalities` 的模型只接受文本，图像/PDF 附件会被丢弃。
- 省略 `limit.output` 时，Chord 按默认的 `64000` 输出预算推导输入预算
  （`limit.context` 减 64000）；Responses provider 默认不发
  `max_output_tokens`，需要显式执行上限时设
  `compat.responses.send_max_output_tokens: true`。
- 模板上写 `compaction`，引用它的模型条目全部继承；不写则继承全局阈值。
  触发依据是上一次 provider 返回的 usage，一次大工具结果就可能让下一次请求
  越线，所以阈值只是调优目标、不是包票。会话不长时保持全局默认即可，只有
  长期跑 agentic 任务才需要按模型调。
- `reasoning_continuity` 有两种模式：`openai_visible` 按 Chat Completions
  约定原样回放原生 `reasoning_content`；`anthropic_unsigned` 用于返回无签名
  thinking（而非 Claude 风格签名块）的 Messages 兼容接口，回放同 provider/model
  的无签名 thinking。两者都能把其他 wire family 的可移植可见 reasoning 转成
  目标形状；target 仍拒绝该形状时，严格兼容降级会丢弃 reasoning carrier，但
  保留工具轮次。要求完整 reasoning 历史或 preserved thinking 的后端另外设置
  `preserve_history: true`，完整 assistant 历史会原样回放。

## OpenAI GPT（Responses 兼容接口）

```yaml
openai:
  - "$OPENAI_API_KEY"
```

### GPT-5.4 / 5.5 / 5.6 / 6

这四个系列的窗口、reasoning、输入模态和压缩策略大多相同。GPT-5.6（Sol /
Terra / Luna）与 GPT-6（Astra / Sol / Luna）共用 `&window-1050k-128k`、
`&gpt-reasoning-full` 和 `&gpt-cost-first`，只是各自的 `cost` 不同；GPT-5.4 /
5.5 用较窄的 reasoning 档位和 400K 窗口。1.05M 档不公布独立输入上限，只声明
`context` 和 `output`，Chord 按 `context - output` 推出 922K 输入预算。

```yaml
model_templates:
  gpt-cost-first: &gpt-cost-first
    compaction:
      threshold: 0.25      # 约 231K 触发，低于 272K 计价线
      reminder: 0.2

providers:
  openai:
    type: responses
    api_url: https://api.openai.com/v1/responses
    models:
      gpt-5.4:
        <<: [*window-1050k-128k, *gpt-reasoning-basic, *vision-pdf]
        cost:
          input: 2.5
          output: 15
          cache_read: 0.25
      gpt-5.5:
        <<: [*window-400k-128k, *gpt-reasoning-basic, *vision-pdf]
        cost:
          input: 5
          output: 30
          cache_read: 0.5
      gpt-5.6-sol:
        <<: [*window-1050k-128k, *gpt-reasoning-full, *vision-pdf, *gpt-cost-first]
        cost:
          input: 5
          output: 30
          cache_read: 0.5
          cache_write: 6.25
          input_tiers:
            - above_input_tokens: 272000
              input: 10
              output: 45
              cache_read: 1
              cache_write: 12.5
      gpt-5.6-terra:
        <<: [*window-1050k-128k, *gpt-reasoning-full, *vision-pdf, *gpt-cost-first]
        cost:
          input: 2
          output: 12
          cache_read: 0.2
          cache_write: 2.5
          input_tiers:
            - above_input_tokens: 272000
              input: 4
              output: 18
              cache_read: 0.4
              cache_write: 5
      gpt-5.6-luna:
        <<: [*window-1050k-128k, *gpt-reasoning-full, *vision-pdf, *gpt-cost-first]
        cost:
          input: 0.2
          output: 1.2
          cache_read: 0.02
          cache_write: 0.25
          input_tiers:
            - above_input_tokens: 272000
              input: 0.4
              output: 1.8
              cache_read: 0.04
              cache_write: 0.5
      gpt-6-astra:
        <<: [*window-1050k-128k, *gpt-reasoning-full, *vision-pdf, *gpt-cost-first]
        cost:
          input: 10
          output: 50
          cache_read: 1
          cache_write: 12.5
          input_tiers:
            - above_input_tokens: 272000
              input: 20
              output: 75
              cache_read: 2
              cache_write: 25
      gpt-6-sol:
        <<: [*window-1050k-128k, *gpt-reasoning-full, *vision-pdf, *gpt-cost-first]
        cost:
          input: 2
          output: 10
          cache_read: 0.2
          cache_write: 2.5
          input_tiers:
            - above_input_tokens: 272000
              input: 4
              output: 15
              cache_read: 0.4
              cache_write: 5
      gpt-6-luna:
        <<: [*window-1050k-128k, *gpt-reasoning-full, *vision, *gpt-cost-first]
        cost:
          input: 0.1
          output: 0.5
          cache_read: 0.01
          cache_write: 0.125
          input_tiers:
            - above_input_tokens: 272000
              input: 0.2
              output: 0.75
              cache_read: 0.02
              cache_write: 0.25

model_pools:
  default:
    - openai/gpt-6-sol@medium
    - openai/gpt-5.6-sol@xhigh
```

### 模型差异

| 模型 | 窗口 | 输入模态 | 输入 / 输出价格（每 1M token） | 长上下文质量（MRCR v2 8-needle） |
| --- | --- | --- | ---: | --- |
| GPT-5.4 | 1.05M | 文本、图片、PDF | $2.5 / $15 | 未公布分段结果 |
| GPT-5.5 | 400K | 文本、图片、PDF | $5 / $30 | 未公布分段结果 |
| GPT-5.6 Sol | 1.05M | 文本、图片、PDF | $5 / $30 | 512K–1M 约 73.8% |
| GPT-5.6 Terra | 1.05M | 文本、图片、PDF | $2 / $12 | 512K–1M 约 72.5% |
| GPT-5.6 Luna | 1.05M | 文本、图片、PDF | $0.20 / $1.20 | 256K–1M 两段均为 41.3% |
| GPT-6 Astra | 1.05M | 文本、图片、PDF | $10 / $50 | 512K–1M 为 96.3% |
| GPT-6 Sol | 1.05M | 文本、图片、PDF | $2 / $10 | 未公布分段结果 |
| GPT-6 Luna | 1.05M | 文本、图片 | $0.10 / $0.50 | 未公布分段结果 |

价格只列基础输入 / 输出；配置里的 `cost` 已含缓存价格和长上下文费率。
GPT-5.4 / GPT-5.5 支持 `supported_service_tiers: [fast, slow]`，需要
service tier 时在 provider 或模型条目上声明，并在 `cost` 里配倍数。

- **窗口**：1.05M 档只声明 `context` 和 `output`，不写 `input`（这些模型未
  公布独立输入上限，922K 输入预算由 `context - output` 推出）。GPT-5.5 是
  400K 档，保留 `input: 272000`。账号或中转仍是旧档位时，相应模型按
  `400000 / 272000 / 128000` 配置。
- **reasoning**：GPT-5.6 与 GPT-6 的 effort 档位为
  `low / medium / high / xhigh / max`，默认 `medium`；GPT-5.6 与 GPT-6 Sol、
  Luna 还接受 `none`，GPT-6 Astra 不接受。GPT-5.4 / GPT-5.5 只有
  `high`、`xhigh` 两个 variant。Responses 启用 reasoning 时默认请求
  `summary: auto`，不需要摘要时可设为 `none`。GPT-5.6 的
  `reasoning.mode: pro` 当前未暴露。
- **长上下文计价**（官方 API）：prompt 输入**超过** 272K 时，整次请求按
  输入 / 缓存 2 倍、输出 1.5 倍重新计价，272K 本身不触发。中转与 Codex
  OAuth 自行定价，这条不一定适用。

#### 上下文压缩

示例里的 GPT-5.6 / GPT-6 统一用 `threshold: 0.25`，按 922K 预算约在 231K
触发，给 272K 计价线留出空间。压缩本身也会调用摘要模型并舍弃部分原始上下文。
GPT-5.4 / GPT-5.5 没有专门配方，不写 `compaction` 就用全局默认。

区间平均值只能粗略参考质量变化：GPT-5.6 Sol / Terra 在 256K–512K 为
91.5% / 89.6%，512K–1M 降到 73.8% / 72.5%；Luna 两段都是 41.3%。Sol /
Terra 可按质量需要把阈值提高到 `0.5–0.65`；Luna 不建议照搬。

GPT-6 Astra 在 256K–512K 为 100%、512K–1M 为 96.3%，接受 272K 以上费率后
可以把阈值提高到 `0.6–0.7`（约 553K–645K）；`0.7–0.8` 更偏容量，质量代价
也更明显。GPT-6 Sol / Luna 尚无公开分段结果，先沿用成本优先阈值，等自己
量过长上下文质量再调整。

验证：

```bash
chord doctor models --model openai/gpt-6-sol@medium
chord doctor models --model openai/gpt-5.4@xhigh
```

## Codex OAuth preset

当你要使用 ChatGPT/Codex OAuth，而不是 API key 时，用这个配置。Codex OAuth
与上方 API key 示例的区别只在 provider preset 和认证方式：模型窗口与 API
一致。

本节使用的模型档位：

```yaml
providers:
  codex:
    preset: codex
    type: responses
    models:
      gpt-6-astra: {<<: [*window-codex-1050k-128k, *gpt-reasoning-full, *vision-pdf]}
      gpt-6-sol: {<<: [*window-codex-1050k-128k, *gpt-reasoning-full, *vision-pdf]}
      gpt-6-luna: {<<: [*window-codex-1050k-128k, *gpt-reasoning-full, *vision]}
      gpt-5.6-sol: {<<: [*window-codex-1050k-128k, *gpt-reasoning-full, *vision-pdf]}
      gpt-5.4: {<<: [*window-codex-1050k-128k, *gpt-reasoning-basic, *vision-pdf]}
      gpt-5.5: {<<: [*window-400k-128k, *gpt-reasoning-basic, *vision-pdf]}

model_pools:
  default:
    - codex/gpt-6-sol@medium
    - codex/gpt-5.5@xhigh
```

Codex 的窗口由 `context`、`input`、`output` 三个字段共同描述：`context`
是输入加输出的总窗口，`input` 和 `output` 是其中各自独立的硬上限，两个
上限不必相加等于 `context`。输入接近上限时，留给输出的空间自然会变少。
正文的 `&window-1050k-128k` 面向不公布独立输入上限的官方 API，所以这里
改用带 `input: 922000` 的 `&window-codex-1050k-128k`。

登录：

```bash
chord auth codex
```

要点：

- 同时使用 API key 和 Codex OAuth 时，因为凭据和模型配额不同，应保留两个 provider，并分别配置模型限制。
- 每个条目都带上了与上文配方相同的 `reasoning` 和 `modalities`。没有
  `reasoning` 块时请求里完全不发 reasoning 参数，effort 交给后端默认值，
  也不会请求摘要。
- 初始安装向导按同样的档位写完整 Codex 目录（另外还有 `gpt-5.2`、
  `gpt-5.3-codex`、`gpt-5.6-terra`、`gpt-5.6-luna`），但只写 `limit`、
  模型池也不带后缀；想让这些模型也有思考摘要和附件输入，按上面的方式补
  `reasoning` 与 `modalities`。
- Codex 订阅窗口由服务端模型目录控制，而不是模型页：目录值历史上多次变动、
  账号间也不一致（输入侧出现过低至 272K 的档位）。`/status` 在首个请求前可能
  显示配置值、请求后才回落真实值。依赖 1.05M 窗口前先实测该端点实际接受的
  输入量，账号/中转仍是旧档位时为该 provider 改用 `400000 / 272000 / 128000`。
- GPT-6 Astra 正在上线后头几周内向 Codex 推出（需要 Codex CLI 0.153.0
  或更新版本），Codex 订阅窗口官方尚未公布。配方沿用 GPT-5.6 Sol 的
  `1050000 / 922000 / 128000` 作为保守起点；上线后请按账号的服务端目录
  核对，并把三个字段都调成实测窗口再用于长会话。
- 这份 preset 不含 `cost`（订阅不按 token 计费），也不含 `compaction`：
  按上文 GPT 的[上下文压缩](#上下文压缩)一节挑一个阈值，写在模板或模型条目上。
  触发点按实测可用预算计算；API 的 >272K 整单 2× 计价悬崖只在路由实际
  采用 OpenAI API 长上下文价格时才适用。
- 这些数值跟随当前 Codex 模型目录，未来 Codex 版本可能调整。后端配额变化时，要同时更新三个字段。

## Anthropic Claude

```yaml
anthropic:
  - "$ANTHROPIC_API_KEY"
```

```yaml
model_templates:
  claude-opus: &claude-opus
    <<: [*window-1m-128k, *vision-pdf]
    cost:
      input: 5
      output: 25
      cache_read: 0.5
      cache_write: 6.25
      cache_write_1h: 10
    thinking:
      type: adaptive
      display: summarized
    variants:
      high:
        thinking:
          effort: high
      xhigh:
        thinking:
          effort: xhigh

providers:
  anthropic:
    type: messages
    api_url: https://api.anthropic.com/v1/messages
    models:
      claude-opus-5: *claude-opus
      claude-opus-4.8: *claude-opus
      claude-opus-4.7: *claude-opus

model_pools:
  default:
    - anthropic/claude-opus-5@high
```

Opus 5、4.8、4.7 的上下文窗口（1M）、最大输出（128K）、定价、adaptive
thinking 与输入模态完全一致，因此共用同一个 `&claude-opus` 模板，只有
模型 ID 不同。用不到的型号可以删掉，`model_pools` 指向你想用的模型即可
（例如 `anthropic/claude-opus-5@high`）。

如果想要更低成本的 Claude 配置，可沿用同样结构，改为 `claude-sonnet-5`、`cost: {input: 2, output: 10}`，并按需把 `output` 调低（例如 64000）做保守的本地分配。Sonnet 5 的 $2 / $10（每百万 token）定价已于 2026 年 8 月转为永久。

`claude-fable-5-1`（2026 年 9 月发布）沿用同样的结构：1M 上下文、128K 最大输出、adaptive thinking 和 PDF 支持都一样，只有 cost 块不同。它沿用了 Fable 5 的 $10 / $50（每百万 token 输入 / 输出）费率，但缓存读取降到每百万 token $0.25（是基础输入价的 0.025x，而不是常见的 0.1x 乘数），所以 `cache_read` 要填 0.25，不要按比例填成 1.0。`claude-fable-5` 仍可用，费率相同，只有缓存读取是 $1.0。

```yaml
# 需要同一文件上方的 `&claude-opus` 模板。
model_templates:
  claude-fable-5.1: &claude-fable-5-1
    <<: *claude-opus
    cost:
      input: 10
      output: 50
      cache_read: 0.25
      cache_write: 12.5
      cache_write_1h: 20

providers:
  anthropic:
    type: messages
    api_url: https://api.anthropic.com/v1/messages
    models:
      claude-fable-5-1: *claude-fable-5-1

model_pools:
  default:
    - anthropic/claude-fable-5-1@high
```

### Claude Opus 5.5

`claude-opus-5-5`（2026 年 9 月发布）在多数工作上达到 Fable 5.1 的水平，价格
是 $4 / $20（每百万 token），低于 Opus 5 的 $5 / $25，不到 Fable 5.1 的
$10 / $50 一半，缓存读取 $0.20；1M 上下文、128K 最大输出、adaptive thinking
和 PDF 支持都沿用同一套。只有 cost 块和 variants 不同：它的默认 effort 是
`medium`，比 Opus 5 的 `high` 低一档。

```yaml
# 需要同一文件上方的 `&claude-opus` 模板。
model_templates:
  claude-opus-5.5: &claude-opus-5-5
    <<: *claude-opus
    cost:
      input: 4
      output: 20
      cache_read: 0.2
      cache_write: 5
      cache_write_1h: 8
    variants:
      medium:
        thinking:
          effort: medium
      high:
        thinking:
          effort: high
      xhigh:
        thinking:
          effort: xhigh

providers:
  anthropic:
    type: messages
    api_url: https://api.anthropic.com/v1/messages
    models:
      claude-opus-5-5: *claude-opus-5-5

model_pools:
  default:
    - anthropic/claude-opus-5-5@medium
```

要点：

- thinking 始终开启：请求里关掉 thinking、或使用强制工具调用，都会报错。
  `thinking.type` 保持 `adaptive`。
- 工具调用之间的文本现在从 `thinking` 块返回，默认 `display` 下是空的；模板里的
  `display: summarized` 让这段进度在 Chord 里照常显示，不会在两个工具之间突然
  安静。
- thinking 块绑定产生它的模型和对话前缀。本地改写历史（自动压缩、请求级剪裁）
  之后，API 会把重放的块判为 `bound to a different conversation` 而拒绝；Chord
  会识别这类错误、丢掉 thinking 块后重试，会话照常继续，只是压缩前的推理原文
  没了。

### Claude 5 的压缩调优

Claude 5 全系（Fable 5.1、Opus 5.5、Opus 5、Sonnet 5）都是 1M 上下文、128K 最大输出、全窗口统一按 token 计费。MRCR v2 8-needle 显示 Opus 级模型即使到 1M 仍能保持 ~76%（当前所有模型族里最平坦的曲线），可靠窗口确实很大。Opus 4.7 时代的模型为换取「拒绝而非编造」牺牲了检索准确率；Opus 5、Opus 5.5 和 Fable 5.1 恢复了强长上下文检索；Opus 5.5 的缓存读取（每百万 token $0.20）低于 Opus 5 和 Fable 5.1，压缩后重读文件的成本更低。日常用直接不写 `compaction` 块，跟全局默认走
（`threshold` 0.8，可用预算约 872K 里约 698K 触发）；跑数小时的 agentic 长会话
再设 `threshold: 0.7`（约 610K），少在 512K 以上的轻度退化带深处待。

```yaml
# 直接在既有 claude-fable-5-1 模板上加 compaction，引用它的 provider 全部继承
model_templates:
  claude-fable-5-1: &claude-fable-5-1
    <<: [*window-1m-128k, *vision-pdf]
    compaction: {threshold: 0.7}   # 针对数小时 agentic 长会话调低到 0.7
```

`reminder` 故意省略：派生值 0.60 对这些模型是
合理的提前量，只有想更早/更晚提示时才显式设置。Opus 5 等同一可靠档的
模型加同样一行即可。

注意 Opus 4.7 起换了 tokenizer：同样文本在 Claude 5 模型上比老模型多约 30% token，所以在老模型上感觉合适的上下文预算要相应下调。

## 走 Chat Completions 网关的 thinking

网关可以把多个模型暴露在 `/v1/chat/completions` 上，再把请求转成上游原生 API。
Chord 的 `thinking.*` 与线路无关，但网关只认它自己转换逻辑里的字段形状，所以
Chord 会按模型名推断出的方言，把这些配置写进 chat 请求体：

| 模型 | Chord 追加的字段 | 取值来源 |
| --- | --- | --- |
| Gemini | `extra_body.google.thinking_config` | `thinking.level`、`thinking.budget`、`thinking.include_thoughts`；键名用 snake_case，预算与级别的冲突规则、`include_thoughts` 默认值都和原生线路一致 |
| Claude | `thinking: {type, budget_tokens}` | `thinking.type`、`thinking.budget`、`thinking.display` |
| DeepSeek、GLM、Kimi K2.x、Doubao | `thinking: {type}` | `thinking.type`，`adaptive` 映射成 `enabled` |
| Qwen | `enable_thinking` | `thinking.type`、`thinking.budget` |

没有配置 thinking 块的模型不会追加任何字段；不属于上表的模型也不会把 thinking 配置
写进 chat 请求体，这类情况按下文显式指定方言。

```yaml
model_templates:
  gemini-flash: &gemini-flash
    <<: *window-1049k-64k
    thinking:
      include_thoughts: true
    variants:
      high: {thinking: {level: high}}
      medium: {thinking: {level: medium}}
      low: {thinking: {level: low}}

  claude-chat: &claude-chat
    <<: *window-200k-64k
    thinking: {type: enabled, budget: 8192}

  deepseek-chat: &deepseek-chat
    <<: *window-1m-64k
    reasoning: {effort: high}
    thinking: {type: enabled}

  glm-chat: &glm-chat
    <<: *window-200k-64k
    thinking: {type: enabled}
    compat:
      # 家族特有的附加项留在 override 里；Chord 会把它合并进由上面模型级
      # thinking 块生成的 thinking 对象。
      request_overrides:
        body:
          thinking: {clear_thinking: false}

providers:
  gateway:
    type: chat-completions
    api_url: https://example.com/v1/chat/completions
    models:
      gemini-3.8-flash: *gemini-flash
      claude-fable-5.1: *claude-chat
      deepseek-v4.1-flash: *deepseek-chat
      glm-5.2: *glm-chat

model_pools:
  default:
    - gateway/gemini-3.8-flash@high
    - gateway/claude-fable-5.1
    - gateway/deepseek-v4.1-flash
    - gateway/glm-5.2
```

- 不需要额外配置：字段完全由你已配置的 thinking 项生成，同一份模板既能在网关后面
  生效，也能直接连模型的原生端点。
- 端点拒绝未知请求体字段、既不忽略也不转换时，用
  `compat.chat_completions.native_thinking: off`（模型级或 provider 级）关掉。
- 模型名看不出上游（网关别名、私有部署）时直接指定形状：`gemini`、`gemini-3`、
  `anthropic`、`thinking`、`qwen`。只知道是 Gemini 家族时用 `gemini`；已确认别名
  指向 Gemini 3、需要缺失签名修复时用 `gemini-3`。`claude`、`deepseek`、`glm`、
  `kimi`、`doubao` 等家族名仍可使用。
- Kimi K3 不接受 K2.x 的 `thinking` 参数，所以不要给它配模型级 thinking 块；K2.x
  模型按上面的说明使用该块。
- `reasoning.effort` 仍按可移植的 `reasoning_effort` 字段发送。网关自己映射 effort
  时（Claude、Gemini 网关通常如此），只配它就够。Google 官方兼容端点里
  `reasoning.effort` 与 `extra_body.google.thinking_config` 互斥，二者只设其一。
- 思考摘要以 `reasoning_content` 返回并显示为 thinking。上游是否真的返回摘要文本仍
  取决于网关和模型；无论是否返回，级别和预算都会作用到请求上。

### 思考状态与签名

模型的原生 API 会返回与后端绑定的回放状态，经过网关时也一样，而且下一次调用必须
把它带回去。Chord 按模型所属家族用对应的形状承托：

| 家族 | 网关返回的位置 | Chord 回传的位置 |
| --- | --- | --- |
| Gemini | 每步一个 thought signature：`tool_calls[].extra_content.google.thought_signature`（Google 官方兼容端点）、`tool_calls[].thought_signature`，或 `provider_specific_fields.thought_signature` | 该步第一条工具调用的 `extra_content.google.thought_signature` |
| Claude | assistant 消息级的 `thinking_blocks`（LiteLLM 的约定），也读 `provider_specific_fields` 里的镜像 | 同一个 `thinking_blocks` 数组 |

这些 blob 对 Chord 不透明，只由产出它的后端校验，所以 Chord 按**模型家族**回放，
而不是看请求走哪条线路：在 Gemini 端点上拿到的签名，续聊时换成网关、换个 provider
或直连原生 API 都能继续用，反过来也一样。请求落到别的家族时会剥掉这些 blob；其中
可读的思考文本仍会按目标接受的形式作为普通 thinking 发出。

Chat Completions 模型使用别名时，用 `native_thinking: anthropic` 或 `gemini`
明确后端家族；已确认是 Gemini 3 且需要签名修复时用 `gemini-3`。Chord 会把家族
信息随响应保存，恢复会话后也不必靠别名猜测来源。
只有家族匹配，才会转换回放载体：例如 Messages 思考块中的 Gemini 签名，切到
Chat Completions 后会放在第一条工具调用上。Gemini 工具续轮即使没有可见思考文本，
也会保留配置的思考控制参数。

状态缺失或被拒时，Chord 会修好请求再发，而不是发一个必然失败的请求：

- Gemini 3 不接受缺 thought signature 的 function call 步骤。最后一条用户消息之后
  的 assistant 步骤签名已丢时（换了模型、网关把它丢了），Chord 会补上官方文档给出的
  占位值 `skip_thought_signature_validator`，后端接受它代替真实签名。这个修复需要知道
  端点确实是 Gemini 3：网关别名把上游名字藏了的话，显式 pin
  `native_thinking: gemini-3` 即可。只声明家族的 `gemini` 不会默认推断模型版本。
- Claude 线路当前回合已经没有可回放的 `thinking_blocks` 时，该请求不会再带 `thinking`
  控制字段：发出的历史里没有对应的思考块，声明了思考反而会被拒。
- 后端确实拒绝某个签名时，仍会按回放兼容等级逐级降级：Chord 剥掉 blob 重试，而不是
  让这一轮直接失败。

`native_thinking: off` 会一并停掉这些状态的发送与回放，适用于拒绝原生字段的端点。

网关是否真的返回这些状态仍取决于它自己：代理若丢掉 `extra_content` 或
`thinking_blocks`，Chord 也就无从回放。

## Google Gemini

```yaml
gemini:
  - "$GEMINI_API_KEY"
```

```yaml
model_templates:
  # Gemini 3.x Flash 共用形状：1M 窗口、`level` 控制的 thinking。
  gemini-flash: &gemini-flash
    <<: [*window-1049k-64k, *vision-pdf]
    thinking:
      level: high

providers:
  gemini:
    api_url: https://generativelanguage.googleapis.com/v1beta/models
    models:
      gemini-3.8-flash: *gemini-flash

model_pools:
  default:
    - gemini/gemini-3.8-flash
```

要点：

- `api_url` 保持在 `/models` 基础路径即可；Chord 会自动追加 `/{model}:streamGenerateContent?alt=sse`。
- `type` 可以省略；Chord 会根据 `/models` 路径自动识别 Gemini。
- Gemini 3.8 Flash（2026 年 9 月 2 日 GA）是目前的主力模型：1M token 上下文、最大 64K 输出，thinking 级别为 `low` / `medium`（官方默认）/ `high`。它不支持 `minimal`，且 `thinking_budget` 已废弃，所以上面模板只用 `level`；模板固定用 `high` 服务 agentic 场景；日常任务降到 `medium` / `low` 可以省延迟和 token。
- Gemini 3.5 / 3.6 Flash 也是同一套结构，并且仍然接受 `minimal`；Flash-Lite 系列则以 `minimal` 为默认值。Gemini 3.1 Pro 只接受 `low` / `medium` / `high`，同样不支持 `minimal`，所以不要把一个 `minimal` variant 套用到整个家族。

### Gemini 的压缩调优

Gemini 长上下文表现随档位差异极大，没有统一的压缩规则：

- **Gemini 3.1 Pro** 的多针长上下文确实弱（公开 MRCR v2 8-needle 检索约 0.26），所以要保留激进压缩：`threshold` 取可用预算的约 0.2、`reminder` 约 0.15（1M 窗口约合 150K–210K）。
- **Gemini 3.8 Flash、Flash-Lite** 为 1M 窗口设计，长上下文表现很好，激进提前压缩只会丢掉它们还能用的上下文。Flash 用全局默认（`threshold` 0.8）或直接不写该模板块即可。3.8 Flash 靠更高的 token 消耗换取更好的准确率，所以长时间 agentic 任务里用量上涨属于正常现象，不是该提前压缩的信号。

```yaml
# 按模型分别配 Gemini 的 compaction；引用该模板的 provider 全部继承。
model_templates:
  gemini-pro: &gemini-pro
    <<: [*window-1049k-64k, *vision-pdf]
    compaction: {threshold: 0.2, reminder: 0.15}
    thinking:
      include_thoughts: true
    variants:
      high: {thinking: {level: "high"}}
      medium: {thinking: {level: "medium"}}
      low: {thinking: {level: "low"}}
```

计费提醒：只有 **Gemini 3.1 Pro** 在超过 200K 输入后进入更高输入档（整请求按高价档计费）；Gemini 3.8 Flash 与 Flash-Lite 在任何上下文长度下都是平价，所以 Flash 没有为省钱而提前压缩的理由，只有你的工作负载确实出现质量退化，才压。如果你既要长可靠窗口、又要 Pro 级质量，那才是该换用 GPT-5.6 Sol / Claude 5 这类模型的场景。

## GLM / BigModel Coding Plan

```yaml
bigmodel:
  - "$BIGMODEL_API_KEY"
```

下面三个模板是 GLM-5.x 系列通用基础：`chat` 模板的
`thinking.type: enabled` + `clear_thinking: false` 与 `reasoning.effort`
取值落在 GLM-5.2（动态思考，effort 支持 `max`/`xhigh`/…/`none`）和
GLM-5.3 / 5.3-Flash（强制思考，effort 仅 `max`/`high`/`low`）的交集上，
所以同一套模板可直接用于 5.2、5.3 与 5.3-Flash。`glm-5.2-*` 命名取自引入
这些 compat 设置的模型，不代表只适用于 5.2。

```yaml
model_templates:
  glm-5.2-chat: &glm-5-2-chat
    <<: *window-1m-128k
    reasoning:
      effort: max
    compat:
      request_overrides:
        rename_body_fields:
          max_completion_tokens: max_tokens
        body:
          thinking:
            type: enabled
            clear_thinking: false
      reasoning_continuity:
        mode: openai_visible
        preserve_history: true

  glm-5.2-messages: &glm-5-2-messages
    <<: *window-1m-128k
    thinking:
      type: adaptive
      effort: max
    compat:
      request_overrides:
        headers:
          anthropic-beta: null
      reasoning_continuity:
        mode: anthropic_unsigned

  glm-5.2-responses: &glm-5-2-responses
    <<: *window-1m-128k
    reasoning:
      effort: max

  glm-5.3-chat: &glm-5-3-chat
    <<: *glm-5-2-chat
    variants:
      low:
        reasoning:
          effort: low
      high:
        reasoning:
          effort: high
      max:
        reasoning:
          effort: max

  glm-5.3-flash: &glm-5-3-flash
    <<: [*glm-5-3-chat, *vision-pdf]

providers:
  bigmodel:
    type: chat-completions
    api_url: https://open.bigmodel.cn/api/coding/paas/v4/chat/completions
    models:
      glm-5.2: *glm-5-2-chat
      glm-5.3: *glm-5-3-chat
      glm-5.3-flash: *glm-5-3-flash

  bigmodel-messages:
    type: messages
    api_url: https://open.bigmodel.cn/api/anthropic/v1/messages
    models:
      glm-5.2: *glm-5-2-messages

  glm-responses:
    type: responses
    api_url: https://example.com/v1/responses
    models:
      glm-5.2: *glm-5-2-responses

model_pools:
  default:
    - bigmodel/glm-5.3-flash
```

要点：

- Chat Completions 需要 `thinking.type: enabled`、`reasoning_effort` 和
  `max_tokens`。`request_overrides` 添加 GLM 思考字段并重命名动态计算的输出
  上限字段。
- Messages 兼容接口使用 `thinking` 和 `output_config.effort`。除非对应接口
  明确支持，否则应关闭 Anthropic beta header；签名回放能力不能只凭 wire
  格式推断，只有确认 endpoint 接受自身无签名 thinking 的工具循环后，才按
  共用节配置 `anthropic_unsigned`。
- GLM 的 `/responses` 由网关自行实现。只有网关明确说明支持 OpenAI
  Responses 映射时，才单独使用仅含 `reasoning.effort` 的模板。
- GLM-5.3（2026 年 8 月 GA）沿用 GLM-5.2 的纯文本规格（1M 上下文、128K
  最大输出），可以直接复用上面的 GLM-5.2 模板，只需把 provider `models`
  里的模型 ID 换成 `glm-5.3`。
- GLM-5.3 和 5.3-Flash 的 `reasoning_effort` 只支持 `low` / `high` / `max`
  三档（GLM-5.2 另外支持 `xhigh` / `medium` / `minimal` / `none` 并映射），
  所以 `glm-5.3-chat` 模板额外加了这三档 `variants`；5.3 和 5.3-Flash 请用
  `glm-5.3@low|high|max` 这类引用，不要套用 5.2 的 effort 取值集合。
- GLM-5.3-Flash（2026 年 8 月发布）是 GLM-5 系列首个原生多模态模型：
  支持图像 / 视频 / 文件输入，上下文 1M、最大输出 128K。PDF 输入官方支持：
  GLM Chat Completion API 接受 `file` content block，`file` 对象可选
  `file_id` / `file_url` / `file_data`（Base64 的 `data:<MIME>;base64,...`
  URL），单文件 ≤ 50M，支持 pdf/txt/word/jsonl/xlsx/pptx 等格式；这与 Chord
  的 chat-completions PDF 负载（`type: file` + `filename` + `file_data`）
  完全一致，无需兼容配置。文本参数与 GLM-5.3 一致，所以从上面的 Chat
  Completions 模板继承，只需补 `modalities.input`。`thinking.type` 只支持
  `enabled`（无法关闭思考），chat 模板已配置好。第三方中转可能只实现了
  旧的仅 URL 形式 `file_url`，依赖 Base64 `file_data` 前先确认中转支持。
- 示例默认池优先选 `glm-5.3-flash`：Coding Plan 主力、原生多模态输入。
  纯文本场景把池条目换成 `bigmodel/glm-5.3`；需要 GLM-5.2 更宽的 effort
  档位（`xhigh` / `medium` / `minimal` / `none`）时也可以保留
  `bigmodel/glm-5.2`：上面的 provider `models` 仍把它列为可选文本模型，
  沿用同一套模板。

### GLM-5.x 的压缩调优

GLM-5.2/5.3 标称 1M 窗口，但独立的长上下文评测把 GLM/Qwen 这一类开放权重
模型的可靠工作窗口放在 200K–256K 左右（大约是标称 1M 的 20–25%）。如果你在
GLM 上跑长探索型会话，请在可用预算的四分之一附近压缩：

```yaml
# 给你已经在用的 glm 模板（glm-5.2-chat / glm-5.2-messages /
# glm-5.3-chat ...）加上 compaction；引用该模板的模型条目全部继承。
model_templates:
  glm-5.2-chat: &glm-5-2-chat
    <<: *window-1m-128k
    compaction: {threshold: 0.25, reminder: 0.2}
```

上面的配方里 GLM-5.2 由多个 provider 提供（`bigmodel` chat、
`bigmodel-messages`、`glm-responses`）；每个引用该模板的模型条目都会拿到同一份
`compaction`。

## DeepSeek

```yaml
deepseek:
  - "$DEEPSEEK_API_KEY"
```

`deepseek-flash` 就是 DeepSeek-V4.1-Flash：1M 上下文、默认开启思考，三条
wire family 都原生支持图像输入。官方 API 上，旧的 `deepseek-v4-flash` 与
`deepseek-v4-flash-vision-exp` 仍被接受，会路由到这里并按 Flash 价格计费；
`deepseek-v4-pro` 的请求也会在 2026-09-14 12:00（北京时间）之后路由过来，
直到 V4.1-Pro 发布。

```yaml
model_templates:
  deepseek-v4.1-chat: &deepseek-v4-1-chat
    <<: [*window-1m-64k, *vision]
    reasoning:
      effort: high
    variants:
      low:
        reasoning:
          effort: low
      high:
        reasoning:
          effort: high
      max:
        reasoning:
          effort: max
    compat:
      request_overrides:
        rename_body_fields:
          max_completion_tokens: max_tokens
        body:
          thinking:
            type: enabled
      reasoning_continuity:
        mode: openai_visible
        preserve_history: true
      forced_tool_choice:
        suppress_in_thinking: true

  deepseek-v4.1-messages: &deepseek-v4-1-messages
    <<: [*window-1m-64k, *vision]
    thinking:
      type: adaptive
      effort: high
    variants:
      low:
        thinking:
          effort: low
      high:
        thinking:
          effort: high
      max:
        thinking:
          effort: max
    compat:
      request_overrides:
        headers:
          anthropic-beta: null
      reasoning_continuity:
        mode: anthropic_unsigned
        preserve_history: true

  deepseek-v4.1-responses: &deepseek-v4-1-responses
    <<: [*window-1m-64k, *vision]
    reasoning:
      effort: high
    variants:
      low:
        reasoning:
          effort: low
      high:
        reasoning:
          effort: high
      max:
        reasoning:
          effort: max
    compat:
      responses:
        send_reasoning_include: false
        send_max_output_tokens: true
      reasoning_continuity:
        mode: openai_visible
        preserve_history: true

providers:
  deepseek:
    type: chat-completions
    api_url: https://api.deepseek.com/v1/chat/completions
    models:
      deepseek-flash: *deepseek-v4-1-chat

  deepseek-messages:
    type: messages
    api_url: https://api.deepseek.com/anthropic/v1/messages
    models:
      deepseek-flash: *deepseek-v4-1-messages

  deepseek-responses:
    type: responses
    api_url: https://api.deepseek.com/v1/responses
    models:
      deepseek-flash: *deepseek-v4-1-responses

model_pools:
  default:
    - deepseek/deepseek-flash@high
```

要点：

- DeepSeek Chat thinking 使用 `thinking.type`、顶层 `reasoning_effort` 和
  `max_tokens`。`request_overrides` 提供请求形状差异。
  请求带 tools 时，DeepSeek 要求后续每一轮都完整回传历史
  `reasoning_content`，否则返回 `400`，所以模板设置 `preserve_history: true`
  让 Chord 在本地保留已完成轮次的思考；不带 tools 时该字段会被忽略。
  DeepSeek 在启用 thinking 时会拒绝 forced tool
  choice，所以模板会把 loop 强制的 `tool_choice: required` 降级为后端默认
  选择。
- DeepSeek Responses 支持 `tool_choice: required`，因此模板保留 loop 的强制
  工具选择。该接口直接返回明文 `reasoning_text`，因此无需请求加密 reasoning；
  它支持 `max_output_tokens`，模板会继续显式发送上限。接口不支持的字段
  （`store`、`background`、`previous_response_id` 等）会被静默忽略；流以
  `response.completed` / `incomplete` / `failed` 事件结束，没有
  `data: [DONE]`。
- DeepSeek Messages 支持 `output_config.effort`；Chord 从 `thinking.effort`
  生成该字段。兼容接口应关闭 Anthropic beta header：它只对 Files API 生效。
  `thinking.budget_tokens` 会被接受但忽略：思考深度由 effort 值决定，不是
  token 预算。其 Anthropic 兼容接口也可能返回无签名 thinking，按共用节配置
  `anthropic_unsigned`。
- 三个 wire family 都能收图，图片按输入 token 计费（官方上限为单图 1024
  token）。接口支持 inline base64、外部 URL 与 Files API `file_id`，按文件
  内容识别格式（JPEG / PNG / GIF / WebP），且图片只能出现在 user 消息中：
  system 或 assistant 消息带图会返回 `400`。请求限制：请求体不超过
  48 MiB、单请求最多 600 张图、图片总量不超过 64 MiB（用 `file_id` 时
  200 MiB）、单边不超过 8192 px（单请求图片达到 15 张时降到 4096 px）。
  Chord 固定用 inline base64，所以在 Chord 内部用不了外部 URL 或 `file_id`。
  - `image_url`（Chat）或 `input_image`（Responses）上的 `detail` 接受
    `low` / `high` / `original`（与 `high` 等价）/ `auto`；Chord 在
    Responses 上发送 `auto`，在 Chat 上不带该字段，也不提供按请求配置。
  - [`view_image`](./tools_CN.md) 工具能把本地图片加载进上下文，但只有把
    这个模型放在 `messages` 或 `responses` provider 的池首才行：
    `chat-completions`（`deepseek`）provider 能在用户消息里收图，却无法在
    tool result 里返回图片。
- 第三方 `/responses` 端点由网关自行实现；只有网关明确说明映射方式时，
  才使用 `reasoning.effort` 和 `openai_visible`。
- 对兼容网关，请使用该网关 / 账号实际公开的模型 ID 和限制。见
  [常见问题排查：DeepSeek / OpenAI 兼容 thinking 模式 400](./troubleshooting_CN.md#deepseek--openai-兼容-thinking-模式-400)。

补充：

- 官方定价页标注的最大输出为 384K；这里 `limit.output: 64000` 是保守的
  本地分配，需要更长输出时按需调大。
- `reasoning_effort`（Chat）与 `output_config.effort`（Messages）接受
  `low` / `high` / `max`，Responses 的 `reasoning.effort` 还接受 `none`
  （关闭思考）；默认值是 `high`。其余取值由后端重映射：`medium` 和
  `xhigh` 映射到 `high`，所以模板只定义 `low` / `high` / `max`
  三个 variant。
- Responses API 位于 `api.deepseek.com/v1/responses`；响应中的
  `output_tokens_details.reasoning_tokens` 由 Chord 按标准 reasoning 回显
  处理，无需额外配置。
- Flash 价格（每百万 token，off-peak | peak）：缓存命中 $0.003 | $0.006，
  缓存未命中 $0.15 | $0.30，输出 $0.60 | $1.20。peak 时段为 UTC 周一至
  周五 01:00–04:00 与 06:00–10:00。见
  [DeepSeek 官方定价](https://api-docs.deepseek.com/quick_start/pricing/)。

### 仍在提供 V4 一代模型的网关

有些 provider 仍在提供 V4 一代的权重，模型 ID 是 `deepseek-v4-flash` 或
`deepseek-v4-pro`。这一代只收文本：flash 和 pro 传图会返回 `400`（一代里
只有实验性的 `deepseek-v4-flash-vision-exp` 收图），所以这类条目不能声明
`image` 输入。直接复用 `*deepseek-v4-1-chat` 会把 `image` 一起继承过来，
用 `modalities` 覆盖，或者干脆按纯文本另建模板：

```yaml
# 网关仍在提供 V4 一代模型时：模板照抄，只保留文本。
# merge 序列里靠前的锚点优先，所以 text-only 放在前，覆盖 V4.1 模板的图像模态。
model_templates:
  deepseek-v4-chat: &deepseek-v4-chat
    <<: [*text-only, *deepseek-v4-1-chat]

providers:
  deepseek-gateway:
    type: chat-completions
    api_url: https://example.com/v1/chat/completions
    models:
      deepseek-v4-flash: *deepseek-v4-chat
```

### DeepSeek V4.1 Flash 的压缩调优

DeepSeek V4.1 Flash 标称 1M 窗口，但长距离可靠性是这个家族的短板：上一代
V4 的独立 multi-needle 评测中，V4 Pro 在 1M 处只有约 41%（8-needle），
单 needle 约 78%：这种陡降和 Gemini 3.1 Pro 的悬崖如出一辙。V4.1 目前
没有公开的长上下文评测，在此之前仍按同样的口径处理：把可靠工作窗口按
约 200K 对待，尽早压缩。Flash 家族即使全部缓存未命中，也远比同级模型便宜
得多，所以频繁压缩的代价比在高端模型上低，尽早压、多压几次：

```yaml
# 给上面配方里的 deepseek-v4.1-chat / -messages / -responses 模板加上
# compaction；引用这些模板的模型条目会全部继承。
model_templates:
  deepseek-v4.1-chat: &deepseek-v4-1-chat
    <<: [*window-1m-64k, *vision]
    compaction: {threshold: 0.25, reminder: 0.2}
```

DeepSeek 的缓存命中价是业界最低的（$0.003/M），因此一次能保住可缓存前缀的
压缩，在重复读取场景下几乎是免费的。短的交互式会话保持全局默认即可，只有
真正跑长时间 agentic 任务时才需要单独给模型条目调参。

## Qwen 保留历史思考

Qwen 通过 `reasoning_content` 返回可见思考，但大多数型号默认忽略历史
消息里的该字段。只有模型文档明确支持 `preserve_thinking` 时才应开启
回放：目前是 Qwen 3.8 Max，3.7 Max / Plus / Flash，以及 3.6 Max
preview / Plus（含带日期的快照版本）。请以官方支持列表为准；较早的
Qwen 3/3.5 即使会输出思考，也应保持 continuity 关闭。

```yaml
model_templates:
  qwen-preserved: &qwen-preserved
    <<: *window-1m-65k
    compat:
      request_overrides:
        body:
          enable_thinking: true
          preserve_thinking: true
      reasoning_continuity:
        mode: openai_visible
        preserve_history: true

providers:
  qwen:
    type: chat-completions
    api_url: https://dashscope-intl.aliyuncs.com/compatible-mode/v1/chat/completions
    models:
      qwen3.7-plus: *qwen-preserved

model_pools:
  default:
    - qwen/qwen3.7-plus
```

请按账号和区域文档替换上下文限制及 endpoint。`preserve_thinking: true`
时，历史思考会计入输入 token 和费用；`preserve_history: true` 让 Chord
不在客户端剥离这段历史。

## Kimi

Kimi K3 是当前旗舰思考模型，提供 1M token 上下文、始终启用思考，
`reasoning_effort` 接受 `low` / `high` / `max`（默认 `max`）；会话中途
切换 effort 会让 prefix cache 失效，所以不要频繁改。它要求多轮对话和
工具调用循环完整回传 assistant 消息（包括 `reasoning_content`）。不要
发送 K2.x 的 `thinking` 参数，也不要显式发送采样字段：K3 把
`temperature` 固定为 1.0、`top_p` 固定为 0.95、penalties 固定为 0。

```yaml
model_templates:
  kimi-k3: &kimi-k3
    <<: *window-1049k-131k
    reasoning:
      effort: max
    compat:
      chat_completions:
        mcp_system_tools_message: true
      reasoning_continuity:
        mode: openai_visible
        preserve_history: true

  kimi-k2.7-code: &kimi-k2-7-code
    <<: *window-256k-32k
    compat:
      reasoning_continuity:
        mode: openai_visible
        preserve_history: true

  kimi-k2.6-thinking: &kimi-k2-6-thinking
    <<: *window-256k-32k
    compat:
      request_overrides:
        body:
          thinking:
            type: enabled
            keep: all
      reasoning_continuity:
        mode: openai_visible
        preserve_history: true

providers:
  kimi:
    type: chat-completions
    api_url: https://api.moonshot.ai/v1/chat/completions
    models:
      kimi-k3: *kimi-k3
      kimi-k2.7-code: *kimi-k2-7-code
      kimi-k2.6: *kimi-k2-6-thinking

model_pools:
  default:
    - kimi/kimi-k3
```

`mcp_system_tools_message` 是显式的模型能力开关，不靠模型名猜测。开启后，
运行时 manual MCP 声明会固定在原对话位置，不再改写顶层工具列表。不接受
「只带 `tools`、不带 `content` 的 `role: system` 消息」的网关应保持关闭；
fallback 池能力不一致时，Chord 会自动采用各模型都能接受的顶层工具形态。

K2.7 Code 是 256K 上下文、面向编码的纯思考型号；它的 thinking 和
`keep: all` 行为固定，因此模板不发送 `thinking` 对象。K2.6 是 256K
上下文的通用混合思考型号，所以显式设置这两个字段。
K2.5 不支持保留历史思考。

对于所有使用 `openai_visible` 的模板（DeepSeek、GLM、受支持的 Qwen 和
Kimi），Chord 首次会把原生 reasoning 乐观回放给任何 Chat Completions
目标，因此 Kimi K2.6/K2.7→K3 这类官方允许的同 provider 升级和同模型跨
provider fallback 都能保留连续性。工具模式契约要求完整 reasoning 历史的
后端（DeepSeek）和 preserved-thinking 模板（GLM `clear_thinking: false`、
Qwen `preserve_thinking`、Kimi K3 / `keep: all`）都设置
`preserve_history: true`。若目标拒绝原生 reasoning，Chord 只会
删除或转换不兼容的 reasoning 负载；已完成且成对的工具调用和结果仍会保留。
目标连结构化形状也不接受时，严格降级会把已完成的动作历史文本化，而
不会把外部工具事实静默删除。

### 跨协议 fallback 的连续性

当模型池在 Chat Completions、Responses、Messages 或 Gemini 之间切换时，
Chord 会保留已完成工具轮次中可迁移的部分：

- 已完成且成对的调用与结果会尽量转换为目标协议的结构化工具表示；
- 与工具轮次绑定的可见 reasoning（`reasoning_content`、无签名 thinking
  文本、Responses reasoning summary 或 Gemini thought 文本）只有在目标提供
  结构化 reasoning carrier（`openai_visible` 或 `anthropic_unsigned`）时才会
  转换；否则会被丢弃，而不会注入普通 assistant 正文；
- Claude signature、Responses 加密 reasoning、Gemini thought signature 等
  provider 专属 opaque 状态不会被伪造，也不会复制到不兼容协议；
- 若目标拒绝合成后的结构化形状，严格兼容降级会把完整调用/结果历史文本化，
  而不是静默删除。

纯 reasoning-only 历史不会转换为 fallback 文本。这样可以把跨协议上下文
集中在与动作相关的状态上，避免为和工具轮次无关的旧思考链重复付费。

## Grok（xAI）

xAI 推荐通过 Responses API 使用 Grok。Grok 4.7 支持文本和图片输入、
function calling、structured output、reasoning，上下文 500K。xAI
也接受 PDF 附件，以 `input_file` 提供公开 `file_url` 或已上传的 `file_id`
即可，服务端会自动启用 `attachment_search` 工具；但 Chord 发送 PDF 用的是
inline base64 `file_data`，xAI 的 Responses API 不接受非图片的 inline 字节，
所以这里的 `modalities.input` 不声明 `pdf`。Grok 4.7 的 reasoning text 走
`response.reasoning_text.*` 流事件，摘要走 `response.reasoning_summary_text.*`；
Chord 两类都映射到统一 thinking stream，同时保存有序 Responses output item
以延续工具调用状态。Responses API 对 Grok 4.7 总是返回
`reasoning.encrypted_content`，Chord 原样回放这些 reasoning item，模型跨轮
保持思考。

```yaml
model_templates:
  grok-4.7: &grok-4-7
    limit: {context: 500000}
    reasoning:
      effort: high
    variants:
      low:
        reasoning:
          effort: low
      medium:
        reasoning:
          effort: medium
      high:
        reasoning:
          effort: high
      xhigh:
        reasoning:
          effort: xhigh
    modalities: {input: [text, image]}
    cost:
      input: 2
      output: 6
      cache_read: 0.5
      input_tiers:
        - above_input_tokens: 200000
          input: 4
          output: 12
          cache_read: 1

providers:
  xai:
    type: responses
    api_url: https://api.x.ai/v1/responses
    models:
      grok-4.7: *grok-4-7

model_pools:
  default:
    - xai/grok-4.7
```

xAI 只公布了 Grok 4.7 的 500K 总上下文窗口，没有再给出更低的独立模型输出
上限，因此这里省略 `limit.output`，Chord 不会向 xAI 发送 `max_output_tokens`，
由 API 在剩余上下文内安排输出。要显式执行固定上限时，配置 `limit.output`、
提高 Chord 的 `max_output_tokens`，并按共用节打开
`compat.responses.send_max_output_tokens`。

`cost` 块描述 xAI 的整单分档：prompt 一到 200K token，整单所有 token 都按
高档计费，`input_tiers` 把这个规则带进 Chord 的费用统计。

模型 ID 用 `grok-4.7`；想在池引用里固定档位就用 `@low` / `@xhigh`。
Grok 4.6 仍在服务且价格相同，同一份模板只换模型 ID。不要配置
`openai_visible`：xAI Responses 使用原生有序 output / reasoning 状态，而非
Chat Completions 的 `reasoning_content`。`reasoning.effort` 支持 `low`、
`medium`、`high`、`xhigh`（Grok 4.6 及以后；更早的模型把 `xhigh` 当
`high`）；`high` 是默认值，且 reasoning 不可关闭。

### Chat Completions

Grok 4.7 也能走 OpenAI 兼容的 `/v1/chat/completions`，但官方已经把它标成
Responses 的上一代接口，新集成引导去 Responses；这条线路仍保留文档，网关和
既有集成还能用。它同样接受 `reasoning_effort`（`low`、`medium`、`high`
默认、`xhigh`）；reasoning 模型不接受 `stop`、`presence_penalty`、
`frequency_penalty`，`max_tokens` 已弃用，应改用 `max_completion_tokens`。

xAI 自己的 Chat Completions 对 reasoning 模型不回传 reasoning content，
第三方网关也各不相同。一直没有回传时，Chord 回放 assistant tool call 没有
reasoning content 可用，会按「该后端无法回放 reasoning」处理，从出现工具调用
的下一次请求起剥离 `reasoning_effort`，按请求设置的 effort 就只对每个回合的
首个请求生效。想让 effort 和 reasoning 请求覆盖项在整个回合都保持生效，就用
`compat.chat_completions.keep_reasoning_effort: true`：

```yaml
model_templates:
  grok-4.7: &grok-4-7
    limit:
      context: 500000
      output: 64000
    reasoning:
      effort: high
    compat:
      chat_completions:
        keep_reasoning_effort: true
    variants:
      low:
        reasoning:
          effort: low
      medium:
        reasoning:
          effort: medium
      high:
        reasoning:
          effort: high
      xhigh:
        reasoning:
          effort: xhigh
    modalities:
      input: [text, image]

providers:
  grok-gateway:
    type: chat-completions
    api_url: https://example.com/v1/chat/completions
    models:
      grok-4.7: *grok-4-7

model_pools:
  default:
    - grok-gateway/grok-4.7@xhigh
```

`openai_visible` 依然不用配：Grok 不要求回放 `reasoning_content`。缓存命中
取决于粘性路由：xAI 在 `/v1/responses` 和 Chat Completions 上都接受
`prompt_cache_key`，并映射为 `x-grok-conv-id`；网关两者都不透传时，每个请求
都会以缓存未命中重发。

### Grok 4.7 的压缩调优

Grok 4.7 在 200K prompt token 处有一道整单计费线：200K 以下
按 $2 输入 / $0.5 缓存 / $6 输出（每 1M）计费，prompt 达到 200K 则整单按
$4 / $1 / $12 计收。

上面配方都没写 `limit.output`，可用输入预算约 `500000 − 64000 = 436000`。
`threshold` 取 0.4，约 174K 触发，留了余量；用哪个 Grok 模板就加在哪个上，
引用它的 provider 自动继承：

```yaml
model_templates:
  grok-4.7: &grok-4-7
    limit: {context: 500000}
    compaction: {threshold: 0.4, reminder: 0.35}
```

该阈值下的派生值是 0.36，这里显式写 0.35，只是让
压力提示来得稍早一点。

## MiniMax（OpenAI 兼容接口）

```yaml
minimax:
  - "$MINIMAX_API_KEY"
```

OpenAI 兼容端点是 `https://api.minimax.io/v1/chat/completions`。`MiniMax-M3`
是支持多模态的旗舰，窗口 1M token；M2.x 系列（`MiniMax-M2.7`、
`MiniMax-M2.5`、`MiniMax-M2.1`、`MiniMax-M2` 及各自的 `-highspeed` 变体）
只收文本，窗口 204,800 token。M3 默认开启思考（不传 `thinking` 即为
adaptive），M2.x 始终开启、无法关闭；只有 M3 接受
`thinking: {type: disabled}` 跳过思考。

```yaml
model_templates:
  minimax-m3: &minimax-m3
    limit: {context: 1000000}
    modalities: {input: [text, image]}

  minimax-m2x: &minimax-m2x
    limit: {context: 204800}
    modalities: {input: [text]}

providers:
  minimax:
    type: chat-completions
    api_url: https://api.minimax.io/v1/chat/completions
    models:
      MiniMax-M3: *minimax-m3
      MiniMax-M2.7: *minimax-m2x

model_pools:
  default:
    - minimax/MiniMax-M3
```

MiniMax 只公布了上下文窗口，没有公布输出上限，所以模板不写 `limit.output`，
沿用全局上限。M3 还能收视频，但 Chord 的 chat 线路只发送文本和图片。

默认请求形状下，思考内容会带标签写在 assistant 的 `content` 里，官方要求这段
content 完整保留。Chord 会原样回放 assistant content，所以默认形状无需额外
配置；代价是思考会作为普通正文显示，而不是进入 reasoning 展示区。

想让思考走 reasoning 通道，就设置 `reasoning_split: true`：接口会把思考放进
`reasoning_content`，同时返回 `reasoning_details`。Chord 能解析并回放
`reasoning_content`，但没有 `reasoning_details` 的对应实现，而官方要求两者都
完整保留。只有当你的端点接受纯 `reasoning_content` 回放时，才用下面这种形状：

```yaml
# 结构化 reasoning：MiniMax 把思考移到 reasoning_content。
model_templates:
  minimax-m3-split: &minimax-m3-split
    <<: *minimax-m3
    compat:
      request_overrides:
        body:
          reasoning_split: true
      reasoning_continuity:
        mode: openai_visible
        preserve_history: true
```

### MiniMax M3 的压缩调优

MiniMax-M3 在 512K 输入以上费率翻倍：≤512K 按标准价，>512K 按长上下文
价，缓存读同样翻倍。

M3 模板没写 `limit.output`，可用输入预算约 `1000000 − 64000 = 936000`
（`512000 / 936000 ≈ 0.55`）。`threshold` 取 0.5，约 468K 触发，留了余量。
加在已用的 M3 模板上即可：

```yaml
model_templates:
  minimax-m3: &minimax-m3
    limit: {context: 1000000}
    compaction: {threshold: 0.5, reminder: 0.45}
```

该阈值下的派生值是 0.45，显式写出来只是把默认值摆明。
M2.x 系列（204800 窗口）没有长度加价的说法，沿用全局默认。

## 小米 MiMo（OpenAI 兼容接口）

```yaml
mimo:
  - "$MIMO_API_KEY"
```

MiMo-V2.6-Pro 和 MiMo-V2.6-Flash 是小米在 MiMo 开放平台上的全模态模型：
上下文 1,048,576 token，输出上限 131,072（也是 `max_completion_tokens` 的
默认值和上限），支持图片输入、function calling、structured output，思考默认
开启。平台同时提供 OpenAI 与 Anthropic 兼容接口，这里走
`https://api.xiaomimimo.com/v1/chat/completions`：只有 chat 线路文档化了
Chord 需要的 `reasoning_content` 回放契约。

思考模式有一条硬性回放契约：多轮工具调用时，接口要求把之前所有
`reasoning_content` 传回去，缺了会报 `400 - Invalid Format`，所以模板开了
`openai_visible` 加 `preserve_history: true`。思考开关是
`thinking: {type: ...}` 对象，只有模型显式钉住 chat 方言
（`native_thinking: thinking`）时 Chord 才会发这个字段——`mimo-*` 不在 Chord
按模型名推断的名单里。

```yaml
model_templates:
  mimo-v2.6-base: &mimo-v2-6-base
    <<: [*window-1049k-131k, *vision]
    thinking:
      type: enabled
    variants:
      off:
        thinking:
          type: disabled
    compat:
      chat_completions:
        native_thinking: thinking
      reasoning_continuity:
        mode: openai_visible
        preserve_history: true

providers:
  mimo:
    type: chat-completions
    api_url: https://api.xiaomimimo.com/v1/chat/completions
    models:
      mimo-v2.6-pro:
        <<: *mimo-v2-6-base
        cost:
          input: 0.435
          output: 0.87
          cache_read: 0.0036
      mimo-v2.6-flash:
        <<: *mimo-v2-6-base
        cost:
          input: 0.14
          output: 0.28
          cache_read: 0.0028

model_pools:
  default:
    - mimo/mimo-v2.6-pro
```

- `mimo-v2.6-pro` 适合复杂、长程的活；`mimo-v2.6-flash` 更便宜，窗口、模态和
  长度限制相同。`mimo-v2.6-pro-ultraspeed` 是同一模型的加速服务档，平台按
  定制服务售卖。
- 思考默认开启；不需要思考的回合用 `mimo/mimo-v2.6-pro@off`，它会发
  `thinking: {type: disabled}`。平台在思考模式下会把 `temperature` 固定为
  1.0、`top_p` 固定为 0.95，Chord 本来也不发这两个参数。
- `preserve_history: true` 会把已完成回合的思考留在对话里，因为接口要求如此；
  这部分历史每次请求都按输入 token 计费，MiMo 的提示缓存按缓存读价
  （Pro 每 1M $0.0036）吸收。
- 缓存写入目前免费，而 `cache_write` 没法表达“免费”：不写就按输入价估算写入
  部分，缓存多的会话成本估算会略高。
- 价格：Pro 每 1M $0.435 输入 / $0.87 输出 / $0.0036 缓存命中，Flash
  $0.14 / $0.28 / $0.0028，没有长上下文加价。
- 后端会静默丢弃 `auto` 以外的 `tool_choice`；想让 Chord 不再发送强制选择，
  加 `compat.forced_tool_choice: {auto_only: true}`。
- `max_completion_tokens` 同时覆盖可见输出和思考 token，长时间思考会吃掉
  Chord 默认的 64000 输出预算。思考重的活被截断时，提高全局
  `max_output_tokens` 或模型的 `limit.output`（上限 131072）。
- 平台同时支持 `api-key` 头和 bearer 认证；Chord 默认用 bearer，端点或 key
  类型不接受时改 `auth_scheme: api-key`。
- 平台文档没列 `stream_options`。流式请求被拒时，可设
  `compat.chat_completions.send_stream_options: false`；MiMo 的流式 chunk
  自带 usage，用量驱动的自动压缩不受影响。
- 不配 `compaction` 块：MiMo 没有公布长上下文计费档，也没有公布长上下文
  质量区间，跟全局默认走。推导出的输入预算是
  `1048576 − 131072 = 917504`。

验证：

```bash
chord doctor models --model mimo/mimo-v2.6-pro
```

## Meta Muse Spark

```yaml
meta:
  - "$MODEL_API_KEY"
```

Muse Spark 1.3 是 Meta 的 agentic/编程模型，跑在 Meta Model API 上：上下文
1,048,576 token，官方参考配置的输出上限 131,072，输入支持文本、图片和 PDF
（接口还收 video 和 audio，但 Chord 的 Responses wire 发不出去），思考始终
开启，effort 可取 `minimal` / `low` / `medium` / `high` / `xhigh` / `max`。
三个兼容面里要选 Responses：只有它跨轮携带思考，Chord 会把思考作为加密
reasoning item 回放。

```yaml
model_templates:
  muse-spark-1.3: &muse-spark-1-3
    <<: [*window-1049k-131k, *vision-pdf]
    reasoning:
      effort: high
      summary: auto
    variants:
      minimal:
        reasoning:
          effort: minimal
      low:
        reasoning:
          effort: low
      medium:
        reasoning:
          effort: medium
      high:
        reasoning:
          effort: high
      xhigh:
        reasoning:
          effort: xhigh
      max:              # 仅 Standard 档
        reasoning:
          effort: max

providers:
  meta:
    type: responses
    api_url: https://api.meta.ai/v1/responses
    models:
      muse-spark-1.3: *muse-spark-1-3

model_pools:
  default:
    - meta/muse-spark-1.3@xhigh
```

- 默认请求形状不需要 `compat`。Responses provider 本来就会发
  `include: ["reasoning.encrypted_content"]` 加 `store: false`，正是 Meta 推荐
  的 stateless 重放组合；回放 reasoning item 时 Chord 会显式带上 `summary`
  字段，这是 Meta 的硬性要求。`prompt_cache_key` 默认发送且受支持，
  `client_metadata` 则接受后忽略。
- Muse Spark 始终思考，`reasoning.effort: none` 会返回 `HTTP 400`，不要加
  `none` variant。`max` 仅 Standard 档提供。
- 示例池从 `@xhigh` 起步；要拉满思考就换 `@max`，日常想快一点可以降到
  `@medium` 或 `@low`。
- `muse-spark-1.3-contributor` 是同一模型的低价档，代价是允许 Meta 用你的
  prompt 和 completion 训练；能接受这个交换再用，而且该档没有 `max`。
- `limit.output` 取 Meta 参考配置里的 `131072`；想让 Chord 显式执行这个上限，
  按共用节打开 `compat.responses.send_max_output_tokens`。
- Meta 的发布评测显示长上下文检索基本不衰减（MRCR v2 8-needle 在 256K–512K
  是 98.5，512K–1M 是 98.1），没有已知的质量悬崖要压，沿用全局 compaction
  阈值即可。
- 另有 Messages 兼容端点（`https://api.meta.ai/v1/messages`）给 Anthropic
  形态的客户端用；本页只写 Responses 路径。Chat Completions 不跨轮携带思考，
  agentic 场景不推荐。

验证：

```bash
chord doctor models --model meta/muse-spark-1.3@xhigh
```

## 如何验证任意一份配置

复制完配置后，先跑一个定向检查：

```bash
chord doctor models --model provider/model
```

然后再验证你实际要用的 variant，例如：

```bash
chord doctor models --model openai/gpt-5.6-sol@xhigh
chord doctor models --model codex/gpt-5.5@xhigh
chord doctor models --model anthropic/claude-opus-5@high
```

## 按模型调压缩

**按模型调压缩。** 本页每一份 recipe 都是把模型接进 `model_pools` /
`providers` 的接线配置。想按模型分别调整上下文自动压缩，在
模型自身定义或模板上加 `compaction` 块即可（详见[上下文压缩](./context-management_CN.md#上下文压缩compaction)）：

```yaml
model_templates:
  luna-full-window: &luna-full-window
    <<: *window-1050k-128k
    compaction: {threshold: 0.25, reminder: 0.2}   # 把用量留在 272K 长上下文计价档之下

providers:
  openai:
    models:
      gpt-6-luna: *luna-full-window
```

没有 `compaction` 块的模型继承全局 `context.compaction.threshold`；
`reminder` 未设置时按 `threshold` 派生（[推导方式与调参建议](./context-management_CN.md#上下文压缩compaction)）；`reminder: -1`
则只关闭该模型的压力提醒，自动压缩保持开启。
这两个字段调的是 usage-driven 自动压缩路径，**无论是否启用 `model_driven` 都生效**。
本页给出的建议把模型的 `threshold` 调到可靠工作窗口的**上沿**（压缩把
上下文维持在该区间内），需要时可把 `reminder` 设在它下方一点。某模型的
长上下文可靠性没有依据可写时，省略 `compaction` 块、让它用全局默认即可。
