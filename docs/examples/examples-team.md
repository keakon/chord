# Team setup

<!-- description: A reliability-first team with strong coordination and review, midrange implementation, and lightweight discovery. -->

This page shows a shared project layout under `.chord/`:

- keep personal credentials and default providers in global config
- put team-shared hooks, LSP, commands, and defaults in project-level `.chord/config.yaml`
- let the main role finish small tasks directly and delegate substantial independent work
- use separate contexts for discovery, implementation, and independent review

The global `config.yaml` from this page is available as a ready-to-copy file: [`team-ready.yaml`](./team-ready.yaml).

This example uses the reliability-first recipe from [Choosing models](../model-choice.md). Opus 5.5 handles coordination, expert work, and review in `deep`; Sonnet 5.5 handles implementation in `coding`; GPT-6 Luna handles discovery in `fast`. Compaction uses `coding` because preserving task state needs more than path discovery. The managed presets supply model limits from the catalog; the two Anthropic models explicitly use `high` effort.

For everyday work, replace the model reference in `deep` with `openai/gpt-6.1-sol`. For an Anthropic-only setup, keep `deep` and `coding` on the Anthropic models, point `fast` at `anthropic/claude-sonnet-5-5`, and remove the unused OpenAI provider and credential. Pool names do not impose capability levels or automatic escalation.

Get the model configuration working first, then add only the roles you need. Supply the scripts referenced by hook commands, or remove those hooks until ready. Keep credentials in personal configuration, never in the shared repository.

## `~/.config/chord/config.yaml`

```yaml
providers:
  openai:
    preset: openai
  anthropic:
    preset: anthropic
    models:
      claude-opus-5-5:
        thinking:
          type: adaptive
          effort: high
      claude-sonnet-5-5:
        thinking:
          type: adaptive
          effort: high

model_pools:
  deep:
    - anthropic/claude-opus-5-5
  coding:
    - anthropic/claude-sonnet-5-5
  fast:
    - openai/gpt-6-luna

context:
  compaction:
    threshold: 0.8
    model_pool: coding
desktop_notification: true
log_level: info
```

## `~/.config/chord/auth.yaml`

```yaml
openai:
  - "$OPENAI_API_KEY"
anthropic:
  - "$ANTHROPIC_API_KEY"
```

## `<repo>/.chord/config.yaml`

```yaml
context:
  compaction:
    model_pool: coding

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
description: "Owns requirements, task decomposition, design coordination, and final acceptance."
mode: "main"
model_pools:
  - deep
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
- Finish small, clear tasks directly. Delegate substantial independent work without duplicating it.
- Own cross-task decisions and final acceptance against the user's full requirements.

## Available Sub-agents

- **expert**: reasoning, bug investigation, architecture, complex implementation
- **coder**: bounded implementation and verification
- **reviewer**: read-only correctness review, tests, lint
- **explorer**: read-only repo discovery

## Workflow

1. Gather enough evidence to identify dependencies and unsettled decisions before dispatching implementation.
2. Give each worker the goal, non-goals, constraints, settled decisions, dependencies, and acceptance criteria. Use result_schema when a machine-readable result is needed; schema validity does not prove correctness.
3. Keep each shared design decision with one owner. Start with one writing worker; parallelize only independent deliverables with disjoint expected write scopes and settled interfaces.
4. Use notify on the existing task for clarification or rework. Replan when new evidence invalidates an assumption; do not take over just because a worker is quiet.
5. Give reviewer the original requirements, actual changes, and verification evidence for substantial changes. Check integration and remaining limitations before declaring the whole task complete.
```

## Credentials to prepare

Set `OPENAI_API_KEY` and `ANTHROPIC_API_KEY` for the global providers. Before copying the project-level hook config, create the referenced scripts under `./scripts/chord-hooks/` and make them executable, or remove those hook entries until your team has real scripts.

## Verify

```bash
chord doctor models --pool deep
chord doctor models --pool coding
chord doctor models --pool fast
```

Run these from the repository root so Chord loads both global config and `<repo>/.chord/config.yaml`. They validate the selected model pools using the same provider transport path as normal runtime requests. They do not measure coordination quality; compare representative tasks before lowering a role's model tier.

## Common failures

- Hook command not found: the project copied hook entries but did not create `./scripts/chord-hooks/*`.
- Permission prompts are too broad for the team: start by changing risky `shell` rules from `ask` to `deny`, then relax only the commands your workflow needs.
- Agents do not appear: ensure files are under `<repo>/.chord/agents/` or the global agents directory and include valid front matter.
- A cheap model keeps producing unusable work: check the specification and verification evidence, then change its model or replan. Successful API requests do not trigger a quality-based pool upgrade.

## `<repo>/.chord/agents/coder.md`

```md
---
name: "coder"
description: "Implements and verifies bounded changes against settled behavior and interfaces."
mode: "subagent"
model_pools:
  - coding
permission:
  "*": allow
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
- Choose local implementation details within the settled behavior and interfaces. Report evidence that invalidates the plan or requires a product, architecture, or security decision.
- Run focused verification, then the integration checks the task requires.
- Return actual changes, verification commands and results, evidence references, and unverified items. Report a true blocker to the owner instead of marking the task complete.
```

## `<repo>/.chord/agents/reviewer.md`

```md
---
name: "reviewer"
description: "Read-only reviewer for correctness, tests, and lint."
mode: "subagent"
model_pools:
  - deep
permission:
  "*": deny
  read: allow
  view_image: allow
  grep: allow
  glob: allow
  shell:
    "*": ask
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
- Form an independent assessment from the original requirements and actual changes before relying on the implementer's explanation.
- Run approved tests and lint checks without modifying project source files. Report findings with locations, trigger conditions, and supporting evidence; distinguish passed, failed, and unverified checks.
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
---
## Scope

- Find candidate files, callers, existing patterns, and relevant tests. Return paths and line references; separate observed facts from hypotheses and missing information.
- Do not make design decisions or modify files.
```

## `<repo>/.chord/agents/expert.md`

```md
---
name: "expert"
description: "Judgment-heavy agent for bug analysis, architecture, and complex implementation."
mode: "subagent"
model_pools:
  - deep
permission:
  "*": allow
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

- Investigate bugs and difficult design questions against the task's constraints. Escalate unresolved product decisions to the owner.
- Implement and verify a difficult part directly when handing it to coder would lose essential context. Delegate routine follow-up through the owner only after its behavior and acceptance criteria are settled.
- Return evidence, verification results, remaining uncertainty, and any decision that affects other tasks.
```

The main agent owns final acceptance. A valid structured result or a reviewer approval does not replace checking the original requirement and integrated result. Small tasks need no delegation; ordinary development usually needs only a coder and optional review. Invoke expert for the difficult part instead of making it a mandatory approval layer.

The workers in this example cannot delegate further. Explorer has no shell access; reviewer asks before running commands because test and lint scripts can write files or execute arbitrary code. Inspect those commands before approving them. Write-capable workers retain explicit confirmation for risky shell operations; coordinate Git operations through the main agent.

Declare each task's `expected_write_scope` honestly. It helps coordination but does not restrict file access; role permissions control tools. For concurrent writes that need isolation, use separate checkouts or Git worktrees and verify the integrated result.
