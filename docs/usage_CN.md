# 使用指南

本文档介绍 Chord 的主要运行模式、核心交互方式，以及你在日常使用中最常用到的功能。

## 运行模式

Chord 有两条主要使用路径：

- **本地 TUI**：默认模式，直接在当前进程内运行 MainAgent
- **Headless 控制面**：通过 `chord headless` 使用 stdio JSONL 与外部 gateway / bot 集成

大多数个人开发场景推荐直接使用本地 TUI。

## TUI 基本交互

启动后输入框默认聚焦，可以直接输入消息并按 `Enter` 发送。

工具调用卡片会尽量保持路径简洁：对于 `Read`、`Write`、`Edit` 这类文件工具，如果路径位于当前工作目录内，TUI 会优先显示相对路径；如果路径位于当前目录之外，则继续显示绝对路径。

工具参数和工具结果会按终端安全的纯文本展示。Chord 会把外部输出中的 ANSI/control sequence 转义成字面内容，而不是当作终端样式执行；看起来像 Markdown 的普通工具结果也会保持原始文本，不会被重新排版成 assistant Markdown。

常用操作：

- `Esc`：切换到 Normal 模式；在 main 视图运行中再次按 `Esc` 可取消当前 turn
- `i`：回到输入模式
- `j` / `k`：在消息卡片之间移动
- `gg` / `G`：跳到开头 / 结尾
- `/`：搜索消息
- `Ctrl+J`：打开消息目录
- `Ctrl+P`：切换主角色模型池
- `Ctrl+G`：导出诊断包
- `q`：双击退出
- `Ctrl+C`：双击退出

## 会话

Chord 会为当前项目维护持久化会话。

常见方式：

- `chord`：新建会话
- `chord --continue`：恢复当前项目最近的非空会话
- `chord --resume <session-id>`：恢复指定会话
- `chord resume <session-id>`：跨 worktree 恢复 — 自动定位会话所在的 chord 管理 worktree（或主仓库），切换目录后恢复
- `chord import <source> <file>`：导入外部会话到 Chord（当前仅支持 `opencode` export JSON）
- `/new`：在 TUI 内创建新会话
- `/resume`：在 TUI 内选择历史会话

退出时，如果当前会话可恢复，Chord 会打印对应的恢复命令。

### 导入外部会话

Chord 支持把外部 coding agent 的历史会话导入为 Chord 可恢复的 session。

当前支持的来源：

- `opencode`：`opencode export <sessionID>` 导出的 JSON

示例：

```bash
opencode export <sessionID> > export.json
chord import opencode export.json
chord resume <sid>
```

说明（Phase 1）：

- tool 调用与结果会以“纯文本”形式导入（不做结构化 tool replay）。
- reasoning 不会作为 provider thinking payload 导入。默认（`--reasoning strict`）会丢弃非签名 reasoning；使用 `--reasoning visible` 可把 reasoning 作为普通文本导入。
- 导入后的 session 会包含 `import-report.json`，记录转换统计与 warnings。

常用参数：

- `--project <path>`：写入哪个 project（默认当前目录）
- `--sid <id>`：指定 session id（默认自动生成）
- `--dry-run`：只解析输出报告，不写入 session
- `--json`：输出机器可读 JSON
- `--force`：覆盖已存在的 `--sid`

## Worktree

需要在同一项目里并行做多个任务且互不干扰时，Chord 可以为任务创建独立的 git worktree：

- `chord --worktree`：创建或进入一个 chord 管理的 worktree（不指定名字时自动按时间戳生成）
- `chord --worktree feat-auth`：创建或进入名为 `feat-auth` 的 worktree（分支 `chord/feat-auth`）；可与 `--continue` / `--resume` 组合，作用于该 worktree 自身的会话历史
- `chord headless -d <repo> --worktree feat-auth`：headless 同款行为；`ready` 事件 payload 包含 worktree 的 `name`、`branch`、`path`、`repo_root`
- `chord worktree list`：列出当前仓库的 chord 管理 worktree
- `chord worktree remove <name>`：删除 worktree 及其 sessions/cache/exports；默认保留分支。`--delete-branch` 仅在已合并时删除分支；`--force` 强制删除脏 worktree 和分支。
- `chord worktree finish <name>`：把 worktree 分支 rebase 回主线并回收（默认主线：main worktree 当前检出的分支），然后 fast-forward 更新主线、删除 worktree 并删除分支。可用 `--onto <branch>` 指定主线分支，用 `--force` 放宽 clean 检查；若 rebase 出现冲突，命令会输出分步指引（`git status`、`git rebase --show-current-patch`，再按情况选择 `--skip` / `--continue` / `--abort`），并保留 worktree/分支供你处理后重跑。若该 worktree 已有进行中的 rebase，`finish` 会先明确提示“先收尾当前 rebase”，不会再尝试启动新的 rebase。

创建/进入 worktree 属于启动级动作（会改变 chord 运行所在的 project），所以它放在 `chord` 的 flag 上、不归属 `chord worktree` 子命令；后者只承担纯管理操作（`list`、`remove`、`finish`）。

Worktree 路径位于 `<state-dir>/worktrees/<repo-id>/<slug>`（仓库目录之外），每个 worktree 拥有独立的 project key，session 与 cache 自动隔离。worktree 仅包含被 git 追踪的文件；主仓库未提交的改动不会自动带过去。

## 常用本地控制命令

以下命令由本地运行时处理，不会原样发送给模型：

- `/new`：新建会话
- `/resume`：恢复会话
- `/models`：查看模型池状态或切换当前视图对象的模型池（main 视图 = 当前主角色；SubAgent 视图 = 该 agent）
- `/models --agent <name> <pool>`：直接设置指定 agent 的模型池
- `/export`：导出当前会话
- `/compact`：手动触发上下文压缩
- `/stats`：查看用量统计
- `/diagnostics`：导出用于排障的诊断包
- `/loop on [target]` / `/loop off`：开启或关闭持续执行模式

## 多 Agent 与焦点切换

Chord 支持 MainAgent 与 SubAgent 协作。

- `Tab`：切换 main agent 角色
- `Shift+Tab`：在 main agent 与各 sub agent 之间切换焦点

在 SubAgent 视图中，你可以查看该 agent 的上下文与输出；已结束的 SubAgent 视图是只读的。

## 图片输入与查看

当前支持：

- 从剪贴板粘贴图片
- 把图片文件作为附件发送给当前聚焦的 Agent
- 在支持的终端里直接查看图片

常用操作：

- `Ctrl+V` / `Cmd+V`：优先读取剪贴板图片，否则粘贴文本
- `Ctrl+F`：把输入框中的图片路径加入当前消息附件
- `Enter` / `o` / `Space`：在 Normal 模式下打开当前消息中的图片

## 复制文本

- 可在转录区内用鼠标拖选 TUI 里的文本
- `Cmd+C`：在会把这个按键转发给 Chord 的 macOS 终端中，复制当前转录区选中的文本
- `Ctrl+C`：仍用于取消 / 退出，不用于复制转录区文本

## Headless 模式

`chord headless` 适合以下场景：

- bot / gateway 集成
- 自动化脚本驱动
- 无需本地 TUI 的外部控制面接入

它使用：

- stdin：一行一个 JSON 命令
- stdout：一行一个 JSON 事件

更详细的说明见 [Headless 集成](./headless_CN.md)。

## 日常使用建议

- 首次接入时，先用最小 provider 配置确认请求能跑通
- 需要更强代码感知时，再配置 LSP
- 需要外部工具接入时，再添加 MCP 或 Hooks
- 对高风险工具保持 `ask`，不要直接全局 `allow`

## 相关文档

- [配置与认证](./configuration_CN.md)
- [权限与安全](./permissions-and-safety_CN.md)
- [扩展与定制](./customization_CN.md)
- [常见问题排查](./troubleshooting_CN.md)
