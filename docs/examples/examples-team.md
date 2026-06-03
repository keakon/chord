# Team setup

This page shows a **shared project layout** under `.chord/`:

- keep personal credentials and default providers in global config
- put team-shared hooks, LSP, commands, and defaults in project-level `.chord/config.yaml`
- let the primary role orchestrate work instead of editing everything directly
- split sub-agents into read-only discovery, mechanical execution, and review

## `~/.config/chord/config.yaml`

```yaml
providers:
  anthropic:
    type: messages
    api_url: https://api.anthropic.com/v1/messages
    models:
      claude-opus-4.8:
        limit:
          context: 1000000
          input: 1000000
          output: 128000
        thinking:
          type: adaptive
          effort: medium
          display: summarized

model_pools:
  thinking:
    - anthropic/claude-opus-4.8
  fast:
    - anthropic/claude-opus-4.8

context:
  compaction:
    threshold: 0.8
desktop_notification: true
log_level: info
```

## `~/.config/chord/auth.yaml`

```yaml
anthropic:
  - "$ANTHROPIC_API_KEY"
```

## `<repo>/.chord/config.yaml`

```yaml
context:
  compaction:
    model_pool: fast
    reserved: 16000

hooks:
  on_tool_call:
    - name: audit-shell
      tools: ["shell"]
      command: ["./scripts/chord-hooks/audit-shell.sh"]
      timeout: 5

  on_tool_batch_complete:
    - name: golangci-lint
      tools: ["edit", "write", "delete"]
      paths: ["**/*.go"]
      min_changed_files: 1
      command: ["./scripts/chord-hooks/run-golangci-lint.sh"]
      result: append_on_failure
      result_format: tail
      max_result_lines: 80
      join: before_next_llm

  on_before_tool_result_append:
    - name: redact-keys
      tools: ["shell", "web_fetch", "read"]
      command: ["./scripts/chord-hooks/redact-keys.sh"]
      timeout: 3

lsp:
  gopls:
    command: gopls
    file_types: [".go"]
    root_markers: ["go.work", "go.mod", ".git"]

commands:
  /review: |
    Review the staged diff for correctness, security, and style.
    Highlight the most important issues first.
  /commit: |
    Generate a Conventional Commit message from the staged diff.
```

## `<repo>/.chord/agents/orchestrator.md`

```md
---
name: "orchestrator"
description: "Primary agent for multi-file work — plans, delegates, and synthesizes results."
mode: "main"
model_pools:
  - thinking
permission:
  "*": allow
  handoff: deny
  delete: ask
  shell:
    "sudo *": ask
    "rm *": ask
    "rmdir *": ask
    "mv *": ask
    "git add *": ask
    "git checkout *": ask
    "git clean *": ask
    "git commit *": ask
    "git push *": ask
    "git reset *": ask
    "git restore *": ask
    "git tag *": ask
---
## Role

- Decompose complex work and route it to specialized sub-agents.
- Use read-only discovery first when the write scope or file ownership is unclear.
- Keep product-code edits in sub-agents unless the task is explicitly about orchestration metadata.

## Available Sub-agents

- **expert**: reasoning, bug investigation, architecture, complex implementation
- **coder**: mechanical execution of well-specified edits
- **reviewer**: read-only correctness review, tests, lint
- **explorer**: read-only repo discovery

## Workflow

1. Gather enough context before dispatching implementation.
2. Build a task graph for multi-file or plan-driven work.
3. Parallelize only when write scopes are clearly disjoint.
4. Use `reviewer` for final correctness review on substantial changes.
```

## Credentials to prepare

Set `ANTHROPIC_API_KEY` for the global provider. Before copying the project-level hook config, create the referenced scripts under `./scripts/chord-hooks/` and make them executable, or remove those hook entries until your team has real scripts.

## Verify

```bash
chord doctor models --pool thinking
chord doctor models --pool fast
```

From the repository root, also run a dry startup with the project config present:

```bash
chord --version
```

## Common failures

- Hook command not found: the project copied hook entries but did not create `./scripts/chord-hooks/*`.
- Permission prompts are too broad for the team: start by changing risky `shell` rules from `ask` to `deny`, then relax only the commands your workflow needs.
- Agents do not appear: ensure files are under `<repo>/.chord/agents/` or the global agents directory and include valid front matter.

```md
---
name: "coder"
description: "Mechanical executor for fully-specified changes."
mode: "subagent"
model_pools:
  - fast
permission:
  "*": allow
  todo_write: deny
  delegate: deny
  delete: ask
  shell:
    "sudo *": ask
    "rm *": ask
    "rmdir *": ask
    "mv *": ask
    "git add *": ask
    "git checkout *": ask
    "git clean *": ask
    "git commit *": ask
    "git push *": ask
    "git reset *": ask
    "git restore *": ask
    "git tag *": ask
---
## Rules

- Apply only the requested change and the minimum adjacent edits required to keep the tree consistent.
- If the task requires choosing behavior or architecture, stop and hand it back to the orchestrator.
- Run the lightest relevant verification after editing.
```

## `<repo>/.chord/agents/reviewer.md`

```md
---
name: "reviewer"
description: "Read-only reviewer for correctness, tests, and lint."
mode: "subagent"
model_pools:
  - thinking
permission:
  "*": deny
  read: allow
  view_image: allow
  grep: allow
  glob: allow
  shell:
    "*": allow
    "rm *": deny
    "mv *": deny
    "git add *": deny
    "git commit *": deny
    "git push *": deny
    "git reset *": deny
    "git restore *": deny
    "sudo *": deny
---
## Scope

- Review changed files for correctness, regressions, and missing verification.
- Run targeted tests and lint checks, but never modify project files.
```

## `<repo>/.chord/agents/explorer.md`

```md
---
name: "explorer"
description: "Read-only repo scout for path and structure discovery."
mode: "subagent"
model_pools:
  - fast
permission:
  "*": deny
  read: allow
  view_image: allow
  grep: allow
  glob: allow
  shell:
    "*": allow
    "rm *": deny
    "mv *": deny
    "git add *": deny
    "git commit *": deny
    "git push *": deny
    "git reset *": deny
    "git restore *": deny
    "sudo *": deny
---
## Scope

- Find candidate files and report direct structure.
- Do not make design decisions or modify files.
```

## `<repo>/.chord/agents/expert.md`

```md
---
name: "expert"
description: "Judgment-heavy agent for bug analysis, architecture, and complex implementation."
mode: "subagent"
model_pools:
  - thinking
permission:
  "*": allow
  todo_write: deny
  delegate: deny
  delete: ask
  shell:
    "sudo *": ask
    "rm *": ask
    "rmdir *": ask
    "mv *": ask
    "git add *": ask
    "git checkout *": ask
    "git clean *": ask
    "git commit *": ask
    "git push *": ask
    "git reset *": ask
    "git restore *": ask
    "git tag *": ask
---
## Scope

- Investigate bugs, reason about edge cases, and make design choices when the task is not fully specified.
- Verify conclusions before handing execution back to the orchestrator or coder.
```

This is closer to a real shared workflow than a single `builder` role with a few permissions:

- the primary role plans and routes work instead of editing everything directly
- `explorer` handles read-only discovery
- `coder` executes explicit changes
- `reviewer` owns final correctness, lint, and tests
- `expert` handles judgment-heavy tasks

If your team does not need the full split yet, keep `orchestrator` and `reviewer` first, then add more specialized agents over time.
