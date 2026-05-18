# 使用指南

主要运行模式、核心交互方式以及日常高频功能。

## 运行模式

Chord 有两条主要使用路径：

- **本地 TUI**：默认模式，直接在当前进程内运行 MainAgent
- **Headless 控制面**：通过 `chord headless` 使用 stdio JSONL 与外部 gateway / bot 集成

大多数个人开发场景推荐直接用本地 TUI。

## TUI 基本交互

启动后输入框默认聚焦，直接输入消息按 `Enter` 发送。

工具调用卡片会尽量保持路径简洁：`Read`、`Write`、`Edit` 等文件工具在路径位于当前工作目录内时优先显示相对路径；位于当前目录之外则显示绝对路径。

工具参数和工具结果按终端安全的纯文本展示。外部输出中的 ANSI/control sequence 会被转义成字面内容，不会当作终端样式执行；看起来像 Markdown 的普通工具结果也会保持原始文本，不会被重新排版成 assistant Markdown。

`Read` 和 `Write` 工具卡片会用带行号、语法高亮的预览展示文件内容。`Edit` 卡片会在识别文件类型时为 unified diff 做语法高亮；`.mdx` 文件会复用 Markdown 高亮，即使是不支持的扩展名，diff 删除/新增行的红/绿色背景也会保留。内容较长时默认只显示前 10 行，并给出 `[space] toggle expand/collapse` 提示；聚焦卡片后按 `Space`、`Enter` 或 `o` 可展开或折叠。

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
- `Ctrl+G`：导出诊断包
- `q`：双击退出
- `Ctrl+C`：双击退出

## 会话

Chord 为当前项目维护持久化会话。

常见方式：

- `chord`：新建会话
- `chord --continue`：恢复当前项目最近的非空会话
- `chord --resume <session-id>`：恢复指定会话
- `chord resume <session-id>`：跨 worktree 恢复——自动定位会话所在的 chord 管理 worktree（或主仓库），切换目录后恢复
- `chord import <source> [file]`：导入外部会话到 Chord（支持 `opencode`/`codex`/`claude`）
- `/new`：在 TUI 内创建新会话
- `/resume`：在 TUI 内选择历史会话

退出时若当前会话可恢复，Chord 会打印对应的恢复命令。

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

- **Tools**：默认策略按来源区分。Codex 使用保守的 `auto` 工具导入，只在高置信度映射时保留结构化工具；OpenCode 会把 tool/shell payload 导入为可读纯文本，不支持 `--tool-mode structured`；Claude 默认使用 `--tool-mode auto`，仅在具备 signed thinking 时保留结构化工具调用，否则降级为纯文本。
- **Reasoning**：Chord 只会把 Anthropic signed thinking 导入为 `thinking_blocks`；非签名 reasoning 默认（`--reasoning strict`）丢弃，使用 `--reasoning visible` 可作为普通文本导入。
- 导入后的 session 包含 `import-report.json`，记录转换统计与 warnings。
- 运行时会在每次请求前对历史消息做 provider 兼容标准化，导入后切换模型/provider 不会重播不兼容 payload。

常用参数：

- `--project <path>`：写入哪个 project（默认当前目录）
- `--sid <id>`：指定 session id（默认自动生成）
- `--id <session-id>`：按来源 session id 查找输入文件（支持 `codex` / `claude`）
- `--root <path>`：`--id` 查找的根目录
- `--tool-mode auto|text|structured`：工具导入策略（默认值取决于来源）
- `--reasoning off|visible|strict`：reasoning 导入策略（默认 `strict`）
- `--dry-run`：只解析输出报告，不写入 session
- `--json`：输出机器可读 JSON
- `--force`：覆盖已存在的 `--sid`

## Worktree

需要在同一项目里并行做多个任务且互不干扰时，Chord 可以为任务创建独立的 git worktree：

- `chord --worktree`：创建或进入 chord 管理的 worktree（不指定名字时自动按时间戳生成）
- `chord --worktree feat-auth` / `chord worktree feat-auth`：创建或进入名为 `feat-auth` 的 worktree（分支 `chord/feat-auth`）；可与 `--continue` / `--resume` 组合，作用于该 worktree 自身的会话历史
- `chord headless -d <repo> --worktree feat-auth`：headless 同款行为；`ready` 事件 payload 包含 worktree 的 `name`、`branch`、`path`、`repo_root`
- `chord worktree list`：列出当前仓库的 chord 管理 worktree
- `chord worktree remove <name>`：删除 worktree 及其 sessions/cache/exports；默认保留分支。`--delete-branch` 仅在已合并时删除分支；`--force` 强制删除脏 worktree 和分支。
- `chord worktree finish <name>`：先把目标主线分支合并进真实 worktree 分支（默认目标分支为主 worktree 当前检出的分支），再把完成后的 worktree 状态以单个 squash commit 合回该目标分支，随后 fast-forward 更新目标分支，并删除 worktree 与分支。可用 `--onto <branch>` 指定目标分支，或用 `--check` 在临时 worktree 中预检“该目标分支能否干净合入 worktree”而不改动真实 worktree。若这一步 merge 会冲突，`finish` 会报告冲突文件，保持目标分支不变，并把真实 worktree 保留在这次 merge 中，供你解决后重跑。若该 worktree 已有进行中的 rebase 或 merge，`finish` 会先明确提示“先收尾当前操作”，不会叠加新的 finish 流程。

创建或进入 worktree 会改变 Chord 运行所在的 project。你可以使用 `chord --worktree <name>`，也可以使用 `chord worktree <name>`。`worktree` 子命令同时承担 `list`、`remove`、`finish` 等管理操作。

Worktree 路径位于 `<state-dir>/worktrees/<repo-id>/<slug>`（仓库目录之外），每个 worktree 拥有独立的 project key，session 与 cache 自动隔离。worktree 只包含被 git 追踪的文件；主仓库未提交的改动不会自动带过去。

## 常用本地控制命令

以下命令由本地运行时处理，不会原样发送给模型。在 TUI 中输入 `/` 会打开补全列表；列表可见时，`Tab` 或 `Enter` 会先补全当前选中的命令，之后再按 `Enter` 才会执行或发送补全后的命令：

- `/new`：新建会话
- `/resume`：恢复会话
- `/models`：查看模型池状态或切换当前视图对象的模型池（main 视图 = 当前主角色；SubAgent 视图 = 该 agent）
- `/models --agent <name> <pool>`：直接设置指定 agent 的模型池
- `/mcp`：打开 MCP server 选择器；`/mcp status` 输出状态；`/mcp enable|disable <server>` 可在空闲时切换手动 server
- `/compact`：手动触发上下文压缩
- `/help`：切换内置 cheatsheet 浮层（等同 Normal 模式按 `?`）

下面几个命令有更多交互细节，单独展开说明。

### MCP 选择器

按 `Ctrl+O` 打开 MCP server 选择器。它会列出已配置的 MCP server、连接状态，以及手动 server 当前是否启用/禁用。可用 `j` / `k` 移动，`Enter` 切换当前手动 server，`e` 启用，`d` 禁用，`Esc` 关闭。

Agent 运行中也可以打开选择器查看 MCP 状态，不需要等待当前 turn 结束。但运行中面板是只读的：启用/禁用操作会被禁用，直到 agent 回到 idle。自动启动的 MCP server 在选择器中始终只读；只有配置了 `manual: true` 的 server 才能在运行时切换状态。

### `/export` — 导出当前会话

将当前会话导出为 Markdown（默认）或 JSON。

```text
/export                  # 默认：导出为 Markdown，保存到 session artifacts 目录
/export ~/out.md         # 指定输出路径
/export --json           # 导出为 JSON 格式
/export ~/out.json       # 文件名以 .json 结尾时自动识别为 JSON
```

导出内容包括全部对话消息以及当前会话的用量统计。导出成功后 TUI 会显示保存路径。

### `/stats` — 用量统计浮层

打开一个浮层，分两个维度浏览用量数据：

- **范围（Scope）**：`Session`（当前会话）或 `Project`（当前项目的聚合统计）。按 `s` 键切换。
- **视图（View）**：`Overview`（总览）、`Models`（按模型细分）、`Agents`（按 agent 细分）。Project 额外支持 `Dates`（按日期细分）。按 `Tab` / `Shift+Tab` 切换视图。

Session Overview 展示：LLM 调用次数、输入/输出 token、缓存读写 token、reasoning token、估算成本。Models 和 Agents 视图以表格展示各维度详细拆解。

Project 统计自动从本地 sessions 目录聚合，支持 `today`、`7d`、`30d`、`90d`、`all` 五种时间范围。切换到 Project 时可能短暂显示"加载中"，稍后会展示统计数据。

浮层打开期间，所有活动搜索自动取消。按 `Esc` 关闭。也可在 Normal 模式用 `$` 键直接打开。

### `/rules` — 会话规则管理器

打开一个浮层，查看当前会话中通过权限确认弹窗"允许并记住规则"添加的规则。

- `↑` / `↓` 或 `j` / `k`：移动光标
- `d`：删除当前规则
- `o`：在系统编辑器中打开规则对应的配置文件
- `Esc` / `q`：关闭

规则旁会显示作用域（`session` / `project` / `global`）和落盘文件路径。这些规则是**动态添加的临时规则**，与 `config.yaml` 中预写的权限规则互补，不修改原有配置文件。

### `/loop` — 持续执行模式

持续执行模式让 agent 在每一轮结束后自动继续，无需反复催促。适合那种“帮我搞定这个功能”的一次性指令——你只需发一条消息，agent 会自己迭代、验证、直到完成、确实卡住，或你明确确认退出。

只有当当前 MainAgent 角色可以使用 `Done` 工具时，`/loop` 才可用；如果该角色把 `Done` 隐藏或拒绝，`/loop` 就不可用。

启用方式：

```text
/loop on                           # 开启，agent 会尝试完成当前会话中的所有剩余任务
/loop on 实现用户认证模块            # 开启并指定目标任务
/loop off                          # 关闭，回到普通模式
/loop                              # 查看当前状态
```

`/loop on` 后面的文字会作为任务目标发给 agent。省略时默认为“继续完成当前会话中所有剩余任务”。每次开启默认限制最多 10 轮迭代，超出自动停止。

**工作流程：** `/loop on` 后发送一条任务指令（如“实现用户认证模块”）。agent 会按以下循环推进：

1. **executing**：执行任务，调用工具做实际工作
2. **assessing**：评估当前进度，决定下一步
3. **verifying**：运行校验（跑测试、lint 等）
4. **继续或申请退出**：如果仍有工作，就继续推进；如果它认为 loop 可以结束，必须通过 `Done` 工具提出退出请求

如果 `Done` 在退出条件尚未满足时被调用，Chord 会拒绝这次退出请求，并让 agent 自动继续运行。只有当退出条件满足时，Chord 才会弹出本地确认框，而不是立即停止。`Done` 工具必须带一个非空的 `report` 参数来承载最终完成报告，确认框展示的也正是这份报告。你确认退出后，loop 才真正结束并回到 idle；如果你选择继续运行，loop 会自动继续。

runtime 自动注入的继续用用户消息，只会发生在 assistant 这一轮以 `end_turn` / `stop` / `done` 一类终止型 stop reason 结束、且这一轮没有返回任何 tool call 的情况下。如果模型这一轮已经返回了 tool call，loop 的继续会留在工具调用链内部：Chord 会记录 tool result、更新 loop 状态，并且可以在 TUI 中显示 loop 提示，但除非你手动发送新消息，否则不会再追加伪造的用户消息。

**如何用 `/loop` + `Done` 更充分利用 Codex 额度：** loop 模式不会凭空增加额度，但它能让现有额度更多花在“端到端推进任务”上，而不是反复等待你手动发下一句。在普通模式下，模型很容易在完成一个局部里程碑后停下来等你继续；之后你还要再花一轮去说“继续”、“顺手把测试跑了”、“把失败修掉再试一次”。开启 `/loop` 后，agent 会在同一任务目标下持续迭代，直到真正满足停止条件，或者必须通过 `Done` 申请退出。这里 `Done` 的拦截很关键：它能阻止过早收工，逼着 agent 尽量把“实现 → 测试 → 修失败 → 再验证 → 总结”这一整条链路走完，再把控制权交还给你。

比较适合 Codex 高额度利用率的用法是：

1. 用明确目标开启 loop（例如 `/loop on 实现功能 X，并补测试`）
2. 一次性给出完整指令和验收标准
3. 让 agent 自己继续完成编辑、跑测试、修回归和再次验证
4. 只有在最终 `Done` 请求出现、且你确认任务真的完成时才结束

对于多步骤编码任务，这通常能提高额度利用效率，因为会减少很多“人工催一步”的无效往返，比如“继续”、“把测试也跑一下”、“把失败用例修掉再来一次”。但也不要为了消耗额度而让 `/loop` 漫无目标地跑：如果任务本身偏探索、需求不明确、或中途很可能频繁需要产品决策，普通模式通常更省额度，也更容易控制。

如果任务确实受阻，agent 仍可使用 `<blocked>category: reason</blocked>` 报告阻塞。你也随时可以用 `Esc` 取消当前轮。

**状态栏提示：** 开启后 TUI 状态栏会显示 `[↻]` 标记，告诉你当前处于持续执行模式。

**适用场景：** 多步骤任务（生成代码 → 写测试 → 调试 → 优化）、需要反复迭代的开发工作。不适合：一次性查询、单纯的问答。

也可以**自定义** slash 命令（按项目或全局），见 [扩展与定制 — 自定义 slash 命令](./customization_CN.md#自定义-slash-命令)。

## 多 Agent 与焦点切换

Chord 支持 MainAgent 与 SubAgent 协作。

- `Tab`：循环切换 main agent 的模式（role，显示在状态栏；仅在 main 视图生效）
- `Shift+Tab`：在 main agent 与各 sub agent 之间循环切换当前查看的 agent 视图

在 SubAgent 视图中可查看该 agent 的上下文与输出；已结束的 SubAgent 视图只读。

## 图片输入与查看

当前支持：

- 从剪贴板粘贴图片
- 把图片文件作为附件发送给当前聚焦的 Agent
- 在支持的终端里直接查看图片
- 编辑 / fork 含图片的历史用户消息；按路径恢复的图片会在重新发送该消息时再次加载

常用操作：

- 主输入框中的 `Ctrl+V` / `Cmd+V`：优先读取剪贴板图片；若检测到图片，会把图片作为附件添加，并在光标处插入类似 `[image1]` 的占位符，同时也会继续插入终端提供的粘贴文本（若有）。若无图片，则粘贴文本。粘贴完成后，光标会停留在插入内容（占位符/文本）末尾。
- 权限确认弹窗的文本框中的 `Ctrl+V` / `Cmd+V`：始终按文本粘贴，不会附加图片
- 每条输入框消息最多支持 5 张 inline 图片附件
- 手动输入 `[image1]` 这类占位符文本本身不会附加图片；只有 Chord 内部插入的 inline 图片占位符才会绑定真实附件
- 若要按路径附图：先在输入框中填入图片路径，再给 `insert_attach_file` 配一个自定义快捷键
- `Enter` / `o` / `Space`：Normal 模式下打开当前消息中的图片

## 复制文本

- 可在转录区内用鼠标拖选 TUI 里的文本
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

## 日常使用建议

- 首次接入时，先用最小 provider 配置确认请求能跑通
- 需要更强代码感知时再配置 LSP
- 需要外部工具接入时再添加 MCP 或 Hooks
- 高风险工具保持 `ask`，不要直接全局 `allow`

## 相关文档

- [配置与认证](./configuration_CN.md)
- [权限与安全](./permissions-and-safety_CN.md)
- [扩展与定制](./customization_CN.md)
- [常见问题排查](./troubleshooting_CN.md)
