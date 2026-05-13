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

In the TUI confirmation dialog, `A` opens the add-rule picker for the current tool call; press `Enter` in that picker to save the selected rule and allow the current call.

Permissions can be defined in Agent config. Start with this recommended personal-development template, then tighten or relax it for your project's risk profile:

```yaml
permission:
  "*": allow
  Handoff: deny
  Delegate: deny
  Delete: ask
  Shell:
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

This means: allow most tools by default; disable `Handoff` and `Delegate`; require confirmation for file deletion and common high-risk shell/git commands. Permission rules use “last match wins”, so the more specific `Shell` rules above override the top-level `"*": allow`. This is reasonable for a single-user trusted workspace; shared repositories, team services, or automated headless deployments should tighten it further.

> Permissions are Agent-level configuration, not a simple global switch.

## Shell / shell risk

`Shell` can execute system commands and should be treated carefully. `Shell` and `Spawn` are intentionally non-interactive: Chord does not wire model-controlled stdin into child processes, Unix child processes run without a controlling TTY, and high-confidence interactive commands are rejected before execution. Plain stdin reads such as shell `read`/`select` observe EOF instead of waiting for model input; provide data explicitly with a pipe, here-doc, file, or arguments when a command expects input. Login wizards, terminal editors, pagers/full-screen TUIs, password prompts, and commands that require `/dev/tty` should be run manually in a real terminal or rewritten with explicit non-interactive input/flags.

Platform notes for `Shell` / `Spawn`:

- On Unix, Chord starts child processes in a new session and cleans up by process group on timeout/cancellation.
- On Windows, Chord still keeps `Shell` / `Spawn` non-interactive, but there is no Unix-equivalent `setsid`/process-group control path here; timeout/cancellation cleanup falls back to direct process termination and may be less complete for descendant processes.

Common rewrites:

- Use `git commit -m "message"` or `git commit -F file` instead of editor-driven `git commit`
- For amend flows that should preserve the existing message, use explicit non-editor forms such as `git commit --amend --no-edit` or `git commit --amend -C HEAD`
- Avoid interactive Git patch workflows (`git add -p`, `git commit -p`, `git stash -p`) from `Shell` / `Spawn`; stage explicit pathspecs or run them manually
- Remove TTY allocation flags from container commands (`docker exec -it`, `docker run -t`, `podman run -t`, `kubectl exec -it`) unless you are running them manually in a real terminal
- Use `npm init -y` / `--yes` or provide all required options explicitly
- Use `sudo -n` when you want sudo to fail non-interactively instead of prompting
- Pipe input or use a here-doc when a command truly accepts non-interactive stdin

Recommendations:

- Keep file deletion, bulk rewrites, network downloads, and database operations as `ask` or `deny` by default
- Set `allow` only for a small set of predictable development commands
- Do not treat permission matching as a security sandbox

**Important**: Chord's permission matching is product-level risk control, not OS-level isolation or a security sandbox.

## File modification risk

`Write`, `Edit`, and `Delete` directly modify workspace files.

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
