# Built-in tools

This page lists every built-in tool name the model can call. Use these exact names in agent `permission:` rules, hook `tools:` filters, and skill `allowed_tools` lists.

For how `allow` / `ask` / `deny` are evaluated, including the special coupling between the orchestration tools, see [Permissions & Safety](./permissions-and-safety.md).

## How to use this page

Find the section for the job you are doing:

- **Read and edit files:** [Files](#files), [Search and navigation](#search-and-navigation).
- **Run commands and long jobs:** [Execution](#execution).
- **Fetch a page:** [Web](#web).
- **Plan, ask, and delegate:** [Workflow](#workflow), [Orchestration and control](#orchestration-and-control).
- **External tool servers:** [MCP tools](#mcp-tools).

## Files

| Tool | What it does |
| --- | --- |
| `read` | Read a local file into context, with optional 1-based `offset` / `limit` line paging. |
| `write` | Create a file or intentionally replace a whole file. |
| `edit` | Replace exact text in one existing file. |
| `apply_patch` | Apply a Codex-style patch envelope (`*** Begin Patch`): add, update, delete, or move files. Independent file groups may succeed partially; check applied changes before retrying. |
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

When the queried position is not on an identifier — a line number that lands on the comment above a declaration, for example — the failure includes the requested line and its neighbors with line numbers, so the position that was actually queried is visible without another read.

## Execution

| Tool | What it does |
| --- | --- |
| `shell` | Run commands; long commands can continue as background jobs. See below. |
| `job_output` | Read new job output, or wait briefly for output or completion. |
| `job_list` | List the background jobs you can read or stop (id, status, elapsed, quiet duration, label), including jobs started by the main agent and by your direct owner. Only active jobs are listed unless `include_finished: true` is passed. |
| `job_kill` | Stop a background job by `job_id`, with an optional `reason`. |

### Command execution and timeouts

Run a non-interactive shell command, either in the foreground or as a background job with `run_in_background: true`. A foreground command that runs past `yield_time_ms` (default 90000) is promoted to a background job automatically; `timeout_ms` caps execution: a foreground command defaults to 600000 and is capped at 600000, while `run_in_background: true` is capped at 21600000 (6h) and carries no deadline unless `timeout_ms` is given; `0` means no deadline.

A foreground command that cannot be promoted (one made only of deliberate waits (`sleep`) and short `git` queries, or a command that does not parse) keeps the default cap even when `timeout_ms` is `0`, so no foreground call can block the turn without a deadline; a long `git` operation (`clone`, `fetch`, `pull`, `push`, `submodule`, `gc`, `fsck`, `repack`, `bundle`, `filter-branch`) is promotable like any other long command.

Long commands do not have to block the turn. A command that outlives its foreground budget keeps running as a background job, the tool card names its job id, and the agent is notified when the job finishes, so it can do independent work or end the turn and be woken by the completion instead of waiting.

A job owns the whole process group its command starts, not only the direct child. A command that exits while children it started keep running stays active until they exit, and those children stay under the job's `timeout_ms` deadline, `job_kill`, and session cleanup, so `nohup … &` no longer escapes by outliving the wrapper that started it. A process that leaves the group on purpose (`setsid`, `setpgid`) is outside the job again — Chord does not scan the process tree for it. A stop signals the group only while a member it recorded when the command exited is still in that group; when that cannot be proven, `job_kill` reports the teardown as unconfirmed instead of signalling a group number that may have been recycled. Waiting needs no such proof: a job stays active while the group answers at all, so a descendant that inherits the group keeps the job alive even when Chord cannot list its pid. On platforms without process groups (Windows), a job still ends with its direct process: `job_kill`, the job's deadline, and session cleanup can only stop that process, and the result reports the teardown as unconfirmed because the descendants it may leave behind cannot be observed.

`job_output` reads incremental output and only reports what is new, and a bounded wait that expires leaves the job alive. Consecutive job-completion wakes with no user input in between are bounded; after that, further completions wait for your next message. A background job also ends with the session (switching sessions or exiting the client stops it), so day-scale work belongs in an external runner such as tmux, systemd, or CI.

`job_list` shows the jobs that are running or stopping, with the label, elapsed time, quiet duration, and how much of the deadline is left; pass `include_finished: true` to also see retained finished ones.

### Reading background output

Read a background job's output since the previous read, then its `[status: ...]` line. `wait` selects whether the call blocks: `none` (default) returns what is available now, `output` waits for the next output, and `exit` waits for the job to finish, each capped at 30s by the runtime. A wait that expires is not an error: the job keeps running and the reply reports it as running, plus a `[notice]` line that says whether the `exit` wait timed out or was cancelled and how long the job has been quiet. Repeated non-blocking reads that find no new output are reported as polling and then rejected, so keep reading only while there is a reason to. Terminal escape sequences are stripped from what the model sees.

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

`save_artifact` takes two mutually exclusive parameter shapes. `filename` with `content` (plus `mode: create` / `append` / `overwrite` when needed) writes or updates a session artifact; alternatively, the `result_type` + `result` pair (`result` must be a JSON object) stores the payload as an immutable, content-addressed result under `artifacts/results/` and returns a ResultRef (`id`, `result_type`, `rel_path`, `sha256`, `size_bytes`), which `complete` accepts directly as its `result_ref`.

## Orchestration and control

These tools control agent workflows rather than local side effects, so YOLO does not sweep their permission rules aside: it removes the confirmation friction of file edits and shell commands, not the role's boundary. See [Permissions & Safety: Special permission semantics](./permissions-and-safety.md#special-permission-semantics) for how `handoff`, `delegate`, `cancel`, `done`, and `compact_context` resolve under YOLO.

| Tool | What it does |
| --- | --- |
| `done` | Request loop exit with a final Markdown report. Mounted only while a loop is running, so ordinary sessions never see it. See [Usage: continuous execution mode](./usage.md#loop-continuous-execution-mode). |
| `handoff` | Transfer a plan/work to another role for execution. |
| `delegate` | Start a sub-task and return its handle immediately. Role permissions determine capabilities; declared scope coordinates work. |
| `cancel` | Cancel a delegated worker; requires `delegate` to be enabled. |
| `complete` | SubAgent-side: mark the current delegated task as complete with a summary. |
| `escalate` | SubAgent-side: request parent-agent intervention without ending the task. |
| `notify` | Send a non-blocking update to the owner or a specific delegated worker. A targeted message resumes a worker that already finished or failed, with its own transcript; a cancelled task is not resumable. See the message forms below. |

### Delegation and work scope

`delegate` starts a delegated SubAgent workstream and returns its startup handle (`task_id` / `agent_id`) immediately, without waiting for completion. The call must include an `expected_write_scope` that declares the narrowest `files`, `path_prefix`, or `modules` scope covering the work.

The declaration is coordination metadata, not an enforced boundary: whether the worker may modify files at all is decided by its role's permission rules (a role that denies `write`, `edit`, `delete`, and `apply_patch` registers none of them), and the runtime never blocks a worker's file tools outside the declared paths.

Declaring an honest narrow scope keeps sibling-overlap hints meaningful: when the declared scope overlaps another still-active task's, the delegation still starts and the handle carries `scope_conflict: true` with `suggested_task_id` and `suggested_action: serialize_or_worktree`, which tells you to run the two tasks serially, coordinate the shared edits through `notify`, or give the new worker its own git worktree.

A read-only task should pick an agent whose role registers no file-modifying tools and pass an empty scope, which is accepted only for such roles; a role that can write files must declare a non-empty scope or the delegation is rejected. Command tools such as `shell` are never scope-restricted and stay governed by the role's permission rules. Denying `delegate` also disables `cancel` and nested delegation for that role.

### Notifications and replies

- **Owner update:** omit `target_task_id`. Use `message_type: progress` (the default) or `notice`; optional `subtype`, `correlation_id`, and a JSON-object `payload` up to 32 KiB are available for this form. MainAgent cannot send owner updates.
- **Plain targeted message:** provide `target_task_id`, `message`, and optionally `kind`. Omit `message_type`, `subtype`, `correlation_id`, and `payload`. Use this form for corrections or follow-up work.
- **Reply to a pending request:** provide `target_task_id`, `message_type: response`, and the request's **required** `correlation_id`, together with `message` and optionally `kind`. This form does not accept `subtype` or `payload`. Roles that can only notify their owner cannot send targeted replies.

### Long-text control tools

`done`, `complete`, and `escalate` may carry a long Markdown report, summary, or escalation reason. While the arguments are still streaming, the TUI shows a temporary `N chars received` indicator; once they are complete, the prose is rendered as Markdown in the card body. `complete` also keeps structured completion details: changed files, remaining limitations, known risks, follow-up recommendations, and artifact references.

These cards are always expanded and their header is only the tool name: the report is the card. The same applies to `compact_context`, `delegate`, `question`, `notify`, `write`, `edit`, `apply_patch`, `delete`, `todo_write`, and `handoff`: no disclosure marker, and the fold keys leave them as they are. Only `read`, `grep`, `glob`, `shell`, `cancel`, and generic tool calls fold, marked with `▸` / `▾`. See [Usage: TUI basics](./usage.md#tui-basics) for the fold keys and how collapsed cards look.

`delegate` has one tool result: the asynchronous startup handle. Later `complete` calls and mailbox updates are separate runtime events that update the existing delegated task/card by stable `task_id`; they never produce additional `delegate` tool results. Each `complete` report raises an owner-visible **AGENT COMPLETE** notification card, and terminal worker failures are shown as **AGENT BLOCKED** and wake the direct owner.

### Delegated task boundaries

Agent-to-agent messages respect request boundaries: if the target is busy, the message is queued and included in its next LLM request instead of interrupting the active one; a resumable idle target is woken to receive it.

Progress and notice updates sent to an idle main agent are not purely informational: every undelivered update is kept and delivered in the order it was produced, and at the next between-turn boundary Chord merges the pending updates into a single delivery batch that wakes the main for one extra turn (one extra LLM request) before it can go quiet again.

Mailbox and coordination state is durable: parent-child request/response records and queued payloads survive compaction and restart, and delivery stays idempotent across task rehydration.

The runtime, not the model, is the source of truth for delegation state. A worker that fails to emit a coordination tool (`complete`, `escalate`, or `notify`) receives one bounded follow-up request; if it still cannot comply, or provider/model retries are exhausted, Chord marks it failed, records a `risk_alert`, and wakes the owner. A rehydrated runtime may receive a new `agent_id`; coordination should continue through the stable delegated `task_id`.

## MCP tools

Tools exposed by configured MCP servers are registered as `mcp_<server>_<tool>` (for example `mcp_search_web_search_exa`) and can be referenced in permission rules by that full name. Use `allowed_tools` in the MCP server config to limit which remote tools are registered at all; see [Configuration: MCP](./configuration.md#mcp).

## Related

- [Permissions & Safety](./permissions-and-safety.md)
- [Usage](./usage.md)
- [Customization](./customization.md)
