# Permissions & Safety

Chord is a coding agent that can read files, modify files, execute commands, and call external tools. Before public or shared use, make sure you understand its permission model and safety boundaries.

## Principles

- Keep high-risk actions as `ask` by default
- Use `deny` for actions that are clearly dangerous or unnecessary
- Use `allow` only for low-risk, predictable actions
- Put API keys in `auth.yaml` or environment variables; do not commit them into project files

## Permission model

Typical permission states:

- `allow`: auto-allow
- `ask`: require confirmation before execution
- `deny`: reject directly

Rules are keyed by tool name; the full list of built-in tool names is in [Built-in tools](./tools.md).

In the TUI confirmation dialog, `A` opens the add-rule picker for the current tool call; press `Enter` in that picker to save the selected rule and allow the current call.

Permissions can be defined in Agent config. Start with this recommended personal-development template, then tighten or relax it for your project's risk profile:

```yaml
permission:
  "*": allow
  handoff: deny
  delegate: deny
  delete: ask
  web_fetch:
    "http://localhost:8000/*": ask
    "http://169.254.169.254/*": deny
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
```

This means: allow most tools by default; disable `handoff` and `delegate`; require confirmation for file deletion, selected WebFetch URL patterns, and common high-risk shell/git commands. Permission rules use “last match wins”, so the more specific `web_fetch` and `shell` rules above override the top-level `"*": allow`. This is reasonable for a single-user trusted workspace; shared repositories, team services, or automated headless deployments should tighten it further. This page starts from `"*": allow` as a trusted-workspace baseline; for a least-privilege baseline instead, the `builder` agent in [Configuration — Agent config](./configuration.md#agent-config) starts from `"*": deny` and opts in only to the tools a role needs. Pick whichever baseline matches your trust model.

### Special permission semantics

Most tools use the literal `allow` / `ask` / `deny` meaning above, but a few orchestration tools intentionally have extra coupling so permission settings match the workflow Chord can safely run:

- `edit` and `patch` are one file-editing tool family with two model-facing formats. A rule for one editor applies to the other editor when the other editor has no explicit same-tool rule. This includes `deny`: `*: allow` followed by `edit: deny` disables both `edit` and `patch`, because `patch` inherits the edit-family denial. Configure both names when you want different behavior per format. For example, `edit: allow` plus `patch: deny` disables `patch` but keeps `edit` available, so GPT/o-series models fall back to `edit`; conversely, `patch: allow` plus `edit: deny` keeps `patch` available for non-GPT models that would otherwise prefer `edit`.
- `handoff` and `done` are treated as control gates. Setting either one to `deny` hides or disables that workflow. Setting it to `allow` or `ask` makes the workflow available; Chord may still show local confirmation at the actual handoff/finish point (for example the loop `done` confirmation). This means `ask` is not a second, stronger workflow mode for these tools: it mainly keeps the tool visible/available while preserving Chord's built-in confirmation gate. The trade-off avoids confusing the model with an available control tool that is later impossible to complete, while still preventing silent role switches or premature loop exits.
- `delegate` matches its `agent_type` argument, so a role can restrict delegation to selected SubAgent definitions. For example, the ordered rules below deny every target except `reviewer` and require confirmation before delegating to `tester`:

  ```yaml
  permission:
    delegate:
      "*": deny
      reviewer: allow
      tester: ask
  ```

  Denied targets are omitted from the Delegate tool schema and coordination prompt. If every configured SubAgent target is denied, Delegate is hidden. Permission rules use last-match-wins ordering, so put the wildcard fallback before the specific target rules.
- `delegate` also controls the delegation workflow as a group. If it is entirely disabled by an effective wildcard `deny`, Chord disables SubAgent cancellation via `cancel`, hides nested `delegate`/`cancel` from SubAgents, and limits SubAgent `notify` to owner-only follow-up instead of arbitrary target routing. The reason is that cancelling or targeting other delegated tasks is part of managing delegated workstreams; allowing those pieces while disabling delegation would create a partial control plane that can interfere with work the role is not allowed to orchestrate.
- `cancel` therefore depends on `delegate`: even if `cancel: allow` is configured, `cancel` is denied when `delegate` is disabled. To allow a role to cancel delegated work, enable both `delegate` and `cancel`.
- `question: ask` is normalized to `allow`. The `question` tool already asks the user a structured question and waits for their answer, so adding a separate permission confirmation before asking the question would create a redundant prompt without reducing the risk of the final decision.
- YOLO does not override `handoff`, `delegate`, `cancel`, or `done`; those control-tool permissions remain enforced even when ordinary file/shell/web permissions are bypassed. Under YOLO, a broad `"*": allow` rule does not grant these protected tools by itself; configure each protected tool directly when the role should use it.

> Permissions are Agent-level configuration, not a simple global switch.

For `shell`, a specific `allow` pattern such as `"git *": allow` does not auto-allow compound commands containing unquoted shell separators (`;`, `&&`, `||`, `|`, `&`, or newlines). Those calls fall through to the next matching rule, typically `ask` or `deny`. Use this as a safety backstop, not as shell sandboxing; keep broad rules like `shell: allow` or `shell: { "*": allow }` for only fully trusted roles.

## Shell / shell risk

`shell` can execute system commands and should be treated carefully. `shell` and `spawn` are intentionally non-interactive: Chord does not wire model-controlled stdin into child processes, Unix child processes run without a controlling TTY, and high-confidence interactive commands are rejected before execution. Plain stdin reads such as shell `read`/`select` observe EOF instead of waiting for model input; provide data explicitly with a pipe, here-doc, file, or arguments when a command expects input. Login wizards, terminal editors, pagers/full-screen TUIs, password prompts, and commands that require `/dev/tty` should be run manually in a real terminal or rewritten with explicit non-interactive input/flags.

Platform notes for `shell` / `spawn`:

- On Unix, Chord starts child processes in a new session and cleans up by process group on timeout/cancellation.
- On Windows, Chord still keeps `shell` / `spawn` non-interactive, but there is no Unix-equivalent `setsid`/process-group control path here; timeout/cancellation cleanup falls back to direct process termination and may be less complete for descendant processes.

Common rewrites:

- Use `git commit -m "message"` or `git commit -F file` instead of editor-driven `git commit`
- For amend flows that should preserve the existing message, use explicit non-editor forms such as `git commit --amend --no-edit` or `git commit --amend -C HEAD`
- Avoid interactive Git patch workflows (`git add -p`, `git commit -p`, `git stash -p`) from `shell` / `spawn`; stage explicit pathspecs or run them manually
- Remove TTY allocation flags from container commands (`docker exec -it`, `docker run -t`, `podman run -t`, `kubectl exec -it`) unless you are running them manually in a real terminal
- Use `npm init -y` / `--yes` or provide all required options explicitly
- Use `sudo -n` when you want sudo to fail non-interactively instead of prompting
- Pipe input or use a here-doc when a command truly accepts non-interactive stdin

Recommendations:

- Keep file deletion, bulk rewrites, network downloads, and database operations as `ask` or `deny` by default
- Use `web_fetch` URL patterns when you want to gate local/private services or sensitive endpoints, for example `web_fetch: { "http://localhost:8000/*": ask }`
- Set `allow` only for a small set of predictable development commands
- Do not treat permission matching as a security sandbox

**Important**: Chord's permission matching is product-level risk control, not OS-level isolation or a security sandbox.

## File modification risk

`edit`, `write`, and `delete` directly modify workspace files. `edit` is for local changes to one existing file, `write` creates or intentionally replaces a whole file, and `delete` removes whole files. `read`, `view_image`, and `grep` are read-only, but they still operate on local filesystem paths and can expose local file contents to the transcript/model context. Path-reading tools intentionally reject blocked device-style paths such as standard-stream device files (`/dev/stdin`, `/dev/stdout`, `/dev/stderr`, and similar) instead of treating them as normal files. Local text file tools prefer UTF-8 or BOM-marked Unicode (UTF-8/UTF-16/UTF-32) and retain constrained support for common regional encodings such as GB18030, Big5, and Shift-JIS. Ambiguous or unsupported encodings fail fast; `web_fetch` still honors declared HTTP response charsets.

If a file has changed since Chord last observed it in the session, Chord warns instead of rejecting when the tool can still validate the current contents. Risky non-empty pre-write contents are backed up under the active session directory and the tool result includes the backup path. Empty files and non-risky continuous agent-owned edits do not create backups. Backups are capped at 10 per path, 200 per session, 10 MiB per file, and 50 MiB per session; if a required backup would exceed those limits or otherwise fail, the edit can still proceed but the tool result says no backup was created and why. Session deletion/cleanup removes these backups with the session directory.

Recommendations:

- Use Git in important repositories so changes are easy to review and roll back
- Keep production config, deployment scripts, and secret files as `ask`
- Use finer-grained rules for generated files or test artifact directories

## Credentials and config

- Store API keys in `~/.config/chord/auth.yaml` when possible
- Environment-variable references are also supported
- Do not put real secrets in example configs, scripts, or project repositories
- Restrict `auth.yaml` permissions, for example `chmod 600 ~/.config/chord/auth.yaml`

## Headless boundary

`chord headless` is suitable as a lower-level control plane for bots/gateways, but it does not provide multi-tenant isolation, browser security boundaries, or complete permission hosting by itself.

If you connect it to a chat platform, automation system, or team service, enforce additional controls in the outer layer:

- Which working directories may be accessed
- Which commands may be called
- Who can approve high-risk operations
- How events are audited and retained

## Network and external integrations

Chord can integrate with:

- provider APIs
- LSP
- MCP
- Hooks
- local shell commands

Each capability expands the runtime boundary. Before enabling one, confirm:

- Whether you really need it
- Which resources it can read or write
- How to roll back or disable it when it fails
- Whether sensitive data may be sent to an external service

## Usage recommendations

- Start with a minimal provider config and minimal permissions
- Observe behavior in a personal repository before gradually relaxing permissions
- In shared repositories or team environments, do not globally `allow` by default
- Expose only the minimum necessary Hook and MCP tools

## Related

- [Configuration & Auth](./configuration.md)
- [Customization](./customization.md)
- [Headless](./headless.md)
