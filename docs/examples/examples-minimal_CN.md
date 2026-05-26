# 最小可用

这是最小可用的单人配置：一个 provider、一个 key、一个模型池。

## `~/.config/chord/auth.yaml`

```yaml
anthropic:
  - "$ANTHROPIC_API_KEY"
```

## `~/.config/chord/config.yaml`

```yaml
providers:
  anthropic:
    type: messages
    api_url: https://api.anthropic.com/v1/messages
    models:
      claude-opus-4.7:
        limit:
          context: 1000000
          input: 1000000
          output: 128000
        thinking:
          type: adaptive
          effort: medium
          display: summarized
        modalities:
          input: [text, image]

model_pools:
  default:
    - anthropic/claude-opus-4.7

context:
  compaction:
    threshold: 0.8
desktop_notification: true
log_level: info
```

适合先确认：provider 能连通、凭据没问题、基本对话和自动压缩都正常。

## 需要准备的凭据

在 shell 中设置 `ANTHROPIC_API_KEY`，或把 `auth.yaml` 里的环境变量引用替换为真实 key；不要把真实 key 提交到仓库。

## 验证命令

```bash
chord doctor models --model anthropic/claude-opus-4.7
```

## 常见失败原因

- `401` / `403`：key 缺失、过期，或 Chord 运行环境没有读到该环境变量。
- `404` / model not found：当前 Anthropic 账号未启用这个模型名。
- 上下文长度错误：按 provider 账号实际公开限制更新 `limit.context`、`limit.input`、`limit.output`。
