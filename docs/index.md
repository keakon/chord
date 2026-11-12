# Chord Docs

**A faster, cheaper, lighter terminal coding agent.**

Six coding agents each ran the same DeepSWE v1.1 task (adding streaming JSON iteration to `httpx`) on the same model (`deepseek-flash`). Chord was fastest and cheapest: 6m37s at an estimated $0.052, about 1/3 of Claude Code's time and cost. See [Performance](./performance.md) for the full comparisons and test conditions.

A session uses tens of megabytes of memory, so you can run several agents at once even on modest hardware.

The [Quickstart](./quickstart.md) takes you through installation, model setup, and your first task. See [supported accounts and APIs](./model-choice.md#supported-accounts-and-apis) to check compatibility with your existing keys or accounts. With the default configuration, Chord edits files and runs commands directly. File deletions through `delete` and deletions or moves through `apply_patch` require confirmation. To approve other actions before they run, see [Permissions & Safety](./permissions-and-safety.md).

[中文](./index_CN.md)

## Getting started

- [Quickstart](./quickstart.md): Install and send your first task
- [Usage](./usage.md): Everyday controls, sessions, long tasks, and parallel work
- [Keybindings](./keybindings.md): Find and customize keyboard controls

## Models and credentials

- [Configuration & Auth](./configuration.md): Configuration files, credentials, model pools, and field reference
- [Choosing models](./model-choice.md): Pick a channel and model by budget and role
- [Model configuration](./model-configs.md): Connect models through the catalog, refresh it, and configure custom gateways
- [Reasoning and thinking](./reasoning.md): Choose thinking settings and understand usage implications
- [Context management](./context-management.md): Understand compaction, reduction, and long-session settings

## Tools and safety

- [Permissions & Safety](./permissions-and-safety.md): Set approval rules and understand risks
- [Built-in tools](./tools.md): Look up tool names, purposes, and key limits
- [Edit tools](./edit-tools.md): Understand file edits, partial success, and retries
- [Image generation and editing](./image-generation.md): Configure an image pool, generate or edit images, and keep saved originals

## Customization and integration

- [Customization](./customization.md): Configure roles, skills, code diagnostics, and external tools
- [External coding-agent subagents](./external-agents.md): Delegate subtasks to Claude Code running in an external process
- [Hooks](./hooks.md): Automate notifications, checks, and tool-result processing
- [Project Memory](./project-memory.md): Store durable, reviewable cross-session project memory
- [Headless](./headless.md): Control Chord from scripts or another interface
- [ACP Agent Mode](./acp.md): Serve the Agent Client Protocol so ACP clients can drive Chord

## Configuration examples

- [Examples](./examples/index.md): Choose a complete setup for your workflow
- [Minimal setup](./examples/examples-minimal.md): One provider and one model pool
- [Codex + LSP](./examples/examples-codex-workstation.md): Sign-in, code diagnostics, and a reviewer
- [OpenAI-compatible gateway](./examples/examples-openai-compat.md): Multiple keys and a backup endpoint
- [Team setup](./examples/examples-team.md): Project configuration and role-based work

## Reference and troubleshooting

- [CLI reference](./cli.md): Commands, options, and examples
- [Paths](./paths.md): Locate configuration, sessions, and caches; understand cleanup
- [Environment variables](./environment.md): Path, proxy, and debugging settings
- [Platform support](./platforms.md): Find platform-specific behavior
- [Performance](./performance.md): Measurements, test conditions, and troubleshooting slowness
- [Troubleshooting](./troubleshooting.md): Find your next step by symptom
- [Glossary](./glossary.md): Look up terminology
