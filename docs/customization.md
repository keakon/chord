# Customization

Start with the behavior you want to change. You do not need to configure every extension.

| Goal | Extension |
| --- | --- |
| Follow project conventions | [Repository instructions](#repository-instructions) |
| Assign models and permissions by role | [Agents](#agents) |
| Load specialized knowledge or procedures on demand | [Skills](#skills) |
| Automate notifications, checks, or tool-result processing | [Hooks](#hooks) |
| Get code diagnostics, definitions, and references | [LSP](#lsp) |
| Connect external tools | [MCP](#mcp) |
| Save reusable prompts | [Custom commands](#custom-slash-commands) |

## Repository instructions

Add `AGENTS.md` files when a project needs durable instructions for automated agents, such as coding conventions, verification commands, safety rules, or repository-specific review expectations.

At session start, Chord searches from the working directory up to the project root, then loads `AGENTS.md` files from root to working directory. Starting at the project root loads only its root file. Main and sub-agents receive these rules; they do not override higher-priority instructions.

For Python projects, Chord searches the same directories for the nearest valid virtual environment, checking `.venv`, `venv`, then `env` at each level. It asks agents to prefer that interpreter. Working directory, platform, and virtual environment information remain available after compaction, so you do not need to repeat them.

## Agents

You can override or add role definitions:

- Global: `~/.config/chord/agents/`
- Project: `.chord/agents/`

Supported file formats are `.md` (YAML frontmatter plus Markdown prompt body) and `.yaml` / `.yml` (plain YAML with `prompt` or `system_prompt`).

Common uses:

- Set different model chains for different roles
- Set different permissions for different roles
- Add specialized reviewer, backend, frontend, docs, or other roles
- Reuse a built-in role prompt block under your own role name via `prompt_preset`, and extend it with `prompt_append` instead of replacing it

For the full agent schema (fields, examples, and delegation options), see [Configuration & Auth: Agent config](./configuration.md#agent-config).

## Skills

Chord discovers Skills from these directories by default:

- `.chord/skills/`
- `.agents/skills/`
- `~/.config/chord/skills/`
- additional directories configured via `skills.paths`

At runtime, Chord does not preload every skill body into the system prompt. The model calls the `skill` tool to load matching skill content on demand.

In the TUI, the **SKILLS** panel lists discovered skills. A skill turns green only after the `skill` tool successfully loads it during the session. Failed skill loads do not mark the skill as invoked, and unknown (not-discovered) skills are not shown until they are discovered.

Each agent can see and load only the skills its permissions allow. Loading state is separate: a skill loaded by the main agent is not automatically marked as loaded by a sub-agent. Restoring a sub-task recovers its loading history and applies current permissions.

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

A quick example of desktop notification when an agent goes idle:

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
    options:
      staticcheck: true
      analyses:
        minmax: true
        rangeint: true
        slicescontains: true
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

`options` contains language-server workspace settings. For gopls, put settings such as `staticcheck` and the `analyses` map there; `init_options` is sent only as LSP initialization metadata and is not the correct location for gopls settings. Analyzer names and defaults depend on the installed gopls version. Recent gopls releases already enable most `modernize` analyzers by default, while explicit `true` entries document and preserve the checks you rely on and `false` disables an individual analyzer. Chord forwards information and hint diagnostics from gopls after a Go file is changed, but errors and warnings take priority within the default 10-diagnostic output limit.

This edit-time LSP feedback is incremental and does not replace a whole-repository CI gate. If a project adopts the standalone `modernize` command for CI, first clear and review the existing findings, then pin the command version instead of using `@latest`; some suggested fixes, such as changing `omitempty` to `omitzero`, intentionally change serialization behavior and require review.

Availability depends on whether the corresponding language server is installed locally. Chord automatically discovers nested TypeScript/JavaScript projects and Python environments without per-project configuration. For Pyright, when no Python interpreter is configured, it searches from the LSP workspace root upward for the nearest valid `.venv`, `venv`, or `env`, without crossing the Chord project root, and caches the resulting LSP client. It probes `.venv/bin/python`, `venv/bin/python`, and `env/bin/python` on Unix-like systems; Windows uses the corresponding `Scripts/python.exe` paths.

`file_types` controls whether a language server handles a file. Use `root_markers` to override how Chord selects that server's workspace root. TypeScript/JavaScript and Pyright use built-in markers when none are configured; other servers fall back to the Chord project root.

For a matching file, Chord discovers the language-server workspace root as the nearest ancestor directory (at or below the project root) that contains any of the configured `root_markers`; if none matches, it falls back to the project root. The discovery is per file, so servers may be rooted at different directories and the same server name can run one instance per root. This is what lets nested frontend packages (for example a `frontend/` under a repository root that is otherwise a backend project) get a language server rooted at the package, where its `node_modules`, `tsconfig.json`, and other package-local configuration live. A single server name keeps at most 8 live instances; browsing a monorepo with more marker directories than that shuts down the least recently used one, which restarts on the next file read under its root.

When `root_markers` is omitted, TypeScript/JavaScript uses `tsconfig.json`, `jsconfig.json`, and `package.json`; Pyright uses `pyrightconfig.json`, `pyproject.toml`, and `requirements.txt`. Each server ignores the other language's markers. Chord recognizes the server by its configured name or executable basename: `typescript` / `typescript-language-server`, `pyright` / `pyright-langserver`, or `basedpyright` / `basedpyright-langserver`. Windows executable suffixes are supported. For a custom wrapper command, keep a recognized server name or configure `root_markers` explicitly. Discovery always stays at or below the project root. Explicit `root_markers` override these defaults.

You usually do not need to set `python.pythonPath` manually. Set it only when you need to override automatic detection with a custom interpreter path. Likewise, `python.analysis` settings are optional tuning knobs for Pyright behavior such as type-checking strictness. Use nested `options` sections for server settings, for example:

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

MCP servers expose external tools or remote data sources to the model.

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

## Notifications

Hooks or the desktop notification config can alert you when:

- permission confirmation is required
- a question is waiting for input
- an agent has fully stopped

## Related

- [Configuration & Auth](./configuration.md)
- [Permissions & Safety](./permissions-and-safety.md)
- [Troubleshooting](./troubleshooting.md)
