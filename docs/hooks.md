# Hooks

Hooks let you run external commands at well-defined points in Chord's lifecycle: when a tool is about to run, when an LLM call returns, when an agent goes idle, etc. They are useful for notifications, auditing, automation gates, and post-batch checks.

This page is the complete reference. For higher-level usage advice, see [Customization](./customization.md).

## How a hook runs

When a registered point fires, Chord:

1. **Spawns the configured command** (either a `shell` line or an `argv` list).
2. **Sends a JSON envelope on stdin** (see [Envelope](#envelope)).
3. **Sets a small set of `CHORD_HOOK_*` environment variables** (see [Env vars](#environment-variables)).
4. **Sets the working directory to the project root**.
5. **Reads the hook's stdout** as either a sync result, an automation result, or ignored output (depending on the point's category).
6. **Enforces a timeout** (default 30 seconds; configurable per hook).

Hook stdout that is not valid JSON is treated as a parse failure and the hook is logged as `failed`. Hooks that exit non-zero are logged as `failed`. Failures **never crash** Chord.

## Hook categories

Chord groups the 14 trigger points into three categories that decide what the hook can return:

| Category       | Points                                                                       | Behavior                                                                                              |
| -------------- | ---------------------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------- |
| **sync**       | `on_tool_call`, `on_before_llm_call`, `on_before_tool_result_append`         | Synchronous interceptor. Stdout JSON `{"action": "continue\|block\|modify", "message": "...", "data": {...}}`. `block` aborts the action; `modify` replaces the data flowing downstream. |
| **automation** | `on_tool_batch_complete`                                                     | Async background task. Stdout JSON `{"status": "...", "summary": "...", "body": "...", "severity": "...", "append_context": bool, "notify": bool}`. Result can be joined into the next context, optionally. |
| **observer**   | All other points (`on_idle`, `on_session_start`, `on_after_llm_call`, ...)   | Stdout is logged as a plain string, but cannot block or modify. Pure side-effect.                      |

## Trigger points

| Point                            | Category    | When it fires                                                                                       | Common `data` fields                                                |
| -------------------------------- | ----------- | --------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------- |
| `on_session_start`               | observer    | A session is created or resumed                                                                     | session metadata                                                    |
| `on_session_end`                 | observer    | A session is closed cleanly                                                                         | session metadata, summary stats                                     |
| `on_before_llm_call`             | sync        | Just before a request is sent to the model                                                          | `model`, `messages`                                                 |
| `on_after_llm_call`              | observer    | After a model response (and any retries) completes                                                  | `model`, `usage`, `error` (on failure)                              |
| `on_tool_call`                   | sync        | Before a tool actually runs                                                                         | `tool_name`, `args`, `timeout_ms`                                   |
| `on_tool_result`                 | observer    | After a tool returns                                                                                | `tool_name`, `output`, `error`                                      |
| `on_before_tool_result_append`   | sync        | Before a tool result is appended to context (last chance to redact / replace)                        | `tool_name`, `output`, `error`                                      |
| `on_tool_batch_complete`         | automation  | After a batch of tools in a turn completes (typical for editing batches)                             | `changed_files`, `tool_calls`                                       |
| `on_before_compress`             | observer    | Before context compaction runs                                                                      | `reason`, current `usage`                                           |
| `on_after_compress`              | observer    | After context compaction finishes                                                                   | `reason`, before/after `usage`                                      |
| `on_idle`                        | observer    | Agent transitions to idle (turn finished, awaiting input)                                            | `agent_id`                                                          |
| `on_wait_confirm`                | observer    | A tool needs user confirmation (permission `ask`)                                                   | `tool_name`, `args`                                                 |
| `on_wait_question`               | observer    | The model asked a question and is waiting for an answer                                             | `question`                                                          |
| `on_agent_error`                 | observer    | An agent reports an error (LLM error, tool failure, etc.)                                           | `error`, `error_kind`                                               |

The exact `data` fields can evolve. To stay future-proof, treat unknown fields as opaque and rely on the keys you actually need.

## Envelope

Every hook receives this JSON document on stdin:

```json
{
  "point": "on_tool_call",
  "timestamp": "2026-05-08T12:00:00.000Z",
  "session_id": "20260508120000000",
  "turn_id": 7,
  "agent_id": "main",
  "agent_kind": "main",
  "project_root": "/path/to/project",
  "selected_model": "anthropic/claude-opus-4.7",
  "running_model": "anthropic/claude-opus-4.7",
  "data": {
    "tool_name": "Bash",
    "args": { "command": "git status" }
  }
}
```

## Environment variables

In addition to stdin, Chord sets the following variables before exec'ing the hook command (empty values are not set):

| Variable                          | Source                          |
| --------------------------------- | ------------------------------- |
| `CHORD_HOOK_POINT`                | Envelope `point`                |
| `CHORD_HOOK_SESSION_ID`           | Envelope `session_id`           |
| `CHORD_HOOK_TURN_ID`              | Envelope `turn_id`              |
| `CHORD_HOOK_AGENT_ID`             | Envelope `agent_id`             |
| `CHORD_HOOK_AGENT_KIND`           | Envelope `agent_kind`           |
| `CHORD_HOOK_PROJECT_ROOT`         | Envelope `project_root`         |
| `CHORD_HOOK_SELECTED_MODEL`       | Envelope `selected_model`       |
| `CHORD_HOOK_RUNNING_MODEL`        | Envelope `running_model`        |
| `CHORD_HOOK_TOOL_NAME`            | Convenience: extracted from `data.tool_name` if present |
| `CHORD_HOOK_TIMEOUT_MS`           | Convenience: extracted from `data.timeout_ms` if present |
| `CHORD_HOOK_ERROR_KIND`           | Convenience: extracted from `data.error_kind` if present |

Anything you put under the hook's `environment:` map is also passed through verbatim.

## Stdout contracts

### Sync hooks

```json
{
  "action": "continue",
  "message": "optional human-readable note",
  "data": null
}
```

- `continue` (default if stdout is empty) — let the action proceed.
- `block` — abort the action; `message` is shown to the user.
- `modify` — replace the data flowing downstream with `data`. The exact shape of `data` matches the original payload of that point (e.g. for `on_tool_call`, `data` should be the modified tool args).

### Automation hooks (`on_tool_batch_complete`)

```json
{
  "status": "success",
  "summary": "linted 12 files, 0 issues",
  "body": "details...",
  "severity": "info",
  "append_context": false,
  "notify": false
}
```

- `status`: `success` or `failed`.
- `severity`: `info`, `warning`, `error`. Defaults to `info`, or `error` when `status == failed`.
- `append_context: true` requests Chord to feed the result into the next LLM call.
- `notify: true` surfaces the summary to the user.

### Observer hooks

Stdout is recorded in logs as a plain string. There is no schema — feel free to print whatever helps you debug.

## HookDef fields

```yaml
hooks:
  on_tool_call:
    - name: audit-bash
      command: ["./scripts/audit-bash.sh"]   # or: shell: "./scripts/audit-bash.sh"
      timeout: 10                             # seconds; default 30
      tools: ["Bash"]                         # glob match on tool name
      paths: ["src/**/*.go"]                  # glob match on relevant paths
      agents: ["main", "reviewer"]            # glob match on agent name
      agent_kinds: ["main", "subagent"]       # exact match
      models: ["anthropic/*"]                 # glob match on selected/running model
      min_changed_files: 0                    # only run if at least N files changed
      only_on_error: false                    # only run when there is an error in payload
      join: background                        # automation only: background | before_next_llm
      result: notify_only                     # automation only: ignore | notify_only | append_on_failure | always_append
      result_format: summary                  # automation only: summary | tail | full
      max_result_lines: 50                    # automation only
      max_result_bytes: 4096                  # automation only
      debounce_ms: 0
      concurrency: ""                         # serialize key
      retry_on_failure: 0
      retry_delay_ms: 0
      environment:
        AUDIT_LEVEL: strict                   # injected verbatim
```

Filters are AND-ed: a hook runs only if every populated filter matches.

## Examples

### 1. Notify on idle (observer)

```yaml
hooks:
  on_idle:
    - name: notify-idle
      command:
        - osascript
        - -e
        - 'display notification "Chord is idle" with title "Chord"'
```

The hook ignores stdout; the side effect is the notification.

### 2. Block destructive bash commands (sync)

```yaml
hooks:
  on_tool_call:
    - name: deny-rm-rf
      tools: ["Bash"]
      shell: |
        # Read envelope, check the command, optionally block
        jq -e '.data.args.command | test("^rm -rf|^sudo")' \
          && echo '{"action":"block","message":"Destructive command blocked"}' \
          || echo '{"action":"continue"}'
```

`jq` reads the envelope from stdin; if the regex matches, the hook prints `{"action":"block",…}` and Chord aborts the call.

### 3. Run lint after edit batches (automation)

```yaml
hooks:
  on_tool_batch_complete:
    - name: golangci-lint
      tools: ["Edit", "Write", "Delete"]
      paths: ["**/*.go"]
      min_changed_files: 1
      shell: |
        out=$(golangci-lint run ./... 2>&1) || status=failed
        cat <<JSON
        {
          "status": "${status:-success}",
          "summary": "golangci-lint",
          "body": $(jq -Rs . <<<"$out"),
          "append_context": ${status:+true,$0}false
        }
        JSON
      result: append_on_failure
      result_format: tail
      max_result_lines: 80
      join: before_next_llm
```

When the lint fails, the truncated tail is appended to the next LLM context so the model can react.

### 4. Strip API keys from tool output (sync, modify)

```yaml
hooks:
  on_before_tool_result_append:
    - name: redact-keys
      tools: ["Bash", "WebFetch", "Read"]
      shell: |
        envelope=$(cat)
        redacted=$(jq '.data.output |= (gsub("sk-[A-Za-z0-9_-]{20,}"; "sk-REDACTED"))' <<<"$envelope")
        echo "{\"action\":\"modify\",\"data\": $(jq '.data' <<<"$redacted")}"
```

## Debugging hooks

Set `CHORD_HOOK_DEBUG=1` before launching Chord — every hook invocation will be logged with input, output, exit code, and duration. See [Environment variables](./environment.md#development-and-debugging).

When a hook misbehaves:

1. Check `chord.log` for `hook execution status=failed/timed_out`.
2. Run the command manually with the same envelope on stdin to reproduce.
3. Verify the JSON output is valid (`echo "$out" | jq .`).

## Related

- [Customization](./customization.md) — higher-level recipes
- [Configuration & Auth](./configuration.md) — full `config.yaml` schema
- [Environment variables](./environment.md) — `CHORD_HOOK_DEBUG`
- [Permissions & Safety](./permissions-and-safety.md) — when to use hooks vs permission rules
