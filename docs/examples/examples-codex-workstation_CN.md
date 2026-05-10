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
          output: 32000
        reasoning:
          summary: auto
        text:
          verbosity: medium
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
          output: 16384

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
  auto_compact: true
  compact_threshold: 0.8
  compact_model: fast/gpt-5.4-mini
  compaction:
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

- split-limit 模型要把 `limit.input` 配出来，自动压缩和 oversize 恢复才会按输入预算工作。
- 降低 `max_output_tokens` 或模型 `limit.output` 可以控成本，但不会把 `272k` 的输入上限变大。
- 不同 provider 的同名模型仍会分别参与 fallback；Chord 不会仅因为模型名相同就直接跳过。
