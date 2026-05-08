# Headless

`chord headless` is Chord's lightweight control-plane entry point, suitable for bot, gateway, or automation-script integration.

## What it is

- No TUI
- Interacts over stdio
- Input is JSON commands (one per line)
- Output is JSON envelopes (one per line)

It is suitable for outer-layer integration, but it does not provide a browser frontend, multi-tenant isolation, or complete permission hosting.

## Start

```bash
chord headless
# or
go run ./cmd/chord/ headless
```

CLI flags: `-d/--session-dir`, `-c/--continue`, `-r/--resume`, `--worktree`. See [CLI — `chord headless`](./cli.md#chord-headless).

## Wire format

- **stdin**: one JSON command per line
- **stdout**: one JSON envelope per line. Other diagnostic output goes to stderr; never parse stderr as protocol.

Every outbound envelope has the shape:

```json
{ "type": "<event-type>", "payload": { ... } }
```

The first line you receive is always `{"type": "ready", ...}` — wait for it before sending other commands.

## Commands

You send these on stdin. Unknown command types are answered with an `error` envelope.

### `subscribe`

Select which event types you want pushed. Defaults to none (you receive only request/response envelopes for your own commands until you subscribe).

```json
{"type": "subscribe", "events": ["activity", "assistant_message", "idle", "tool_result"]}
```

Response:

```json
{"type": "subscribe_response", "payload": {"events": ["activity", "assistant_message", "idle", "tool_result"]}}
```

Available event types: `activity`, `assistant_message`, `idle`, `confirm_request`, `question_request`, `error`, `agent_done`, `info`, `toast`, `tool_result`, `assistant_rollback`, `todos`.

### `status`

Request a snapshot of the current backend state.

```json
{"type": "status"}
```

Response:

```json
{
  "type": "status_response",
  "payload": {
    "session_id": "20260508120000000",
    "busy": false,
    "phase": "",
    "phase_detail": "",
    "pending_confirm": null,
    "pending_question": null,
    "last_error": "",
    "last_outcome": "completed",
    "updated_at": "2026-05-08T12:00:00Z"
  }
}
```

### `send`

Send a user message to the agent. Slash commands work the same as in the TUI; bare `/models` is treated as `/models status` because there is no TUI overlay.

```json
{"type": "send", "content": "Please summarize the project structure."}
```

If a `confirm_request` or `question_request` is pending and the user sends a regular message (not via `confirm` / `question` below), Chord auto-dismisses the pending interaction so the new message is consumed.

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
    "status": "current role: thinking\n..."
  }
}
```

`status` is a plain-text snapshot that mirrors `/models status`.

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
  "rule_pattern": "Bash:^git status$",
  "rule_scope": "session"
}
```

`action` follows whatever the model/runtime offered (`allow`, `deny`, `allow_once`, …). Optional `rule_pattern` + `rule_scope` (`session` / `project` / `user_global`) installs a permission rule along with the answer; omit both for one-shot decisions.

### `question`

Answer a pending `question_request`.

```json
{"type": "question", "request_id": "r-…", "answers": ["yes"], "cancelled": false}
```

For multi-select questions, pass multiple strings in `answers`. Pass `"cancelled": true` to dismiss the question without answering.

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
| `error`               | Command parse / execution error                                                            | `message`                                                         |

### Subscribable

| Type                    | When                                                                                              | Notable payload fields                                                                                       |
| ----------------------- | ------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------ |
| `activity`              | Agent enters a new phase                                                                          | `agent_id`, `type` (`connecting`, `streaming`, `compacting`, …), `detail`                                    |
| `assistant_message`     | A complete assistant message is ready for consumption                                             | `agent_id`, `text`, `tool_calls`                                                                             |
| `idle`                  | Agent is ready to receive input again                                                             | `last_outcome` (`completed` / `cancelled` / `error`)                                                         |
| `tool_result`           | A tool execution finished                                                                         | `call_id`, `name`, `status` (`success` / `error` / `cancelled`), `agent_id`                                  |
| `confirm_request`       | A tool needs explicit confirmation                                                                | `request_id`, `tool_name`, `args_json`, `needs_approval`, `already_allowed`, `timeout_ms`                    |
| `question_request`      | The model asked the user a question                                                               | `request_id`, `tool_name`, `question`, `options`, `option_details`, `default_answer`, `multiple`, `timeout_ms` |
| `agent_done`            | A SubAgent completed its task                                                                     | `agent_id`, `task_id`, `summary`                                                                             |
| `assistant_rollback`    | Discard in-flight streamed assistant output (mostly relevant for streaming UIs)                   | `agent_id`, `reason`                                                                                         |
| `info`                  | Informational message from the runtime                                                            | `agent_id`, `message`                                                                                        |
| `toast`                 | Transient notification surfaced to the user in the TUI; safe to ignore in headless                | `agent_id`, `message`, `level` (`info` / `warn` / `error`)                                                   |
| `todos`                 | Replacement todo list                                                                             | `todos[]` with `{title, status}`                                                                             |
| `error`                 | Runtime error                                                                                     | `agent_id`, `message`                                                                                        |

`assistant_message.text` is empty only in pathological cases — Chord logs a warning when this happens, and gateway integrations should usually skip such messages instead of forwarding empty text downstream.

## Slash compatibility via `send`

For convenience, headless also accepts these via `send` so you can drive Chord from a chat surface that only has a single text input:

- `/models status`, `/models <pool>`, `/models --agent <name> <pool>`
- `/help`, `/stats`, `/diagnostics`, `/compact`, `/loop on`, `/loop off`

Bare `/models` is treated as `/models status`. Some slash commands are TUI-only (e.g. `/new`, `/resume` — they require an interactive picker); attempting them in headless mode returns an `error` envelope explaining "X is only available in local TUI mode".

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
      "events": ["activity", "assistant_message", "idle", "tool_result"]})
send({"type": "send", "content": "Summarize the project structure."})
```

In production, also handle `confirm_request` (reply via `confirm`) and `question_request` (reply via `question`); the agent will block waiting for those replies.

## chord-gateway — recommended way to consume headless

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
- [CLI — chord headless](./cli.md#chord-headless)
- [Permissions & Safety](./permissions-and-safety.md)
- [Troubleshooting](./troubleshooting.md)
