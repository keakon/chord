# Codex + LSP

这个场景适合：

- 用 `preset: codex` 走 OAuth
- 想开 Go / Python LSP
- 想额外放一个只读 reviewer agent

## `~/.config/chord/config.yaml`

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

  fast:
    type: chat-completions
    api_url: https://api.openai.com/v1/chat/completions
    models:
      gpt-5.4-mini:
        limit:
          context: 200000
          input: 183616
          output: 128000

model_pools:
  thinking:
    - codex/gpt-5.5@high
    - codex/gpt-5.5
  fast:
    - fast/gpt-5.4-mini

lsp:
  gopls:
    command: gopls
    file_types: [".go"]
    root_markers: ["go.work", "go.mod", ".git"]
  pyright:
    command: pyright-langserver
    args: ["--stdio"]
    file_types: [".py", ".pyi"]
    options:
      python.analysis:
        typeCheckingMode: standard

context:
  compaction:
    threshold: 0.8
    model_pool: fast
    reserved: 16000

desktop_notification: true
prevent_sleep: true
ime_switch_target: com.apple.keylayout.ABC
log_level: info
```

## `~/.config/chord/auth.yaml`

Codex OAuth 凭据通常直接通过 `chord auth codex` 写入，不必手填。它们会存到当前 provider 名下面，所以按上面的配置，`auth.yaml` 里通常会出现类似：

```yaml
codex:
  - refresh: "..."
    access: "..."
    expires: 1774009702606
    account_id: acc-1
```

Chord 也可以从只包含 refresh 的条目启动，并在首次刷新后补齐 `access`、`expires` 和身份元数据：

```yaml
codex:
  - refresh: "..."
```

## `~/.config/chord/agents/reviewer.md`

```md
---
name: "reviewer"
description: "Read-only code reviewer"
mode: "subagent"
model_pools:
  - thinking
permission:
  "*": deny
  Read: allow
  Grep: allow
  Glob: allow
  Shell:
    "*": allow
    "rm *": deny
    "mv *": deny
    "git add *": deny
    "git commit *": deny
    "git push *": deny
    "sudo *": deny
---
## Role

- Review recent code changes for correctness, risk, and missing verification.
- Stay read-only; do not modify project files.
```

这里最关键的是三点：

- 大多数模型只写 `limit.context` 就够了，也就是保证“输入 + 请求输出”不超过总窗口。
- 某些 GPT 模型还额外有单独的输入上限。这时要配置 `limit.input`，让 Chord 知道何时在 prompt 过大前压缩；否则它会从 `limit.context` 中扣除有效请求输出后推导输入预算。
- `limit.output` 是模型的最大输出能力。Chord 默认 `max_output_tokens` 仍是 `32000`，所以实际请求会取更小的输出上限；修改这个请求上限不会把 provider 的 `272k` 输入上限变大。
- 不同 provider 的同名模型仍会分别参与 fallback；Chord 不会仅因为模型名相同就直接跳过。

## 需要准备的凭据

为 `codex` provider 执行 `chord auth codex`。如果保留 `fast` 这个 OpenAI 兼容 provider，还需要在 `auth.yaml` 中为 `fast` 提供 OpenAI API key，或使用你选择的环境变量引用。

依赖诊断前请先安装可选 LSP：

```bash
go install golang.org/x/tools/gopls@latest
npm install -g pyright
```

## 验证命令

```bash
chord doctor models --pool thinking
chord doctor models --pool fast
```

## 常见失败原因

- OAuth 打开但无法完成：在能打开本地浏览器的环境中重新运行 `chord auth codex`，或改用 device-code 登录路径。
- `thinking` 池正常但 `fast` 池失败：`fast` provider 的 OpenAI API key 缺失，或账号上的模型名不同。
- LSP 状态不可用：`gopls` 或 `pyright-langserver` 未安装到 `PATH`。
