# Permissions & Safety

Chord is a coding agent that can read files, modify files, execute commands, and call external tools. Inspect actions before approving them. Permission rules control risk; they are not an operating-system sandbox.

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

A rule that names a nonexistent tool matches nothing, so a typo silently leaves that tool unmatched instead of failing. Background work runs through `shell` (with `run_in_background: true`) plus the `job_output`, `job_list`, and `job_kill` tools.

In the TUI confirmation dialog, `M` opens the add-rule picker for the current tool call; press `Enter` in that picker to save the selected rule and allow the current call. For `delete`, the picker suggests reusable parent-directory rules instead of one-off exact-file rules. Directories covering more paths that still need approval appear first, `*` (any delete path) is always available, and `**` (anything under the current working directory) is also available when every requested path is inside that directory. The broad `**` and `*` choices are never selected by default.

Permissions can be defined in Agent config. Start with this recommended personal-development template, then tighten or relax it for your project's risk profile:

```yaml
permission:
  "*": allow
  handoff: deny
  delegate: deny
  delete: ask
  web_fetch:
    "localhost:8000": ask
    "169.254.0.0/16": deny
    "10.0.0.0/8": deny
    "192.168.0.0/16": deny
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

This means: allow most tools by default; disable `handoff` and `delegate`; require confirmation for file deletion, selected WebFetch URL patterns, and common high-risk shell/git commands. Permission rules use "last match wins", so the more specific `web_fetch` and `shell` rules above override the top-level `"*": allow`. This is reasonable for a single-user trusted workspace; shared repositories, team services, or automated headless deployments should tighten it further. This page starts from `"*": allow` as a trusted-workspace baseline; for a least-privilege baseline instead, the `builder` agent in [Configuration: Agent config](./configuration.md#agent-config) starts from `"*": deny` and opts in only to the tools a role needs.

Permission matching examines the tool call and the session working directory (the directory the tool executes in). For `shell`, only the command string is matched — a `workdir` argument does not participate. For file tools (`read`, `write`, `edit`, `apply_patch`, `delete`, `view_image`), the target path is normalized against the working directory before rules are matched: a path inside the working directory is matched in cwd-relative form (so `foo.go`, `./foo.go`, and an absolute spelling of the same file all hit the same rule), while a path outside the working directory stays absolute.

File-tool rule patterns are scoped by their form:

- `*` matches every path spelling: the same "any path" it always meant.
- A relative pattern (`**`, `src/**`, `tmp/*`) is anchored to the working directory and only matches in-cwd paths. `**` therefore means "everything under the current directory"; `./**` is accepted as the same thing.
- An absolute pattern (`/Users/me/other/**`, `~/other/**`, `/**`) only matches out-of-cwd absolute paths. `/**` means "every absolute path"; combining it with `**` covers the same ground as `*`. On Windows, home-relative patterns accept either separator (`~\other\**` and `~/other/**`).
- An absolute rule no longer matches a file inside the working directory — write the in-cwd rule in relative form instead.

Shell rules only constrain the submitted command string: they do not sandbox the command's filesystem effects, and an allowed command can still `cd` elsewhere, invoke another program, or act on an absolute path. Use narrow shell patterns for approval policy and an OS-level sandbox when actual filesystem confinement is required.

### WebFetch target matching

`web_fetch` rule patterns use network-aware host matching. A pattern has the
shape `host[:port]`:

- **host**: a domain (`example.com`), a domain wildcard (`*.internal`, `*`), a literal IP (`127.0.0.1`, `::1`), or a CIDR range (`10.0.0.0/8`, `169.254.0.0/16`, `fd00::/8`). Bracket IPv6 addresses and IPv6 CIDRs when specifying a port, for example `[fd00::/8]:443`.
- **port**: omit it (or use `*`) for any; use a single port (`8080`) or a range (`8000-9000`). When the requested URL omits its port, the port defaults from the URL scheme (http→80, https→443).
- Scheme and path patterns are not supported.

```yaml
web_fetch:
  "*": allow                  # default: allow everything
  "0.0.0.0/8": deny
  "10.0.0.0/8": deny
  "127.0.0.0/8": deny
  "169.254.0.0/16": deny      # cloud metadata endpoint
  "192.168.0.0/16": deny
  "*:8000-9000": ask         # any host on these ports needs confirmation
  "*.internal": deny         # internal domains
```

Matching happens **before the request is sent**, against the URL the model supplied.
It is not re-checked against the resolved connection IP, so a hostname that resolves
to an internal address, or an HTTP redirect to one, is **not** blocked by an IP/CIDR
rule. Treat these rules as intent-level gating, not a network sandbox.

### Special permission semantics

Most tools use the literal `allow` / `ask` / `deny` meaning above, but a few orchestration tools intentionally have extra coupling so permission settings match the workflow Chord can safely run:

- `edit` and `apply_patch` are one file-editing tool family with two model-facing formats (`patch` is accepted as a legacy alias for `apply_patch`). A rule for one editor applies to the other editor when the other editor has no explicit same-tool rule. This includes `deny`: `*: allow` followed by `edit: deny` disables both `edit` and `apply_patch`, because `apply_patch` inherits the edit-family denial. Configure both names when you want different behavior per format. For example, `edit: allow` plus `apply_patch: deny` disables `apply_patch` but keeps `edit` available, so GPT/o-series models fall back to `edit`; conversely, `apply_patch: allow` plus `edit: deny` keeps `apply_patch` available for non-GPT models that would otherwise prefer `edit`.
- `handoff` and `done` are treated as control gates. Setting either one to `deny` hides or disables that workflow. Setting it to `allow` or `ask` makes the workflow available; Chord may still show local confirmation at the actual handoff/finish point (for example the loop `done` confirmation). This means `ask` is not a second, stronger workflow mode for these tools: it mainly keeps the tool visible/available while preserving Chord's built-in confirmation gate. The trade-off avoids confusing the model with an available control tool that is later impossible to complete, while still preventing silent role switches or premature loop exits.
- `done` is the loop workflow's exit signal, and Chord mounts it **only while a loop is running**. An ordinary session does not carry its definition at all, which keeps it out of every request and spares the model the choice between answering directly and calling a completion tool. Entering loop mode mounts it: as an additional tool where the provider can accept one mid-session (Responses-family models and Kimi dynamic tools), and through a one-time tool-surface rebuild (costing one prompt-cache miss) everywhere else. Leaving loop mode takes it back off. `/loop on` is refused with a toast when a rule denies `done`, so `done: deny` reserves loop termination for you.
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
- Two control tools are exempt from wildcard-only rules, because the feature that makes them reachable is itself the authorization: `compact_context` (registered only when `context.compaction.model_driven` is enabled) and `done` (mounted only while a loop is running). An allowlist role's `"*": deny` neither hides them nor rejects their calls: otherwise enabling model-driven compaction or starting a loop would silently do nothing until you also knew to allow an internal tool name. Only a rule that *names* the tool overrides this: `deny` removes it, `ask` keeps it and confirms each call, `allow` matches the default. Narrow globs such as `compact_*` count as naming it, and so do rules written with an argument pattern, since neither tool takes a permission-matching argument. Neither has an external side effect (one shrinks the context, the other ends a loop), so a wildcard rule has no capability to protect here.
- YOLO removes the high-frequency confirmation friction of ordinary work: file edits and shell commands. While it is on, the main agent's ordinary tools skip permission checks entirely: `ask` rules do not raise a confirmation and `deny` rules do not block. The bypass only widens permissions (nothing that works with YOLO off becomes unavailable while it is on), and switching YOLO off restores the original permissions. YOLO is **not** a redefinition of the role's boundary. Control tools change the agent topology or the session lifecycle rather than the risk of a single operation, and they are called rarely enough that confirming them was never the friction YOLO targets, so under YOLO they keep following their configured rules: `handoff`, `delegate`, `cancel`, `done`, and `compact_context`. They split into two groups:
  - `handoff`, `delegate`, and `cancel` grant the role a capability it did not otherwise have: handing the session to another role, spawning delegated work, cancelling work it does not own. Under YOLO they keep following their configured rules, with exactly one relaxation: an `ask` rule passes without raising the shared confirmation dialog. `allow` stays allowed, `deny` keeps rejecting, and wildcard defaults behave as they do without YOLO: a broad `"*": allow` keeps these tools usable, an allowlist's `"*": deny` keeps them denied, and a ruleset that never mentions them (or no rules at all) resolves the same way it would with YOLO off. YOLO therefore never adds an orchestration capability the rules do not already grant: a single-agent role such as `builder` stays single-agent while YOLO is on because its own rules deny `handoff` and `delegate`, and those denials keep applying. Turning YOLO on to skip edit confirmations is not a statement that the role should now be orchestrating SubAgents.
  - `done` and `compact_context` only end or shrink the current unit of work, so YOLO leaves their dedicated semantics untouched: each behaves exactly as it does without YOLO, and a rule that names one of them keeps its effect — `deny` still removes the tool or workflow, and an explicit `ask` on `compact_context` still confirms each call.
  - SubAgents evaluate their own rules and inherit the mode at execution time: while YOLO is on, their `ask` decisions (ordinary tools and the mechanism tools alike) pass without raising the shared confirmation dialog, while their `deny` rules keep rejecting (a read-only worker keeps its `write: deny`). The inheritance is live: switching YOLO off restores their confirmations on later calls.

> Permissions are Agent-level configuration, not a simple global switch.

For `shell`, a specific `allow` pattern such as `"git *": allow` does not auto-allow compound commands containing unquoted shell separators (`;`, `&&`, `||`, `|`, `&`, or newlines). Those calls fall through to the next matching rule, typically `ask` or `deny`. Use this as a safety backstop, not as shell sandboxing; keep broad rules like `shell: allow` or `shell: { "*": allow }` for only fully trusted roles.

A command-specific `allow` does, however, cover the full capability of that command, including output redirections and inline environment-assignment prefixes. If `echo *` is allowed, then `echo secret > ~/.bashrc`, `echo x >> file`, `data > /dev/tcp/host/port`, and `LD_PRELOAD=./x.so echo hi` are all allowed — the redirection target and the environment prefix are part of that single shell command, not a separate tool call, so they are not matched or gated on their own. Grant a command-level `allow` only to commands whose worst case (arbitrary file writes via redirection, an overridden environment) you accept; otherwise keep them at `ask`.

## Shell / shell risk

`shell` can execute system commands and should be treated carefully. `shell` is intentionally non-interactive whether the command runs in the foreground or as a background job: Chord does not wire model-controlled stdin into child processes, Unix child processes run without a controlling TTY, and high-confidence interactive commands are rejected before execution. Plain stdin reads such as shell `read`/`select` observe EOF instead of waiting for model input; provide data explicitly with a pipe, here-doc, file, or arguments when a command expects input. Login wizards, terminal editors, pagers/full-screen TUIs, password prompts, and commands that require `/dev/tty` should be run manually in a real terminal or rewritten with explicit non-interactive input/flags.

Platform notes for `shell` (foreground or background job):

- On Unix, Chord starts child processes in a new session and cleans up by process group on timeout/cancellation.
- On Windows, Chord still keeps `shell` non-interactive for foreground commands and background jobs, but there is no Unix-equivalent `setsid`/process-group control path here; timeout/cancellation cleanup falls back to direct process termination and may be less complete for descendant processes.

Common rewrites:

- Use `git commit -m "message"` or `git commit -F file` instead of editor-driven `git commit`
- For amend flows that should preserve the existing message, use explicit non-editor forms such as `git commit --amend --no-edit` or `git commit --amend -C HEAD`
- Avoid interactive Git patch workflows (`git add -p`, `git commit -p`, `git stash -p`) from `shell`; stage explicit pathspecs or run them manually
- Remove TTY allocation flags from container commands (`docker exec -it`, `docker run -t`, `podman run -t`, `kubectl exec -it`) unless you are running them manually in a real terminal
- Use `npm init -y` / `--yes` or provide all required options explicitly
- Use `sudo -n` when you want sudo to fail non-interactively instead of prompting
- Pipe input or use a here-doc when a command truly accepts non-interactive stdin

Recommendations:

- Keep file deletion, bulk rewrites, network downloads, and database operations as `ask` or `deny` by default
- Use `web_fetch` patterns to gate local/private services or sensitive endpoints — by host/port (`web_fetch: { "localhost:8000": ask }`) or by address range (`web_fetch: { "169.254.0.0/16": deny, "*:8000-9000": ask }`)
- Set `allow` only for a small set of predictable development commands
- Do not treat permission matching as a security sandbox

**Important**: Chord's permission matching is product-level risk control, not OS-level isolation or a security sandbox.

## File modification risk

File tools act directly on the workspace; they are not a dry run. Use Git for important repositories and keep production configuration, deployment scripts, and secret files subject to `ask`.

### Which actions change files

| Action | Effect |
| --- | --- |
| `write` | Create a file or replace an entire existing regular file. |
| `edit` / `apply_patch` | Change local content; patches can also add, move, and delete files. Independent file groups may succeed partially, so inspect applied changes before retrying. |
| `delete` | Delete a file without requiring a complete read first. Deleting a symbolic link removes the link, not its target; `write` refuses to follow symbolic links. |
| `read` / `view_image` / `grep` | Leave files unchanged, but their content can enter the conversation and model requests. Read-only does not prevent sensitive data from leaving the machine. |

### Checks and backups before writing

- **Whole-file replacement**: `write` does not refuse solely because the model has not read the current version. Unread, partially read, or subsequently changed content is backed up when possible, with the location reported in the result. Content matching the model's most recent write or edit needs no additional backup.
- **Unreadable previous content**: the write may still proceed without a backup; the result warns about the risk. A new file does not require a previous-version backup.
- **Local edits**: `edit` and `apply_patch` check their anchors against the current file. If a changed file still has valid anchors, editing may proceed after a warning, with a best-effort backup of risky non-empty previous content.
- **Deletion**: if the model has not observed the file's current content, Chord attempts to back up its exact disk bytes, including empty files. Backups do not follow symbolic links.

Backups are limited to 10 per path, 200 per session, 10 MiB per file, and 50 MiB per session. A failed or oversized backup does not stop a local edit, and the failure may appear only in logs. **Session backups are not a substitute for version control; cleaning up a session also deletes its backups.**

### File-reading limits

Path-reading tools reject restricted device paths such as `/dev/stdin`, `/dev/stdout`, and `/dev/stderr`. Local text tools prefer UTF-8 or BOM-marked Unicode, with limited support for encodings such as GB18030, Big5, and Shift-JIS. Ambiguous or unsupported encodings fail with an error. `web_fetch` decodes the character set declared by the HTTP response.

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

- In shared repositories or team environments, do not globally `allow` by default
- Expose only the minimum necessary Hook and MCP tools

## Related

- [Configuration & Auth](./configuration.md)
- [Customization](./customization.md)
- [Headless](./headless.md)
