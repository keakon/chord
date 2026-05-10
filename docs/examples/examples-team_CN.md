# 团队方案

这一页展示的是**项目共享**的 `.chord/` 布局：

- 全局配置只放个人凭据和默认 provider
- 项目级 `.chord/config.yaml` 放团队共享的 hooks、LSP、命令和默认策略
- 主角色负责拆任务与编排
- 子角色按只读探索、机械执行、复核分工

## `~/.config/chord/config.yaml`

```yaml
providers:
  anthropic:
    type: messages
    api_url: https://api.anthropic.com/v1/messages
    models:
      claude-opus-4.7:
        limit:
          context: 1000000
          input: 1000000
          output: 128000
        thinking:
          type: adaptive
          effort: medium

model_pools:
  thinking:
    - anthropic/claude-opus-4.7
  fast:
    - anthropic/claude-opus-4.7

context:
  auto_compact: true
  compact_threshold: 0.8

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
  compact_model: anthropic/claude-opus-4.7
  compaction:
    reserved: 16000

hooks:
  on_tool_call:
    - name: audit-shell
      tools: ["Shell"]
      command: ["./scripts/chord-hooks/audit-shell.sh"]
      timeout: 5

  on_tool_batch_complete:
    - name: golangci-lint
      tools: ["Edit", "Write", "Delete"]
      paths: ["**/*.go"]
      min_changed_files: 1
      command: ["./scripts/chord-hooks/run-golangci-lint.sh"]
      result: append_on_failure
      result_format: tail
      max_result_lines: 80
      join: before_next_llm

  on_before_tool_result_append:
    - name: redact-keys
      tools: ["Shell", "WebFetch", "Read"]
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
  Handoff: deny
  Delete: ask
  Shell:
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

## `<repo>/.chord/agents/coder.md`

```md
---
name: "coder"
description: "Mechanical executor for fully-specified changes."
mode: "subagent"
model_pools:
  - fast
permission:
  "*": allow
  TodoWrite: deny
  Delegate: deny
  Delete: ask
  Shell:
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
  TodoWrite: deny
  Delegate: deny
  Delete: ask
  Shell:
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

这个团队方案比“单个 builder + 少量权限规则”更接近真实协作：

- 主角色先做任务分解，而不是直接一把梭改代码
- `explorer` 负责只读探路
- `coder` 只做明确改动
- `reviewer` 负责最后的 correctness / lint / tests
- `expert` 处理需要判断的复杂任务

如果团队不需要这么完整，也可以先只保留 `orchestrator` + `reviewer` 两个角色，再慢慢加细分 agent。
