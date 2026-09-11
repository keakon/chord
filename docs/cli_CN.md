# CLI 参考

这里列出 Chord 面向用户的命令、子命令和 flag，也包括二进制通过 Cobra 暴露的 `help` 与 `completion` 辅助命令。

首次使用建议先看 [快速开始](./quickstart_CN.md)。

## 用法摘要

```text
chord [全局 flag] [命令] [命令 flag] [参数]
```

不带任何命令时，`chord` 在当前目录启动本地 TUI。

## 命令一览

| 命令                              | 用途                                                              |
| --------------------------------- | ----------------------------------------------------------------- |
| `chord`                           | 启动本地 TUI                                                      |
| `chord auth [provider]`           | 用 `preset: codex` provider 登录 OAuth                            |
| `chord headless`                  | 无 TUI 启动，stdio JSON 控制面                                    |
| `chord doctor config`             | 校验全局 / 项目配置文件                                            |
| `chord doctor models`             | 诊断已配置的 provider/model 调用链                                |
| `chord cleanup status`            | 查看路径定位器管理的 state/cache/logs 体积                        |
| `chord cleanup <kind>`            | 清理 `sessions` / `cache` / `logs` / `project`（默认 dry-run）    |
| `chord worktree list`             | 列出当前仓库下 chord 管理的 worktree                              |
| `chord worktree remove <name>`    | 移除 chord 管理的 worktree                                        |
| `chord worktree finish <name>`    | 先将目标分支合入真实 worktree，再把结果 squash 回去并删除 worktree |
| `chord resume <session-id>`       | 按 session id 恢复，自动定位到对应的 worktree                     |
| `chord import <source> [file]`    | 把外部 agent 会话导入 Chord；可识别工具会在参数能标准化时转换为结构化 Chord 工具卡 |
| `chord completion <shell>`        | 为 `bash`、`fish`、`powershell` 或 `zsh` 生成 shell completion 脚本 |
| `chord help [command]`            | 显示命令帮助                                                      |

## 全局 flag

下列 flag 所有命令都接受，与环境变量、`config.yaml` 协同生效（优先级：CLI flag > 环境变量 > 配置文件）。

| Flag             | 说明                                                                                                | 环境变量              | 默认值                                                                       |
| ---------------- | --------------------------------------------------------------------------------------------------- | --------------------- | ---------------------------------------------------------------------------- |
| `--api-base`     | 本次运行的 provider 基础 URL 覆盖；设置后优先于各 provider 的 `api_url` | `CHORD_API_BASE`      | 空                                                                           |
| `--config-home`  | 配置主目录，包含 `config.yaml`、`auth.yaml`、`agents/`、`skills/`、`commands/`                      | `CHORD_CONFIG_HOME`   | 已设 `$XDG_CONFIG_HOME` 时取 `$XDG_CONFIG_HOME/chord`，否则 `~/.config/chord` |
| `--state-dir`    | 持久运行时状态（sessions、exports、logs、project registry、worktree metadata）                      | `CHORD_STATE_DIR`     | 已设 `$XDG_STATE_HOME` 时取 `$XDG_STATE_HOME/chord`，否则 `~/.local/state/chord` |
| `--cache-dir`    | 可重建缓存（runtime caches、临时产物）                                                              | `CHORD_CACHE_DIR`     | 已设 `$XDG_CACHE_HOME` 时取 `$XDG_CACHE_HOME/chord`，否则 `~/.cache/chord`  |
| `--sessions-dir` | 仅覆盖 sessions 根目录                                                                              | `CHORD_SESSIONS_DIR`  | `<state-dir>/sessions`                                                       |
| `--logs-dir`     | 仅覆盖 logs 目录                                                                                    | `CHORD_LOGS_DIR`      | `<state-dir>/logs`                                                           |
| `-h`, `--help`   | 显示当前命令帮助                                                                                    | —                     | false                                                                        |
| `-v`, `--version`| 打印构建与运行时版本信息后退出                                                                      | —                     | false                                                                        |

完整目录布局见 [目录与路径](./paths_CN.md)。完整环境变量列表见 [环境变量](./environment_CN.md)。

`-v/--version` 只在 root 命令上可用。各子命令都支持 `-h/--help`，并接受上表中的路径 / API 覆盖类全局 flag。

## `chord`（默认 — TUI）

在当前目录启动本地 TUI。该目录会成为 session working directory：相对文件路径、省略的 `shell` workdir，以及省略的 `grep` / `glob` 搜索根都会从这里解析。当 `--worktree` 或 `chord resume` 切换到 Chord 管理的 worktree 时，该 worktree 路径会成为 session working directory；文件工具本身不需要理解 git worktree。Chord 会在第一条用户消息前（以及上下文压缩后）注入这个目录，让模型看到与工具一致的路径基准。面向用户的工具卡片可以为了可读性显示相对该目录的路径，但原始 tool-call 参数和 session 导出会保留模型实际传入的路径，便于审计。

首次启动时，如果全局 `config.yaml` 缺失且 Chord 能取得控制 TTY，就会先启动一次性的初始化向导，再进入 TUI。向导会写入 `config.yaml`，必要时再写入 `auth.yaml`，如果已有匹配的 `auth.yaml` 凭据则尽量直接复用，也可以在初始化过程中直接完成 Codex OAuth 登录，并在结束时展示实际路径。仅仅 stdin 被重定向并不会禁用向导：只要还能打开控制 TTY，初始化仍会在那里交互。只有没有控制 TTY 时，Chord 才会直接报初始化错误，不会等待输入。`help`、`version` 和非 root 子命令都不会触发这个向导。

### Flag

| Flag                         | 说明                                                                                                                                                        |
| ---------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `-c`, `--continue`           | 恢复本项目最近一个非空、且未被其它进程占用的会话                                                                                                            |
| `-r`, `--resume <id>`        | 恢复当前项目内指定 session id 的会话（要恢复其它 chord 管理 worktree 里的会话，用 `chord resume <id>`）                                                     |
| `--fork-history[=N]`         | 先把 `--resume` 指定的会话在某次压缩边界上 fork 出来，再恢复这个 fork：省略 N 表示最近一次已应用边界，或传 `history-N` 序号（如 `=2`）。只能与 `--resume` 一起用，且该会话必须属于当前项目——fork 其它 worktree 里的会话用 `chord resume <id> --fork-history`。源会话即使正被其它进程打开也可以 fork（见下文[恢复会话](#恢复会话)） |
| `--yolo`                     | 启动时启用 YOLO 模式：普通工具跳过权限检查、不再弹确认；handoff、delegate、cancel、done 和 compact_context 仍按各自配置的规则执行                                                       |
| `-w`, `--worktree [name]`    | 创建或进入 chord 管理的 git worktree（不传名字时自动命名）；与 `--continue` / `--resume` 配合可作用于该 worktree 自己的会话历史                              |

`--continue` 与 `--resume` 互斥；`--fork-history` 只能与 `--resume` 搭配，不能与 `--continue` 或 `--worktree` 组合。

`--continue` 取当前进程能够打开的、最近有活动的非空会话。Chord 按 `main.jsonl` 与已有 `usage-summary.json` 两个修改时间中较新的那个排序，不会扫描完整会话，也不会额外维护项目级索引。复制或恢复会话文件可能改动文件时间；没有更新这两个文件的活动，也不会改变排序。

已被另一个运行中的 Chord 进程占用的会话会被跳过，改用下一个候选；跳过时会给出一条一次性提示（TUI 里是 toast，headless 的 ready 信封里有 `skipped_locked_sessions` 字段，日志里也会记录），不会静默切换。如果所有会话都被占用，Chord 会新建一个。`--resume <id>` 指定的是某一个具体会话，因此该会话已被占用时会报错，而不会替换成别的会话。

### 恢复会话

两条入口走的是同一条恢复管线，区别只在如何定位会话：

- `chord --resume <id>`（别名 `-r`）恢复**当前项目内**的会话：该会话必须属于当前目录所在项目，Chord 不会切换目录。它可以与 `--continue` / `--worktree` 组合，也是脚本与 headless 使用的形态。如果会话属于其它 chord 管理 worktree，会以"找不到会话"报错。
- `chord resume <id>` 从**任意目录**按 session id 恢复：它读取仓库索引，找到该会话属于哪个 chord 管理 worktree（或主仓库），切换过去再恢复。

一句话选择：人已经在会话所在项目里 → 用 `chord --resume`；人在别处、或不确定会话在哪个 worktree → 用 `chord resume <id>`。

### 示例

```bash
# 直接启动
chord

# 恢复最近一个会话
chord --continue

# 恢复指定 session
chord --resume 20260428064910975

# 创建/进入 chord 管理的 worktree
chord --worktree feat-auth

# 进入 worktree 并恢复其内最近会话
chord --worktree feat-auth --continue
```

## `chord auth [provider]`

在基础配置完成后再用它登录。该命令用于 `preset: codex` 的 OAuth provider，并把凭据存入 `~/.config/chord/auth.yaml`。Chord 还会把机器维护的共享 OAuth 运行时状态保存在 `~/.config/chord/auth.state.json`，这样额度 / reset 缓存不会频繁改写 `auth.yaml`。不带 provider 名时，Chord 自动选择唯一的 codex provider；多个时会让你选。首次向导也可以在初始化过程中直接完成这条 Codex OAuth 登录链路；`chord auth codex` 仍然适合后续重新登录或补登录。

正常模型请求过程中，如果 OAuth 返回 HTTP 401/403 `token_invalidated`、token revoked、refresh token 过期或账号停用等错误，Chord 会把它视为永久凭据故障。匹配的 OAuth 运行时状态会被标记为已过期、已停用或已失效（优先使用账号 / 用户元数据，其次回退到 refresh token 哈希），该凭据会从可选 key 池中移除，并刷新 TUI 侧边栏的 `Keys` 计数。错误面板的重试记录会显示 OAuth 邮箱 / 账号，以及打码后的 `key=...` 标识（展示少量前缀和后缀，便于人工区分）。需要恢复账号时重新运行 `chord auth` 登录；需要移除不可用残留时运行 `chord auth state clean`。

### Flag

| Flag             | 说明                                                                                                                                                                 |
| ---------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `--device-code`  | 改用 device-code 流程（在 provider 网页粘贴一次性 code），而非本地浏览器回调。适用于 SSH / 无桌面 / WSL 等无法本地打开浏览器的环境                              |

### 示例

```bash
# 自动选择
chord auth

# 显式指定 provider 名
chord auth codex

# Headless / SSH 环境
chord auth codex --device-code
```

### `chord auth refresh <provider>`

刷新某个 `preset: codex` provider 下所有带 refresh token 的 OAuth 凭据。命令会逐条输出 refreshed、failed 或 skipped；API key 和没有 refresh token 的 OAuth 条目会被跳过。任一刷新失败时，命令会继续处理剩余凭据，并在结束后返回错误。

刷新成功后会更新 `auth.yaml`，并同步 `~/.config/chord/auth.state.json` 中匹配的运行时条目，同时保留 quota/reset 提示。

```bash
chord auth refresh codex
```

### `chord auth state list`

列出 `~/.config/chord/auth.state.json` 中已过期、已停用或已失效的 OAuth 运行时状态条目。该命令不会列出 `auth.yaml` 中已不存在对应 OAuth 凭据的孤儿 state；如需同时清理无效和孤儿 state，请使用 `chord auth state clean`。

```bash
chord auth state list
```

### `chord auth state clean`

清理 `~/.config/chord/auth.state.json` 中已失效的 OAuth 运行时状态条目、`auth.yaml` 中已不存在对应 OAuth 凭据的孤儿状态，并同步清理 `~/.config/chord/auth.yaml` 中匹配的过期 / 已停用 / 已失效 OAuth 凭据。

典型用途：

- 清理过期 / 已停用 / 已失效账号残留的共享缓存状态和匹配凭据；
- 在轮换或下线账号后让 `auth.state.json` 与 `auth.yaml` 保持同步；
- 移除已被 Chord 标记为过期、已停用或已失效的不可用 OAuth 凭据。

```bash
chord auth state clean
```

## `chord headless`

无 TUI 启动 Chord。stdin 接收 JSON 命令，stdout 输出 JSON envelope。完整协议见 [Headless](./headless_CN.md)。

### Flag

| Flag                         | 说明                                                            |
| ---------------------------- | --------------------------------------------------------------- |
| `-d`, `--session-dir <dir>`  | headless 会话目标项目目录（默认当前目录）                       |
| `-c`, `--continue`           | 恢复目标目录下最近一个会话                                      |
| `-r`, `--resume <id>`        | 恢复目标目录下指定 session id 的会话                            |
| `-w`, `--worktree [name]`    | 启动前创建或进入 chord 管理的 worktree                          |

### 示例

```bash
chord headless
chord headless -d /path/to/repo --continue
chord headless -d /path/to/repo --worktree feat-auth
```

## `chord doctor config`

检查全局与项目 `config.yaml` 里的未知字段、类型不对的值、YAML 语法错误，以及不合理的配置值（比如非法的 `retry_backoff`、负数 diagnostics 阈值）。命令会一次性列出所有问题，而不是遇到第一个就停。

Chord 的配置加载器遇到这些问题只会写日志并照常启动，把出错的值当作未配置处理。这个命令把它们显式列出来，方便你在不翻日志的情况下校验配置文件。

### Flag

| Flag      | 说明                          |
| --------- | ----------------------------- |
| `--json`  | 输出机器可读的 JSON 报告      |

全局配置始终会检查；当前工作目录下的项目配置（`.chord/config.yaml`）存在时也会检查。只要有问题，命令就以状态码 2 退出，方便脚本和 CI 使用。

### 示例

```bash
# 校验全局 + 项目配置
chord doctor config

# 脚本用的机器可读报告
chord doctor config --json
```

## `chord doctor models`

对已配置的模型调用链执行轻量诊断，使用与正常 LLM 请求相同的 provider transport 路径。命令会加载 `config.yaml` / `auth.yaml`，把每个目标解析成 canonical `provider/model[@variant]`，应用 model 默认 tuning 和 variant tuning，并报告成功/失败、延迟、文本 chunk 数、可用时的 token usage，以及 Responses provider 的最终 transport（`http` 或 `websocket`）。它使用的配置视图与正常运行时一致：会先加载全局配置，再叠加项目级配置。

默认情况下，Chord 会为每个 provider 测试一个代表模型。代表模型选择是稳定的：优先取所有 `model_pools` 中最先引用该 provider 的模型；若没有任何池引用该 provider，则取该 provider 下按名称排序的第一个 model。每个诊断目标默认只发起 1 次请求；只有在明确想重试瞬时故障时才使用 `--retry`。如果某个 provider 配置了多个 credential，诊断会刻意只使用第一个 credential，避免后续 key 掩盖该 credential 的失败。

### Flag

| Flag                   | 说明                                                                                                  |
| ---------------------- | ----------------------------------------------------------------------------------------------------- |
| `--provider <name>`    | 只测试指定 provider 的代表模型；也可为裸 `--model` 值提供 provider                                    |
| `--model <ref>`        | 测试单个模型。使用 `provider/model[@variant]`；只有同时传 `--provider` 时才允许 `model[@variant]`      |
| `--pool <name>`        | 按顺序独立测试指定 `model_pools` 中的每个模型 ref                                                     |
| `--all-models`         | 测试 `--provider` 下配置的全部模型（必须与 `--provider` 一起使用）                                    |
| `--all-pools`          | 测试所有已配置 model pool                                                                             |
| `--timeout <duration>` | 每个模型请求的超时时间（默认 `30s`）                                                                  |
| `--retry <count>`      | 每个目标最多请求次数（默认 `1`；400/401/403 等客户端/鉴权错误不会重试）                               |
| `--fail-fast`          | 第一次请求失败或配置错误后停止                                                                        |
| `--json`               | 输出机器可读 JSON 报告                                                                                |

`--model`、`--pool` 与 `--all-pools` 互斥。Pool 检查不会走 fallback：每个池条目都会单独请求，避免后续模型成功掩盖某个不可用的 fallback 目标。

### 示例

```bash
# 用代表模型冒烟测试所有已配置 provider
chord doctor models

# 测试某个 provider 的代表模型
chord doctor models --provider openai

# 测试精确模型或 variant
chord doctor models --model openai/gpt-5.5
chord doctor models --model openai/gpt-5.5@high
chord doctor models --provider openai --model gpt-5.5@high

# 审计单个模型池或全部模型池
chord doctor models --pool thinking
chord doctor models --all-pools --json

# 测试某 provider 下配置的全部模型
chord doctor models --provider openai --all-models --fail-fast
```

## `chord cleanup`

检查或清理路径定位器管理的 state、cache、logs 目录。

### `chord cleanup status`

打印 state、cache、logs 三类目录的体积，以及会话数和项目数。只读。

```bash
chord cleanup status
```

输出示例：

```text
state_dir: /Users/me/.local/state/chord (29.6 GB)
cache_dir: /Users/me/.cache/chord (847 B)
logs_dir: /Users/me/.local/state/chord/logs (263.5 MB)
sessions: 42 across 7 projects
```

### `chord cleanup sessions | cache | logs | project`

清理指定类别的数据。**默认是 dry-run**——加 `--yes` 才真正删除。

| Flag                          | 说明                                                                                  |
| ----------------------------- | ------------------------------------------------------------------------------------- |
| `--older-than <duration>`     | 仅清理早于该时长的条目（Go duration 语法，如 `720h` 表示 30 天）                    |
| `--yes`                       | 真正删除；不加此 flag 时仅预览将被删除的内容                                        |

| 类别        | 清理内容                                                                                                                   |
| ----------- | -------------------------------------------------------------------------------------------------------------------------- |
| `sessions`  | `<state-dir>/sessions/<project-key>/` 下的旧会话目录；当项目会话目录在会话删除后只剩 `project.json` 时，也会一并移除        |
| `cache`     | `<cache-dir>/runtime/` 下的可重建缓存                                                                                     |
| `logs`      | `<state-dir>/logs/` 下的轮转日志                                                                                          |
| `project`   | 孤立的项目元数据（项目目录已不存在的注册项）                                                                               |

### 示例

```bash
# 预览将被清理的内容
chord cleanup sessions --older-than 720h

# 真正清理 30 天前的会话
chord cleanup sessions --older-than 720h --yes

# 清空可重建缓存（下次启动会自动重建）
chord cleanup cache --yes
```

输出示例：

```text
would remove /Users/me/.local/state/chord/sessions/project-a/202605120001 (263.5 MB)
would remove /Users/me/.local/state/chord/sessions/project-b (490 B)
would remove 1 sessions, 1 empty project dirs, total 263.5 MB
dry-run: pass --yes to delete
```

最后一行汇总本次将要删除的条目与总大小。会话目录与空壳项目目录（只剩一个 `project.json`）分开计数，字节总量不会让人误以为是空壳目录占用的。加 `--yes` 后同样输出这些行，只是 `would remove` 变为 `removed`，末尾不再有 `dry-run` 提示。

```text
removed /Users/me/.local/state/chord/sessions/project-a/202605120001 (263.5 MB)
removed 1 sessions, total 263.5 MB
```

## `chord worktree`

管理 chord 管理的 git worktree。可使用 `chord worktree <name>`（或 `chord --worktree <name>`）创建或进入一个 worktree 并在其中启动会话；本命令的子命令用于 `list`、`remove`、`finish` 等管理操作。

Worktree 落地在 `<state-dir>/worktrees/<repo-id>/<slug>`（仓库之外），每个 worktree 拥有独立 project key，sessions 与 cache 自动隔离。

### `chord worktree list`

列出当前仓库下 chord 管理的所有 worktree。

### `chord worktree remove <name>`

删除 worktree 目录及其 sessions、cache、exports。**默认保留分支**。

| Flag                | 说明                                                                                            |
| ------------------- | ----------------------------------------------------------------------------------------------- |
| `--force`           | worktree 有未提交修改也强删；强删分支                                                           |
| `--delete-branch`   | 同时删除 worktree 分支。不加 `--force` 时仅在分支已合并的前提下删除                       |

### `chord worktree finish <name>`

先把目标分支合并进真实 worktree 分支，再将完成后的 worktree 状态以单个 squash commit 合回该目标分支，随后 fast-forward 该目标分支，并删除 worktree 与分支。

| Flag                     | 说明                                                                                                                               |
| ------------------------ | ---------------------------------------------------------------------------------------------------------------------------------- |
| `--onto <分支>`          | 要先合入 worktree、再 squash 回去的目标分支（默认主 worktree 当前分支）                                                           |
| `--check`                | 在临时 worktree 中预检“目标分支能否干净合入 worktree”；真正执行 finish 时若出现冲突，真实 worktree 可能停留在 merge 状态，等待你解决                                             |
| `-m, --message <message>` | 覆盖自动生成的 squash commit message，手动指定最终 finish commit 的说明                                                           |

如果把目标分支合并进 worktree 时会冲突，`finish` 会打印冲突详情，保持目标分支不变，并把真实 worktree 保留在这次 merge 中，供你解决后重跑。

如果 worktree 内已经有进行中的 rebase 或 merge，`finish` 会直接退出，避免叠加新的合并流程。

只想提前判断会不会冲突、又不想改动真实 worktree / 分支 / 目标分支时，用 `--check`。真正执行 `finish` 则不是无副作用操作：如果目标分支合入 worktree 时发生冲突，Chord 会把真实 worktree 保留在该 merge 状态，等你解决后再重跑 `finish`。

想手动控制最终 squash commit 的说明时，用 `-m/--message` 覆盖自动生成的 message。

真正执行 `finish` 且需要生成 squash commit 时，还要求本地 git commit identity 可用（`user.name` / `user.email`，或 `GIT_AUTHOR_*` / `GIT_COMMITTER_*`）。`--check` 只做到 merge 预检，因此不依赖 commit identity。

### 示例

```bash
chord worktree list
chord worktree remove feat-old --delete-branch
chord worktree finish feat-auth --onto main
chord worktree finish feat-auth --onto main -m "feat(auth): finalize auth flow"
```

## `chord resume <session-id>`

按 session id 恢复会话。与 `chord --resume` 不同，此命令能自动定位该 session 所属的 chord 管理 worktree 并切换过去——即便当前 cwd 不在那个 worktree 内也可以。何时用哪个入口见上文[恢复会话](#恢复会话)。

```bash
chord resume 20260428064910975
```

### 在某次压缩边界上 fork 会话

带 `--fork-history` 时，不再恢复原会话，而是把该会话在某次压缩边界上的历史复制成一个**全新会话**再恢复它。原会话不会被改动，也不需要先关闭：fork 只读该会话的归档文件、另写一个新的会话目录，所以源会话即使正被另一个 Chord 进程占用也可以 fork（普通 resume 在这种情况下会报错）。

```bash
chord resume 20260428064910975 --fork-history      # fork 最近一次已应用的边界
chord resume 20260428064910975 --fork-history=2    # fork 第 2 次已应用的边界
```

- 压缩边界就是会话中途应用过的 `[Context Summary]` checkpoint：每次压缩都会把压缩前的完整会话备份为 `main.pre-compress-N.jsonl`，并配套写入 `history-N.status.json`，只有应用全部完成才标记为 applied。fork 到已应用的边界 N 会还原那一代会话的真实状态：fork 的 `main.jsonl` 逐条取自 `main.pre-compress-N.jsonl`，正文记录原样复制——包括它开头的 checkpoint 摘要卡，那是当时真实看到的状态的一部分（fork 第 2 次边界时，第 1 次的摘要卡就在顶部）。更早的 `history-1..N-1.md` 压缩归档会一并复制过来，checkpoint 里的历史地图因此仍然有效：模型可以按需读取归档，查边界之前被压缩掉的内容。边界自身的 `history-N.md` 不会复制——被 fork 的 `pre-compress-N` 正文仍原样带着那些消息，复制它只会让内容重复。更早的内容通过归档查看，而不是拼回会话正文。
- 消息正文（用户输入、助手回复、工具调用与结果、diff）原样保留：内容里的会话号、路径、命令是当时的历史事实，不做改写。图片/PDF 附件会复制进新会话并改写引用路径。
- fork 出的新会话从零开始：usage/token 统计与运行状态不复制。`session-meta.json` 记录 `forked_from`，并沿用源会话的 worktree 归属与手动启用的 MCP server。
- 新会话 id 会打印出来，随后自动恢复进入 TUI。会话至少要有一次已应用的压缩；请求超出可用范围的边界会报错，并列出合法的 `history-N` 取值。

**不复制的内容：** fork 只带主会话正文与压缩归档，其余一律不复制。子代理的独立会话记录、委托任务（task）状态、mailbox、后台任务、artifacts，以及源会话 `subagents/`、`artifacts/`、`snapshot.json` 下的其他运行期状态——这些状态属于正在运行的源会话，无法在历史时间点如实重建，usage 统计同样绑定源会话。影响：浏览历史不受影响；但如果在 fork 里继续干活，fork 之前的委托任务只保留为可见的消息卡片——无法再查询或恢复执行，依赖那些子代理/任务的线也接不下去。fork 内部新发起的子代理与任务一切正常。

## `chord import <source> [file]`

将外部 agent 会话导入为 Chord 可恢复的会话。当前支持的 source：`opencode`、`codex`、`claude`。

Claude Code 导入会尽力重建**非 sidechain 的主会话**，而不是盲目导入最新的原始叶子节点。compact 边界会用于重建，但不会渲染为可见 transcript 消息。sidechain/sub-agent 条目默认会从主导入 session 中排除；检测到时，CLI 输出会报告跳过数量，`import-report.json` 会记录 Claude 专属诊断信息，并在存在 sidechain agent ID 时一并记录。

可识别的导入工具会始终在参数能标准化时转换成最接近的当前 Chord 工具卡（结构化的工具调用与对应结果），包括把文件修改显示为 `edit`、`apply_patch`、`write` 或 `delete`。只有无法识别的记录（没有 Chord 映射、缺少 call id、或参数无法标准化）才会保留为可读的 fallback 文本，而不是原始 JSON。转换后的导入工具不会恢复 Chord FileTracker snapshot；如果编辑导入会话涉及的文件前需要最新文件上下文或 stale-change 风险提示，请重新 `read`。

### Flag

| Flag                      | 说明                                                                                                                |
| ------------------------- | ------------------------------------------------------------------------------------------------------------------- |
| `--project <path>`        | 写入哪个项目（默认当前目录）                                                                                        |
| `--sid <id>`              | 指定 Chord session id（默认自动生成）                                                                               |
| `--id <session-id>`       | 按 source 端 session id 查找（仅支持 `codex` 与 `claude`）                                                        |
| `--root <path>`           | 配合 `--id` 使用的根目录（codex 默认 `~/.codex/sessions`，claude 默认 `~/.claude/projects`）                        |
| `--reasoning <mode>`      | 推理导入策略：`off`、`visible`、`strict`（默认 `strict`）                                                           |
| `--dry-run`               | 仅解析与报告，不写入会话                                                                                            |
| `--json`                  | 输出机器可读 JSON 摘要                                                                                              |
| `--force`                 | 允许覆盖已存在的 `--sid`                                                                                            |

### 示例

```bash
# OpenCode export
opencode export <sessionID> > export.json
chord import opencode export.json
chord resume <sid>

# Codex 直接传文件
chord import codex ~/.codex/sessions/YYYY/MM/DD/rollout-*.jsonl

# Codex 按 session id
chord import codex --id <session-id>

# Claude Code 直接传文件
chord import claude ~/.claude/projects/**/<sessionId>.jsonl

# Claude Code 按 session id
chord import claude --id <session-id>
```

完整的工具/推理策略、转换告警、provider 安全 wire view 见 [使用指南 — 导入外部会话](./usage_CN.md#导入外部会话)。

## `chord completion <shell>`

为 `bash`、`fish`、`powershell` 或 `zsh` 生成 shell completion 脚本。生成后按对应 shell 的常规方式加载即可。

```bash
chord completion zsh
chord completion bash
chord completion fish
chord completion powershell
```

## `chord help [command]`

显示命令帮助，效果等同于给对应命令传 `--help`。

```bash
chord help
chord help doctor models
chord doctor models --help
```

## 从源码运行

从源码运行时一定要用包路径，**不要**用 `main.go`：

```bash
go run ./cmd/chord/
go run ./cmd/chord/ headless
go run ./cmd/chord/ --worktree feat-auth
```

`go run cmd/chord/main.go` 不会加载 `main` 包的其余文件，会失败。

## 相关

- [快速开始](./quickstart_CN.md)
- [使用指南](./usage_CN.md)
- [配置与认证](./configuration_CN.md)
- [目录与路径](./paths_CN.md)
- [环境变量](./environment_CN.md)
- [Headless](./headless_CN.md)
