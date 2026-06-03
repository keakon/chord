# Glossary

Quick reference for the terms that appear across Chord's docs.

## MainAgent

The single main agent for a session. It owns the user-facing conversation and is the only agent that can spawn SubAgents. The active main mode (role) is shown in the TUI status bar and can be cycled with `Tab` (main view only).

## SubAgent

A delegated agent spawned by the MainAgent (or another SubAgent, when delegation depth allows) to work on a focused task. SubAgents have their own conversation budget (context window), system prompt, and permissions, and report back via an `agent_done` event with a summary. Cycle the focused agent view across the main agent and SubAgents with `Shift+Tab`.

## Pool (model pool)

A named, ordered list of `provider/model` (and `model@variant`) refs. Each agent references one or more pools. At runtime, Chord uses the first pool's first ref by default and falls back through the rest when a request fails. Switch the active pool with `/models`, `Ctrl+P`, or `chord headless` `models` command.

## Variant

A named parameter preset for a single model — for example `claude-opus-4.8@high` selects a higher reasoning effort. Variants are defined under `models.<name>.variants` in `config.yaml` and referenced as `provider/model@variant` inside model pools.

## Compaction

The runtime process of summarizing earlier conversation into a compact context summary so a long session can keep going without exceeding the model's context window. Auto-compaction means Chord triggers this process before the request gets too large; you can also trigger it manually with `/compact`. See [Configuration — Compaction](./configuration.md#context-compaction).

## Reduction (context reduction)

A lightweight, deterministic pruning pass that runs before every LLM request. It trims stale tool results from the current prompt based on age and size heuristics — it never modifies saved session history on disk. Unlike compaction, reduction does not call an LLM and is entirely invisible to users. See [Configuration — Reduction](./configuration.md#context-reduction).

## Service tier

A user-togglable request tier (`/tier standard|fast|slow`) that asks the active provider/model to use the closest supported latency / cost mode. OpenAI maps this to its service-tier knobs (`fast` → `fast`, `slow` → `flex`), Anthropic maps `fast` to `speed` / service-tier controls and leaves `slow` unsupported, and Gemini uses its own routing / thinking controls where applicable. The tier is a request hint, not a guarantee; the provider may still fall back under load or model limits. Billing follows the active provider/model's configured pricing for the effective tier; use `cost.service_tier_multipliers` to model tier-specific price multipliers when the provider charges differently.

## Loop mode

A specialized execution mode where Chord runs autonomously in a continuous loop — processing tasks, tools, and results — without waiting for user input. Essential for long-running tasks. While loop mode is active, context reduction (request-level trimming) is disabled for newly added messages so the agent can retain its working state across iterations; context compaction remains enabled so long sessions can continue after the context budget is spent.

## Thinking

An extended reasoning or chain-of-thought phase that some models perform before producing their final answer. In Chord, thinking behavior is configured per model via `thinking.type`, `thinking.effort`, `thinking.budget`, and `thinking.level`. For Gemini, `thinking.level` maps to the model's thinking level (e.g. `low`, `high`). The optional `thinking_translation` feature appends translated thinking cards in the TUI.

## Turn

A single user-to-agent interaction round: you send a message → Chord processes it → the model responds with text and/or tool calls → tools execute → the model potentially responds again → the agent becomes idle. Context reduction `*_age_turns` settings are effective-age thresholds: user turns are one source of age, and long single-turn assistant/tool message progress can also make earlier tool output stale.

## Session

A persistent conversation record stored under `<state-dir>/sessions/<project-key>/`. Each session contains the full message history, turn metadata, and compaction archives. Sessions are project-scoped and survive restarts.

## OAuth

An authentication flow (device-code flow) used by Codex preset providers to obtain API access tokens. Chord stores stable OAuth fields in `auth.yaml` and frequently changing runtime state (quota snapshots, reset times) in `auth.state.yaml`. Use `chord auth` to log in; `chord auth codex --device-code` for headless environments.

## Key rotation

The strategy that controls when Chord reselects a provider credential / API key. `on_failure` keeps using the current key until it fails or cools down; `per_request` reselects before every request and is useful for load balancing across independent keys. It does not switch models.

## Key order

The strategy that controls how Chord chooses among selectable keys. `sequential` uses stable order / least-recently-used selection; `random` chooses randomly; `smart` is Codex-only and considers quota snapshots, soft cooldown, and reset timing for OAuth accounts.

## Context window

The maximum number of tokens a model can handle in one request. For most models, the practical rule is: prompt input + requested output must fit inside this window. In config this is `limit.context`.

## Model limits (`limit.*`)

Per-model numbers that tell Chord how much room the provider allows:

- `limit.context`: the total request window.
- `limit.input`: a separate input cap, only needed when the provider publishes one.
- `limit.output`: the model's maximum output capacity.

## Split limits

A provider term for models that publish more than one limit, usually a total context window plus a separate input cap. Some GPT models work this way. If provider docs list both numbers, configure both `limit.context` and `limit.input` so Chord can compact before the input is too large.

## Requested output cap (`max_output_tokens`)

The maximum output Chord asks for in a request. It is separate from the model's own `limit.output`. At runtime, Chord uses the smallest applicable value: `max_output_tokens`, the model's `limit.output`, and any remaining room in `limit.context`.

## Oversize recovery

The retry path Chord uses after a provider rejects a request as too large. Chord compacts or trims the conversation according to the configured input budget, then retries when it can do so safely.

## Worktree

A chord-managed git worktree (under `<state-dir>/worktrees/<repo-id>/<slug>`) with its own project key, sessions, cache, and exports. Create or enter one via `chord --worktree <name>` or `chord worktree <name>`; manage existing ones via `chord worktree list / remove / finish`. Useful for running multiple parallel chord tasks on the same repo without crosstalk. See [Paths — Worktrees](./paths.md#worktrees).

## Skill

A reusable, on-demand piece of expertise expressed as a markdown body plus YAML frontmatter (`SKILL.md`). The model loads matching skills via the `skill` tool when relevant — Chord does not preload them into every prompt. Discovered from `.chord/skills/`, `.agents/skills/`, `~/.config/chord/skills/`, and any extra paths configured via `skills.paths`. See [Customization — Skills](./customization.md#skills).

## Hook

An external command that runs at a well-defined point in Chord's lifecycle (before a tool call, after an LLM call, on idle, etc.). Hooks receive a JSON envelope on stdin, set `CHORD_HOOK_*` environment variables, and may block / modify / observe depending on the trigger point's category. See [Hooks](./hooks.md).

## MCP (Model Context Protocol)

A protocol for exposing external tools or data sources to an AI agent. In Chord, MCP servers are configured under `mcp:` in `config.yaml`; each server appears as a set of tools with the prefix `mcp_<server>_<tool>`. Use `allowed_tools` to register only a curated subset and avoid sending unused tool schemas to the model.

## Headless

Chord without the TUI. `chord headless` exposes a stdio JSONL control plane suitable for bot / gateway / automation integration. The companion project [chord-gateway](https://github.com/keakon/chord-gateway) wraps it for chat platforms. See [Headless](./headless.md).

## Speculative execution (early tool execution)

Chord may execute a small safe subset of tool calls *while the model response is still streaming* (as soon as tool arguments are complete) to reduce the "finalize gap". Speculative file mutations are real on-disk writes that get rolled back if the finalize discards the call. Always enabled; not user-configurable.

## Project key

A stable, sanitized identifier Chord computes from a project's canonical filesystem root (e.g. `HOME-projects-chord` for `~/projects/chord`). Used as the namespace for sessions, runtime cache, exports, and worktree identity. If two distinct paths sanitize to the same key, Chord appends an 8-character fingerprint. See [Paths — `<project-key>`](./paths.md#project-key--what-is-it).

## Permission action

The result of evaluating a permission rule against a tool call. The three outcomes are:

- `allow` — auto-execute
- `ask` — pause and require user confirmation
- `deny` — refuse outright

Permissions are agent-level config: global defaults live in `~/.config/chord/agents/<role>.yaml`, and project overrides live in `.chord/agents/<role>.yaml`. They are product-level risk control, not an OS-level sandbox. See [Permissions & Safety](./permissions-and-safety.md).

## Local TUI vs Local mode

"Local TUI" and "local mode" both refer to the default `chord` invocation: an in-process MainAgent driving a terminal UI. There is no IPC, no socket, no separate server. Contrast with `chord headless`, which runs the runtime without a TUI for external control planes.

## Diagnostics bundle

A snapshot exported by `Ctrl+G`: includes recent log lines, runtime state, TUI-specific debug info, and a recent lightweight LLM request trace from the current session. Full raw request / SSE dumps still require `log_level: debug`. Use it when reporting bugs. See [Troubleshooting — When to check logs](./troubleshooting.md#when-to-check-logs).

## Insert / Normal mode

The two TUI modes inspired by Vim. **Insert** is the input-focused mode where you type messages; **Normal** is for navigation, search, fold, scroll, and meta operations. `Esc` leaves Insert; `i` (or any unbound printable key) returns to Insert. See [Keybindings](./keybindings.md).

## Custom slash command

A user-defined command of the form `/name [args]` that, when entered in the input box, expands into a fixed (or `$ARGUMENTS`-templated) text and is sent to the model as a user message. Defined under `commands:` in `config.yaml` or as files under `commands/`. See [Customization — Custom slash commands](./customization.md#custom-slash-commands).

## Related

- [Quickstart](./quickstart.md)
- [Usage](./usage.md)
- [Configuration & Auth](./configuration.md)
