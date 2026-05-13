# CLI 参考

Chord 的所有命令、子命令和 flag。

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
| `chord doctor models`             | 诊断已配置的 provider/model 调用链                                |
| `chord cleanup status`            | 查看路径定位器管理的 state/cache/logs 体积                        |
| `chord cleanup <kind>`            | 清理 `sessions` / `cache` / `logs` / `project`（默认 dry-run）    |
| `chord worktree list`             | 列出当前仓库下 chord 管理的 worktree                              |
| `chord worktree remove <name>`    | 移除 chord 管理的 worktree                                        |
| `chord worktree finish <name>`    | 把 worktree 分支 rebase 回主线、fast-forward，然后删除 worktree   |
| `chord resume <session-id>`       | 按 session id 恢复，自动定位到对应的 worktree                     |
| `chord import <source> [file]`    | 把外部 agent 会话导入 Chord                                       |

## 全局 flag

下列 flag 所有命令都接受，与环境变量、`config.yaml` 协同生效（优先级：CLI flag > 环境变量 > 配置文件）。

| Flag             | 说明                                                                                                | 环境变量              | 默认值                                                                       |
| ---------------- | --------------------------------------------------------------------------------------------------- | --------------------- | ---------------------------------------------------------------------------- |
| `--api-base`     | provider 未配 `api_url` 时，作为统一基础 URL                                                       | `CHORD_API_BASE`      | 空                                                                           |
| `--config-home`  | 配置主目录，包含 `config.yaml`、`auth.yaml`、`agents/`、`skills/`、`commands/`                      | `CHORD_CONFIG_HOME`   | 已设 `$XDG_CONFIG_HOME` 时取 `$XDG_CONFIG_HOME/chord`，否则 `~/.config/chord` |
| `--state-dir`    | 持久运行时状态（sessions、exports、logs、project registry、worktree metadata）                      | `CHORD_STATE_DIR`     | 已设 `$XDG_STATE_HOME` 时取 `$XDG_STATE_HOME/chord`，否则 `~/.local/state/chord` |
| `--cache-dir`    | 可重建缓存（runtime caches、临时产物）                                                              | `CHORD_CACHE_DIR`     | 已设 `$XDG_CACHE_HOME` 时取 `$XDG_CACHE_HOME/chord`，否则 `~/.cache/chord`  |
| `--sessions-dir` | 仅覆盖 sessions 根目录                                                                              | `CHORD_SESSIONS_DIR`  | `<state-dir>/sessions`                                                       |
| `--logs-dir`     | 仅覆盖 logs 目录                                                                                    | `CHORD_LOGS_DIR`      | `<state-dir>/logs`                                                           |

`--config` 是 `--config-home` 的隐藏别名，仅为向后兼容保留；新脚本请用 `--config-home`。

完整目录布局见 [目录与路径](./paths_CN.md)。完整环境变量列表见 [环境变量](./environment_CN.md)。

## `chord`（默认 — TUI）

在当前目录启动本地 TUI。首次启动时按需创建项目根 `.chord/` 目录，并在 `<state-dir>/projects/<project-key>.json` 注册该项目。

### Flag

| Flag                         | 说明                                                                                                                                                        |
| ---------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `-c`, `--continue`           | 恢复本项目最近一个非空会话                                                                                                                                  |
| `-r`, `--resume <id>`        | 恢复指定 session id 的会话                                                                                                                                  |
| `-w`, `--worktree [name]`    | 创建或进入 chord 管理的 git worktree（不传名字时自动命名）；与 `--continue` / `--resume` 配合可作用于该 worktree 自己的会话历史                              |

`--continue` 与 `--resume` 互斥。

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

用 `preset: codex` provider 登录 OAuth，凭据存入 `~/.config/chord/auth.yaml`。Chord 还会把机器维护的共享 OAuth 运行时状态保存在 `~/.config/chord/auth.state.yaml`，这样额度 / reset 缓存不会频繁改写 `auth.yaml`。不带 provider 名时，Chord 自动选择唯一的 codex provider；多个时会让你选。

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

### `chord auth state clean`

清理 `~/.config/chord/auth.state.yaml` 中已失效的 OAuth 运行时状态条目，不会动 `auth.yaml` 中的真实凭据。

典型用途：

- 清理过期 / 已停用账号残留的共享缓存状态；
- 在轮换或下线账号后收缩 `auth.state.yaml`；
- 保留凭据的前提下重置缓存的 OAuth 状态。

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

| 类别        | 清理内容                                                                                |
| ----------- | --------------------------------------------------------------------------------------- |
| `sessions`  | `<state-dir>/sessions/<project-key>/` 下的旧会话目录                                   |
| `cache`     | `<cache-dir>/runtime/` 下的可重建缓存                                                  |
| `logs`      | `<state-dir>/logs/` 下的轮转日志                                                       |
| `project`   | 孤立的项目元数据（项目目录已不存在的注册项）                                          |

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
dry-run: pass --yes to delete
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

将 worktree 分支 rebase 到主线、fast-forward 主分支，然后删除 worktree 与分支。

| Flag             | 说明                                                                                                                  |
| ---------------- | --------------------------------------------------------------------------------------------------------------------- |
| `--onto <分支>`  | 目标主分支（默认主 worktree 当前分支）                                                                                |
| `--force`        | 放宽 clean-tree 检查；用 `git rebase --autostash`；回收时强删分支                                                     |
| `--check`        | 在临时 worktree 里预检 rebase 是否能干净通过，不改动真实 worktree 和分支                                              |

rebase 出现冲突时，`finish` 会打印恢复指引（`git status`、`git rebase --show-current-patch`，再根据情况选择 `--skip` / `--continue` / `--abort`），同时保留 worktree 与分支，让你解决冲突后重跑。

worktree 内已有进行中的 rebase 时，`finish` 直接退出，提示先完成已有 rebase。

只想提前判断会不会冲突、又不想把真实 worktree 留在半个 rebase 状态时，用 `--check`。

### 示例

```bash
chord worktree list
chord worktree remove feat-old --delete-branch
chord worktree finish feat-auth --onto main
```

## `chord resume <session-id>`

按 session id 恢复会话。与 `chord --resume` 不同，此命令能自动定位该 session 所属的 chord 管理 worktree 并切换过去——即便当前 cwd 不在那个 worktree 内也可以。

```bash
chord resume 20260428064910975
```

## `chord import <source> [file]`

将外部 agent 会话导入为 Chord 可恢复的会话。当前支持的 source：`opencode`、`codex`、`claude`。

### Flag

| Flag                      | 说明                                                                                                                |
| ------------------------- | ------------------------------------------------------------------------------------------------------------------- |
| `--project <path>`        | 写入哪个项目（默认当前目录）                                                                                        |
| `--sid <id>`              | 指定 Chord session id（默认自动生成）                                                                               |
| `--id <session-id>`       | 按 source 端 session id 查找（仅支持 `codex` 与 `claude`）                                                        |
| `--root <path>`           | 配合 `--id` 使用的根目录（codex 默认 `~/.codex/sessions`，claude 默认 `~/.claude/projects`）                        |
| `--tool-mode <mode>`      | 工具导入策略：`auto`、`text`、`structured`（默认值因 source 而异）                                                  |
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
