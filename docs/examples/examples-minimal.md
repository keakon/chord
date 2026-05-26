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

Use this when you want to verify provider connectivity, credentials, basic chat flow, and conversation compaction with the smallest working config.

## Credentials to prepare

Set `ANTHROPIC_API_KEY` in your shell or replace the environment-variable reference in `auth.yaml` with a real key stored outside the repository.

## Verify

```bash
chord doctor models --model anthropic/claude-opus-4.7
```

## Common failures

- `401` / `403`: the key is missing, expired, or not visible in the environment where Chord runs.
- `404` / model not found: the model name is not enabled on your Anthropic account.
- Context-limit errors: update `limit.context`, `limit.input`, and `limit.output` to match your provider account's published limits.
