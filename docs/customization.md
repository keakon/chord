# Customization

Start with the behavior you want to change. You do not need to configure every extension.

| Goal | Extension |
| --- | --- |
| Follow project conventions | [Repository instructions](#repository-instructions) |
| Assign models and permissions by role | [Agents](#agents) |
| Load specialized knowledge or procedures on demand | [Skills](#skills) |
| Automate notifications, checks, or tool-result processing | [Hooks](#hooks) |
| Get code diagnostics while editing | [LSP](#lsp) |
| Connect external tools | [MCP](#mcp) |
| Save reusable prompts | [Custom commands](#custom-slash-commands) |

## Repository instructions

Add `AGENTS.md` files when a project needs durable instructions for automated agents, such as coding conventions, verification commands, safety rules, or repository-specific review expectations.

At session start, Chord searches from the working directory up to the project root, then loads `AGENTS.md` files from root to working directory. Starting at the project root initially loads its root file. Successful local reads and searches discover rules along the accessed directory chain. Writes, edits, patches, deletes, and shell execution check their target directories first: if rules have not reached the model yet, the operation stays unexecuted and the model reconsiders it after the rules are saved. Chord never automatically replays that operation. Main and sub-agents maintain independent rule snapshots; deeper rules apply within their subtree and do not override higher-priority instructions.

Shell checks cover its working directory only; Chord does not parse paths or `cd` commands inside shell text. External projects, MCP, and provider-native tools are outside automatic directory discovery. Each directory observation stays fixed for the current binding; resume the session or change its working directory to refresh rules. Compaction preserves the exact active rule text. Unreadable or oversized rules block modifications in affected directories; check the reported path and permissions, or use a larger-context model when the input budget is insufficient.

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

To run a subagent on an external coding-agent process, see [External coding-agent subagents](./external-agents.md).

## Skills

Chord discovers Skills from these directories by default:

- `.chord/skills/`
- `.agents/skills/`
- `~/.config/chord/skills/`
- additional directories configured via `skills.paths`

At runtime, Chord does not preload every skill body into the system prompt. The model calls the `skill` tool to load matching skill content on demand.

In the TUI, the **SKILLS** panel lists discovered skills, one icon per row and no status text. The icon shape carries the state:

| Icon | Meaning |
| --- | --- |
| `○` | idle: model-invocable, body not loaded, no path match yet |
| `◎` | relevant: a project read matched the skill's `paths` and the model has (or is about to get) the name-and-description notice |
| `●` | loaded: the body is live in this agent's context, whether the `skill` tool loaded it or you loaded it with `/skill` |
| `↻` | reload: the body was loaded but compaction archived it; load it again to use it |
| `◌` | manual-only: the model never sees it, only an explicit `/skill` load brings the body in |
| `⊘` | denied: the role's ruleset refuses the skill (selector rows only) |

The icon color is the accent: green for a live body, amber for a body that needs reloading; everything else stays dim. Failed skill loads do not mark the skill as invoked, and unknown (not-discovered) skills are not shown until they are discovered.

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
resources:
  - references/style.md
---

Follow Effective Go and Go Code Review Comments.
```

The `description` is what the model sees in the `Available Skills` list when deciding whether a skill matches, so put the trigger conditions there; Chord truncates a description longer than 1024 characters with a trailing `...`.

### Explicit loads

`/skill <name> [args]` loads a skill on the spot. Chord commits the line as an
ordinary user message and appends the skill body as a `skill` tool result in
the same turn, so the model gets the instructions without deciding to call the
tool itself. Everything after the name is passed through as the skill's
arguments, replacing `${CHORD_SKILL_ARGS}` in the body:

```text
/skill go-expert review the parser package
```

The line follows the focused agent, so a SubAgent can be given a skill the same
way. Bare `/skill` opens the selector instead: it lists every skill the current
agent may load, manual-only ones first, and backfills `/skill <name>` with a
trailing space so you can type arguments. A ruleset-denied skill is shown
disabled with the reason, and an unknown name is refused with a toast.

A synthesized load counts exactly like a model load. It is recorded in the
session, the skill shows as loaded, and resuming the session recovers that
state; durable compaction that archives the tool pair clears it, the same as a
restart.

### Keep a skill out of the model's catalog

`disable-model-invocation: true` in frontmatter keeps a skill out of the
model's catalog: it is missing from the `Available Skills` list and the `skill`
tool listing, and the model cannot load it even when it names it. A role whose
skills are all manual-only does not register the `skill` tool at all. You can
still load such a skill with `/skill <name>`, and the panel marks it with `◌`.
The skill directory's `chord.yaml` sidecar can override the flag in either
direction. Rulesets still apply, so a skill they deny is refused for both sides, and
`chord doctor skills` reports ruleset visibility rather than this flag: a
manual-only skill still shows `visible` there while never reaching the model.

### Declared resources

When a skill body depends on files inside its own directory, list them in the
optional `resources` frontmatter field as paths relative to the skill root.
Only listed entries are checked; prose mentions of other paths are ignored.

- Entries must stay inside the skill root and resolve to regular files.
  Missing entries, escapes (`..`, absolute paths, symlinks pointing outside
  the root), directories, and special files fail the check.
- Empty files warn instead of failing: they read as nothing but are easy to
  miss.
- Resource problems never hide a skill. A skill with missing resources still
  loads and stays visible; the `skill` tool output carries a warning block
  before the body, and the TUI card marks it.
- A doctor report is a point-in-time check, not a guarantee. Runtime warnings
  are re-stat'ed on every load, so a file deleted after the check still warns
  when the skill runs.

Literal `${CHORD_SKILL_DIR}/<path>` references in the body get the same
check as an auxiliary hint and warn at most. Bare relative paths in prose
are not scanned.

Use `paths` to suggest a skill after a successful local `read` matches a project file:

```yaml
paths:
  - "internal/tui/**"
  - "**/*.go"
```

Patterns are case-sensitive, checkout-relative globs, combined with OR. Use `/` separators and `dir/**` for a directory; absolute paths, `..`, negation, and malformed patterns disable automatic discovery for that skill. `chord doctor skills` reports these errors. Omitted or empty effective `paths` means an unconditional skill; `**` waits for any project-file read. A nonempty sidecar `paths` list replaces frontmatter patterns; an omitted or empty sidecar list does not.

Conditional skills stay out of the startup catalog. Matching publishes a bounded name-and-description notice after the tool round closes; it does not load instructions, execute a skill, or grant permissions. The model may still call an allowed skill by name before a match, and `/skill` can explicitly load allowed conditional or manual-only skills. The selector marks states with the same icons as the panel: `◎` for a path match or a recorded notice, `●` for an available body, and `↻` for a body compaction archived.

Only successful local text reads trigger discovery, including partial reads. Shell, grep, glob, image/PDF tools, MCP, and provider-native tools do not. Symlinks match their real target inside the current checkout. Discovery is independent for each agent; compaction carries activation metadata and may publish a short reminder, without restoring skill bodies automatically.

## Diagnose skills

`chord doctor skills` reports why a configured skill never reaches the model.
It reuses the runtime discovery order and parser, but keeps invalid and
shadowed files as their own rows instead of skipping them silently. Add
`--json` for a machine-readable report.

Each row carries four independent dimensions:

- `integrity`: `passed` means the runtime keeps the file; `failed` means it
  is skipped (bad YAML, missing `name`/`description`, unreadable file).
- `load`: whether the body reads back (`passed`/`failed`/`not_run`).
- `visibility`: whether the `builder` ruleset hides the skill
  (`visible`/`denied`/`not_checked`). This dimension covers the ruleset only:
  `disable-model-invocation` is not part of it, so a manual-only skill still
  reports `visible`.
- `resources`: health of declared resources
  (`passed`/`failed`/`warning`/`none`).
- `shadowed`: a same-name skill from a higher-priority directory wins.

Exit codes follow the `doctor` family: `1` when any skill fails integrity or
load, `2` when the check itself cannot run. That covers unreadable config and
scan problems: an unreadable directory, a broken symlink, or a scan path that
is not a directory. Scan problems are listed in the report (`scan_issues` in
`--json`) and the report is still printed. Denied skills, shadowed duplicates,
resource problems, and an empty skill set do not change the exit code. Pass
`--strict` to also fail when the ruleset is unavailable or a check did not
run. Loading and reading a skill says nothing about whether the model will
pick it.

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

LSP returns semantic diagnostics after file writes.

Typical config:

```yaml
lsp:
  gopls:
    command: gopls
    file_types: [".go"]
    root_markers: ["go.work", "go.mod", ".git"]
    options:
      gopls:
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

`options` holds the workspace settings Chord returns for the server's `workspace/configuration` requests. Keys are section names, so gopls settings such as `staticcheck` and the `analyses` map belong under a `gopls` key, while Pyright settings use `python` / `python.analysis`. A flat top-level map is not delivered: when a section has no matching key, Chord answers with an empty object. `init_options` is sent only as LSP initialization metadata and is not the correct location for gopls settings. Analyzer names and defaults depend on the installed gopls version. Recent gopls releases already enable most `modernize` analyzers by default, while explicit `true` entries document and preserve the checks you rely on and `false` disables an individual analyzer. Chord forwards information and hint diagnostics from gopls after a Go file is changed, but errors and warnings take priority within the default 10-diagnostic output limit.

gopls settings form one flat map. The gopls documentation groups them under names such as `build` and `ui`, but those group names are not keys: write `directoryFilters` directly under `gopls` (the dotted `build.directoryFilters` is accepted too), because gopls rejects a nested `build:` map as an unknown setting. `directoryFilters` limits the directories gopls loads packages from, which helps a workspace with large subtrees that hold no Go code, such as a frontend `web/` directory. A configured list replaces the default `["-**/node_modules"]`, so keep that entry; directories whose names start with `.` or `_` are skipped anyway. The filters do not apply to gopls's own file watching (`fileWatcher`). Leave it at its default `off`: `fsnotify` watches every workspace directory and can exhaust file descriptors on macOS.

```yaml
lsp:
  gopls:
    command: gopls
    file_types: [".go"]
    options:
      gopls:
        staticcheck: true
        analyses:
          ST1000: false
```

Disabling one noisy analyzer does not require giving up staticcheck: keep `staticcheck: true` and set that analyzer to `false` under `analyses`. `ST1000: false`, for example, drops staticcheck's package-comment warnings while every other staticcheck check keeps running, whereas `staticcheck: false` disables the whole staticcheck set. gopls v0.23 has no `checks` option (`gopls api-json` lists the settings the installed version accepts), so the `checks: ["all", "-ST1000"]` form seen in older examples does nothing; the per-analyzer entry is the switch that works. Chord reads these settings when it starts a language server, so restart Chord after editing them.

gopls also caches analyzer results on disk, and every project that uses the same gopls binary shares that cache: a restarted language server reuses the results for unchanged packages and only recomputes the packages you edited plus their dependents. `maxFileCacheBytes` tunes that cache's soft limit (default 1 GB; entries unused for five days are dropped regardless of the limit), keeping more results warm across restarts; `GOMAXPROCS` under `env` caps the analyzer parallelism behind the short multi-core CPU bursts after an edit, trading a longer analysis wall time for a lower peak.

```yaml
lsp:
  gopls:
    command: gopls
    file_types: [".go"]
    env:
      GOMAXPROCS: "4"
    options:
      gopls:
        maxFileCacheBytes: 4000000000
```

Entries under `env` are appended to the language server process's inherited environment. One optional entry matters for gopls daemon sharing: with `-remote=auto`, gopls locates the daemon socket through `XDG_RUNTIME_DIR` and falls back to the session's temp directory, so sessions launched with different `TMPDIR` settings get separate daemons, and a recycled temp directory leaves the running daemon unreachable. Setting `XDG_RUNTIME_DIR` for gopls to an existing per-user directory (on Linux, `/run/user/<uid>`) keeps one daemon across such sessions; Windows does not read this variable and uses a TCP loopback address for an auto-started daemon.

This edit-time LSP feedback is incremental and does not replace a whole-repository CI gate. If a project adopts the standalone `modernize` command for CI, first clear and review the existing findings, then pin the command version instead of using `@latest`; some suggested fixes, such as changing `omitempty` to `omitzero`, intentionally change serialization behavior and require review.

Availability depends on whether the corresponding language server is installed locally. Chord automatically discovers nested TypeScript/JavaScript projects and Python environments without per-project configuration. For Pyright, when no Python interpreter is configured, it searches from the LSP workspace root upward for the nearest valid `.venv`, `venv`, or `env`, without crossing the file’s repository or workspace boundary, and caches the resulting LSP client. It probes `.venv/bin/python`, `venv/bin/python`, and `env/bin/python` on Unix-like systems; Windows uses the corresponding `Scripts/python.exe` paths.

TypeScript servers additionally need a TypeScript installation they can run: `typescript-language-server` loads `lib/tsserver.js` from the workspace's `node_modules` and falls back to a global installation only when the workspace has none. When neither provides it, the server fails at `initialize`; Chord reports that server as failed (red dot in the info panel) and writes the reason to the log. Two ways to give it one:

- Install the project's dependencies (`pnpm install`, `npm install`, ...) so the server uses the workspace's own TypeScript, keeping diagnostics aligned with the compiler the project builds with.
- Or point the server at another installation with `init_options.tsserver.fallbackPath`, which applies only when the workspace has no usable TypeScript (`tsserver.path` always wins instead):

```yaml
lsp:
  typescript:
    command: typescript-language-server
    args: ["--stdio"]
    file_types: [".ts", ".tsx", ".js", ".jsx"]
    init_options:
      tsserver:
        # .../lib/tsserver.js of an installation that ships tsserver.js
        fallbackPath: /path/to/typescript/lib/tsserver.js
```

TypeScript 7 ships without `lib/tsserver.js` (the language service moved into the native compiler), so a workspace pinned to TypeScript 7 needs a `fallbackPath` pointing at a release that still provides it (6.x or earlier), and its diagnostics then come from that older compiler. Chord logs the TypeScript version each TypeScript server loaded, along with any warning the server reports about it.

`file_types` controls which files a language server handles. `root_markers` selects its workspace root. Without an explicit override, gopls uses `go.work` and `go.mod`; TypeScript/JavaScript uses `tsconfig.json`, `jsconfig.json`, and `package.json`; Pyright uses `pyrightconfig.json`, `pyproject.toml`, and `requirements.txt`.

Chord searches upward from the edited file for the nearest matching marker. Discovery stops at the containing Git checkout or a working directory bound to the session, using that boundary when no marker matches. External projects without Git need a matching root marker; otherwise LSP is skipped.

Edits in another worktree or repository, such as `../companion`, start a language server rooted in that project without changing the session’s working directory. All instances use the current session’s LSP configuration; Chord does not load another project’s Chord configuration automatically. Each root has its own instance. Tool results include diagnostics only from the instances serving that edit, and the sidebar shows directory names for external or multiple workspaces.

Switching the working directory preserves other workspaces’ instances and diagnostics. Each server name keeps at most 8 live instances, evicting the least recently used instance when necessary and restarting it on demand. Background maintenance closes least recently used documents when an instance exceeds 256 open documents, and also closes idle documents. Instances with no open documents are released after sustained inactivity and restarted on a later edit. Background maintenance also reclaims instances whose directories have disappeared.

For open files and files with diagnostic records, Chord monitors external edits, atomic replacements, and deletions, with periodic reconciliation for missed events. A changed file loses its stale in-memory document and diagnostics so the language server can read the current disk contents. Deletion clears its diagnostics and sidebar counts while preserving session history. Monitoring covers relevant files and directories in active workspaces without recursively scanning the repository or adding user-facing notices.

Chord recognizes servers by their configured name or executable basename: `gopls`, `typescript` / `typescript-language-server`, `pyright` / `pyright-langserver`, or `basedpyright` / `basedpyright-langserver`. Windows executable suffixes are supported. For custom wrapper commands, retain a recognized server name or configure `root_markers` explicitly.

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
