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

Permissions can be defined in Agent config. Start with this recommended personal-development template, then tighten or relax it for your project's risk profile:

```yaml
permission:
  "*": allow
  Handoff: deny
  Delegate: deny
  Delete: ask
  Bash:
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

This means: allow most tools by default; disable `Handoff` and `Delegate`; require confirmation for file deletion and common high-risk shell/git commands. Permission rules use “last match wins”, so the more specific `Bash` rules above override the top-level `"*": allow`. This is reasonable for a single-user trusted workspace; shared repositories, team services, or automated headless deployments should tighten it further.

> Permissions are Agent-level configuration, not a simple global switch.

## Bash / shell risk

`Bash` can execute system commands and should be treated carefully.

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
