# 模型配置速查

当你已经确定要用哪一类 provider / model，只想要一段可复制的起始配置时，用这一页。字段语义和完整 schema 仍以[配置与认证](./configuration_CN.md)为准；完整的多文件工作站 / 团队布局示例见[配置示例](./examples/index_CN.md)。

## OpenAI Responses 兼容接口：GPT-5.4 / GPT-5.5 / GPT-5.6

Codex OAuth、Responses 中转和其他兼容 provider 都复用这里的模型限制。
使用 API key 的 provider 需要在 `~/.config/chord/auth.yaml` 中配置同名条目：

```yaml
openai:
  - "$OPENAI_API_KEY"
```

### GPT-5.4

```yaml
providers:
  openai:
    type: responses
    api_url: https://api.openai.com/v1/responses
    supported_service_tiers: [fast, slow]
    models:
      gpt-5.4:
        limit:
          context: 1050000
          input: 950000
          output: 128000
        cost:
          input: 2.5
          output: 15
          cache_read: 0.25
        reasoning:
          summary: auto
        variants:
          high:
            reasoning:
              effort: high
          xhigh:
            reasoning:
              effort: xhigh
        modalities:
          input: [text, image, pdf]

model_pools:
  default:
    - openai/gpt-5.4@high
```

验证：

```bash
chord doctor models --model openai/gpt-5.4@high
```

### GPT-5.5

```yaml
providers:
  openai:
    type: responses
    api_url: https://api.openai.com/v1/responses
    supported_service_tiers: [fast, slow]
    models:
      gpt-5.5:
        limit:
          context: 400000
          input: 272000
          output: 128000
        cost:
          input: 5
          output: 30
          cache_read: 0.5
        reasoning:
          summary: auto
        variants:
          high:
            reasoning:
              effort: high
          xhigh:
            reasoning:
              effort: xhigh
        modalities:
          input: [text, image, pdf]

model_pools:
  default:
    - openai/gpt-5.5@high
```

验证：

```bash
chord doctor models --model openai/gpt-5.5@high
```

### GPT-5.6 alias（`gpt-5.6` → Sol）

```yaml
providers:
  openai:
    type: responses
    api_url: https://api.openai.com/v1/responses
    models:
      gpt-5.6:
        limit:
          context: 500000
          input: 372000
          output: 128000
        cost:
          input: 5
          output: 30
          cache_read: 0.5
          cache_write: 6.25
        reasoning:
          effort: medium
          summary: auto
        variants:
          low:
            reasoning:
              effort: low
          high:
            reasoning:
              effort: high
          xhigh:
            reasoning:
              effort: xhigh
          max:
            reasoning:
              effort: max
        modalities:
          input: [text, image]

model_pools:
  default:
    - openai/gpt-5.6@high
```

如果你要固定价格 / 行为，直接改用明确模型 ID：

### GPT-5.6 Sol

```yaml
providers:
  openai:
    type: responses
    api_url: https://api.openai.com/v1/responses
    models:
      gpt-5.6-sol:
        limit:
          context: 500000
          input: 372000
          output: 128000
        cost:
          input: 5
          output: 30
          cache_read: 0.5
          cache_write: 6.25
        reasoning:
          effort: medium
          summary: auto
        variants:
          max:
            reasoning:
              effort: max
        modalities:
          input: [text, image]
```

### GPT-5.6 Terra

```yaml
providers:
  openai:
    type: responses
    api_url: https://api.openai.com/v1/responses
    models:
      gpt-5.6-terra:
        limit:
          context: 500000
          input: 372000
          output: 128000
        cost:
          input: 2.5
          output: 15
          cache_read: 0.25
          cache_write: 3.125
        reasoning:
          effort: medium
          summary: auto
        variants:
          max:
            reasoning:
              effort: max
        modalities:
          input: [text, image]
```

### GPT-5.6 Luna

```yaml
providers:
  openai:
    type: responses
    api_url: https://api.openai.com/v1/responses
    models:
      gpt-5.6-luna:
        limit:
          context: 500000
          input: 372000
          output: 128000
        cost:
          input: 1
          output: 6
          cache_read: 0.1
          cache_write: 1.25
        reasoning:
          effort: medium
          summary: auto
        variants:
          max:
            reasoning:
              effort: max
        modalities:
          input: [text, image]
```

要点：

- `gpt-5.6` 当前会解析到 Sol，因此它的 `cost` 应按 Sol 费率填写。
- GPT-5.6 API 可用的 reasoning effort 包括 `none`、`low`、`medium`、`high`、`xhigh`、`max`。
- `reasoning.summary: auto` 是可选项；如果不希望 Chord 显式请求可读 reasoning 摘要，可以留空不写。
- Chord 当前尚未暴露 GPT-5.6 的 `reasoning.mode: pro`。

验证：

```bash
chord doctor models --model openai/gpt-5.6@max
```

## Codex OAuth preset

当你要使用 ChatGPT/Codex OAuth，而不是 API key 时，用这个配置。模型限制
与上面的 GPT-5.x 示例相同，只有 provider preset 和认证方式不同。

本页统一使用以下 GPT-5.x 限制：

| 模型 | `limit.context` | `limit.input` | `limit.output` |
| --- | ---: | ---: | ---: |
| GPT-5.4 | 1,050,000 | 950,000 | 128,000 |
| GPT-5.5 | 400,000 | 272,000 | 128,000 |
| GPT-5.6 Sol / Terra / Luna | 500,000 | 372,000 | 128,000 |

三个字段都要保留：`context` 表示 Codex 开放的输入加输出总窗口，`input`
和 `output` 则是其中各自独立的硬上限。两个独立上限不必相加等于
`context`；输入接近上限时，留给输出的空间自然会变少。

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
        variants:
          high:
            reasoning:
              effort: high
          xhigh:
            reasoning:
              effort: xhigh
          max:
            reasoning:
              effort: max
      gpt-5.4:
        limit:
          context: 1050000
          input: 950000
          output: 128000
      gpt-5.6-sol:
        limit:
          context: 500000
          input: 372000
          output: 128000

model_pools:
  default:
    - codex/gpt-5.5@high
```

登录：

```bash
chord auth codex
```

要点：

- 同时使用 API key 和 Codex OAuth 时，因为凭据不同，应保留两个 provider，但复用同一组模型限制。
- GPT-5.4 使用 `1050000 / 950000 / 128000`，分别对应 1.05M 总窗口、Codex 的 95% 有效输入预算和模型最大输出。
- `gpt-5.6-sol`、`gpt-5.6-terra`、`gpt-5.6-luna` 都使用 `500000 / 372000 / 128000`。
- 这些数值跟随当前 Codex 模型目录，未来 Codex 版本可能调整。后端配额变化时，要同时更新三个字段。

## Anthropic Claude

在 `~/.config/chord/auth.yaml` 中配置：

```yaml
anthropic:
  - "$ANTHROPIC_API_KEY"
```

```yaml
providers:
  anthropic:
    type: messages
    api_url: https://api.anthropic.com/v1/messages
    models:
      claude-opus-4.8:
        limit:
          context: 1000000
          output: 128000
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
        modalities:
          input: [text, image, pdf]

model_pools:
  default:
    - anthropic/claude-opus-4.8@high
```

如果想要更低成本的 Claude 配置，可沿用同样结构，改为 `claude-sonnet-4.6`、`output: 64000`，并按你的账号 / provider 文档填写 Sonnet 费率。

## Google Gemini

在 `~/.config/chord/auth.yaml` 中配置：

```yaml
gemini:
  - "$GEMINI_API_KEY"
```

```yaml
providers:
  gemini:
    api_url: https://generativelanguage.googleapis.com/v1beta/models
    models:
      gemini-3.5-flash:
        limit:
          context: 1048576
          output: 65536
        modalities:
          input: [text, image, pdf]
        thinking:
          budget: -1
          level: high

model_pools:
  default:
    - gemini/gemini-3.5-flash
```

要点：

- `api_url` 保持在 `/models` 基础路径即可；Chord 会自动追加 `/{model}:streamGenerateContent?alt=sse`。
- `type` 可以省略；Chord 会根据 `/models` 路径自动识别 Gemini。

## GLM / BigModel Coding Plan

在 `~/.config/chord/auth.yaml` 中配置：

```yaml
bigmodel:
  - "$BIGMODEL_API_KEY"
```

```yaml
providers:
  bigmodel:
    type: chat-completions
    api_url: https://open.bigmodel.cn/api/coding/paas/v4/chat/completions
    models:
      glm-5.2:
        limit:
          context: 1000000
          output: 64000
        reasoning:
          effort: high

model_pools:
  default:
    - bigmodel/glm-5.2
```

要点：

- GLM-5.2 的 OpenAI-compatible 和 Anthropic-compatible 模板应分开维护。
- 这里给的是 `type: chat-completions` 模板；只有你的网关明确暴露了 Responses endpoint 时，才改为 `type: responses`。

## DeepSeek / OpenAI-compatible chat-completions

在 `~/.config/chord/auth.yaml` 中配置：

```yaml
deepseek:
  - "$DEEPSEEK_API_KEY"
```

```yaml
providers:
  deepseek:
    type: chat-completions
    api_url: https://api.deepseek.com/chat/completions
    models:
      deepseek-reasoner:
        limit:
          context: 128000
          output: 8192
        reasoning:
          effort: high

model_pools:
  default:
    - deepseek/deepseek-reasoner
```

要点：

- 对 OpenAI-compatible 网关，请使用该网关 / 账号实际公开的模型 ID 和限制。
- 一些 thinking 模式的 OpenAI-compatible provider 会要求在工具循环中保留可见 reasoning 连续性。见[常见问题排查 — DeepSeek / OpenAI 兼容 thinking 模式 400](./troubleshooting_CN.md#deepseek--openai-兼容-thinking-模式-400)。

## 如何验证任意一份配置

复制完配置后，先跑一个定向检查：

```bash
chord doctor models --model provider/model
```

然后再验证你实际要用的 variant，例如：

```bash
chord doctor models --model openai/gpt-5.6@max
chord doctor models --model codex/gpt-5.5@max
chord doctor models --model anthropic/claude-opus-4.8@high
```
