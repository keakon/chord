# Customization

Chord supports multiple optional extension points. Start with the basics, then add capabilities gradually.

## Repository instructions

Add `AGENTS.md` files when a project needs durable instructions for automated agents, such as coding conventions, verification commands, safety rules, or repository-specific review expectations.

At session start, Chord discovers applicable `AGENTS.md` files by walking from the current working directory up to the project root. It then injects the complete non-empty contents in project-root-to-current-working-directory order, with each section labeled by its path relative to the current working directory (for example, `## ../../AGENTS.md`, `## ../AGENTS.md`, and `## AGENTS.md` when running from a nested directory). If the current working directory is the project root, only the root `AGENTS.md` is loaded. The instructions are sent in the LLM request as an internal user-role message before the first real user message. AGENTS.md content is delivered under a self-identifying header: a first line of `# AGENTS.md instructions` followed by an `<INSTRUCTIONS> ... </INSTRUCTIONS>` block. This meta message may not appear in the visible transcript, but main and sub-agents treat it as durable workspace guidance unless it conflicts with higher-priority system, developer, or user instructions.

Chord also discovers one Python virtual environment for the session by walking from the current working directory upward to the project root and checking `.venv`, `venv`, then `env` at each level. When the first valid environment is found, the prompt shows its path relative to the current working directory and asks agents to prefer that interpreter for Python commands.

The system prompt is fully static: it contains identity, guidelines, and capabilities but no dynamic fields. Working directory, platform, current date, and the detected virtual environment path are delivered via the same session-context meta user message that carries AGENTS.md content — injected before the first real user message, not embedded in the cached system prefix. This keeps the system prompt identical across sessions, days, and working directories, maximizing prefix-cache reuse. The session-context meta message is re-injected after context compaction so the environment is never lost.

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

At runtime, Chord does not preload every skill body into the system prompt. The model calls the `skill` tool to load matching skill content on demand.

In the TUI, the **SKILLS** panel lists discovered skills. A skill turns green only after the `skill` tool successfully loads it during the session. Failed skill loads do not mark the skill as invoked, and unknown (not-discovered) skills are not shown until they are discovered.

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

Hooks let you run external commands at well-defined runtime points (before a tool call, after an LLM call, on idle, on tool-batch complete, etc.) for notifications, auditing, automation checks, or tool-result cleanup.

Quick example — desktop notification when an agent goes idle:

```yaml
hooks:
  on_idle:
    - name: notify-idle
      command: ["osascript", "-e", "display notification \"Chord is idle\" with title \"Chord\""]
```

For the full list of trigger points (14 in total), the JSON envelope contract, sync vs automation vs observer categories, and richer examples, see the dedicated [Hooks](./hooks.md) page.

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

In local mode, MCP connects asynchronously after the TUI starts. Auto-start servers still start in the background, but the first LLM request waits until they either connect successfully or reach a terminal failure state.

Use `manual: true` for MCP servers you do not need in every conversation. The server stays disabled at startup, Chord does not connect to it, and its tool descriptions are not added to the default LLM tool context, reducing everyday context overhead. When you need it, enable it manually with `/mcp` (menu) or `/mcp enable <server>`.

In the TUI, press `Ctrl+O` to open the MCP selector. It can be opened while the agent is running to inspect server state and toggle manual servers. Changes made during a running turn apply to the next model request, so the current in-flight request keeps the tool surface and prompt it started with.

Only `manual: true` servers can be changed at runtime. Auto-start servers remain part of the default tool context, stay read-only, and are not affected by `/mcp enable|disable`.

## Custom slash commands

You can define project-level or global slash commands in `config.yaml` to wrap common templates or operations as shortcuts.

```yaml
commands:
  /review: "Please review the code changes in the current diff, focusing on correctness and security."
  /commit: "Please generate a concise commit message based on the current staged changes."
```

Type `/review`, accept the autocomplete with `Tab` or `Enter` if it is shown, then press `Enter`; Chord sends the corresponding text as a user message to the model. Custom commands also appear in the `/` autocomplete list.

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
