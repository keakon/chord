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
  compaction:
    threshold: 0.85
    model_pool: small

log_level: info
```

需要全局代理的话，再额外加：

```yaml
proxy: socks5://127.0.0.1:1080
```

这个示例的重点不是 agent，而是 provider / key / pool 的故障转移链。

如果你的 OpenAI 兼容后端支持 provider 侧 thinking / reasoning（例如 thinking 模式下的 DeepSeek），部分 provider 会要求把上一轮工具调用中的 thinking/reasoning 内容再次带到下一次请求里；如果这个要求没有满足，provider 可能会直接返回 400 错误。切换到不同协议家族的模型时，Chord 会按目标 provider 规范化这部分隐藏状态，而不会原样回放不兼容的 thinking/reasoning 字段。

## 需要准备的凭据

在 Chord 运行环境中设置 `auth.yaml` 引用的网关密钥（`PRIMARY_KEY_A`、`PRIMARY_KEY_B`、`PRIMARY_KEY_C`、`BACKUP_KEY`），或替换为你自己的仓库外 secret 引用。

## 验证命令

```bash
chord doctor models --pool big
chord doctor models --pool small
```

## 常见失败原因

- `401` / `403`：某个网关 key 缺失、过期，或无权访问请求的模型。
- `404` / model not found：网关暴露的模型 ID 与示例不同。
- 始终没有 fallback 到 backup：primary endpoint 返回的是“成功 HTTP 响应中的 provider 错误”，而不是可重试的传输/provider 故障。
