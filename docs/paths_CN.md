# 目录与路径

Chord 读写的所有文件和目录，以及如何安全地清理。

## 三层布局

| 层级               | 默认路径                                                | 用途                                                                                               |
| ------------------ | ------------------------------------------------------- | -------------------------------------------------------------------------------------------------- |
| **配置主目录**     | `$XDG_CONFIG_HOME/chord` 或 `~/.config/chord`           | 用户编辑的配置：providers、模型池、自定义 agent、自定义 skill、自定义 slash 命令                  |
| **state 目录**     | `$XDG_STATE_HOME/chord` 或 `~/.local/state/chord`       | 持久运行时状态，丢了会失忆：sessions、exports、logs、project registry、worktrees                    |
| **cache 目录**     | `$XDG_CACHE_HOME/chord` 或 `~/.cache/chord`             | 可重建运行时缓存；任何时候都可以删                                                                  |

三个位置都可以通过环境变量、CLI flag 或 `config.yaml` 的 `paths:` 节覆盖——见 [环境变量](./environment_CN.md) 和 [CLI 全局 flag](./cli_CN.md#全局-flag)。

## 配置主目录 — `~/.config/chord/`

这些文件由你编辑，可以视为源文件。

首次直接运行 `chord` 时，如果全局 `config.yaml` 缺失且 Chord 能拿到控制 TTY，它会启动一次性初始化向导，并在结束时输出 `config.yaml` 和 `auth.yaml` 的实际解析路径。当你通过 `--config-home`、`CHORD_CONFIG_HOME` 启动，或在 Windows 上 `~` 不易直观定位时，这个行为尤其有用。

```text
~/.config/chord/
├── config.yaml            # chord 全局配置
├── auth.yaml              # API key / OAuth 凭据（建议 chmod 600）
├── auth.state.json        # 机器维护的共享 OAuth 运行时状态 / 额度缓存
├── agents/                # 全局 agent 定义（.md 或 .yaml）
├── commands/              # 全局自定义 slash 命令（每个 .md 一个）
└── skills/                # 全局 skill，每个为 <name>/SKILL.md
```

`config.yaml` 的 schema 见 [配置与认证](./configuration_CN.md)。Agent 见 [扩展与定制 — Agent](./customization_CN.md#自定义-agents)。Skill 见 [扩展与定制 — Skills](./customization_CN.md#skills)。自定义 slash 命令见 [扩展与定制 — 自定义 slash 命令](./customization_CN.md#自定义-slash-commands)。

`auth.state.json` 是共享运行时缓存，用来保存 OAuth 状态、Codex 额度快照、reset 时间和 warm-up 时间戳。它由 Chord 自动维护，通常不需要手工编辑。删除它是安全的，但在后续 warm-up 重新填充前，会暂时失去跨重启保留的额度排序缓存。

## state 目录 — `~/.local/state/chord/`

Chord 写在这里。删了就丢历史。

```text
~/.local/state/chord/
├── sessions/
│   └── <project-key>/
│       ├── project.json                # canonical-root、display-name、时间戳
│       └── <session-id>/               # 单个会话
│           ├── main.jsonl
│           ├── traces/
│           │   └── llm-trace.jsonl     # 轻量 LLM 请求 trace（默认开启）
│           └── …                       # 该会话的其他产物
├── projects/
│   └── <project-key>.json              # 注册表指针，用于跨项目查找
├── exports/
│   └── <project-key>/                  # `/export` 输出（markdown / JSON）
├── worktrees/
│   └── <repo-id>/
│       └── <slug>/                     # chord 管理的 git worktree（位于仓库之外）
└── logs/
    ├── chord.log                       # 当前日志
    ├── chord.log.1                     # 轮转
    ├── chord.log.2                     # 轮转
    └── tui-dumps/                      # `Ctrl+G` 输出
```

### `<project-key>` 是什么？

Chord 用项目的规范文件系统根路径（解析符号链接、规范化大小写）作为身份，再据此推导一个稳定、清洗后的 key——例如 `~/projects/chord` 的 key 为 `HOME-projects-chord`。两个项目清洗后冲突时，Chord 追加 8 字符指纹消歧。完整的规范根路径也会写入 `project.json`，所以即使路径相似，注册表也不会混淆。

Sessions、运行时缓存、exports 都以这个 key 为索引——在 `~/projects/chord` 重新跑 `chord` 能找到上次的会话。

### Worktree

`chord --worktree <name>` 会在 `worktrees/<repo-id>/<slug>` 下创建 chord 管理的 git worktree，**位于原仓库之外**，拥有自己的 project key。每个 chord 管理的 worktree 的 sessions、cache、exports 因此天然隔离。

清理 worktree（仅删 chord 一侧的数据），用 `chord worktree remove <name>`——见 [CLI — chord worktree](./cli_CN.md#chord-worktree)。**不要**手动删 worktree 目录，那会留下注册表中的孤儿条目（之后会被 `chord cleanup project` 标记）。

## cache 目录 — `~/.cache/chord/`

全是可重建数据，任何时候都可以删，代价仅是一次重新预热。

```text
~/.cache/chord/
└── runtime/
    └── session-cache/
        └── <project-key>/
            └── <session-id>/           # 内存会话快照、恢复状态
```

## 项目级目录 — `<project>/.chord/`

`chord` 首次在某项目启动时会按需创建项目根下的 `.chord/`。这是**唯一**位于用户仓库内部的 chord 目录。

```text
<project>/.chord/
├── config.yaml            # 项目级覆盖（与全局 ~/.config/chord/config.yaml 合并）
├── agents/                # 项目级 agent（覆盖或扩展全局 agent）
├── commands/              # 项目级自定义 slash 命令
└── skills/                # 项目级 skill
```

项目级文件优先级高于全局（同名 key 覆盖）。把 `.chord/` 提交到仓库通常是好事——团队成员可以共享同一套 agent 与 slash 命令。

`auth.yaml` **永远不会**从 `.chord/` 读取：凭据必须在 `~/.config/chord/auth.yaml`。

## 日志

| 文件                                   | 内容                                                                  |
| -------------------------------------- | --------------------------------------------------------------------- |
| `<state-dir>/logs/chord.log`           | 当前运行日志（golog 纯文本）                                          |
| `<state-dir>/logs/chord.log.1`         | 上一轮轮转                                                            |
| `<state-dir>/logs/chord.log.2`         | 更早的轮转                                                            |
| `<state-dir>/logs/tui-dumps/`          | `Ctrl+G` 生成的诊断快照（用于报 bug）                                 |

可用 `--logs-dir <path>` 或 `CHORD_LOGS_DIR=<path>` 覆盖目录。

典型日志行：

```text
[I 2026-05-02 12:00:00 file:123 pwd=/path pid=1234 sid=20260502015258426] message key=value
```

key-value 片段仅作人类可读文本，不是稳定的结构化日志 schema。

## 维护

优先使用 `chord cleanup`，**不要**直接 `rm -rf`——前者了解哪些路径删了会留下孤儿注册项。

| 目标                        | 命令                                                  |
| --------------------------- | ----------------------------------------------------- |
| 查看各层占用                | `chord cleanup status`                                |
| 释放旧会话占用空间          | `chord cleanup sessions --older-than 720h --yes`      |
| 清空运行时缓存              | `chord cleanup cache --yes`                           |
| 清理日志轮转                | `chord cleanup logs --older-than 168h --yes`          |
| 移除孤儿项目注册项          | `chord cleanup project --yes`                         |
| 移除 chord 管理的 worktree  | `chord worktree remove <name>`                        |

`cleanup` 全部子命令默认是 **dry-run**——不加 `--yes` 时只预览不真删。完整参考见 [CLI — chord cleanup](./cli_CN.md#chord-cleanup)。

## 哪些可以手动删？

| 路径                                              | 可以手动删吗？                                                                                                       |
| ------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------- |
| `~/.cache/chord/`                                 | 可以，随时。下次启动会重建。                                                                                         |
| `<state-dir>/logs/chord.log.1` 和 `.2`            | 可以。当前 `chord.log` 还在被 chord 写，建议用 `chord cleanup logs` 处理，避免误碰 live 文件。                       |
| `<state-dir>/exports/<project-key>/`              | 可以——这些是 `/export` 的输出，面向用户。                                                                              |
| `<state-dir>/sessions/<project-key>/<sid>/`       | 确定要丢这个 session 的历史的话，可以。更建议 `chord cleanup sessions --older-than …`。                           |
| `<state-dir>/sessions/<project-key>/`             | **不建议**：会丢这个项目**所有**会话。                                                                               |
| `<state-dir>/projects/<project-key>.json`         | **不建议**：手动改会让注册表不一致。请用 `chord cleanup project`。                                                     |
| `<state-dir>/worktrees/...`                       | **不建议**：用 `chord worktree remove <name>`。                                                                      |
| `~/.config/chord/auth.state.json`                 | 可以。它只是机器维护的共享缓存；删掉只会丢失已缓存的 OAuth / quota 状态，之后可由 warm-up 重新生成。                    |
| `~/.config/chord/`                                | 仅当想完全重装时。删 `auth.yaml` 之前确保 key 还在别处。                                                       |
| `<project>/.chord/`                               | 仅当确实想丢弃项目级 chord 配置时。这个目录通常入 git。                                                       |

## 相关

- [CLI — 全局 flag](./cli_CN.md#全局-flag)
- [环境变量](./environment_CN.md)
- [配置与认证](./configuration_CN.md)
- [常见问题排查](./troubleshooting_CN.md)
