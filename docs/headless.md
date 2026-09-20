# Headless

`chord headless` is Chord's lightweight control-plane entry point, suitable for bot, gateway, or automation-script integration.

## What it is

- No TUI
- Interacts over stdio
- Input is JSON commands (one per line)
- Output is JSON envelopes (one per line)

It is suitable for outer-layer integration, but it does not provide a browser frontend, multi-tenant isolation, or complete permission hosting.

> **Protocol stability:** Chord is pre-1.0, so the headless protocol can change between releases. Treat unknown envelope fields and event types as opaque, pin the Chord version your integration was tested against, and check the [changelog](https://github.com/keakon/chord/blob/main/CHANGELOG.md) before upgrading.

## Start

```bash
chord headless
# or
go run ./cmd/chord/ headless
```

CLI flags: `-d/--session-dir`, `-c/--continue`, `-r/--resume`, `-w/--worktree`. See [CLI: `chord headless`](./cli.md#chord-headless).

## Wire format

- **stdin**: one JSON command per line
- **stdout**: one JSON envelope per line. Other diagnostic output goes to stderr; never parse stderr as protocol.

Every outbound envelope has the shape:

```json
{ "type": "<event-type>", "payload": { ... } }
```

State-carrying envelopes (event-loop pushes, command-path announcements such as `role_change` and `handoff_cancelled`, and `status_response` snapshots) additionally carry a monotonic `seq` (`{ "type": "<event-type>", "seq": 12, "payload": { ... } }`). Pushes leave the process in `seq` order, but a `status_response` snapshot is copied and emitted on the command path, so a snapshot taken before a newer push can arrive after it. Any cached-state mutation bumps `seq`, even when the gateway did not subscribe to the corresponding push or the mutation has no push at all: auto-dismissing a pending confirm or question on `send`, or an explicit `confirm` / `question` / `handoff` reply; so a later `status_response` is strictly newer than one copied before the mutation. Integrations that merge `status_response` into cached state must drop a snapshot whose `seq` is smaller than an already-seen `seq`. Every `status_response`, including one sent before the first push, has a nonzero `seq`. Reset the highest observed version when a new process sends `ready`; versions are local to that process.

The first line you receive is always `{"type": "ready", ...}`; wait for it before sending other commands.

## Try one interaction first

After configuring a model, start `chord headless` in a terminal:

1. Wait for `ready` on stdout before sending commands.
2. Enter `{"type":"status"}` on stdin and press Enter. Expect a `status_response`.
3. Enter `{"type":"send","content":"Explain this project without changing files."}` and press Enter to send a read-only task.
4. Read the returned events. If an approval, question, or handoff needs a response, use its corresponding command below; waiting for your answer is not a stuck task.

In an integration, keep the process's stdin open, read stdout line by line, and collect stderr separately. You can defer event filtering: before `subscribe` is sent, all subscribable events are forwarded.

## Commands

You send these on stdin. Unknown command types are answered with an `error` envelope.

### `subscribe`

Select which event types you want pushed. If you never send `subscribe`, Chord forwards **all** optional event types by default. Sending `subscribe` replaces that default with an explicit allowlist.

```json
{"type": "subscribe", "events": ["activity", "assistant_message", "idle", "done_completion"]}
```

Response:

```json
{"type": "subscribe_response", "payload": {"events": ["activity", "assistant_message", "idle", "done_completion"]}}
```

Available event types: `activity`, `assistant_message`, `idle`, `confirm_request`, `question_request`, `question_resolved`, `notification`, `handoff_request`, `handoff_cancelled`, `role_change`, `error`, `agent_started`, `agent_notify`, `agent_done`, `info`, `toast`, `done_completion`, `local_shell_result`, `assistant_rollback`, `todos`, `compaction_status`, `session_switched`, `background_result`, `context_notice`.

### `status`

Request a snapshot of the current backend state.

```json
{"type": "status"}
```

Response:

```json
{
  "type": "status_response",
  "seq": 1,
  "payload": {
    "session_id": "20260508120000000",
    "busy": false,
    "phase": "",
    "phase_detail": "",
    "pending_confirm": null,
    "pending_question": null,
    "pending_handoff": null,
    "last_error": "",
    "last_outcome": "completed",
    "current_role": "builder",
    "updated_at": "2026-05-08T12:00:00Z"
  }
}
```

`session_id` tracks the active session, not just the startup snapshot. An in-band switch that replaces the session without restarting the process (handoff plan execution, `/resume <id>`, `/new`) updates the tracked id, and the change is announced with an explicit `session_switched` push; the cached value alone never counts as the gateway having seen the new session. Restores that keep the session (startup replay, durable compaction rewrite) only refresh the timestamp and emit nothing. The tracked id moves even without a `session_switched` subscription, so `status_response` always reports the session the runtime actually runs.

### `send`

Send a user message to the agent. Slash commands work the same as in the TUI; bare `/models` is treated as `/models status` because there is no TUI overlay.

```json
{"type": "send", "content": "Please summarize the project structure."}
```

If a `confirm_request`, `question_request`, or `handoff_request` is pending and the user sends a regular message (not via `confirm`, `question`, or `handoff` below), Chord auto-dismisses the pending interaction so the new message is consumed. A pending `confirm_request` is auto-denied with an empty reason and emits no dedicated event; follow the next `status_response` (`pending_confirm` cleared) to stop waiting. A pending `question_request` is closed as `superseded`, and Chord pushes a `question_resolved` event with `reason: "superseded"` to subscribed clients. When the dismissed interaction is a `handoff_request`, Chord also pushes a `handoff_cancelled` event to subscribed clients, just like the runtime-initiated cancellation in the [`handoff`](#handoff) section. The dismissed interaction stops appearing as pending in the next `status_response`.

### `models`

Inspect or change model pools.

```json
{"type": "models", "action": "status"}
```

```json
{"type": "models", "action": "set_current_model_pool", "pool": "thinking"}
```

Response:

```json
{
  "type": "models_response",
  "payload": {
    "ok": true,
    "status": "Model pool: thinking\n..."
  }
}
```

`status` is a plain-text snapshot that mirrors `/models status`.

### `role`

Query or switch the active main role: the remote equivalent of TUI Shift+Tab. `list` returns the current role and the ordered main-mode role list (builder first, planner second when configured, then custom roles alphabetically); `set` switches roles and keeps the conversation history, just like TUI cycling.

```json
{"type": "role", "action": "list"}
```

```json
{"type": "role", "action": "set", "role": "planner"}
```

Response:

```json
{
  "type": "role_response",
  "payload": {
    "ok": true,
    "role": "planner",
    "roles": [
      {"name": "builder", "current": false},
      {"name": "planner", "current": true}
    ]
  }
}
```

`list` puts the active role in `role` and the full list in `roles`, where the entry with `current: true` is the active one. `set` switches to the named role and returns the new state, and the switch takes effect immediately, even while a turn is in flight. A `set` is rejected while a `handoff_request` is pending (`resolve the pending handoff before switching role`), as are switches to the already-active role (`already the active role: <name>`), to unknown names, and to roles that exist only as SubAgent definitions. Failures carry `ok: false` with a human-readable `message`; surface the message verbatim to the user. A successful role switch is also pushed as a `role_change` event when subscribed, and the current role appears in `status_response` as `current_role`. Note that `role_change` and `role_response` are written by different paths, so their arrival order is not guaranteed; treat `role_response` as authoritative.

### `confirm`

Resolve a pending `confirm_request`. Use the `request_id` from the request.

```json
{
  "type": "confirm",
  "request_id": "r-…",
  "action": "allow",
  "final_args_json": "{\"path\":\"...\"}",
  "edit_summary": "",
  "deny_reason": "",
  "rule_pattern": "shell:^git status$",
  "rule_scope": "session"
}
```

`action` follows whatever the model/runtime offered (`allow`, `deny`, `allow_once`, …). Optional `rule_pattern` + `rule_scope` (`session` / `project` / `user_global`) installs a permission rule along with the answer; omit both for one-shot decisions. `session` applies only to the current session; `project` writes to the current project's `.chord/agents/<role>.yaml`; `user_global` writes to the user config directory's `agents/<role>.yaml` (default: `~/.config/chord/agents/<role>.yaml`).

### `question`

Answer a pending `question_request`.

```json
{"type": "question", "request_id": "r-…", "answers": ["yes"], "reason": "answered"}
```

For multi-select questions, pass multiple strings in `answers`. `reason` must be `answered` (submit the selection) or `declined` (dismiss without answering); any other value is rejected with an `error`. Clients cannot submit `no_response`, `superseded`, `cancelled`, or `error` — those describe how Chord closed the request, and are reported through `question_resolved`. The response is accepted only if the request is still open and before its `deadline`; a rejected or late answer returns an `error` and never clears a different pending question.

### `handoff`

Resolve a pending `handoff_request`. Approving starts executing the saved plan with the selected agent; denying appends the rejection reason to the conversation and lets the planner continue from that context.

```json
{"type": "handoff", "request_id": "handoff-…", "action": "accept", "agent": "builder", "pool": "thinking"}
```

```json
{"type": "handoff", "request_id": "handoff-…", "action": "deny", "deny_reason": "Please add rollout steps first."}
```

`action` accepts `accept` / `allow` (or an empty action) to approve and `deny` / `reject` to reject with a reason. `cancel` closes the pending handoff without executing the plan and without appending a rejection message. `agent` defaults to the request's default agent, and optional `pool` switches that agent's model pool before execution.

A pending handoff belongs to the turn and session that raised it. Whenever Chord discards it without a client decision (on a session switch, when a superseding turn starts, or when a `send` auto-dismisses it, as described under [`send`](#send)), Chord pushes a `handoff_cancelled` event to subscribed clients, and the following `status_response` reports `pending_handoff: null`, so an integration stops waiting instead of showing an approval prompt the agent has already abandoned.

### `local_shell`

Execute a local shell command from the headless client side and receive a `local_shell_result` event. This is intended for gateway features that expose `!`-style local commands.

```json
{"type": "local_shell", "command": "git status --short"}
```

`content` is also accepted as a fallback command field. Output combines stdout and stderr, is capped, and the command has a timeout.

> Security: `local_shell` runs `bash -c` in the `chord headless` process environment. It is a direct local command-execution protocol feature, not a model tool request and not a substitute for sandboxing. Gateways that expose it to users must provide their own authentication, authorization, auditing, command filtering, and tenant isolation.

### `cancel`

Cancel the current turn (equivalent to pressing `Esc` twice in the TUI).

```json
{"type": "cancel"}
```

## Events

You receive these on stdout. The list below covers what is emitted by default plus the subscribable types. Treat unknown fields as opaque so future server upgrades don't break your client.

### Always emitted (no subscription needed)

| Type                  | When                                                                                       | Notable payload fields                                            |
| --------------------- | ------------------------------------------------------------------------------------------ | ----------------------------------------------------------------- |
| `ready`               | Server has finished startup and is ready to accept commands                                | `session_id`, worktree info (when applicable: `name`, `branch`, `path`, `repo_root`) |
| `subscribe_response`  | Reply to a `subscribe` command                                                             | `events`                                                          |
| `status_response`     | Reply to a `status` command                                                                | see [`status`](#status)                                           |
| `models_response`     | Reply to a `models` command                                                                | `ok`, `message`, `status`                                         |
| `role_response`       | Reply to a `role` command                                                                  | `ok`, `message`, `role`, `roles[]` with `{name, current}`         |
| `error`               | Command parse / execution error                                                            | `message`, optional `code` (for example `stdin_line_too_long`)    |

### Subscribable

| Type                    | When                                                                                              | Notable payload fields                                                                                       |
| ----------------------- | ------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------ |
| `activity`              | Agent enters a new phase                                                                          | `agent_id`, `type` (`connecting`, `streaming`, `compacting`, …), `detail`                                    |
| `assistant_message`     | A complete assistant message is ready for consumption                                             | `agent_id`, `task_id`, `agent_type`, `parent_agent_id`, `text`, `tool_calls`; delegation fields are empty for main |
| `idle`                  | The main agent and all SubAgents are globally quiescent and ready for input                         | `last_outcome` (`completed` / `cancelled` / `error`), `suppress_user_notification` (`true` unless the agent ran since the previous idle event) |
| `done_completion`      | Done tool completed with a final report. Emitted only while a loop is running, since that is the only time `done` is mounted; the `mode` field is currently always `normal` | `call_id`, `report`, `reason`, `status`, `agent_id`, `mode`                                                  |
| `confirm_request`       | A tool needs explicit confirmation                                                                | `request_id`, `agent_id`, `tool_name`, `args_json`, `needs_approval`, `already_allowed`, `needs_approval_rules`, `already_allowed_rules`, `timeout_ms` |
| `question_request`      | The model asked the user a question                                                               | `request_id`, `agent_id`, `tool_name`, `header`, `question`, `options`, `option_details`, `multiple`, `deadline` (absolute RFC 3339 close time; omitted when no `question_timeout` is set) |
| `question_resolved`     | A published question closed, whether by an answer or by Chord (deadline, supersede, cancel, execution error) | `request_id`, `reason` (`answered`, `declined`, `no_response`, `superseded`, `cancelled`, `error`) |
| `notification`          | A user-facing reminder for an explicit wait that is not a modal request                       | `reason`, `message` |
| `handoff_request`       | A planner saved a handoff plan and needs the client to approve or reject execution                 | `request_id`, `plan_path`, `plan_text`, `plan_error`, `agents[]` with `{name, default, model_pools, current_model_pool}`; `agents` is empty when no eligible target exists |
| `handoff_cancelled`     | A pending handoff was discarded before the client decided — a newer turn, a session switch, or an auto-dismissing `send` superseded it | `request_id`, `reason` (`superseded`)                                                                        |
| `role_change`          | The active main role switched (via TUI Shift+Tab or a `role set` command)                        | `role`                                                                                                   |
| `local_shell_result`    | Result for a `local_shell` command                                                                | `command`, `output`, `failed`, `error` |
| `agent_started`         | A delegated SubAgent runtime started, including an on-demand rehydration of a parked task           | `agent_id`, `previous_agent_id` (set for rehydration), `task_id`, `agent_type`, `description`, `parent_agent_id`, `parent_task_id` |
| `agent_notify`          | An agent sent a non-blocking owner or targeted delegated-workstream update                         | `agent_id`, `task_id`, `agent_type`, `parent_agent_id`, `parent_task_id`, `target_agent_id`, `target_task_id`, `kind`, `subtype`, `message` |
| `agent_done`            | A SubAgent completed its task                                                                     | `agent_id`, `task_id`, `agent_type`, `parent_agent_id`, `parent_task_id`, `summary`                          |
| `assistant_rollback`    | Discard in-flight streamed assistant output (mostly relevant for streaming UIs)                   | `agent_id`, `reason`                                                                                         |
| `info`                  | Informational message from the runtime                                                            | `agent_id`, `message`                                                                                        |
| `toast`                 | Transient notification surfaced to the user in the TUI; safe to ignore in headless                | `agent_id`, `message`, `level` (`info` / `warn` / `error`)                                                   |
| `todos`                 | Replacement todo list                                                                             | `todos[]` with `{id, content, status, active_form}`. Multiple `in_progress` items can be valid when each maps to a distinct active workstream and uses a unique `active_form`. |
| `compaction_status`     | Compaction lifecycle events: `started` and terminal outcomes (`succeeded`, `skipped`, `failed`, `cancelled`) | `status`, `trigger` (`manual`, `usage_driven`, `length_recovery`, `oversize_driven`, `model_driven`), `reason`, `plan_id` (bounded compaction plan identifier for correlating the terminal outcome with the plan that produced it). Progress telemetry is not forwarded. |
| `session_switched`      | The active session changed without restarting the process (handoff plan execution, `/resume <id>`, `/new`) | `session_id` (the new active session) |
| `background_result`     | A finished background job's durable result; the only delivery channel for the JOB RESULT card, which typically lands after the turn is idle so no later `assistant_message` summarizes it | `session_id` (the session Chord had active when it emitted the event), `target_agent_id`, `message_index`, `content` |
| `context_notice`        | Durable context-pressure warning with no other headless channel | `session_id` (the session Chord had active when it emitted the event), `level`, `message`, `message_index` |
| `error`                 | Runtime error                                                                                     | `agent_id`, `message`, optional `code`                                                                         |

If an input line on stdin exceeds the protocol line limit, Chord emits an `error` envelope with `code: "stdin_line_too_long"` and continues reading later lines. Integrations should use `code` for classification when present and keep `message` for human-readable diagnostics.

Silent retry telemetry is never pushed. A retry the TUI only records in the error panel emits no `error` envelope and leaves `last_error` / `last_outcome` (`status_response` and `idle`) untouched, so a turn that hits a silent retry and recovers still reports `completed`. A terminal failure is always followed by a non-silent error, which is the one integrations observe.

`assistant_message.text` may be empty for tool-only rounds (including a SubAgent `Complete` call). Chord logs a warning for observability; gateway integrations should skip the empty message and use `agent_done.summary` as the authoritative SubAgent completion content.

Quiescent SubAgents may release their live runtime while their task and transcript remain durable. A later authorized targeted notification can rehydrate the task with a new `agent_id`; use stable `task_id` for routing and use `previous_agent_id` on `agent_started` to replace runtime-specific labels.

`idle` is a global quiescence signal, not a per-request completion signal. Chord does not emit it while any agent is running, any internal event or actionable mailbox message is queued, a Handoff decision is pending, or a SubAgent has input waiting for its next request. A busy target processes queued messages at the next request boundary; a resumable non-running target is woken first. Progress and notice snapshots bound for the main inbox are actionable rather than informational: while the main is idle, Chord merges the pending undelivered updates into a single delivery batch, delivered in arrival order, and wakes the main for that delivery turn before global idle can fire; every arriving batch therefore costs one extra main turn and LLM request, and the idle event is held back until the batch has been delivered. `suppress_user_notification` does not change the idle state transition; it only tells user-facing integrations not to emit a generic completion reminder when the quiescence was not preceded by real agent work: for example startup, session / model-pool / MCP switches, or other user-initiated navigation. It is `true` unless the agent actually ran (a main turn, loop execution, or active SubAgent work) since the previous idle event. `notification` is the complementary explicit reminder for a runtime state that is waiting for user input; `reason="user_input_required"` currently covers permissions, questions, Handoff, and loop decisions.

## Slash compatibility via `send`

For convenience, headless also accepts these via `send` so you can drive Chord from a chat surface that only has a single text input:

- `/models status`, `/models <pool>`, `/models --agent <name> <pool>`
- `/role status`, `/role <name>`: query or switch the active main role (same operation as the `role` protocol command)
- `/help`, `/stats`, `/compact`, `/loop on`, `/loop off` (only when the active MainAgent role can use the `done` tool)

Bare `/models` is treated as `/models status`, and bare `/role` as `/role status`. `/new` and `/resume <id>` work in headless and switch the session in-band (they emit `session_switched`). Bare `/resume` still needs the TUI picker, and so do a few other slash commands such as `/export`; those return an `error` envelope explaining "X is only available in local TUI mode".

## Minimal Python client

```python
import json
import subprocess
import threading

proc = subprocess.Popen(
    ["chord", "headless", "-d", "/path/to/project"],
    stdin=subprocess.PIPE,
    stdout=subprocess.PIPE,
    stderr=subprocess.DEVNULL,
    bufsize=1,
    text=True,
)

def reader():
    for line in proc.stdout:
        ev = json.loads(line)
        print("<-", ev["type"], ev.get("payload"))

threading.Thread(target=reader, daemon=True).start()

def send(cmd: dict) -> None:
    proc.stdin.write(json.dumps(cmd) + "\n")
    proc.stdin.flush()

# Wait for ready (the first line is always "ready"), then subscribe and send.
send({"type": "subscribe",
      "events": ["activity", "assistant_message", "idle", "done_completion"]})
send({"type": "send", "content": "Summarize the project structure."})
```

In production, also handle `confirm_request` (reply via `confirm`), `question_request` (reply via `question`), and `handoff_request` (reply via `handoff`); the agent will block waiting for those replies.

## chord-gateway: recommended way to consume headless

If you want to drive Chord from a chat surface (Feishu, WeChat, …) or build a multi-user gateway, you usually do **not** need to implement the headless protocol from scratch. The companion project [keakon/chord-gateway](https://github.com/keakon/chord-gateway) already wraps it and adds the bits the protocol intentionally leaves out:

- Process lifecycle: spawning / restarting `chord headless` per session, reaping idle processes.
- Per-tenant isolation: per-user working directory, audit logs, rate limits.
- Adapters for chat platforms: Feishu / WeChat webhooks, message chunking, image relay.
- Permission UX: rendering `confirm_request` and `question_request` as inline replies, mapping replies back to `confirm` / `question` commands.
- Reconnection helpers around the wire format above.

The headless protocol on this page is the lower-level contract, suitable for integrators who need something `chord-gateway` does not cover. If your goal is "let people talk to Chord from their phone", start with chord-gateway and only drop down to headless when you have a specific reason.

## Suitable usage

- Let the outer gateway manage process lifecycle.
- Let the outer system decide which events to show to end users.
- Enforce working-directory, permission, audit, and tenant-isolation controls in the outer layer.

## Not a replacement for

`chord headless` is not:

- a browser application
- a multi-tenant security boundary
- a complete permission sandbox

For higher-level deployment patterns, see [chord-gateway](https://github.com/keakon/chord-gateway).

## Related

- [Usage](./usage.md)
- [CLI: chord headless](./cli.md#chord-headless)
- [Permissions & Safety](./permissions-and-safety.md)
- [Troubleshooting](./troubleshooting.md)
