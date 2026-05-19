# OpenAI-compatible gateway

Use this pattern when you sit behind an OpenAI-compatible gateway and want:

- multiple keys under a provider for rotation or failover
- model-pool fallback from a primary endpoint to a backup endpoint

## `~/.config/chord/auth.yaml`

```yaml
primary:
  - "$PRIMARY_KEY_A"
  - "$PRIMARY_KEY_B"
  - "$PRIMARY_KEY_C"
backup:
  - "$BACKUP_KEY"
```

## `~/.config/chord/config.yaml`

```yaml
providers:
  primary:
    type: chat-completions
    api_url: https://gateway.example.com/v1/chat/completions
    models:
      llama3-70b: &big
        limit:
          context: 128000
          input: 128000
          output: 32768
      llama3-8b: &small
        limit:
          context: 32000
          input: 32000
          output: 8000

  backup:
    type: chat-completions
    api_url: https://backup.example.org/v1/chat/completions
    models:
      llama3-70b: *big
      llama3-8b: *small

model_pools:
  big:
    - primary/llama3-70b
    - backup/llama3-70b
  small:
    - primary/llama3-8b
    - backup/llama3-8b

context:
  compaction:
    threshold: 0.85
    model_pool: small

log_level: info
```

If the gateway itself sits behind a proxy, add this separately:

```yaml
proxy: socks5://127.0.0.1:1080
```

The main point of this example is the provider / key / pool failover chain, not custom agents.

If your OpenAI-compatible backend supports provider-side thinking / reasoning (for example DeepSeek in thinking mode), some providers require the previous tool round's thinking/reasoning content to be included again in the next request. If that requirement is not met, the provider may reject the request with a 400 error. When you switch to a model with a different protocol family, Chord normalizes that hidden state for the target provider instead of replaying incompatible thinking/reasoning fields unchanged.
