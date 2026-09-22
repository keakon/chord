# 使用指南

<!-- description: 日常使用：输入模式、快捷键、会话、slash 命令、worktree、图片和 headless 模式。 -->

让日常 TUI 操作不停下来：发消息、看工具卡、恢复会话，推动长任务。Chord 有两种运行方式：本地 TUI 和 `chord headless` 控制面，本页主要讲 TUI。

## 如何使用本页

无需从头到尾阅读本页：

- **第一个任务：**先看 [TUI 基本交互](#tui-基本交互)了解发送、工具卡和确认；第一次改文件前，先在[权限与安全](./permissions-and-safety_CN.md)定好规则。
- **长任务：**用 [`/loop`](#loop持续执行模式)让实现、测试和修复连续推进，不用反复催促。
- **并行工作：**用 [Worktree](#worktree)让每个任务在自己的 checkout 里干活，会话仍按仓库共享；恢复、分叉与导入见[会话](#会话)。

## 运行模式

Chord 有两条主要使用路径：

- **本地终端界面**：默认模式，在终端里输入任务、查看执行结果和处理确认
- **Headless 模式**：用 `chord headless` 从脚本、网关或聊天机器人控制 Chord

大多数个人开发场景推荐直接用本地 TUI。

## TUI 基本交互

启动后输入框默认聚焦，直接输入消息按 `Enter` 发送。

工具卡片会用终端安全的方式展示预览。会话工作目录内的文件显示相对路径，外部文件显示绝对路径。工具路径参数支持 `~/...` 前缀，会展开到用户主目录。可折叠的卡片默认收起，只保留行范围、匹配数量、命令意图和终态等关键信息；完整输出、诊断、截断详情和 artifact 引用展开后才显示。终态标记旁的 `▸` / `▾` 表示这张卡可以折叠，聚焦后按 `Space`、`Enter` 或 `o` 切换。

`write`、`edit`、`apply_patch`、`todo_write`、`delete`、`handoff` 这些卡片恒展开，正文（文件内容、diff、todo 列表、错误详情或计划路径）直接可见，标题不带折叠标记。`read`、`grep`、`glob`、成功的 `shell` 输出和通用工具调用可以折叠。无论卡片当前是否展开，`yy` 都会复制底层完整内容。

Chord 在后台运行时，当前聚焦的 Agent 从 busy 变为 idle 后，终端标题栏会显示一次性的 `✅` 完成标记。重新聚焦终端会清除该标记；普通的标签页/窗口焦点切换不会重复添加，除非之后又有新的后台工作完成。

常用操作：

- `Esc`：切换到 Normal 模式；main 视图运行中再按 `Esc` 可取消当前 turn
- `i`：回到 Insert 模式
- `j` / `k`：在消息卡片之间移动
- `gg` / `G`：跳到开头 / 结尾
- `/`：搜索消息
- `Ctrl+T`：打开消息目录
- `Ctrl+P`：切换主角色模型池
- `Ctrl+O`：打开 MCP server 选择器
- `Ctrl+E`：打开错误面板，查看当前会话里的错误记录
- `Ctrl+G`：导出诊断包
- `q`：双击退出
- `Ctrl+C`：双击退出

### 错误面板

在 Normal 模式下按 `Ctrl+E` 可打开错误面板，查看当前会话中出现过的错误，包括：

- **中间重试错误**：触发 key 轮换、模型 fallback 或流式重试的 API 错误，例如 429 限流、503 服务不可用、上下文超限或超时。这类错误会静默记录到错误面板，不会打断对话区。换成另一个 fallback 模型是例外：fallback 一开始就会弹出提示，写明失败原因和目标模型，状态栏也会在等待新模型期间持续显示目标、原因和已等待时长。
- **最终错误**：所有重试都失败后显示在对话区的红色错误块。

每条记录会展示：

- 时间（`HH:MM:SS`）
- Provider 和 model，例如 `Anthropic/claude-opus-5`
- 打码后的 API key 标识，例如 `key=sk-a...xyz9`，显示少量前缀和后缀便于安全识别
- HTTP 状态码（如果有）
- API 返回的错误 code / type（如果有）
- 按面板宽度换行后的错误消息

示例：

```text
14:25:38  Anthropic/claude-opus-5  key=sk-a...xyz9  HTTP 503  code=model_not_found
  No available channel for model sample/model under group default
```

导航：

- `j` / `k`：上下滚动一行
- `Ctrl+F` / `Ctrl+B`：向下 / 向上翻页
- `g` / `G`：跳到顶部 / 底部
- `Esc`：关闭面板

错误面板最多保留最近 80 条错误，按新到旧显示。排查模型为什么 fallback、哪些 key 频繁限流或某个 provider 是否持续返回 5xx 时，优先看这里。

## 信息面板

### `USAGE` 区

- `Context` 显示最近一次模型请求由 provider usage 返回的实际输入侧 token 负担。
- `Bytes` 和 `Messages` 描述将发送给模型的会话上下文。请求级 context reduction 运行后，`Bytes` 显示当前请求剪裁后的实际上下文字节数，并用 `↓` 标出当前请求相对未剪裁上下文的节省百分比：`(剪裁前字节数 - 剪裁后字节数) / 剪裁前字节数`。该比例不是跨请求累计值；已冻结复用的剪裁摘要只要仍用于当前请求，其节省量就会计入。恢复会话时，Chord 会预计算同一套剪裁用于展示，让 `Bytes` 一开始就是剪裁后的估算值，而不是等下一次请求后突然变小；在任何请求 surface 都无法准备时，回退显示当前持久上下文估算。
- `Bytes` 统计已安装的系统提示词、消息内容、图片负载，以及工具名/描述；不包含 JSON 转义开销、tool-call 参数 JSON、thinking 元数据，也不包含 stream 设置、思考预算等请求参数。
- 这些剪裁不是持久化压缩：较旧的工具结果通常会在请求中替换成更短的占位摘要，而持久化会话历史保持不变。`/compact`、自动压缩、工具输出增长以及系统提示词或工具定义变化会更新回退用的持久估算；新的请求准备会刷新实际发送请求大小，loop 模式运行中也会同步更新。
- `↑` 显示完整 prompt input，即未缓存输入、cache-read 和 cache-write token 的总和。存在缓存桶时，下面会分别显示 `Uncached`、`Cache R` 和 `Cache W`；`Cache R` 的百分比分母是完整输入侧 prompt tokens。输出 token 不参与计算，因为 prompt cache 只作用于输入侧。
- `Think` 行只在 provider 上报 reasoning/thinking tokens 时显示。这些 token 已包含在输出 token 计费中；该行只是可见性拆解，不是额外的 token 计费桶。
- `Calls` 显示当前聚焦 agent（主 agent、运行中的 SubAgent 或挂起的 task）发起的真实 LLM 请求次数。该数字来自持久化的 usage 账本，会话恢复后依然保留，上下文压缩不会清零。

### `TIME` 区

`TIME` 显示当前聚焦 agent 的墙钟累计时长：`Model`（LLM 流式输出）、`Tools`（工具执行）、`Cooldown`（key/模型冷却）、`User wait`（等待你确认或回答）。这些是各类操作时长的累计，不是把整段经过时间切成互斥区间；并行操作会分别计入对应的桶，所以总和可能超过实际墙钟经过时间。百分比以当前显示的各桶总和为分母。

- 不足 1 秒的桶不显示，也不参与百分比计算；所有桶都不足 1 秒时整个区块隐藏。
- `Model` 包含压缩草稿的流式时间。等待确认对话框、Question 提问或 Handoff 选择器时，工具卡只显示实际执行时间；确认、回答和 handoff 决策等待只计入 `User wait`，不会重复计入 `Tools`。
- 该区块跟随当前聚焦的 agent（主 agent、运行中的 SubAgent 或挂起的 task），会话恢复或 resume 后从 usage ledger 重建。

## 后台任务

有些活会活过发起它的那一轮：耗时较长的 shell 命令，子 agent 拉起的那些也算。只要它还在 running 或 stopping，Chord 就把它当作一个 job 跟踪，一轮结束并不会把它带走。

### JOBS 区

只要有 job 在 running 或 stopping，右侧信息面板就会多出一个 `JOBS` 区。每个 job 占一行，缩进 2 列，依次是标签（有描述用描述，没有就用命令）、耗时，行尾一个 `x`。子 agent 拉起的 job 和你的列在一起。

一个都没有时，这一节整块不出现。

### 窄终端

终端窄到放不下信息面板时，改由状态栏给一个纯文字计数 pill，例如 `2 agents · 1 job`。有空间就两个数都显示；地方不够先省掉 agent 那半，再不够就整个隐藏。点这个 pill 可以打开 JOBS 列表 overlay：信息面板可见时，同一份列表本来就在屏幕上。列表为空时 Chord 会给一条提示，不会弹个空 overlay 出来。

pill 和 `x` 都只认鼠标，没有鼠标上报的终端点不到。任何宽度下都可以用 `ctrl+j`（Normal 模式）打开同一份列表；进到列表后 `j` / `k`（或滚轮）移动选中行，`Enter` 打开该行的确认框。

### 停止 job

点行尾的 `x` 会弹出确认对话框，里面列出 job id、标签、命令、Owner、Status、耗时、最后一次输出的时间，以及最近若干行输出；输出被丢弃过的话，还会写明丢了多少字节、完整日志在哪。

确认只有 `y` 一个键。`n` 和 `esc` 取消，`Enter` 不绑定。确认之后这一行变成 stopping，`x` 也随之消失。

自己动手停的 job 不发 toast。job 结束后 owner 照样会收到完成结果，并写明是你停的；已经活过发起它那一轮的 job 会以 JOB RESULT 卡送达。

`Esc` 和 `Ctrl+C` 都不会停掉后台 job，包括还挂在当前轮里跑的那些：超过前台预算的长命令已经变成 job 了。停它只有一条路：在确认框里确认（点行尾 `x`，或 `ctrl+j` 后用 `j` / `k` 选中再 `Enter`）。job 也不会活过 Chord 进程，退出或切换会话都会终止所有 job。

### 终端标题

只有后台 job 在跑时，终端标题的 spinner 照样转。窗口失焦时，它是唯一还能告诉你「还在干活」的信号。

## 文件引用（`@path`）

在输入框里于行首或空格后输入 `@`，会打开文件补全。

- 裸 `@` 使用缓存的工作区文本文件索引。该索引包含已追踪文件，以及未追踪但未被忽略的文件；同时会跳过 Git ignore 路径、隐藏目录、二进制扩展名和常见噪声目录。
- 你开始输入根目录文件名前缀（例如 `@A`）时，Chord 还会额外直接检查 session working directory。因此像 `AGENTS.md` 这类即使被 `.gitignore` 或本地 Git exclude 排除出缓存索引的根目录文件，仍然可以补全。
- 如果当前 query 已经明显是路径形式，例如 `@docs/`、`@./`、`@~/` 或 `@.config/`，Chord 会切换为直接读取该目录的文件系统补全，而不是继续停留在缓存索引上。也因此，你显式朝某个被忽略路径输入时，路径模式补全仍可能显示这些 ignored 路径。
- 隐藏项默认仍不会显示。若需要查看，请让 query 本身显式包含隐藏路径语义，例如 `@.`、`@.env`、`@./.` 或 `@.config/`。
- 可以追加 1-based 行号后缀，只注入文本文件的一部分：`@path:42` 注入第 42 行，`@path:10-20` 注入第 10 到 20 行。接受补全时只替换路径部分，因此你已经输入的行号后缀会保留；如果真实文件名本身包含数字冒号后缀（例如 `note:12`），则优先按文件名处理，而不是解析成行号范围。
- 补全只是输入辅助。真正发送消息时，Chord 会重新解析最终文本中的 `@path`；如果你在发送前删掉了这个引用，就不会附加该文件。

## 会话

Chord 为当前项目维护持久化会话。

常见方式：

- `chord`：新建会话
- `chord --continue`：恢复当前项目最近的非空会话
- `chord --resume <session-id>`：恢复当前项目内指定 session 的会话
- `chord resume <session-id>`：从任意目录按 session id 恢复，自动定位会话所属的 chord 管理 worktree（或主仓库）并切换过去；带 `--fork-history[=N]` 则改为在某次压缩边界上 fork 出来再恢复（默认最近一次已应用边界；fork 原样复现那一代及压缩归档，usage 与运行状态从零开始）
- `chord import <source> [file]`：导入外部会话到 Chord（支持 `opencode`/`codex`/`claude`）
- `/new`：在 TUI 内创建新会话
- `/resume`：在 TUI 内选择历史会话
- `/rename <标题>`：设置当前会话的显示标题；单独执行 `/rename` 会清空标题

退出时若当前会话可恢复，Chord 会打印对应的恢复命令。

`/new` 会重置历史消息、待办事项和用量等会话状态。当前模型池、服务等级和 MCP 状态等运行时偏好会继续生效，直到进程退出。

自定义标题会显示在会话选择器和终端标题中，只属于显示元数据；`/rename` 不会改变 session ID、目录、对话记录或恢复命令。

### 恢复会话后编辑需要重新读取

恢复会话时，Chord 只会重建允许后续编辑所需的安全状态，不会重新读取文件，也不会把文件内容重新载入对话。如果某个文件自会话结束以来在磁盘上已变化，或它的读取历史没有持久化记录，那么下次对它的 `edit` / `apply_patch` 可能被拦截，agent 会先重新 `read` 再编辑。这是有意为之：避免基于过期文件内容做编辑。

### 导入外部会话

Chord 支持把外部 coding agent 的历史会话导入为可恢复的 session。

当前支持的来源：

- `opencode`：`opencode export <sessionID>` 导出的 JSON
- `codex`：Codex rollout JSONL（通常位于 `~/.codex/sessions/**/rollout-*.jsonl`）
- `claude`：Claude Code transcript JSONL（通常位于 `~/.claude/projects/**/<sessionId>.jsonl`）

示例：

```bash
# OpenCode
opencode export <sessionID> > export.json
chord import opencode export.json
chord resume <sid>

# Codex（直接文件）
chord import codex ~/.codex/sessions/YYYY/MM/DD/rollout-*.jsonl

# Codex（按 session id 查找）
chord import codex --id <session-id> [--root ~/.codex/sessions]

# Claude Code（直接文件）
chord import claude ~/.claude/projects/**/<sessionId>.jsonl

# Claude Code（按 session id 查找）
chord import claude --id <session-id> [--root ~/.claude/projects]
```

说明：

- 可识别的外部工具调用会转换成易读的 Chord 工具卡；不支持的记录会保留为普通文本，不会静默丢弃。
- 导入的工具卡只代表历史记录。编辑相关文件前，请重新 `read` 以获取最新内容和过期变更提醒。
- Anthropic 的签名思考内容会保留；其他推理内容默认省略，可用 `--reasoning visible` 作为普通文本导入。
- Claude 的支线 / 子 Agent 记录不会进入主会话。导入警告、跳过记录和转换统计会写入 `import-report.json`。

常用参数：

- `--project <path>`：写入哪个 project（默认当前目录）
- `--sid <id>`：指定 session id（默认自动生成）
- `--id <session-id>`：按来源工具自带的 session id 查找而非文件路径，Codex 的就是 `codex resume` 退出时打印的那个（支持 `codex` / `claude`）
- `--root <path>`：`--id` 查找的根目录
- `--reasoning off|visible|strict`：reasoning 导入策略（默认 `strict`）
- `--dry-run`：只解析输出报告，不写入 session
- `--json`：输出机器可读 JSON
- `--force`：覆盖已存在的 `--sid`

## Worktree

需要在同一项目里并行做多个任务且互不干扰时，Chord 可以为任务创建独立的 git worktree：

- `chord --worktree`：创建或进入 chord 管理的 worktree（不指定名字时自动按时间戳生成）
- `chord --worktree feat-auth` / `chord worktree feat-auth`：创建或进入名为 `feat-auth` 的 worktree（分支 `chord/feat-auth`）；与 `--continue` / `--resume` 搭配时，以该 worktree 为工作目录继续仓库里最近的会话
- `chord headless -d <repo> --worktree feat-auth`：headless 同款行为；`ready` 事件 payload 包含 worktree 的 `name`、`branch`、`path`、`repo_root`
- `chord worktree list`：列出当前仓库的 chord 管理 worktree
- `chord worktree remove <name>`：删除 worktree 及其 runtime cache，**保留分支与仓库的会话历史**。`--delete-branch` 仅在已合并时删分支，`--force` 强制删除脏 worktree 和分支。
- `chord worktree finish <name>`：先用目标分支更新工作树，再把结果压缩成一个提交合回目标分支，最后删除工作树和分支。可用 `--onto <branch>` 指定目标分支，或用 `--check` 在不改动现有工作树的情况下预检冲突。发生冲突时，目标分支保持不变；解决工作树中的合并冲突后重新运行 `finish` 即可。

创建或进入 worktree 会改变 Chord 运行所在的目录。你可以用 `chord --worktree <name>`，也可以用 `chord worktree <name>`；`worktree` 子命令同时承担 `list`、`remove`、`finish` 等管理操作。会话进行中也可以直接让 agent 切换 worktree。

worktree 工具和命令都要求 `PATH` 里有 `git`。找不到 git 时，agent 的 worktree 工具不会出现在工具列表里，创建或进入 worktree、`list`、`remove`、`finish` 都会拒绝执行，并直接说明缺的是 git 二进制，而不是报成仓库错误。记录过 checkout 的会话照样能恢复：Chord 会说明无法验证该 checkout，在解析出的仓库检出里继续，并保留记录，之后装回 git 再恢复仍会切回那个 checkout。

**落在哪里。** 默认在 `<state-dir>/worktrees/<repo-id>/<slug>`，也就是仓库之外。想换位置就设 `worktree.root`：相对路径以主仓库根为基准，`root: .chord/worktrees` 会落在 `<repo>/.chord/worktrees/<slug>`。这个目录在仓库内时，Chord 会在其中放一个内容为 `*` 的 `.gitignore`，这些 checkout 就不会出现在未跟踪文件里；该文件只负责 `git status` 整洁，而 chord 自己的 `grep` / `glob` 会跳过这个根目录。但别的工具并不知道它：仓库内的 checkout 就是磁盘上的第二份代码树，凡是依赖索引或全仓扫描的工具（LSP 建索引、`docker` build context、会遍历整个仓库的测试运行器）都可能把它一并算进去。留在默认位置就不会有这个问题。

**里面有什么。** 只有被 git 追踪的文件。主工作区未提交的改动不会带过去，被 gitignore 的内容也不会：本地 `AGENTS.md`、`.chord/config.yaml`、agents、skills、plans、memory 都留在主工作区，worktree 里的会话从主工作区读取它们。例外是 `AGENTS.md` 与项目技能：checkout 里自带副本时就用它，所以分支可以带上自己的指令与技能。其余内容的表现和主工作区一致——同一套子代理与记忆。想让某些被忽略的文件跟过去（本地 env、机器相关配置等），就把它们的 pattern 写进仓库根的 `.worktreeinclude`（gitignore 语法）：Chord 在创建时把匹配且被忽略的文件复制过去，已被跟踪的文件绝不覆盖。没有这个文件时，复制 `.env*`。复制只在创建那一刻发生，之后主工作区再改也不会同步过去或同步回来；而默认复制 `.env*` 意味着本地凭据可能落进每个 checkout，所以 pattern 只写 worktree 真正需要的那几个。

**会话按仓库共享。** 同一仓库的所有 checkout 共用一个 session store，所以在 worktree 里开的会话，在主工作区能看到、也能继续，反过来也一样。runtime cache 仍按 checkout 分开，exports 跟着会话走。会话会记录自己当时所在的 checkout，`/resume` 会在那一行标出来，按 checkout 名字也能搜到：`chord resume <id>`、`chord --resume <id>`、`chord --continue` 都会切回去，继续时落在 worktree 里也会把它记下来；记录的那个 worktree 已不存在时先给出提示：`chord resume` 回主工作区继续，`--resume` 与 `--continue` 则在启动 chord 时所在的 checkout 里继续。`chord worktree remove` 和 `chord worktree finish` 都不会删除仓库的会话历史。

**checkout 不是独占的。** 创建或进入 worktree 时，只要目录已存在就复用同一个，也不会阻止两个会话在同一个 checkout 里干活：它们看到的是同一份未提交改动，也可能互相覆盖文件。要并行推进的任务就各给一个 worktree；已经攒了未提交改动的 checkout，就当成只能有一个写者。

**权限跟着会话，不跟着 checkout。** 权限规则对同一仓库的每个 checkout 都生效：主工作区里写 `write src/**: allow`，在 `<worktree>/src/` 里同样允许写；也没法写出「只允许某一个 checkout」的规则。Chord 把仓库内的路径按仓库相对拼写去匹配，所以绝对路径规则永远匹配不到它们。worktree 是用来并行干活的，不是用来收窄权限的。hook、agent 配置、权限规则和 worktree 创建配置都在会话启动时从主工作区读取，会话中途进出 worktree 不会改变它们——想用分支上改过的配置，就在该 checkout 新开一个会话。

## 常用本地控制命令

以下命令由本地运行时处理，不会原样发送给模型。在 TUI 中输入 `/` 会打开补全列表。`Tab` 只补全高亮命令，不执行。`Enter` 在输入还不是该命令时先补全，同一次按键接着执行或发送。继续输入缩小列表时，高亮仍停在当前那一行，回车执行的就是它，而不是缩完后的第一项：

- `/new`：新建会话
- `/resume`：恢复会话
- `/rename <标题>`：设置当前会话的显示标题；单独执行 `/rename` 会清空标题，但不会改变 session ID
- `/models`：查看模型池状态或切换当前视图对象的模型池（main 视图 = 当前主角色；SubAgent 视图 = 该 agent）
- `/models --agent <name> <pool>`：直接设置指定 agent 的模型池
- `/role`：弹出角色对话框并切换当前主角色（builder、planner 与自定义主模式角色），即 `Shift+Tab` 的对话框形式；`/role <name>` 直接切换不弹对话框；`/role status` 打印当前角色与可选角色列表
- `/mcp`：打开 MCP server 选择器；`/mcp status` 输出状态；`/mcp enable|disable <server>` 可切换手动 server。运行时切换会在下一次 LLM 请求生效，不影响当前正在进行的请求。
- `/compact`：手动触发上下文压缩，将当前对话摘要为结构化归档，详见 [上下文管理：上下文压缩](./context-management_CN.md#上下文压缩compaction)
- `/tier standard|fast|slow`：设置后续模型请求的 service tier（包括尚未开始的后续 retry round）。空的 `/tier` 不是状态查询命令；当前有效 tier 请看侧边栏/状态显示。如果手动输入当前 provider/model 不支持的 tier，Chord 会保持当前 tier 不变并显示错误提示。
- `/yolo on|off`：临时放开主 agent 对普通工具的权限检查。开启期间，文件编辑、shell 命令这类调用直接放行：`ask` 不弹确认框，`deny` 规则也不拦截。放开是单向的，只放宽不收紧：关闭 YOLO 时能用的工具，开启期间不会变得不可用；关掉 YOLO 即恢复原权限。`handoff`、`delegate`、`cancel` 仍按配置的规则判定：`allow` 照常放行，`deny` 照常拒绝，`ask` 不再弹确认框、直接放行，通配默认与关闭时行为一致。`done` 和 `compact_context` 维持各自的专门语义。Agent 运行中也可以切换 YOLO：执行期的变化会立刻影响后续工具调用，LLM 可见的工具描述和权限提示则在下一次请求刷新。开启期间 SubAgent 也会继承该模式：需要 `ask` 的调用（普通工具和机制工具都一样）不再弹确认框，但 `deny` 规则依然拒绝。切换 YOLO 会立即影响 SubAgent 的后续调用。
- `/help`：切换内置 cheatsheet 浮层（等同 Normal 模式按 `?`）

启用非标准服务等级后，侧边栏会显示当前值。如果切换模型后该等级不再可用，它会以灰色删除线显示。`Ctrl+R` 只在当前服务商和模型支持的等级之间切换。

下面几个命令有更多交互细节，单独展开说明。

### 项目记忆（Memory）

Chord 可选的跨会话项目记忆（稳定的偏好、项目事实与可复用工作流）已独立成页：[项目记忆](./project-memory_CN.md)。那页说明记录了什么、摘要如何加载进会话、怎么开启自动抽取，以及如何审阅和删除条目。Memory 没有对应的斜杠命令。

### MCP 选择器

按 `Ctrl+O` 打开 MCP server 选择器。它会列出已配置的 MCP server、连接状态，以及手动 server 当前是否启用/禁用。可用 `j` / `k` 移动，`Enter` 切换当前手动 server，`e` 启用，`d` 禁用，`Esc` 关闭。

Agent 运行中也可以打开选择器查看 MCP 状态，不需要等待当前 turn 结束。启用 / 禁用操作在运行中也允许执行，但会延迟生效：当前正在进行的请求继续使用它启动时的 MCP 工具表面和 prompt，变化后的 MCP 状态会在下一次 LLM 请求中体现。自动启动的 MCP server 在选择器中始终只读；只有配置了 `manual: true` 的 server 才能在运行时切换状态。

询问代理能用哪些 MCP 工具时，回答范围是当前角色可见的工具，不代表所有已连接服务器。查看连接状态请用选择器或 `/mcp status`。

外部文件、网页、命令输出、图片以及 MCP 说明和结果只是参考数据，不能自行授权修改任务或执行命令。主代理和子代理的指引都明确了这一点，但提示词不是安全沙箱：仍应谨慎配置工具权限，并审查敏感操作。

### `/export`：导出当前会话

将当前会话导出为 Markdown（默认）或 JSON。

```text
/export                  # 默认：导出为 Markdown，保存到 session artifacts 目录
/export ~/out.md         # 指定输出路径
/export --json           # 导出为 JSON 格式
/export ~/out.json       # 文件名以 .json 结尾时自动识别为 JSON
```

导出内容包括全部对话消息以及当前会话的用量统计。导出成功后 TUI 会显示保存路径。

### `/stats`：用量统计浮层

打开一个浮层，分两个维度浏览用量数据：

- **范围（Scope）**：`Session`（当前会话）或 `Project`（当前项目的聚合统计）。按 `s` 键切换。
- **视图（View）**：`Overview`（总览）、`Models`（按模型细分）、`Agents`（按 agent 细分）。Project 额外支持 `Dates`（按日期细分）。按 `Tab` / `Shift+Tab` 切换视图。

Session Overview 展示：LLM 调用次数、输入/输出 token、缓存读写 token、reasoning token、估算成本；发生过上下文压缩时，还会显示压缩生命周期计数（如 `applied`、`skipped/model_driven`）。Models 和 Agents 视图以表格展示各维度详细拆解。

Project 统计自动从本地 sessions 目录聚合，支持 `today`、`7d`、`30d`、`90d`、`all` 五种时间范围。切换到 Project 时可能短暂显示「加载中」，稍后会展示统计数据。

浮层打开期间，所有活动搜索自动取消。按 `Esc` 关闭。也可在 Normal 模式用 `$` 键直接打开。

### `/rules`：权限规则管理器

打开一个浮层管理已记住的权限规则。即使当前还没有规则也会打开，因此可以手动新增规则。

- `a`：手动添加规则
- `↑` / `↓` 或 `j` / `k`：移动光标
- `d`：删除当前规则
- `o`：在系统编辑器中打开规则对应的配置文件
- `Esc` / `q`：关闭

手动添加规则时，填写 tool 名称和 pattern，然后用 `Ctrl+S` 切换作用域（`session` / `project` / `global`），用 `Ctrl+A` 切换动作（`allow` / `ask` / `deny`）。tool 和 pattern 必填。不会匹配后续工具调用的 pattern 也可以保存，但在实际命中前不会产生效果。

规则旁会显示作用域（`session` / `project` / `global`）和落盘文件路径。`session` 规则只在当前会话内生效；`project` 规则写入当前项目的 `.chord/agents/<role>.yaml`；`global` 规则写入用户配置目录的 `agents/<role>.yaml`（默认 `~/.config/chord/agents/<role>.yaml`）。这些规则会直接更新对应 agent 的 `permission` 配置，删除规则时也会从同一 agent 配置文件移除。

权限确认弹窗也可以用 `M` 添加记住规则。在规则选择器中按 `E` 可在保存前编辑建议 pattern。Delete 确认时，选择器按覆盖本次待审批目标的数量，优先给出可复用的父目录规则。全局 `*` 通配项始终保留；本次请求的所有目标都位于当前工作目录内，选择器还会保留一个相对当前目录的 `**` 选项，方便放行当前目录下后续删除。`**` 和 `*` 都不会默认选中。

### `/loop`：持续执行模式

持续执行模式让 agent 在每一轮结束后自动继续，无需反复催促。适合那种「帮我搞定这个功能」的一次性指令：你只需发一条消息，agent 会自己迭代、验证，直到任务完成、确实卡住，或你明确确认退出。

只有当前 MainAgent 角色可以使用 `done` 工具，`/loop` 才可用：`done` 已注册且没有规则拒绝它。纯通配的 `"*": deny` 不算拒绝：挂载 `done` 正是进入 loop 模式这个动作本身，因此 loop 本身就是授权。想让某个角色用不了 loop 模式，写 `done: deny`，此时 `/loop on` 会被拒绝并给出 toast。

启用方式：

```text
/loop on                           # 开启，agent 会尝试完成当前会话中的所有剩余任务
/loop on 实现用户认证模块            # 开启并指定目标任务
/loop off                          # 关闭，回到普通模式
/loop                              # 查看当前状态
```

`/loop on` 后面的文字会作为任务目标发给 agent。省略时默认为「继续完成当前会话中所有剩余任务」。每次开启默认限制最多 10 轮迭代，超出自动停止。

**工作流程：** `/loop on` 后发送一条任务指令（如「实现用户认证模块」）。agent 会按以下循环推进：

1. **executing**：执行任务，调用工具做实际工作
2. **assessing**：评估当前进度，决定下一步
3. **verifying**：运行校验（跑测试、lint 等）
4. **继续或申请退出**：如果仍有工作，就继续推进；如果它认为 loop 可以结束，必须通过 `done` 工具提出退出请求

Agent 申请结束时，Chord 会检查退出条件，并用本地确认框展示完成报告。确认后停止；拒绝则继续运行。YOLO 模式不会绕过这次确认，也不会绕过 `done` 权限。

`done` 工具只在 loop 运行期间挂载。不在 loop 中时，它根本不在工具面上，因此普通会话不必携带它的定义，模型也不用在「直接回复」和「调用完成工具」之间做选择，普通模式下 agent 直接用常规 assistant 正文结束即可。执行 `/loop on` 时才挂载它：支持 Chord request-only 动态工具挂载的模型（Responses 系模型与 Kimi dynamic tools）会在下一次请求里把它作为一次性动态工具声明补进去，不损失 prompt cache；其余模型则重建一次工具面，那一次请求会打断 prompt cache 复用。如果 `done` 已经在工具面里，Chord 会跳过挂载，不会重复注入。随后 loop 模式通过当前 runtime 的工具调用要求和 continuation 指令，把 `done` 作为明确的退出请求。执行 `/loop off` 会把 `done` 从工具面收回，后续工作恢复普通响应方式，同时取消尚未发送给模型的 loop continuation。

Loop 模式还会检测连续重复的相同工具调用。发现卡住后，Chord 会打断重复；多次触发后，会询问你是停止还是继续。

推荐用法：

1. 用明确目标开启 loop（例如 `/loop on 实现功能 X，并补测试`）
2. 一次性给出完整指令和验收标准
3. 让 agent 自己继续完成编辑、跑测试、修回归和再次验证
4. 只有在最终 `done` 请求出现、且你确认任务真的完成时才结束

这样可以减少「继续」「把测试也跑一下」等人工催促。但不要让 `/loop` 漫无目标地运行：如果任务偏探索、需求不明确或经常需要产品决策，普通模式更容易控制。

如果任务确实受阻，agent 仍可使用 `<blocked>category: reason</blocked>` 报告阻塞。你也随时可以用 `Esc` 取消当前轮。

**状态栏提示：** 开启后 TUI 状态栏会显示 `[↻]` 标记，告诉你当前处于持续执行模式。

**适用场景：** 多步骤任务（生成代码 → 写测试 → 调试 → 优化）、需要反复迭代的开发工作。不适合：一次性查询、单纯的问答。

也可以**自定义** slash 命令（按项目或全局），见 [扩展与定制：自定义 slash 命令](./customization_CN.md#自定义-slash-commands)。

## 多 Agent 与焦点切换

Chord 支持 MainAgent 与 SubAgent 协作。

- `Shift+Tab`：Insert 模式下循环切换 main agent 的模式（role，显示在状态栏；仅在 main 视图生效）；Normal 模式下在 main agent 与各 sub agent 之间循环切换当前查看的 agent 视图

在 SubAgent 视图中可查看该 agent 的上下文与输出，也可提交新输入。completed、failed、cancelled 只描述上一次执行结果，不会让该视图变成只读。

卡片编号以当前查看的 Agent 为单位：main transcript 和每个 SubAgent transcript 都分别从 `#1` 开始并独立递增。切换 Agent 视图时，Chord 会重建该 Agent 当前可用的完整历史；被 rehydrate 的委派任务也会包含其较早实例的历史，因此可用 `Ctrl+B` / `PgUp`、`gg`、搜索和消息目录继续浏览前序卡片，而不是只看到实时尾部。

恢复会话后，所有还原的 agent 都处于 idle，不会假装旧进程里的执行仍然存活。SubAgent 在恢复前的任意状态（包括 waiting、completed、failed、cancelled）都可手动继续：先聚焦该 SubAgent，提交空输入可沿现有上下文继续，提交文字则会创建 follow-up turn。Chord 会先重新获取 SubAgent 并发槽并将其切回 running；空输入不会追加虚构的用户消息。从会话恢复的待处理 mailbox 在 idle 时继续排队，只会在这次明确的手动继续或输入操作中投递；正常运行期间实时产生的 mailbox 事件会立即投递给所属 agent。

启用 `todo_write` 后，当 agent 确实在多个独立且活跃的工作流之间交错推进时，todo 列表可以同时存在多个 `in_progress`。每一项都必须使用唯一的 `active_form`；委派任务也应分别对应独立且仍然活跃的工作流。不要把尚未开始、仅计划中、受前置条件阻塞或只是等待条件的事项标成 `in_progress`。

## 图片与 PDF 输入

当前支持：

- 使用 `Ctrl+V` 或 `Alt+V` 从系统剪贴板附加图片或 PDF
- 当前模型支持对应输入类型时，把图片或 PDF 文件作为附件发送给当前聚焦的 Agent
- 在支持的终端里直接查看图片；PDF 会发送给模型，并在转录区显示为文件 chip，但不会 inline 预览
- 编辑含图片或 PDF 的历史用户消息；如果这条消息已经在转录尾部，就直接在当前会话里回填编辑，否则会 fork 新会话；按路径恢复的附件会在重新发送该消息时再次加载
- 当工具被权限规则允许、有效 model pool 的第一个模型支持 image 输入且这个第一个模型不是 OpenAI Chat Completions API 时，模型可以调用内置 `view_image` 工具把本地 PNG/JPEG 载入上下文。该工具使用与 `read` 相同的本地路径权限处理。

`view_image` 是否可用由有效模型池中的第一个模型决定。使用 OpenAI 模型且工具需要返回图片或文件时，请使用 Responses API；Chat Completions 可以接收用户消息中的图片，但不能接收工具返回的图片。会话中出现图片 / PDF 工具结果后，Chord 会跳过无法安全重放这些内容的备用模型。

常用操作：

- 主输入框中的 `Ctrl+V` 或 `Alt+V`：异步读取系统剪贴板中的图片或 PDF。PNG/JPEG 可直接使用，BMP/WebP 会归一化为 PNG/JPEG。图片会插入类似 `[image1.png]` 的 inline 占位符；PDF 会作为文件附件添加。读取期间按 Enter 会提示等待，因此立即发送不会丢失附件。Windows Terminal 以及由其承载的 WSL 会话请使用 `Alt+V`。
- `Cmd+V`、右键粘贴、菜单粘贴及其他终端 paste 事件：只粘贴文本，绝不会检查剪贴板附件。
- 权限确认弹窗文本框中的 `Cmd+V`：只粘贴文本。
- 每条输入框消息最多支持 5 张 inline 图片附件
- 手动输入 `[image1]` 这类占位符文本本身不会附加图片；只有 Chord 内部插入的 inline 图片占位符才会绑定真实附件
- `@` 文件补全只会在当前模型支持对应输入类型时显示图片 / PDF 文件。手动完整输入的图片 / PDF `@` 引用仍会作为附件接受；若当前模型不支持，发送时会忽略并提示。
- 若要按路径附加图片或 PDF：先在输入框中填入文件路径，再给 `insert_attach_file` 配一个自定义快捷键
- 似乎已加密的 PDF 会显示警告；Chord 仍允许发送，因为最终是否可读取以 provider 解析结果为准。
- `Enter` / `o` / `Space`：Normal 模式下打开当前用户消息或工具结果中的图片

## 复制文本

- 可在转录区内用鼠标拖选 TUI 里的文本
- `yy` 复制当前聚焦的消息卡片；工具卡片会按 Markdown 复制，包含 `# Tool call`、`## Arguments`、`## Result`、`## Diff` 等段落（`edit` 卡片会用 `## old_string` / `## new_string` 展示替换文本，而不是 diff；只有启用时才增加 `## replace_all`）。Done 的拒绝理由会单独放在 `## Rejection reason` 段落中。
- `Cmd+C`：在会把这个按键转发给 Chord 的 macOS 终端中，复制当前转录区选中的文本；若焦点在权限确认弹窗的输入框，则复制该输入框内容
- `Ctrl+C`：仍用于取消/退出，不用于复制转录区文本

## Headless 模式

`chord headless` 适合：

- bot / gateway 集成
- 自动化脚本驱动
- 无需本地 TUI 的外部控制面接入

协议格式：

- stdin：一行一条 JSON 命令
- stdout：一行一条 JSON 事件

详细说明见 [Headless 集成](./headless_CN.md)。

## 模型编辑工具

Chord 会根据当前模型选择文件编辑工具：gpt-5 及之后主版本家族（`gpt-5`、`gpt-5-mini`、`gpt-5-nano`、`gpt-5-codex`、任意 `gpt-5.*` 名称，以及 `gpt-6-astra` 等更高的主版本）和 `codex-auto-review` 使用 `apply_patch`，其余模型默认 `edit`，完整矩阵和依据见 [编辑工具](./edit-tools_CN.md)。在兼容的 Responses 端点上，补丁原生模型还会把 `apply_patch` 以 freeform custom tool 形式发送，而不是 JSON function tool。

模型名或网关的实际表现与推断不符时，可以用 `compat.apply_patch.enabled`（工具面）和 `compat.apply_patch.freeform`（发送形式）按 provider 或模型覆盖。两个键都是三态：省略表示按模型名和端点推断，只需设置要改的那个。字段权威说明见 [配置与认证](./configuration_CN.md)。

## 相关文档

- [配置与认证](./configuration_CN.md)
- [权限与安全](./permissions-and-safety_CN.md)
- [编辑工具](./edit-tools_CN.md)
- [扩展与定制](./customization_CN.md)
- [常见问题排查](./troubleshooting_CN.md)
