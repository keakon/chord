# 使用指南

主要运行模式、核心交互方式以及日常高频功能。

## 运行模式

Chord 有两条主要使用路径：

- **本地 TUI**：默认模式，直接在当前进程内运行 MainAgent
- **Headless 控制面**：通过 `chord headless` 使用 stdio JSONL 与外部 gateway / bot 集成

大多数个人开发场景推荐直接用本地 TUI。

## TUI 基本交互

启动后输入框默认聚焦，直接输入消息按 `Enter` 发送。

工具调用卡片会尽量保持路径简洁：`read`、`edit`、`write`、`delete` 等文件工具在路径位于当前工作目录内时优先显示相对路径；位于当前目录之外则显示绝对路径。

侧边栏和信息面板的变更文件列表会优先完整显示 `+N -N` 行数统计；空间不足时会缩略或省略文件名，而不是截断行数。

工具参数和工具结果按终端安全的纯文本展示。外部输出中的 ANSI/control sequence 会被转义成字面内容，不会当作终端样式执行；看起来像 Markdown 的普通工具结果也会保持原始文本，不会被重新排版成 assistant Markdown。

发现类工具在结果进入会话历史前会使用稳定的 LLM-facing 输出上限：`grep` 最多返回 120 条匹配和 12 KiB 文本；`glob` 最多返回 250 条路径和 16 KiB 文本。这些上限是固定值，不会根据当前剩余上下文窗口动态变化，因此同一个工具调用在模型切换或无关历史增长后仍然更容易复现。字节上限是主限制，因为上下文压力更接近字节/token，而不是行数；按 Chord 粗略的 `1 token ~= 3 bytes` 估算，12 KiB 让单个 Grep 结果约为 4k tokens，16 KiB 让单个 Glob 结果约为 5.3k tokens。匹配数/路径数上限是辅助限制，用来避免大量极短行洪泛。

`read` 和 `write` 工具卡片会用带行号、语法高亮的预览展示文件内容。`edit` 卡片会在识别文件类型时为 unified diff 做语法高亮；`.mdx` 文件会复用 Markdown 高亮，即使是不支持的扩展名，diff 删除/新增行的红/绿色背景也会保留。内容较长时默认只显示前 10 行，并给出 `[space] toggle expand/collapse` 提示；聚焦卡片后按 `Space`、`Enter` 或 `o` 可展开或折叠。

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
- API key 后缀，例如 `key=...xyz9`，只显示末尾 4 位便于安全识别
- HTTP 状态码（如果有）
- API 返回的错误 code / type（如果有）
- 按面板宽度换行后的错误消息

示例：

```text
14:25:38  Anthropic/claude-opus-4-8  key=...xyz9  HTTP 503  code=model_not_found
  No available channel for model sample/model under group default
```

导航：

- `j` / `k`：上下滚动一行
- `Ctrl+F` / `Ctrl+B`：向下 / 向上翻页
- `g` / `G`：跳到顶部 / 底部
- `Esc`：关闭面板

错误面板最多保留最近 80 条错误，按新到旧显示。排查模型为什么 fallback、哪些 key 频繁限流或某个 provider 是否持续返回 5xx 时，优先看这里。

## 工具执行细节

### 流式工具早执行（推测执行）

为缩短服务商最终确认阶段的体感等待，模型响应仍在流式输出、某个工具调用参数刚完整时，Chord 就提前执行一小批安全工具。始终开启，不可配置。

- 允许：`read`、`grep`、`glob`，支持回滚的文件改动（`write`、`edit`、`delete`），以及 `shell` 的保守只读子集（仅单命令，不含管道/重定向/`&&`/`;`）：`pwd`、`ls`、`cat`、`which`，以及 `git status|log|diff|show|branch|rev-parse`。
- 不允许：非只读 `shell`、交互/控制类工具，以及权限为 `ask` 的调用。
- 提前执行的文件改动会真实落盘，但 Chord 会先捕获变更前状态，若最终确认丢弃该调用则自动回滚。同一回合内命中同一路径的冲突推测改动会跳过、留给正式路径；只要回合内存在任何尚未提交的推测改动，读类早执行就会跳过，因此不会读到未提交状态。
- 推测结果可能提前显示在界面，但只有最终确认通过后才追加进对话上下文；未通过的会丢弃，界面标记为「推测执行，已丢弃（不属于上下文）」。

### `edit` 如何匹配补丁

`edit` 通过结构化 `path` 参数接收目标文件；`patch` 参数携带 hunk 文本（`@@` header、前导空格上下文行、`-` 删除、`+` 新增）。误带的 Codex `apply_patch` envelope 行（`*** Begin Patch` / `*** End Patch`，以及与 `path` 匹配且位于开头的 `*** Update File:`）会被剥离；新增/删除/移动、多文件补丁和路径不匹配的 update 标记会被拒绝。

匹配为 Codex 风格的顺序匹配：每个 hunk（及其附带的 `@@` 函数/类/测试 header）从当前搜索位置之后选第一处匹配。命中多个候选时，Chord 应用第一处，并在结果中附上实际匹配行号和其他候选行号，方便模型按需重新 `read`。没有上下文/删除行的 hunk 会失败，因为无法确定插入点。

参数流式输出期间，`edit` 卡片采用与 `write` 类似的路径展示：解析出结构化 `path` 前不显示路径，解析后在卡片标题显示。

## 文件引用（`@path`）

在输入框里于行首或空格后输入 `@`，会打开文件补全。

- 裸 `@` 使用缓存的工作区文本文件索引。该索引包含已追踪文件，以及未追踪但未被忽略的文件；同时会跳过 Git ignore 路径、隐藏目录、二进制扩展名和常见噪声目录。
- 当你开始输入根目录文件名前缀（例如 `@A`）时，Chord 还会额外直接检查当前工作目录。因此像 `AGENTS.md` 这类即使被 `.gitignore` 或本地 Git exclude 排除出缓存索引的根目录文件，仍然可以补全。
- 如果当前 query 已经明显是路径形式，例如 `@docs/`、`@./`、`@~/` 或 `@.config/`，Chord 会切换为直接读取该目录的文件系统补全，而不是继续停留在缓存索引上。也因此，当你显式朝某个被忽略路径输入时，路径模式补全仍可能显示这些 ignored 路径。
- 隐藏项默认仍不会显示。若需要查看，请让 query 本身显式包含隐藏路径语义，例如 `@.`、`@.env`、`@./.` 或 `@.config/`。
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

- **Tools**：Chord 始终在参数能标准化时，把可识别的外部工具调用转换成最接近的当前 Chord 工具卡：Codex、Claude Code 与 OpenCode 记录中的对应工具会显示为 `read`、`shell`、`grep`、`glob`、`edit`、`write` 或 `delete`。只有无法识别的记录（没有 Chord 映射、缺少 call id、或参数无法标准化）才会作为可读的 fallback 卡片保留，而不是丢弃。导入 provenance 会在内部保留，因此这些转换后的工具卡只代表 transcript/history，不会恢复 Chord FileTracker snapshot；如果编辑导入会话涉及的文件前需要最新文件上下文或 stale-change 风险提示，请重新 `read`。
- **Reasoning**：Chord 只会把 Anthropic signed thinking 导入为 `thinking_blocks`；非签名 reasoning 默认（`--reasoning strict`）丢弃，使用 `--reasoning visible` 可作为普通文本导入。
- **Claude 主会话重建**：Claude 导入会尽力重建非 sidechain 的主会话连续片段，而不是简单选择最新的原始叶子节点。compact 边界会参与重建，但不会作为普通 transcript 消息渲染。
- **Claude sidechain**：sidechain / sub-agent transcript 条目默认不会导入到主 session。存在这些条目时，CLI 输出会报告跳过数量，`import-report.json` 会记录 Claude 专属诊断信息，并在可用时记录 sidechain agent ID。
- **Claude fallback 渲染**：无法安全映射到 Chord 结构的可见 Claude artifacts 会尽量导入为可读的 assistant fallback 文本块，而不是原始 JSON blob。
- 导入后的 session 包含 `import-report.json`，记录转换统计与 warnings。
- 运行时会在每次请求前对历史消息做 provider 兼容标准化，导入后切换模型/provider 不会重播不兼容 payload。

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
- `chord worktree finish <name>`：先把目标主线分支合并进真实 worktree 分支（默认目标分支为主 worktree 当前检出的分支），再把完成后的 worktree 状态以单个 squash commit 合回该目标分支，随后 fast-forward 更新目标分支，并删除 worktree 与分支。可用 `--onto <branch>` 指定目标分支，或用 `--check` 在临时 worktree 中预检“该目标分支能否干净合入 worktree”而不改动真实 worktree。若这一步 merge 会冲突，`finish` 会报告冲突文件，保持目标分支不变，并把真实 worktree 保留在这次 merge 中，供你解决后重跑。若该 worktree 已有进行中的 rebase 或 merge，`finish` 会先明确提示“先收尾当前操作”，不会叠加新的 finish 流程。

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

当当前 provider/model 实际启用了非 standard tier 时，侧边栏/状态区域会正常显示它。如果切换 provider/model 后，之前请求的 tier 不再受支持，信息面板仍会以灰色删除线显示请求的 tier，让它保持可见但明确表示未生效。`Ctrl+R` 会跳过不支持的 tier，只在当前 provider/model 可用的 tier 中循环。`/tier` 的 slash 补全会预测与 `Ctrl+R` 相同的下一个 tier；如果唯一可用 tier 是当前已经生效的 `standard`，补全列表会隐藏 `/tier`。

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
- `Bytes` 和 `Messages` 描述将发送给模型的会话上下文。请求级 context reduction 运行后，`Bytes` 显示剪裁后的实际请求字节数，并用 `↓` 标出节省百分比。恢复会话时，Chord 会预计算同一套剪裁用于展示，让 `Bytes` 一开始就是剪裁后的估算值，而不是等下一次请求后突然变小；在任何请求 surface 都无法准备时，回退显示当前持久上下文估算。
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

如果 `done` 在退出条件尚未满足时被调用，Chord 会拒绝这次退出请求，并让 agent 自动继续运行。只有当退出条件满足时，Chord 才会弹出本地确认框，而不是立即停止。`done` 工具必须带一个非空的 `report` 参数来承载最终完成报告，确认框展示的也正是这份报告。`report` 参数仍在流式接收时，Done 工具卡片会像其他流式工具参数一样显示实时的 `chars received` 进度；参数流结束后，这个临时进度提示会被隐藏。你确认退出后，loop 才真正结束并回到 idle；如果你选择继续运行，loop 会自动继续。

`done` 会被有意当作 loop 控制工具，而不是普通的可被权限绕过的工具。只有当前 MainAgent 角色可以使用 `done` 时，`/loop` 才可用；YOLO 也不会绕过 `done` 权限。这样可以避免无权完成 / 退出的角色悄悄接管 loop 终止，也能保留本地确认 gate，防止过早结束。

loop 模式也会防止工具调用卡死循环。如果 MainAgent 连续 3 次发出完全相同的工具调用（工具名相同且参数相同），Chord 会自动拒绝这次工具结果，向模型注入“不要原样重复调用、继续朝 loop 目标推进”的指导，并把它计为一次 loop 拦截。检测使用滑动窗口：如果第 4 次调用仍然完全相同，也会立即再次拒绝。达到 loop 拦截上限后，Chord 会弹出同样的本地确认流程，由你决定停止还是继续。

runtime 自动注入的继续用用户消息，只会发生在 assistant 这一轮以 `end_turn` / `stop` / `done` 一类终止型 stop reason 结束、且这一轮没有返回任何 tool call 的情况下。如果模型这一轮已经返回了 tool call，loop 的继续会留在工具调用链内部：Chord 会记录 tool result、更新 loop 状态，并且可以在 TUI 中显示 loop 提示，但除非你手动发送新消息，否则不会再追加伪造的用户消息。

**如何用 `/loop` + `done` 更充分利用 Codex 额度：** loop 模式不会凭空增加额度，但它能让现有额度更多花在“端到端推进任务”上，而不是反复等待你手动发下一句。在普通模式下，模型很容易在完成一个局部里程碑后停下来等你继续；之后你还要再花一轮去说“继续”、“顺手把测试跑了”、“把失败修掉再试一次”。开启 `/loop` 后，agent 会在同一任务目标下持续迭代，直到真正满足停止条件，或者必须通过 `done` 申请退出。这里 `done` 的拦截很关键：它能阻止过早收工，逼着 agent 尽量把“实现 → 测试 → 修失败 → 再验证 → 总结”这一整条链路走完，再把控制权交还给你。

比较适合 Codex 高额度利用率的用法是：

1. 用明确目标开启 loop（例如 `/loop on 实现功能 X，并补测试`）
2. 一次性给出完整指令和验收标准
3. 让 agent 自己继续完成编辑、跑测试、修回归和再次验证
4. 只有在最终 `done` 请求出现、且你确认任务真的完成时才结束

对于多步骤编码任务，这通常能提高额度利用效率，因为会减少很多“人工催一步”的无效往返，比如“继续”、“把测试也跑一下”、“把失败用例修掉再来一次”。但也不要为了消耗额度而让 `/loop` 漫无目标地跑：如果任务本身偏探索、需求不明确、或中途很可能频繁需要产品决策，普通模式通常更省额度，也更容易控制。

如果任务确实受阻，agent 仍可使用 `<blocked>category: reason</blocked>` 报告阻塞。你也随时可以用 `Esc` 取消当前轮。

**状态栏提示：** 开启后 TUI 状态栏会显示 `[↻]` 标记，告诉你当前处于持续执行模式。

**适用场景：** 多步骤任务（生成代码 → 写测试 → 调试 → 优化）、需要反复迭代的开发工作。不适合：一次性查询、单纯的问答。

也可以**自定义** slash 命令（按项目或全局），见 [扩展与定制 — 自定义 slash 命令](./customization_CN.md#自定义-slash-commands)。

## YOLO 与受保护的控制工具

YOLO 是面向可信本地工作的便捷模式：它会绕过普通 MainAgent 权限检查，让文件编辑、读取、shell 命令、web 请求等工具不再反复确认。但它**不会**绕过 `handoff`、`delegate`、`cancel` 或 `done` 的权限。

这四类工具受保护，是因为它们控制的是 agent 编排流程，而不仅是本地副作用：

- `handoff` 可以在角色之间移交工作 / plan，会改变任务责任归属。
- `delegate` 可以启动或管理委派工作流，并可能并行执行工作。
- `cancel` 可以中断当前 turn。
- `done` 会完成一个 turn，或在 loop 中申请退出，并携带最终报告。

YOLO 下继续执行这些权限，可以避免一个宽泛的“允许工具”开关同时授予工作流控制权。对 loop 模式尤其重要的是 `done`：loop 退出仍受当前角色的 `done` 权限、loop 退出条件检查和本地确认共同约束，因此 YOLO 不会意外让模型过早终止长时间 loop。

YOLO 下，这些受保护工具仍需要显式权限。像 `"*": allow` 这样的宽泛默认规则会被视为普通权限表面的一部分并被 YOLO 绕过，但它本身不会授予 `handoff`、`delegate`、`cancel` 或 `done`；如果某个角色需要使用这些工具，请直接为对应工具配置权限。

## 多 Agent 与焦点切换

Chord 支持 MainAgent 与 SubAgent 协作。

- `Tab`：循环切换 main agent 的模式（role，显示在状态栏；仅在 main 视图生效）
- `Shift+Tab`：在 main agent 与各 sub agent 之间循环切换当前查看的 agent 视图

在 SubAgent 视图中可查看该 agent 的上下文与输出；已结束的 SubAgent 视图只读。

当启用 `todo_write` 但没有可用的 `delegate` workflow 时，todo 列表默认只保留一个 `in_progress`，表示 MainAgent 当前直接执行的焦点工作。
当 `delegate` workflow 可用且确实派出了多个活跃的 delegated workstreams 时，todo 列表可以同时存在多个 `in_progress`，但每一项都应清楚映射到一个真实活跃的委派工作流，并使用唯一的 `active_form`；不要把尚未开始、仅计划中或只是等待条件的事项也标成 `in_progress`。

## 图片与 PDF 输入

当前支持：

- 从剪贴板粘贴图片
- 在当前模型支持对应输入类型时，把图片或 PDF 文件作为附件发送给当前聚焦的 Agent
- 在支持的终端里直接查看图片；PDF 会发送给模型，并在转录区显示为文件 chip，但不会 inline 预览
- 编辑含图片或 PDF 的历史用户消息；如果这条消息已经在转录尾部，就直接在当前会话里回填编辑，否则会 fork 新会话；按路径恢复的附件会在重新发送该消息时再次加载
- 当工具被权限规则允许、有效 model pool 的第一个模型支持 image 输入且这个第一个模型不是 OpenAI Chat Completions API 时，模型可以调用内置 `view_image` 工具把本地 PNG/JPEG 载入上下文。该工具使用与 `read` 相同的本地路径权限处理。

`view_image` 是否可用由有效 model pool 的第一个模型决定，因此 fallback 路由切换模型时工具列表仍保持稳定。使用 OpenAI 模型且工具需要返回图片或文件时，建议优先使用 Responses API：OpenAI Chat Completions 可以接收用户消息中的图片，但它的 `role: "tool"` 消息只能承载文本。一旦当前会话包含工具返回的图片/PDF，Chord 会跳过无法安全重放这类上下文的 fallback 候选，包括不支持 image 输入的模型和 OpenAI Chat Completions；未使用图片/PDF 工具结果的普通会话仍可正常 fallback 到 Chat Completions。工具返回的图片会作为缩略图显示在对应的工具结果卡片中，并可在 Normal 模式下像用户附件图片一样打开。

常用操作：

- 主输入框中的 `Ctrl+V` / `Cmd+V`：优先读取剪贴板图片；若检测到图片，会把图片作为附件添加，并在光标处插入类似 `[image1]` 的占位符，同时也会继续插入终端提供的粘贴文本（若有）。若无图片，则粘贴文本。粘贴完成后，光标会停留在插入内容（占位符/文本）末尾。
- 权限确认弹窗的文本框中的 `Ctrl+V` / `Cmd+V`：始终按文本粘贴，不会附加图片
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
- [扩展与定制](./customization_CN.md)
- [常见问题排查](./troubleshooting_CN.md)
