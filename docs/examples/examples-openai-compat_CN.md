# OpenAI 兼容网关

这个场景适合挂在 OpenAI 兼容网关前：

- 一个 provider 下放多个 key，做轮询或故障切换
- 模型池里先走主 endpoint，再回退到备用 endpoint

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
  auto_compact: true
  compact_threshold: 0.85
  compact_model: primary/llama3-8b

log_level: info
```

需要全局代理的话，再额外加：

```yaml
proxy: socks5://127.0.0.1:1080
```

这个示例的重点不是 agent，而是 provider / key / pool 的故障转移链。
