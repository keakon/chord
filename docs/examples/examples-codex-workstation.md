# Codex + LSP

Use this setup when you want:

- OAuth with `preset: codex`
- Go and Python LSP support
- an extra read-only reviewer agent

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
          # Recommended example when you want readable reasoning summaries from
          # OpenAI Responses reasoning models. This is not Chord's implicit default:
          # leave `summary` unset to omit the field and use provider/model behavior.
          summary: auto
        # Optional for OpenAI GPT-5 / Responses API models. Leave unset to use
        # the provider/model default; set low for shorter visible text output or high
        # when you explicitly want more detailed visible output.
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

Codex OAuth credentials are usually written by `chord auth codex`, so you do not normally hand-edit them. They are stored under the active provider name, so with the config above `auth.yaml` typically contains entries like:

```yaml
codex:
  - refresh: "..."
    access: "..."
    expires: 1774009702606
    account_id: acc-1
```

Chord can also start from a refresh-only entry and populate `access`, `expires`, and identity metadata after the first refresh:

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

Two practical points matter here:

- Most models only need `limit.context`: keep total input + output within that window.
- Some GPT models also have a separate input cap. Set `limit.input` for those models so Chord knows when to compact before the prompt is too large; otherwise it derives the input budget from `limit.context` minus effective requested output.
- `limit.output` is the model's own output capacity. Chord still defaults `max_output_tokens` to `32000`, so actual requests use the smaller output limit; changing that request cap does not increase the provider's `272k` input cap.
- Same-named models on different providers are still tried independently in the fallback chain; Chord does not skip them just because the model name matches.

## Credentials to prepare

Run `chord auth codex` for the `codex` provider. If you keep the `fast` OpenAI-compatible provider, also provide an OpenAI API key under `fast` in `auth.yaml` or through the environment reference you choose.

Install optional LSP servers before relying on diagnostics:

```bash
go install golang.org/x/tools/gopls@latest
npm install -g pyright
```

## Verify

```bash
chord doctor models --pool thinking
chord doctor models --pool fast
```

## Common failures

- OAuth opens but never completes: rerun `chord auth codex` in a local browser-capable environment, or use the device-code login path.
- `fast` pool fails while `thinking` works: the OpenAI API key for the `fast` provider is missing or the model name differs on your account.
- LSP indicators stay unavailable: `gopls` or `pyright-langserver` is not installed on `PATH`.
