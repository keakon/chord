# 使用指南

主要运行模式、核心交互方式以及日常高频功能。

## 运行模式

Chord 有两条主要使用路径：

- **本地 TUI**：默认模式，直接在当前进程内运行 MainAgent
- **Headless 控制面**：通过 `chord headless` 使用 stdio JSONL 与外部 gateway / bot 集成

大多数个人开发场景推荐直接用本地 TUI。

## TUI 基本交互

启动后输入框默认聚焦，直接输入消息按 `Enter` 发送。

工具卡片会用终端安全的方式展示预览。会话工作目录内的文件显示相对路径，外部文件显示绝对路径。较长的文件内容和差异预览默认折叠；聚焦卡片后按 `Space`、`Enter` 或 `o` 即可展开或收起。

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

- **中间重试错误**：触发 key 轮换、模型 fallback 或流式重试的 API 错误，例如 429 限流、503 服务不可用、上下文超限或超时。这类错误会静默记录到错误面板，不会打断对话区。
- **最终错误**：所有重试都失败后显示在对话区的红色错误块。

每条记录会展示：

- 时间（`HH:MM:SS`）
- Provider 和 model，例如 `Anthropic/claude-opus-4-8`
- 打码后的 API key 标识，例如 `key=sk-a...xyz9`，显示少量前缀和后缀便于安全识别
- HTTP 状态码（如果有）
- API 返回的错误 code / type（如果有）
- 按面板宽度换行后的错误消息

示例：

```text
14:25:38  Anthropic/claude-opus-4-8  key=sk-a...xyz9  HTTP 503  code=model_not_found
  No available channel for model sample/model under group default
```

导航：

- `j` / `k`：上下滚动一行
- `Ctrl+F` / `Ctrl+B`：向下 / 向上翻页
- `g` / `G`：跳到顶部 / 底部
- `Esc`：关闭面板

错误面板最多保留最近 80 条错误，按新到旧显示。排查模型为什么 fallback、哪些 key 频繁限流或某个 provider 是否持续返回 5xx 时，优先看这里。

## 文件引用（`@path`）

在输入框里于行首或空格后输入 `@`，会打开文件补全。

- 裸 `@` 使用缓存的工作区文本文件索引。该索引包含已追踪文件，以及未追踪但未被忽略的文件；同时会跳过 Git ignore 路径、隐藏目录、二进制扩展名和常见噪声目录。
- 当你开始输入根目录文件名前缀（例如 `@A`）时，Chord 还会额外直接检查 session working directory。因此像 `AGENTS.md` 这类即使被 `.gitignore` 或本地 Git exclude 排除出缓存索引的根目录文件，仍然可以补全。
- 如果当前 query 已经明显是路径形式，例如 `@docs/`、`@./`、`@~/` 或 `@.config/`，Chord 会切换为直接读取该目录的文件系统补全，而不是继续停留在缓存索引上。也因此，当你显式朝某个被忽略路径输入时，路径模式补全仍可能显示这些 ignored 路径。
- 隐藏项默认仍不会显示。若需要查看，请让 query 本身显式包含隐藏路径语义，例如 `@.`、`@.env`、`@./.` 或 `@.config/`。
- 可以追加 1-based 行号后缀，只注入文本文件的一部分：`@path:42` 注入第 42 行，`@path:10-20` 注入第 10 到 20 行。接受补全时只替换路径部分，因此你已经输入的行号后缀会保留；如果真实文件名本身包含数字冒号后缀（例如 `note:12`），则优先按文件名处理，而不是解析成行号范围。
- 补全只是输入辅助。真正发送消息时，Chord 会重新解析最终文本中的 `@path`；如果你在发送前删掉了这个引用，就不会附加该文件。

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

`/new` 会重置历史消息、待办事项和用量等会话状态。当前模型池、服务等级和 MCP 状态等运行时偏好会继续生效，直到进程退出。

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
- `--id <session-id>`：按来源 session id 查找输入文件（支持 `codex` / `claude`）
- `--root <path>`：`--id` 查找的根目录
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
- `chord worktree finish <name>`：先用目标分支更新工作树，再把结果压缩成一个提交合回目标分支，最后删除工作树和分支。可用 `--onto <branch>` 指定目标分支，或用 `--check` 在不改动现有工作树的情况下预检冲突。发生冲突时，目标分支保持不变；解决工作树中的合并冲突后重新运行 `finish` 即可。

创建或进入 worktree 会改变 Chord 运行所在的 project。你可以使用 `chord --worktree <name>`，也可以使用 `chord worktree <name>`。`worktree` 子命令同时承担 `list`、`remove`、`finish` 等管理操作。

Worktree 路径位于 `<state-dir>/worktrees/<repo-id>/<slug>`（仓库目录之外），每个 worktree 拥有独立的 project key，session 与 cache 自动隔离。worktree 只包含被 git 追踪的文件；主仓库未提交的改动不会自动带过去。

## 常用本地控制命令

以下命令由本地运行时处理，不会原样发送给模型。在 TUI 中输入 `/` 会打开补全列表；列表可见时，`Tab` 或 `Enter` 会先补全当前选中的命令，之后再按 `Enter` 才会执行或发送补全后的命令：

- `/new`：新建会话
- `/resume`：恢复会话
- `/models`：查看模型池状态或切换当前视图对象的模型池（main 视图 = 当前主角色；SubAgent 视图 = 该 agent）
- `/models --agent <name> <pool>`：直接设置指定 agent 的模型池
- `/mcp`：打开 MCP server 选择器；`/mcp status` 输出状态；`/mcp enable|disable <server>` 可切换手动 server。运行时切换会在下一次 LLM 请求生效，不影响当前正在进行的请求。
- `/compact`：手动触发上下文压缩，将当前对话摘要为结构化归档，详见 [配置 — 上下文压缩](./configuration_CN.md#上下文压缩compaction)
- `/tier standard|fast|slow`：设置后续模型请求的 service tier（包括尚未开始的后续 retry round）。空的 `/tier` 不是状态查询命令；当前有效 tier 请看侧边栏/状态显示。如果手动输入当前 provider/model 不支持的 tier，Chord 会保持当前 tier 不变并显示错误提示。
- `/yolo on|off`：临时绕过 MainAgent 工具权限，但仍保留 handoff、delegate、cancel 和 done 权限。Agent 运行中也可以切换 YOLO；执行期权限绕过会立刻影响后续工具调用，而 LLM 可见的工具描述和权限提示会在下一次请求刷新。
- `/help`：切换内置 cheatsheet 浮层（等同 Normal 模式按 `?`）

启用非标准服务等级后，侧边栏会显示当前值。如果切换模型后该等级不再可用，它会以灰色删除线显示。`Ctrl+R` 只在当前服务商和模型支持的等级之间切换。

下面几个命令有更多交互细节，单独展开说明。

### MCP 选择器

按 `Ctrl+O` 打开 MCP server 选择器。它会列出已配置的 MCP server、连接状态，以及手动 server 当前是否启用/禁用。可用 `j` / `k` 移动，`Enter` 切换当前手动 server，`e` 启用，`d` 禁用，`Esc` 关闭。

Agent 运行中也可以打开选择器查看 MCP 状态，不需要等待当前 turn 结束。启用 / 禁用操作在运行中也允许执行，但会延迟生效：当前正在进行的请求继续使用它启动时的 MCP 工具表面和 prompt，变化后的 MCP 状态会在下一次 LLM 请求中体现。自动启动的 MCP server 在选择器中始终只读；只有配置了 `manual: true` 的 server 才能在运行时切换状态。

### `/export` — 导出当前会话

将当前会话导出为 Markdown（默认）或 JSON。

```text
/export                  # 默认：导出为 Markdown，保存到 session artifacts 目录
/export ~/out.md         # 指定输出路径
/export --json           # 导出为 JSON 格式
/export ~/out.json       # 文件名以 .json 结尾时自动识别为 JSON
```

导出内容包括全部对话消息以及当前会话的用量统计。导出成功后 TUI 会显示保存路径。

### 看懂信息面板 `USAGE` 区

- `Context` 显示最近一次模型请求由 provider usage 返回的实际输入侧 token 负担。
- `Bytes` 和 `Messages` 描述将发送给模型的会话上下文。请求级 context reduction 运行后，`Bytes` 显示当前请求剪裁后的实际上下文字节数，并用 `↓` 标出当前请求相对未剪裁上下文的节省百分比：`(剪裁前字节数 - 剪裁后字节数) / 剪裁前字节数`。该比例不是跨请求累计值；已冻结复用的剪裁摘要只要仍用于当前请求，其节省量就会计入。恢复会话时，Chord 会预计算同一套剪裁用于展示，让 `Bytes` 一开始就是剪裁后的估算值，而不是等下一次请求后突然变小；在任何请求 surface 都无法准备时，回退显示当前持久上下文估算。
- `Bytes` 统计已安装的系统提示词、消息内容、图片负载，以及工具名/描述；不包含 JSON 转义开销、tool-call 参数 JSON、thinking 元数据，也不包含 stream 设置、思考预算等请求参数。
- 这些剪裁不是持久化压缩：较旧的工具结果通常会在请求中替换成更短的占位摘要，而持久化会话历史保持不变。`/compact`、自动压缩、工具输出增长以及系统提示词或工具定义变化会更新回退用的持久估算；新的请求准备会刷新实际发送请求大小，loop 模式运行中也会同步更新。
- `Cache R` 显示百分比时，分子是 cache-read tokens，分母是输入侧 prompt tokens 加上 provider 单独上报的 cache-write tokens。输出 token 不参与计算，因为 prompt cache 只作用于输入侧。
- `Think` 行只在 provider 上报 reasoning/thinking tokens 时显示。这些 token 已包含在输出 token 计费中；该行只是可见性拆解，不是额外的 token 计费桶。

### `/stats` — 用量统计浮层

打开一个浮层，分两个维度浏览用量数据：

- **范围（Scope）**：`Session`（当前会话）或 `Project`（当前项目的聚合统计）。按 `s` 键切换。
- **视图（View）**：`Overview`（总览）、`Models`（按模型细分）、`Agents`（按 agent 细分）。Project 额外支持 `Dates`（按日期细分）。按 `Tab` / `Shift+Tab` 切换视图。

Session Overview 展示：LLM 调用次数、输入/输出 token、缓存读写 token、reasoning token、估算成本。Models 和 Agents 视图以表格展示各维度详细拆解。

Project 统计自动从本地 sessions 目录聚合，支持 `today`、`7d`、`30d`、`90d`、`all` 五种时间范围。切换到 Project 时可能短暂显示"加载中"，稍后会展示统计数据。

浮层打开期间，所有活动搜索自动取消。按 `Esc` 关闭。也可在 Normal 模式用 `$` 键直接打开。

### `/rules` — 权限规则管理器

打开一个浮层管理已记住的权限规则。即使当前还没有规则也会打开，因此可以手动新增规则。

- `a`：手动添加规则
- `↑` / `↓` 或 `j` / `k`：移动光标
- `d`：删除当前规则
- `o`：在系统编辑器中打开规则对应的配置文件
- `Esc` / `q`：关闭

手动添加规则时，填写 tool 名称和 pattern，然后用 `Ctrl+S` 切换作用域（`session` / `project` / `global`），用 `Ctrl+A` 切换动作（`allow` / `ask` / `deny`）。tool 和 pattern 必填。不会匹配后续工具调用的 pattern 也可以保存，但在实际命中前不会产生效果。

规则旁会显示作用域（`session` / `project` / `global`）和落盘文件路径。`session` 规则只在当前会话内生效；`project` 规则写入当前项目的 `.chord/agents/<role>.yaml`；`global` 规则写入用户配置目录的 `agents/<role>.yaml`（默认 `~/.config/chord/agents/<role>.yaml`）。这些规则会直接更新对应 agent 的 `permission` 配置，删除规则时也会从同一 agent 配置文件移除。

权限确认弹窗也可以用 `M` 添加记住规则。在规则选择器中按 `E` 可在保存前编辑建议 pattern。Delete 确认会使用保守的路径级建议（精确路径和同目录 pattern），不会提供全局通配。

### `/loop` — 持续执行模式

持续执行模式让 agent 在每一轮结束后自动继续，无需反复催促。适合那种“帮我搞定这个功能”的一次性指令——你只需发一条消息，agent 会自己迭代、验证、直到完成、确实卡住，或你明确确认退出。

只有当当前 MainAgent 角色可以使用 `done` 工具时，`/loop` 才可用；如果该角色把 `done` 隐藏或拒绝，`/loop` 就不可用。

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
4. **继续或申请退出**：如果仍有工作，就继续推进；如果它认为 loop 可以结束，必须通过 `done` 工具提出退出请求

Agent 申请结束时，Chord 会检查退出条件，并用本地确认框展示完成报告。确认后停止；拒绝则继续运行。YOLO 模式不会绕过这次确认，也不会绕过 `done` 权限。

切换 loop 模式不会在可用工具列表中动态添加或移除 `done`。普通模式下，除非另有明确的 runtime 或工作流要求必须发出工具化完成信号，否则 agent 必须直接使用常规 assistant 正文结束；仅仅完成工作或发现 `done` 可用，都不能作为调用理由。Loop 模式则通过当前 runtime 的工具调用要求和 continuation 指令，把 `done` 作为明确的退出请求。执行 `/loop off` 后，后续工作恢复普通响应方式，同时取消尚未发送给模型的 loop continuation。

Loop 模式还会检测连续重复的相同工具调用。发现卡住后，Chord 会打断重复；多次触发后，会询问你是停止还是继续。

推荐用法：

1. 用明确目标开启 loop（例如 `/loop on 实现功能 X，并补测试`）
2. 一次性给出完整指令和验收标准
3. 让 agent 自己继续完成编辑、跑测试、修回归和再次验证
4. 只有在最终 `done` 请求出现、且你确认任务真的完成时才结束

这样可以减少“继续”“把测试也跑一下”等人工催促。但不要让 `/loop` 漫无目标地运行：如果任务偏探索、需求不明确或经常需要产品决策，普通模式更容易控制。

如果任务确实受阻，agent 仍可使用 `<blocked>category: reason</blocked>` 报告阻塞。你也随时可以用 `Esc` 取消当前轮。

**状态栏提示：** 开启后 TUI 状态栏会显示 `[↻]` 标记，告诉你当前处于持续执行模式。

**适用场景：** 多步骤任务（生成代码 → 写测试 → 调试 → 优化）、需要反复迭代的开发工作。不适合：一次性查询、单纯的问答。

也可以**自定义** slash 命令（按项目或全局），见 [扩展与定制 — 自定义 slash 命令](./customization_CN.md#自定义-slash-commands)。

## 多 Agent 与焦点切换

Chord 支持 MainAgent 与 SubAgent 协作。

- `Tab`：循环切换 main agent 的模式（role，显示在状态栏；仅在 main 视图生效）
- `Shift+Tab`：在 main agent 与各 sub agent 之间循环切换当前查看的 agent 视图

在 SubAgent 视图中可查看该 agent 的上下文与输出；已结束的 SubAgent 视图只读。

当启用 `todo_write` 但没有可用的 `delegate` workflow 时，todo 列表默认只保留一个 `in_progress`，表示 MainAgent 当前直接执行的焦点工作。
当 `delegate` workflow 可用且确实派出了多个活跃的 delegated workstreams 时，todo 列表可以同时存在多个 `in_progress`，但每一项都应清楚映射到一个真实活跃的委派工作流，并使用唯一的 `active_form`；不要把尚未开始、仅计划中或只是等待条件的事项也标成 `in_progress`。

## 图片与 PDF 输入

当前支持：

- 使用 `Ctrl+V` 或 `Alt+V` 从系统剪贴板附加图片或 PDF
- 在当前模型支持对应输入类型时，把图片或 PDF 文件作为附件发送给当前聚焦的 Agent
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
- `yy` 复制当前聚焦的消息卡片；工具卡片会按 Markdown 复制，包含 `# Tool call`、`## Arguments`、`## Result`、`## Diff` 等段落。Done 的拒绝理由会单独放在 `## Rejection reason` 段落中。
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
- [编辑工具](./edit-tools_CN.md)
- [扩展与定制](./customization_CN.md)
- [常见问题排查](./troubleshooting_CN.md)
