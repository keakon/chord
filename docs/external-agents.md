# External coding-agent subagents

Only subagents may use an external coding agent. The main agent always uses Chord's internal runtime. Chord owns task records, mailboxes, permission decisions, and completion envelopes, and saves conversation and tool history alongside the external transcript.

The current backend is Claude Code's `stream-json` mode.

## Requirements

- Install the Claude Code CLI and sign in, or provide API credentials through `backend.env`.
- Unix is required. The process runs in the subagent's working directory; each worker's process group belongs to a dedicated supervisor process, and Chord keeps only the business pipes.
- The managed transport targets the Claude Code `stream-json` protocol, not one pinned release; no version is checked or gated at launch.

## Configuration

Create a `mode: subagent` file with a `backend` block in `~/.config/chord/agents/`. Project definitions in `.chord/agents/` override global definitions with the same name.

```markdown
---
name: claude-reviewer
description: Runs a review in an external Claude Code session.
mode: subagent
backend:
  kind: claude-code
  command: claude
  launch_args:
    - --model
    - <model>
permission:
  "*": deny
  read: allow
  grep: allow
  glob: allow
  question: allow
  shell:
    "*": ask
    "git status --short --branch": allow
    "git diff --stat": allow
---

You are a review subagent. Report findings with file paths and line numbers.
```

Replace `<model>` with your model, or omit `--model`. The Markdown body and `prompt_append` are appended as system instructions through initialization, preserving Claude's default prompt. No task input is sent before initialization succeeds.

### Backend fields

| Field | Meaning |
| --- | --- |
| `kind` | Backend adapter. Currently `claude-code`. |
| `command` | CLI executable. Defaults to `claude`, resolved through `PATH`. |
| `launch_args` | Supported ordinary options, passed as an array without shell parsing. |
| `env` | Authentication, model, and network variables, with `${VAR}` expansion from Chord's environment. Expanded credentials are not persisted. |

External backends cannot declare `model_pools`, `mcp`, `prompt_preset`, `variant`, `temperature`, or `max_tokens`.

### Reserved arguments

Chord owns the protocol, session identity, and permission channel:

```text
-p --output-format stream-json --verbose --input-format stream-json
[--session-id <uuid> | --resume <uuid>] --permission-prompt-tool stdio
--permission-mode default --safe-mode --restricted
--setting-sources "" --strict-mcp-config --mcp-config '{"mcpServers":{}}'
--tools <managed tools> --settings <managed settings JSON>
```

Supported `launch_args` are `--model`, `--effort`, `--max-turns`, `--max-budget-usd`, `--include-partial-messages`, `--replay-user-messages`, and additional tool prohibitions through `--disallowedTools` / `--disallowed-tools`. Long options also accept `--flag=value`.

Options that override the protocol, system prompt, permission mode, or session selection are rejected. `--allowedTools`, permission bypass, positional arguments, `--add-dir`, CLI worktrees, background entry points, settings sources, plugins, and MCP options are unsupported. Unclassified options are rejected too.

User tool prohibitions are merged with role-derived prohibitions and cannot remove them. Environment overrides support Anthropic credentials, models and endpoints, the Claude OAuth token, HTTP proxies, and CA certificates. Runtime injection variables are rejected.

## Permissions

Every `can_use_tool` request is translated into Chord's tool names and arguments, then evaluated against the worker role's ruleset:

- `allow`: Chord answers without a prompt.
- `deny`: Chord denies execution and saves a terminal tool record.
- `ask`: a confirmation is bound to the subagent. Timeout, cancellation, or persistence failure does not approve execution. A request left unanswered for five minutes is denied. Edited arguments require a new external tool request.

Path rules apply to file tools, including NotebookEdit's `notebook_path`. Nest the path pattern under the tool name; a top-level key is always matched against the tool name, not its arguments. For example:

```yaml
permission:
  "*": deny
  NotebookEdit:
    "notes/*.ipynb": allow
```

This allows NotebookEdit only for notebooks under `notes/`, matching the same paths as an Edit rule. A `question` rule that resolves to `ask` is treated as `allow`: the question UI itself is the confirmation.

The managed set contains Read, Edit, Write, NotebookEdit, Bash, Glob, Grep, WebFetch, WebSearch, TodoWrite, Agent, AskUserQuestion, and ExitPlanMode. Claude's `Task` spelling is accepted as an alias for `Agent`. Unmapped tools require an explicit role rule using their original name; wildcard `allow` does not allow them. `AskUserQuestion` uses Chord's question UI, answers up to four questions per call, and returns the answers to Claude. `ExitPlanMode` asks the user only when the role ruleset resolves it to allow or ask, and it does not switch Chord's main role; a rule that denies it, or an allowlist with no rule naming it, hides the tool instead. In `default` permission mode Claude may reject it as unavailable before any host authorization.

A tool the role ruleset disables is passed to the CLI through `--disallowedTools`, so it never appears in Claude's own tool list; the calls that remain are still gated one by one. That decision follows the normal rule matching order: Chord hides a tool when the last rule matching its name is a wildcard deny, so a deny rule carrying a specific path pattern does not hide it. A wildcard `deny` fallback therefore belongs before the specific rules that re-enable tools, as in the example above.

Claude can also reject a tool on its own side before requesting host authorization — some tools are unavailable in the CLI's own managed mode. Chord does not bypass these restrictions.

Chord disables project and user customization, hooks, external MCP, plugins, and sandbox auto-approval. The command and administrator policy remain trusted boundaries; role permissions are not an operating-system sandbox. Shell rules use last-match precedence: `git *` is not read-only, and `go test` executes repository code.

## Quota and credentials

| Route | Do | Do not |
| --- | --- | --- |
| CLI login (`claude auth login`) | Keep the normal HOME and login credentials. | Override login with an empty API key or change HOME. |
| API key | Set an `ANTHROPIC_API_KEY` for the intended Console organization. | Store plaintext keys in agent files or use Chord options to select a quota. |

Credential eligibility, quota, and billing follow the account's current policy. Chord does not provide quota or guarantee that credential routes can be combined. Check authentication and usage in the CLI and account console.

## Recipes

Read-only review:

```yaml
backend:
  launch_args:
    - --disallowedTools
    - Edit,Write
permission:
  "*": deny
  read: allow
  grep: allow
  shell:
    "*": ask
    "git status --short --branch": allow
    "git diff --stat": allow
```

Bounded batch runs:

```yaml
backend:
  launch_args:
    - --max-turns
    - "30"
    - --max-budget-usd
    - "5"
```

## Differences from native subagents

- The external model call does not use Chord model pools or key rotation and does not produce Chord usage statistics. While you view the worker, the info panel omits MODEL and hides USAGE, TIME, SKILLS, LSP, and MCP — the last two describe Chord's own integrations, while the external CLI manages its own — and the status bar omits its model, tier, and usage pills; `/models` reports that model pools do not apply to external agents. The sidebar marks the worker's name with an `[external]` suffix.
- Chord sends `interrupt`, closes input, and gives the CLI a short grace period to flush its transcript and exit before terminating the process group. Existing changes are not rolled back; tools without a receipt show an unknown outcome.
- Every external worker is backed by a dedicated supervisor process that owns its process group. If Chord is forcibly killed, the supervisor sees the lifecycle channel close and tears that group down, leaving a lifecycle receipt named after the run under `external-supervisor/` in the state directory. Chord writes a receipt for every run, including runs it settled itself: reading the exit report does not prove Chord persisted it. Receipts older than 30 days are pruned. If Chord and the supervisor are killed together nothing guarantees cleanup, so the receipt may be missing or may describe a run that never proved cleanup. Resume does not clean up orphaned processes; it only accepts the receipt of the exact run the task last launched.
- Quitting Chord while an external worker is still running waits only briefly for the stop sequence; the supervisor finishes the teardown after Chord exits, and the next startup records the task as interrupted by the session shutdown.
- Pause is not supported. Stop and restart the task instead.
- Only one input runs at a time. Incoming messages are durably queued; messages received after sealing run in a new attempt on the same task after the old process exits and settles. Targeted messages to completed or failed tasks resume their session. Cancelled tasks must be delegated again.
- Hooks, skills, and compaction do not apply to the external process. Result contracts do: the declared `result_schema` is delivered to the process as part of its role instructions at initialization, and the final answer must be a single JSON object satisfying it. An answer that is not a JSON object, or that misses what the schema requires, fails the task with the violations named.
- Deleting a Chord session does not delete the external session transcript under your home directory; Chord lists the pinned session ids it leaves behind.
- External subagents accept text input only, not images or PDFs.
- Resume requires an existing external session, proven old-process termination, no input with an unknown outcome, and unchanged command, arguments, environment keys, role instructions, and a working directory that still exists with the same binding. Credentials are resolved again from the current environment. After a crash, the task persists the supervisor run id before each launch; resume reads the receipt named after that exact run and accepts it only when it records proven process-group cleanup. A missing, malformed, version-mismatched, or unproven receipt keeps resume refused.
- Chord's workspace lease serializes writes only for tools Chord executes itself. Tools of an external backend run inside the CLI process, so writes from an external worker and any other writer are not serialized against each other; give overlapping work separate checkouts.
- Completion reports the checkout path, branch, and base commit, plus a diff stat against that base when the checkout was clean at startup. If the checkout was already dirty or Chord could not inspect it, the report explains why and withholds the diff stat instead of claiming a clean baseline. Chord does not track which files the external agent changed: the checkout is shared and other processes may write it too, so inspect the diff against the reported base commit instead.
