# Built-in tools

This page lists every built-in tool name the model can call. Use these exact names in agent `permission:` rules, hook `tools:` filters, and skill `allowed_tools` lists.

For how `allow` / `ask` / `deny` are evaluated — including the special coupling between the orchestration tools — see [Permissions & Safety](./permissions-and-safety.md).

## Files

| Tool | What it does |
| --- | --- |
| `read` | Read a local file into context, with optional 1-based `offset` / `limit` line paging. |
| `write` | Create a file or intentionally replace a whole file. |
| `edit` | Replace exact text in one existing file. |
| `apply_patch` | Apply a Codex-style patch envelope (`*** Begin Patch`): add, update, delete, or move one or more files in a single transactional call. `patch` is accepted as a legacy alias in rules and filters. |
| `delete` | Remove whole files. |
| `view_image` | Load a local PNG/JPEG into context; available only when the active model pool's first model supports image input. Uses the same local-path permission handling as `read`. |

Only one of `edit` / `apply_patch` is exposed to the model at a time, chosen by model family; patch-native models also route file creation/deletion through the `apply_patch` envelope instead of `write`/`delete`. See [Edit tools](./edit-tools.md).

## Search and navigation

| Tool | What it does |
| --- | --- |
| `grep` | Regex/literal content search with output caps; supports multi-root `paths` and `includes` glob filters. |
| `glob` | Path matching by glob pattern(s), with output caps. |
| `lsp` | Semantic definition / references / implementation lookup at a file position, when an LSP server covers the file type. |

In the TUI, an `lsp` card shows the operation and query position in its header (for example, `find references internal/agent/main.go:54:17`), the location count once the query completes, and every returned `path:line:character` location in the expandable details.

## Execution

| Tool | What it does |
| --- | --- |
| `shell` | Run a non-interactive shell command. |
| `spawn` | Start a long-running background process. |
| `spawn_status` | Inspect lifecycle state of a `spawn`-started process. |
| `spawn_stop` | Stop a `spawn`-started process. |

## Web

| Tool | What it does |
| --- | --- |
| `web_fetch` | Fetch a URL as readable text; permission rules can match URL patterns. |

## Workflow

| Tool | What it does |
| --- | --- |
| `todo_write` | Maintain the visible TODO list for the current task. |
| `question` | Ask the user a structured question and wait for the answer. `ask` is normalized to `allow` for this tool. |
| `skill` | Load a discovered skill's content on demand. |
| `save_artifact` | Save or update a session artifact (report, task graph, log) or store an immutable machine-readable result, under the session's artifacts directory. |
| `read_artifact` | Read a session artifact by session-relative path. |

`save_artifact` takes two mutually exclusive parameter shapes. `filename` with `content` (plus `mode: create` / `append` / `overwrite` when needed) writes or updates a session artifact; alternatively, the `result_type` + `result` pair — `result` must be a JSON object — stores the payload as an immutable, content-addressed result under `artifacts/results/` and returns a ResultRef (`id`, `result_type`, `rel_path`, `sha256`, `size_bytes`), which `complete` accepts directly as its `result_ref`.

## Orchestration and control

These tools control agent workflows rather than local side effects, so YOLO mode does **not** bypass their permissions — YOLO removes the friction of confirming file edits and shell commands, not the role's boundary. `handoff`, `delegate`, and `cancel` grant a capability the role did not have. Outside YOLO they follow ordinary permission rules, so a broad `"*": allow` grants them like any other tool — which is why the built-in `builder` denies `handoff` and `delegate` explicitly to stay single-agent. Under YOLO, by contrast, a wildcard never reaches them: they stay denied unless a rule names them directly. `done` and `compact_context` only end or shrink the current unit of work, so the runtime mode that mounts them is their authorization and a wildcard-only rule does not reach them. See [Permissions & Safety](./permissions-and-safety.md).

| Tool | What it does |
| --- | --- |
| `done` | Request loop exit with a final Markdown report. Mounted only while a loop is running, so ordinary sessions never see it and return their completion directly as assistant text. Loop exits remain gated by exit conditions and local confirmation. |
| `handoff` | Transfer a plan/work to another role for execution. |
| `delegate` | Start a delegated SubAgent workstream and return its startup handle (`task_id` / `agent_id`) immediately. It does not wait for completion. The call must include an `expected_write_scope`: use `read_only: true` for research-only work, or declare the narrowest `files`, `path_prefix`, or `modules` scope that covers the work. An empty scope is rejected. The worker's tools and permissions enforce this scope; verification is handled by the owner or CI. Denying `delegate` also disables `cancel` and nested delegation for that role. |
| `cancel` | Cancel a delegated worker; requires `delegate` to be enabled. |
| `complete` | SubAgent-side: mark the current delegated task as complete with a summary. |
| `escalate` | SubAgent-side: request parent-agent intervention without ending the task. |
| `notify` | Send a non-blocking update to the owner or a specific delegated worker. A targeted message resumes a worker that already finished or failed, with its own transcript; a cancelled task is not resumable. Plain targeted messages can also add write paths with `grant_write_scope`. See the message forms below. |

### Notifications and replies

- **Owner update:** omit `target_task_id`. Use `message_type: progress` (the default) or `notice`; optional `subtype`, `correlation_id`, and a JSON-object `payload` up to 32 KiB are available for this form. MainAgent cannot send owner updates.
- **Plain targeted message:** provide `target_task_id`, `message`, and optionally `kind`. Omit `message_type`, `subtype`, `correlation_id`, and `payload`. Use this form for corrections or follow-up work.
- **Reply to a pending request:** provide `target_task_id`, `message_type: response`, and the request's **required** `correlation_id`, together with `message` and optionally `kind`. This form does not accept `subtype`, `payload`, or `grant_write_scope`. Roles that can only notify their owner cannot send targeted replies.


### Long-text control tools

`done`, `complete`, and `escalate` may carry a long Markdown report, summary, or escalation reason. While the arguments are still streaming, the TUI shows a temporary `N chars received` indicator; once they are complete, the prose is rendered as Markdown in the card body. `complete` also keeps structured completion details, such as changed files, verification runs, limitations, risks, follow-up recommendations, and artifact references.

These cards are always expanded and their header is only the tool name: the report is the card, so a collapsed preview with a one-line summary of it on the header would only repeat what the body already shows. The same applies to `compact_context` (objective, completed work, decisions, open issues, next step, state files), `delegate` (description, worker handle, completion), `question` (every question, its options and the selection) and `notify` (target, kind, message): no disclosure marker, and `o` / `Enter` / `Space` leaves them as they are. Cards indexed by an argument instead — `read`, `write`, `edit`, `apply_patch`, `delete`, `grep`, `glob`, `handoff` and the rest — keep their collapsible body and their header index line, and `cancel` keeps both its collapsed form and its `cancel <task_id> (<reason>)` header.

`delegate` has one tool result: the asynchronous startup handle. Later `complete` calls and mailbox updates are separate runtime events that update the existing delegated task/card by stable `task_id`; they never produce additional `delegate` tool results. Each `complete` report raises an owner-visible **AGENT COMPLETE** notification card, and terminal worker failures are shown as **AGENT BLOCKED** and wake the direct owner.

### Delegated task boundaries

Agent-to-agent messages respect request boundaries: if the target is busy, the message is queued and included in its next LLM request instead of interrupting the active one; if the target is idle but resumable, Chord wakes it; progress-only updates never force an otherwise idle agent to run. Mailbox and coordination state is durable: parent-child request/response records and queued payloads survive compaction and restart, and delivery stays idempotent across task rehydration.


The runtime, not the model, is the source of truth for delegation state. A worker that fails to emit a coordination tool (`complete`, `escalate`, or `notify`) receives one bounded follow-up request; if it still cannot comply, or provider/model retries are exhausted, Chord marks it failed, records a `risk_alert`, and wakes the owner. A rehydrated runtime may receive a new `agent_id`; coordination should continue through the stable delegated `task_id`.

## MCP tools

Tools exposed by configured MCP servers are registered as `mcp_<server>_<tool>` (for example `mcp_search_web_search_exa`) and can be referenced in permission rules by that full name. Use `allowed_tools` in the MCP server config to limit which remote tools are registered at all; see [Configuration — MCP](./configuration.md#mcp).

## Related

- [Permissions & Safety](./permissions-and-safety.md)
- [Usage](./usage.md)
- [Customization](./customization.md)
