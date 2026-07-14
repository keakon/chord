# Built-in tools

This page lists every built-in tool name the model can call. Use these exact names in agent `permission:` rules, hook `tools:` filters, and skill `allowed_tools` lists.

For how `allow` / `ask` / `deny` are evaluated — including the special coupling between the orchestration tools — see [Permissions & Safety](./permissions-and-safety.md).

## Files

| Tool | What it does |
| --- | --- |
| `read` | Read a local file into context. |
| `write` | Create a file or intentionally replace a whole file. |
| `edit` | Replace exact text in one existing file. |
| `patch` | Apply unified diff hunks to one existing file. |
| `delete` | Remove whole files. |
| `view_image` | Load a local PNG/JPEG into context; available only when the active model pool's first model supports image input. Uses the same local-path permission handling as `read`. |

## Search and navigation

| Tool | What it does |
| --- | --- |
| `grep` | Regex/literal content search with output caps; supports multi-root `paths` and `includes` glob filters. |
| `glob` | Path matching by glob pattern(s), with output caps. |
| `lsp` | Semantic definition / references / implementation lookup at a file position, when an LSP server covers the file type. |

In the TUI, an `lsp` card summarizes the operation and query position in its header (for example, `find references internal/agent/main.go:54:17`). Completed queries show the location count and, when applicable, the number of files. Paths inside the working directory are displayed relative to it, while the expandable details retain every returned `path:line:character` location. The default `include_declaration=true` setting is omitted; reference queries explicitly show when declarations are excluded.

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
| `save_artifact` | Save or update a session artifact (report, task graph, log) under the session's artifacts directory. |
| `read_artifact` | Read a session artifact by session-relative path. |

## Orchestration and control

These tools control agent workflows rather than local side effects. YOLO mode does **not** bypass permissions for `handoff`, `delegate`, `cancel`, and `done`, and a broad `"*": allow` rule does not grant them by itself — configure each one directly when a role should use it.

| Tool | What it does |
| --- | --- |
| `done` | Send a final report only when the active runtime or workflow explicitly requires a tool-based completion signal, primarily to request loop exit. Ordinary completion must be returned directly as assistant text; merely finishing the work or having `done` available is not a reason to call it. Loop exits remain gated by exit conditions and local confirmation. |
| `handoff` | Transfer a plan/work to another role for execution. |
| `delegate` | Start a delegated SubAgent workstream. Denying it also disables `cancel` and nested delegation for that role. |
| `cancel` | Cancel a delegated worker; requires `delegate` to be enabled. |
| `complete` | SubAgent-side: mark the current delegated task as complete with a summary. |
| `escalate` | SubAgent-side: request parent-agent intervention without ending the task. |
| `notify` | Send a non-blocking update to the owner or a specific delegated worker. |

SubAgent failures are not converted into `complete`. After provider/model retries are exhausted, or when a resumed worker cannot start, Chord closes that runtime as failed, records a `risk_alert`, and wakes its owner/MainAgent. A rehydrated runtime may receive a new `agent_id`; coordination should continue through the stable delegated `task_id`.

## MCP tools

Tools exposed by configured MCP servers are registered as `mcp_<server>_<tool>` (for example `mcp_search_web_search_exa`) and can be referenced in permission rules by that full name. Use `allowed_tools` in the MCP server config to limit which remote tools are registered at all; see [Configuration — MCP](./configuration.md#mcp).

## Related

- [Permissions & Safety](./permissions-and-safety.md)
- [Usage](./usage.md)
- [Customization](./customization.md)
