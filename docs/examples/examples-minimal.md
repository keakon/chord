# Minimal

This is the smallest practical personal setup: one provider, one key, and one model pool.

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

Use this when you want to verify provider connectivity, credentials, basic chat flow, and conversation compaction with the smallest working config.
