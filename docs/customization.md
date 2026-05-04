# Customization

Chord supports multiple optional extension points. Start with the basics, then add capabilities gradually.

## Agents

You can override or add role definitions:

- Global: `~/.config/chord/agents/`
- Project: `.chord/agents/`

Supported file formats are `.md` (YAML frontmatter plus Markdown prompt body) and `.yaml` / `.yml` (plain YAML with `prompt` or `system_prompt`).

Common uses:

- Set different model chains for different roles
- Set different permissions for different roles
- Add specialized reviewer, backend, frontend, docs, or other roles

For the full agent schema (fields, examples, and delegation options), see [Configuration & Auth — Agent config](./configuration.md#agent-config).

## Skills

Chord discovers Skills from these directories by default:

- `.chord/skills/`
- `.agents/skills/`
- `~/.config/chord/skills/`
- additional directories configured via `skills.paths`

At runtime, Chord does not preload every skill body into the system prompt. The model calls the `Skill` tool to load matching skill content on demand.

Minimal structure example:

```text
.chord/skills/
└── go-expert/
    └── SKILL.md
```

`SKILL.md` example:

```markdown
---
name: go-expert
description: Go language development expert
---

Follow Effective Go and Go Code Review Comments.
```

## Hooks

Hooks are useful for:

- notifications
- auditing
- automation checks
- tool-result cleanup or interception

Common trigger points include:

- `on_tool_call`
- `on_before_tool_result_append`
- `on_after_llm_call`
- `on_idle`
- `on_tool_batch_complete`

Example:

```yaml
hooks:
  on_idle:
    - name: notify-idle
      command: ["osascript", "-e", "display notification \"Chord is idle\" with title \"Chord\""]
```

## LSP

LSP can return semantic diagnostics after file writes and provide `definition` / `references` / `implementation` capabilities.

Typical config:

```yaml
lsp:
  gopls:
    command: gopls
    file_types: [".go"]
    root_markers: ["go.work", "go.mod", ".git"]
  pyright:
    command: pyright-langserver
    args: ["--stdio"]
    file_types: [".py", ".pyi"]
  typescript:
    command: typescript-language-server
    args: ["--stdio"]
    file_types: [".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs"]
    root_markers: ["tsconfig.json", "jsconfig.json", "package.json", ".git"]
  rust-analyzer:
    command: rust-analyzer
    file_types: [".rs"]
    root_markers: ["Cargo.toml", "rust-project.json"]
```

Availability depends on whether the corresponding language server is installed locally. For Pyright, Chord automatically uses a project-local virtual environment under the LSP root when no Python interpreter is configured. It probes the platform-appropriate layout: `.venv/bin/python`, `venv/bin/python`, and `env/bin/python` on Unix-like systems, including WSL; `.venv\Scripts\python.exe`, `venv\Scripts\python.exe`, and `env\Scripts\python.exe` on Windows. WSL auto-discovery intentionally does not select Windows virtual environments under `Scripts\python.exe`; create a Linux venv inside WSL or configure `python.pythonPath` explicitly if you need a custom interpreter.

Use `root_markers` when a language server should only run inside directories containing specific project markers. If omitted, `file_types` alone controls whether the server handles a file.

For Python, `root_markers` is usually better left unset. In Chord's current LSP model, `root_markers` only decides whether Pyright starts for a file; it does not re-root Pyright to the nearest `pyproject.toml` or `pyrightconfig.json`. Adding Python root markers by default therefore tends to make Pyright unavailable for valid standalone scripts or lightweight projects without improving workspace-root selection. If you need stricter project scoping, add `root_markers` explicitly for your repo.

You usually do not need to set `python.pythonPath` manually. When no interpreter is configured, Chord already auto-detects a project-local `.venv`, `venv`, or `env` under the LSP root. Set `python.pythonPath` only when you need to override that detection with a custom interpreter path. Likewise, `python.analysis` settings are optional tuning knobs for Pyright behavior such as type-checking strictness. Use nested `options` sections for server settings, for example:

```yaml
lsp:
  pyright:
    command: pyright-langserver
    args: ["--stdio"]
    file_types: [".py", ".pyi"]
    options:
      python.analysis:
        typeCheckingMode: strict
```

If you need to override the interpreter explicitly, add `python.pythonPath` under the same nested `options` structure:

```yaml
lsp:
  pyright:
    command: pyright-langserver
    args: ["--stdio"]
    file_types: [".py", ".pyi"]
    options:
      python:
        pythonPath: .venv/bin/python
```

## MCP

MCP is useful for connecting external tools or remote data sources to Chord.

```yaml
mcp:
  exa:
    url: https://mcp.exa.ai/mcp
```

Use `allowed_tools` to expose only selected tools and reduce token overhead. See [Configuration & Auth](./configuration.md#mcp) for details.

In local mode, MCP connects asynchronously after the TUI starts. A brief unavailable state right after startup does not necessarily mean the config is wrong.

## Custom slash commands

You can define project-level or global slash commands in `config.yaml` to wrap common templates or operations as shortcuts.

```yaml
commands:
  /review: "Please review the code changes in the current diff, focusing on correctness and security."
  /commit: "Please generate a concise commit message based on the current staged changes."
```

Type `/review` and press `Enter`; Chord sends the corresponding text as a user message to the model. Custom commands also appear in the `/` autocomplete list.

Suitable uses:

- Standard code review prompts
- Standard commit-message templates
- Team workflow entry points

## Notifications

You can use Hooks or desktop notification config to notify yourself when:

- permission confirmation is required
- a question is waiting for input
- an agent has fully stopped

## Usage recommendations

- Add LSP first, then consider Hooks / MCP
- Start with a minimal working integration before adding complex automation
- Define permissions and failure behavior for each extension

## Related

- [Configuration & Auth](./configuration.md)
- [Permissions & Safety](./permissions-and-safety.md)
- [Troubleshooting](./troubleshooting.md)
