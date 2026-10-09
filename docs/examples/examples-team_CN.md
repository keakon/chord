# 团队方案

<!-- description: 质量优先的团队配置：强模型协调与审查，中档模型实现，轻量模型探索。 -->

这一页展示项目共享的 `.chord/` 布局：

- 全局配置只放个人凭据和默认 provider
- 项目级 `.chord/config.yaml` 放团队共享的 hooks、LSP、命令和默认策略
- 主角色直接完成小任务，委派实质独立的工作
- 探索、实现和独立审查使用各自的上下文

这一页的全局 `config.yaml` 也提供可直接复制的文件：[`team-ready.yaml`](./team-ready.yaml)。

本例采用[按工作选模型](../model-choice_CN.md)中的可靠性优先配方。`deep` 中的 Opus 5.5 负责协调、专家任务和审查，`coding` 中的 Sonnet 5.5 负责实现，`fast` 中的 GPT-6 Luna 负责探索。压缩使用 `coding`，保留任务状态的要求高于定位文件。托管 preset 从模型目录补齐限额，两个 Anthropic 模型显式使用 `high` effort。

日常开发可将 `deep` 中的模型引用改为 `openai/gpt-6.1-sol`。只使用 Anthropic 时，保留 `deep` 和 `coding` 中的 Anthropic 模型，将 `fast` 指向 `anthropic/claude-sonnet-5-5`，并移除不用的 OpenAI provider 和凭据。池名不强制能力档位，也不会自动升级模型。

先跑通模型配置，再添加需要的角色。Hook 命令引用的脚本需要自行提供；尚未准备好时先移除对应 Hook。凭据保留在个人配置中，不要提交到团队仓库。

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

## 需要准备的凭据

为全局 provider 设置 `OPENAI_API_KEY` 和 `ANTHROPIC_API_KEY`。复制项目级 hook 配置前，请先在 `./scripts/chord-hooks/` 下创建对应脚本并设为可执行；如果团队还没有真实脚本，先删除这些 hook 条目。

## 验证命令

```bash
chord doctor models --pool deep
chord doctor models --pool coding
chord doctor models --pool fast
```

在仓库根目录执行这些命令，Chord 会同时加载全局配置和 `<repo>/.chord/config.yaml`，并用正常运行时相同的 provider transport 路径验证选中的模型池。这些命令不测协调质量；降低角色模型档位前，应比较代表性任务的结果。

## 常见失败原因

- Hook command not found：复制了 hook 条目，但没有创建 `./scripts/chord-hooks/*`。
- 团队权限提示过宽：先把高风险 `shell` 规则从 `ask` 改成 `deny`，只对工作流确需的命令逐步放开。
- Agents 不出现：确认文件在 `<repo>/.chord/agents/` 或全局 agents 目录下，并包含合法 front matter。
- 便宜模型反复产出不可用结果：先检查规格和验证证据，再换模型或重新规划。API 请求成功不会触发按质量升级模型池。

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

主代理负责最终验收。结构化结果合法或 reviewer 通过，都不能替代对原始需求和集成结果的检查。小任务无需委派；普通开发通常只需 coder 和按需审查。expert 用于困难部分，不作为必经审批层。

本例中的 worker 不能继续委派。explorer 没有 shell 权限；reviewer 运行命令前需要确认，因为测试和 lint 脚本也可能写文件或执行任意代码，批准前应检查命令。可写 worker 的高风险 shell 操作保留显式确认；Git 操作由主代理统一协调。

如实声明任务的 `expected_write_scope`。它用于协调，不限制文件访问，工具能力由角色权限决定。并发写入需要隔离时，使用独立检出或 Git worktree，并验证集成后的结果。
