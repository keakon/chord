# Paths and Files

This page describes every file and directory Chord reads or writes, and how to safely clean them up.

## Three layers

| Layer            | Default path                                            | Purpose                                                                                       |
| ---------------- | ------------------------------------------------------- | --------------------------------------------------------------------------------------------- |
| **Config home**  | `$XDG_CONFIG_HOME/chord` or `~/.config/chord`           | Editable user config: providers, model pools, custom agents, custom skills, custom commands   |
| **State dir**    | `$XDG_STATE_HOME/chord` or `~/.local/state/chord`       | Durable runtime state you would not want to lose: sessions, exports, logs, project registry, worktrees |
| **Cache dir**    | `$XDG_CACHE_HOME/chord` or `~/.cache/chord`             | Rebuildable runtime caches; can be deleted at any time                                        |

All three can be moved by environment variable, CLI flag, or `config.yaml` `paths:` — see [Environment variables](./environment.md) and [CLI flags](./cli.md#global-flags).

## Config home — `~/.config/chord/`

You edit these files. Treat them as source.

On the first root `chord` run, if global `config.yaml` is missing and Chord can get a controlling TTY, it starts a one-time setup wizard and then prints the exact resolved paths for `config.yaml` and `auth.yaml`. This is especially useful when you launch with `--config-home`, `CHORD_CONFIG_HOME`, or on Windows where `~` is not the most discoverable form.

```text
~/.config/chord/
├── config.yaml            # global chord config
├── auth.yaml              # API keys / OAuth tokens (chmod 600 recommended)
├── auth.state.json        # machine-managed shared OAuth runtime state / quota cache
├── agents/                # global agent definitions (.md or .yaml)
├── commands/              # global custom slash commands (.md per command)
└── skills/                # global skills, each as <name>/SKILL.md
```

For the `config.yaml` schema, see [Configuration & Auth](./configuration.md). For agents, see [Customization — Agents](./customization.md#agents). For skills, see [Customization — Skills](./customization.md#skills). For custom slash commands, see [Customization — Custom slash commands](./customization.md#custom-slash-commands).

`auth.state.json` is a shared runtime cache for OAuth status, Codex quota snapshots, reset times, and warm-up timestamps. Chord manages it automatically; users normally should not hand-edit it. Deleting it is safe, but Chord will lose restart-stable cached quota ordering until warm-up repopulates it.

## State dir — `~/.local/state/chord/`

Chord writes here. Lose it and you lose history.

```text
~/.local/state/chord/
├── sessions/
│   └── <project-key>/
│       ├── project.json                # canonical-root, display-name, timestamps
│       └── <session-id>/               # one session
│           ├── messages.jsonl
│           ├── traces/
│           │   └── llm-trace.jsonl     # lightweight per-request LLM trace (always on)
│           └── …                       # additional session artifacts
├── projects/
│   └── <project-key>.json              # registry pointer for cross-project lookup
├── exports/
│   └── <project-key>/                  # `/export` output (markdown / JSON)
├── worktrees/
│   └── <repo-id>/
│       └── <slug>/                     # chord-managed git worktree (outside the repo)
└── logs/
    ├── chord.log                       # current log
    ├── chord.log.1                     # rotated
    ├── chord.log.2                     # rotated
    └── tui-dumps/                      # `Ctrl+G` outputs
```

### `<project-key>` — what is it?

Chord identifies a project by its canonical filesystem root, then derives a stable, sanitized key — for example `HOME-projects-chord` for `~/projects/chord`. If two projects collide on the sanitized key, Chord appends an 8-character fingerprint to disambiguate. The full canonical root is stored alongside the key in `project.json`, so the registry stays unambiguous even when paths look similar.

Sessions, runtime cache, and exports are all keyed on this — that is how a fresh `chord` started in `~/projects/chord` finds the previous session for the same project.

### Worktrees

`chord --worktree <name>` creates a chord-managed git worktree under `worktrees/<repo-id>/<slug>` **outside the original repository**, with its own project key. Each chord-managed worktree therefore has isolated sessions, cache, and exports.

To remove a worktree (and only its chord-side data), use `chord worktree remove <name>` — see [CLI — chord worktree](./cli.md#chord-worktree). Manually deleting the worktree directory is not recommended; you would leave orphan registry entries that `chord cleanup project` would later flag.

## Cache dir — `~/.cache/chord/`

Everything here is rebuildable; deleting it is safe at any time, at the cost of one re-warmup.

```text
~/.cache/chord/
└── runtime/
    └── session-cache/
        └── <project-key>/
            └── <session-id>/           # in-memory session snapshots, recovery state
```

## Project-local — `<project>/.chord/`

When `chord` runs in a project for the first time, it ensures the project root has a `.chord/` directory. This is the only chord directory that lives **inside** the user's repository.

```text
<project>/.chord/
├── config.yaml            # project-level overrides (merged with global ~/.config/chord/config.yaml)
├── agents/                # project-level agents (override or extend global agents)
├── commands/              # project-level custom slash commands
└── skills/                # project-level skills
```

Project-level files have higher priority than global ones (same-name keys override). It is normal — and useful — to commit `.chord/` into your repository so that team members share the same agent setup and slash commands.

`auth.yaml` is **never** read from `.chord/`: credentials always live in `~/.config/chord/auth.yaml`.

## Logs

| File                                  | What it contains                                             |
| ------------------------------------- | ------------------------------------------------------------ |
| `<state-dir>/logs/chord.log`          | Current run log (golog plain text)                           |
| `<state-dir>/logs/chord.log.1`        | Previous rotation                                            |
| `<state-dir>/logs/chord.log.2`        | Older rotation                                               |
| `<state-dir>/logs/tui-dumps/`         | `Ctrl+G` snapshots for bug reports                           |

Override the directory with `--logs-dir <path>` or `CHORD_LOGS_DIR=<path>`.

A typical log line looks like:

```text
[I 2026-05-02 12:00:00 file:123 pwd=/path pid=1234 sid=20260502015258426] message key=value
```

Treat key-value fragments as human-readable text, not as a stable structured-logging schema.

## Maintenance

Use `chord cleanup` rather than `rm -rf` — it knows which paths are safe and which would orphan registry entries.

| Goal                              | Command                                                |
| --------------------------------- | ------------------------------------------------------ |
| See how big each layer is         | `chord cleanup status`                                 |
| Free space from old sessions      | `chord cleanup sessions --older-than 720h --yes`       |
| Reset the in-memory cache         | `chord cleanup cache --yes`                            |
| Trim log rotations                | `chord cleanup logs --older-than 168h --yes`           |
| Remove orphan project entries     | `chord cleanup project --yes`                          |
| Remove a chord-managed worktree   | `chord worktree remove <name>`                         |

All `cleanup` subcommands default to **dry-run** — without `--yes` they only list what would be removed. Full reference: [CLI — chord cleanup](./cli.md#chord-cleanup).

## What is safe to delete by hand?

| Path                                          | Safe to delete?                                                                                       |
| --------------------------------------------- | ----------------------------------------------------------------------------------------------------- |
| `~/.cache/chord/`                             | Yes, anytime. Will be rebuilt on next start.                                                          |
| `<state-dir>/logs/chord.log.1` and `.2`       | Yes. Current `chord.log` is in use; prefer `chord cleanup logs` to avoid touching live files.          |
| `<state-dir>/exports/<project-key>/`          | Yes — these are user-facing `/export` outputs.                                                        |
| `<state-dir>/sessions/<project-key>/<sid>/`   | Yes if you want to lose that session's history. Prefer `chord cleanup sessions --older-than …`.        |
| `<state-dir>/sessions/<project-key>/`         | Avoid: this would lose **all** sessions for a project.                                                |
| `<state-dir>/projects/<project-key>.json`     | Avoid: hand-editing leaves the project registry inconsistent. Use `chord cleanup project` instead.     |
| `<state-dir>/worktrees/...`                   | Avoid: use `chord worktree remove <name>`.                                                            |
| `~/.config/chord/auth.state.json`             | Yes. It is a machine-managed shared cache; deleting it only drops cached OAuth/quota state until warm-up repopulates it. |
| `~/.config/chord/`                            | Only if you want a clean reinstall. Do not delete `auth.yaml` unless you have your keys somewhere else. |
| `<project>/.chord/`                           | Only if you really want to drop the project's chord overrides. Often committed to git.                 |

## Related

- [CLI — global flags](./cli.md#global-flags)
- [Environment variables](./environment.md)
- [Configuration & Auth](./configuration.md)
- [Troubleshooting](./troubleshooting.md)
