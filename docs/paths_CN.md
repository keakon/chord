# 目录与路径

Chord 读写的所有文件和目录，以及如何安全地清理。

## 三层布局

| 层级               | 默认路径                                                | 用途                                                                                               |
| ------------------ | ------------------------------------------------------- | -------------------------------------------------------------------------------------------------- |
| **配置主目录**     | `$XDG_CONFIG_HOME/chord` 或 `~/.config/chord`           | 用户编辑的配置：providers、模型池、自定义 agent、自定义 skill、自定义 slash 命令                  |
| **state 目录**     | `$XDG_STATE_HOME/chord` 或 `~/.local/state/chord`       | 持久运行时状态，丢了会失忆：sessions、exports、logs、project registry、worktrees                    |
| **cache 目录**     | `$XDG_CACHE_HOME/chord` 或 `~/.cache/chord`             | 可重建运行时缓存；任何时候都可以删                                                                  |

三个位置都可以通过环境变量、CLI flag 或 `config.yaml` 的 `paths:` 节覆盖——见 [环境变量](./environment_CN.md) 和 [CLI 全局 flag](./cli_CN.md#全局-flag)。

## 新数据该放哪里

以后新增 Chord 文件或目录时，按下面的规则判断：

1. 用户需要直接查看或编辑、内容会引用仓库相对路径，或者需要随项目目录一起移动时，放在项目根目录或 `.chord/`。同时必须写清 Git 语义；隐藏路径不等于天然私有。
2. 用户主动维护、需要跨项目生效的配置放在配置主目录。凭据只能放这里，不能进入项目目录。
3. Chord 维护的持久机器状态放在 state 目录，例如历史记录、记账数据、锁、checkpoint 和注册表。删除 state 可能丢历史，或需要额外恢复。
4. 只有在删除后不会丢用户内容或持久历史，而且 Chord 能从其他来源重建时，才放进 cache 目录。

有些功能需要拆分存储：可移植、用户可读的项目内容放在项目目录；高频记账、锁和可重建索引放在 state 或 cache。不要跨层复制同一份权威内容，每个文件都要有唯一、清楚的权威源。

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

`<session-id>` 是 17 位纯数字（`YYYYMMDDHHmmSSfff`），由本地墙钟生成，因此一眼能看出本地日期时间，作为目录名/文件名也安全。此前生成的会话 ID 由 UTC 派生且不会被改写，因此同一个 sessions 目录里可能两种并存：在非 UTC 时区下，旧 ID 显示的时间与它实际创建的本地时间不同。SID 只是标识和粗略的创建时间提示，不充当会话排序键，所以这种混用不影响 Chord 选择哪个会话。会话列表取 `main.jsonl` 与已有 `usage-summary.json` 两个修改时间中较新的那个。Chord 只 stat 这些小文件，不会扫描完整会话，也不会额外维护项目级索引。复制或恢复文件可能让文件时间失真；没有更新这两个文件的活动，也不会改变排序。

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
├── skills/                # 项目级 skill
└── plans/                 # 用户可见的计划文档
```

项目级文件优先级高于全局（同名 key 覆盖）。把 `.chord/` 提交到仓库通常是好事——团队成员可以共享同一套 agent 与 slash 命令。

`auth.yaml` **永远不会**从 `.chord/` 读取：凭据必须在 `~/.config/chord/auth.yaml`。

### 职责边界

项目目录适合放描述“Chord 在这个项目里应该如何工作”的内容，或团队需要审阅的用户产物：

- `AGENTS.md` 放仓库级指令，适用的 agent 必须遵守。
- `.chord/config.yaml`、`.chord/agents/`、`.chord/commands/` 和 `.chord/skills/` 放项目显式配置与共享能力。
- `.chord/plans/` 放计划文档，是否提交由项目自行决定。
- 项目内 Chord 文件引用仓库文件时，应使用相对项目根的路径，这样移动整个项目目录后仍然有效。

不要把 `.chord/` 当成通用运行时状态目录。会话 transcript、usage ledger、恢复快照、项目注册表、日志、锁和其他不透明的运行时记账数据，应放在上面的 state 目录或 cache 目录中。用户需要直接编辑、使用相对路径引用或随项目移动的人类可读产物，可以放在项目目录，但必须明确 Git 和所有权语义。尤其不要把 `auth.yaml` 或其他凭据放进项目目录。

整个 `.chord/` 并不会自动变成“可安全提交”或“自动忽略”的目录。项目配置和共享 skill 通常适合提交；计划和其他项目本地私有文件则按仓库自身规则处理。提交前应检查 untracked 和 ignored 文件，不要假设所有隐藏项目文件都天然私有。

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
