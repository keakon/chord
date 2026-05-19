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
