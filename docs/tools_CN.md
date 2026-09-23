# 内置工具

本页列出模型可调用的全部内置工具名。在 agent 的 `permission:` 规则、hook 的 `tools:` 过滤器和 skill 的 `allowed_tools` 列表中，请使用这些名称的原样拼写。

`allow` / `ask` / `deny` 的判定方式（包括编排类工具之间的特殊耦合）见[权限与安全](./permissions-and-safety_CN.md)。

## 本页怎么读

按要做的任务找对应小节：

- **读写文件**：[文件](#文件)、[搜索与导航](#搜索与导航)。
- **执行命令与长任务**：[执行](#执行)。
- **抓取网页**：[Web](#web)。
- **规划、提问与委派**：[工作流](#工作流)、[编排与控制](#编排与控制)。
- **外部工具服务**：[MCP 工具](#mcp-工具)。

## 文件

| 工具 | 用途 |
| --- | --- |
| `read` | 读取本地文件进上下文，支持 1-based 的 `offset` / `limit` 行分页。 |
| `write` | 创建文件，或有意整体替换一个文件。 |
| `edit` | 在现有文件中替换精确文本。 |
| `apply_patch` | 应用 Codex 风格补丁（`*** Begin Patch`）：新增、更新、删除或移动文件。独立文件组可能部分成功；重试前先检查已应用的修改。 |
| `delete` | 删除整个文件。 |
| `view_image` | 加载本地 PNG/JPEG 进上下文；仅在生效模型池的第一个模型支持图片输入时可用。本地路径权限处理与 `read` 相同。 |

模型每次只会看到 `edit` / `apply_patch` 中的一个（按模型家族选择）；补丁原生模型的文件创建/删除也经由 `apply_patch` 而非 `write`/`delete`。详见[编辑工具](./edit-tools_CN.md)。

## 搜索与导航

| 工具 | 用途 |
| --- | --- |
| `grep` | 按正则/字面文本搜索内容，输出有上限；支持多根 `paths` 和 `includes` glob 过滤。 |
| `glob` | 按 glob 模式匹配路径，输出有上限。 |
| `lsp` | 在指定文件位置做语义化的 definition / references / implementation 查询，需要对应 LSP server 覆盖该文件类型。 |

在 TUI 中，`lsp` 卡片会在头部概括查询动作和位置（例如 `find references internal/agent/main.go:54:17`），查询完成后显示位置数量，展开详情可看到每个返回的 `path:line:character` 位置。

位置没落在标识符上时（比如行号差一行、点到声明上方的注释里），失败结果会附上该行和上下相邻行的原文与行号，不用再读一次文件就能看出位置到底点到了哪。

## 执行

| 工具 | 用途 |
| --- | --- |
| `shell` | 执行命令；长命令可转为后台任务，详见下方说明。 |
| `job_output` | 增量读取后台任务输出，或限时等待输出、完成。 |
| `job_list` | 列出你可读取或停止的后台 job（id、status、已运行时长、安静时长、标签），含主 agent 与你直接 owner 启动的 job；默认只列活跃 job，传 `include_finished: true` 才会带上保留的终态 job。 |
| `job_kill` | 按 `job_id` 停止后台 job，可选 `reason`。 |

### 命令执行与超时

执行非交互式 shell 命令。默认前台运行；带 `run_in_background: true` 时作为后台 job 启动。前台命令超过 `yield_time_ms`（默认 90000）会自动转成后台 job；`timeout_ms` 限制执行时长：前台命令默认 600000、上限 600000；`run_in_background: true` 上限 21600000（6 小时），且未显式给出 `timeout_ms` 时不设截止；`0` 表示不设截止。不可自动提升的前台命令（只由刻意等待（`sleep`）和短 `git` 查询组成的命令，或无法解析的命令）即使传 `timeout_ms: 0` 也保留默认上限，因此前台调用不会无限阻塞当前回合；耗时较长的 `git` 操作（`clone`、`fetch`、`pull`、`push`、`submodule`、`gc`、`fsck`、`repack`、`bundle`、`filter-branch`）和其它长命令一样可以提升。

长命令不必阻塞当前回合。超出前台预算的命令会继续作为后台 job 运行，工具卡片会显示它的 job id，job 结束时 agent 会收到通知，它可以先做别的事，或结束回合并等完成通知唤醒，无需干等。命令自身退出、但它起的进程组里还有进程时，即使传了 `yield_time_ms: 0` 也会同样转成后台 job，当前回合不会等这些后代结束。

一个 job 管的是命令启动的整个进程组，不只看直接子进程。命令自身退出、但它起的子进程还在跑时，job 会一直保持活跃到子进程也退出；这些子进程同样受这个 job 的 `timeout_ms` 截止、`job_kill` 和会话清理约束，所以 `nohup … &` 不再能靠熬死启动它的那层包装来逃逸。主动脱离进程组的进程（`setsid`、`setpgid`）不在此列——Chord 不会为此扫描进程树。只有命令退出那一刻记录到的某个成员仍在这个组里时，停止才会向它发信号；证明不了的话，`job_kill` 会报告「未能确认进程组已退出」，而不是朝可能已被系统回收的组号发信号。等待本身不需要这份证明：只要进程组还有响应，job 就保持活跃，所以即使 Chord 列不出某个继承了这个组的进程的 pid，job 也照样会继续等下去。在没有进程组的平台（Windows）上，job 只能以直接进程为界：`job_kill`、job 的截止时间和会话清理都只能停掉这个直接进程，结果里的停止状态会标为无法确认，因为命令留下的后代进程观察不到。

`job_output` 只返回增量输出；有界等待超时也不会杀掉 job。后台 job 也会随会话结束（切换会话或退出客户端都会终止它），所以天级任务应交给 tmux、systemd 或 CI 这类外部 runner。

`job_list` 列出正在运行或正在停止的 job，给出标签、已运行时长、安静时长和截止时间还剩多少；传 `include_finished: true` 才会连保留的终态 job 一起列出。

### 读取后台输出

读取后台 job 自上次读取以来的输出，末尾附 `[status: ...]` 状态行。`wait` 决定这次调用是否阻塞：`none`（默认）只返回当前已有输出，`output` 等到有新输出，`exit` 等到 job 结束，每次等待都由 runtime 限制在 30 秒内。等待超时不算错误：job 继续运行，结果里会标成 running，并多一行 `[notice]` 说明这次 `exit` 等待是超时还是被取消，以及 job 已经安静了多久。连续多次非阻塞读取都没有新输出时，会先被提示为轮询、随后被拒绝，所以只在有理由时才继续读。返回给模型的文本会去掉终端转义序列。

## Web

| 工具 | 用途 |
| --- | --- |
| `web_fetch` | 抓取 URL 并转成可读文本；权限规则可按 URL 模式匹配。 |

## 工作流

| 工具 | 用途 |
| --- | --- |
| `todo_write` | 维护当前任务的可见 TODO 列表。 |
| `question` | 向用户提出结构化问题并等待回答。该工具的 `ask` 会被归一化为 `allow`。 |
| `skill` | 按需加载已发现 skill 的内容。 |
| `save_artifact` | 在会话 artifacts 目录下保存或更新会话产物（报告、任务图、日志等），或保存不可变的机器可读结果。 |
| `read_artifact` | 按会话相对路径读取会话产物。 |

`save_artifact` 有两种互斥的参数形态：用 `filename` 加 `content`（必要时配 `mode: create / append / overwrite`）写入或更新会话产物；或者改用 `result_type` 加 `result` 参数对（`result` 必须是 JSON object）把载荷作为不可变、内容寻址的结果存入 `artifacts/results/`，返回的 ResultRef（`id`、`result_type`、`rel_path`、`sha256`、`size_bytes`）可以直接作为 `complete` 的 `result_ref` 传入。

## 编排与控制

这些工具控制的是 agent 工作流而不是本地副作用，YOLO 不会像对普通工具那样把它们的权限规则一起放开：它消除的是确认文件编辑和 shell 命令的摩擦，不是角色的边界。`handoff`、`delegate`、`cancel`、`done`、`compact_context` 在 YOLO 下如何判定，见[权限与安全：特殊权限语义](./permissions-and-safety_CN.md#特殊权限语义)。

| 工具 | 用途 |
| --- | --- |
| `done` | 携带最终 Markdown 报告申请 loop 退出。仅在 loop 运行期间挂载，普通会话看不到它。见[使用指南：持续执行模式](./usage_CN.md#loop持续执行模式)。 |
| `handoff` | 把计划/工作移交给另一个角色执行。 |
| `delegate` | 启动子任务并立即返回句柄；角色权限决定其能力，声明的工作范围用于协调，可选的 `result_schema` 声明交付结果必须包含什么。 |
| `cancel` | 取消一个被委派的 worker；前提是 `delegate` 已启用。 |
| `complete` | SubAgent 侧：携带摘要把当前委派任务标记为完成。 |
| `escalate` | SubAgent 侧：用 `kind: needs_repair` 向 owner 求助但不结束任务；用 `kind: blocked` 报告走不通，任务以失败收口。 |
| `notify` | 向上级代理或指定子代理发送非阻塞通知。定向消息可唤醒已完成或已失败的子代理，并保留它自己的会话历史；已取消的任务不可恢复。具体参数见下方。 |

### 委派任务与工作范围

`delegate` 启动一个委派的 SubAgent 工作流，并立即返回它的启动句柄（`task_id` / `agent_id`），不等它完成。调用必须携带 `expected_write_scope`，用于声明覆盖工作范围的最小 `files` / `path_prefix` / `modules`。

这份声明是协调元数据，不是运行时边界：worker 能否改文件完全由角色的权限规则决定（deny 掉 `write` / `edit` / `delete` / `apply_patch` 的角色注册不到这些工具），声明路径之外的调用不会被运行时拦截。

诚实声明最窄范围，兄弟任务的叠加提示才有意义：新任务的声明范围与另一个仍活跃的任务重叠时，委派照常启动，句柄会带 `scope_conflict: true`、`suggested_task_id` 和 `suggested_action: serialize_or_worktree`，提示你把两个任务串行执行、用 `notify` 协调共享文件的编辑，或让新 worker 在独立的 git worktree 里工作。

只读任务应选择注册不到文件修改工具的角色并传空 scope：空 scope 只对这种角色放行，能写文件的角色必须声明非空范围，否则委派被拒绝。`shell` 这类命令工具不受 scope 约束，可用性由角色的权限规则决定。拒绝 `delegate` 会同时禁用该角色的 `cancel` 与嵌套委派。

### 交付结果契约

`delegate` 可以带一个可选的 `result_schema`，声明 worker 交付的结果必须包含什么。可用的关键字只有 `type`、`required`、`properties`、`items`、`enum` 和 `description`，`type` 只能取 `object`、`array`、`string`、`integer`、`number`、`boolean`，顶层必须是 `type: "object"`。超出这个子集的 schema 会在委派时被拒绝，不会被静默忽略；未声明的字段一律放行，所以契约校验的是「你要的东西在不在、对不对」，而不是枚举 worker 能返回的全部内容。

`complete` 用 `result` 内联交付结果，或用 `result_ref` 引用 artifact（通常是 `save_artifact` 返回的 ResultRef）。Chord 在接受完成前会拿实际载荷校验契约：不符合的结果退回给 worker，指出违规路径，并留给它一次改正机会；再次交付仍不符合时任务以失败收口，拒绝文案最多列出 20 条违规，机读诊断写进任务结算。`result_ref` 的内容读不回来时立即失败，因为重试也修不好存储侧的问题。不带 `result_schema` 时，委派行为与以前一致。

### 升级

`escalate` 是 SubAgent 向 owner 求助的方式，必须用 `kind` 声明是哪一种：

- `needs_repair`：任务卡在只有 owner 才能决定或提供的事情上。请求作为 mailbox 消息投递，worker 停在 `WaitingMain` 等你答复，任务继续存活。
- `blocked`：worker 判断这次尝试走不通。任务以失败收口并带上它给出的原因（owner 视图的 **AGENT BLOCKED** 卡片、`risk_alert` mailbox、`on_agent_error` hook 的 `error_kind: blocked`），而不是继续等待。

同一个任务最多留下两次未获答复的 `needs_repair` 升级；第三次会被拒绝并退回给 worker（escalate 卡片显示 error），同时提示它自己推进能做的部分，或用 `complete` 收口。这条上限是唯一的收敛手段：每次升级都会重新进入 `WaitingMain` 并重置生命周期计时器，卡死超时永远抓不到这个循环。owner 答复了那次升级后额度清零，其他投递不清零。

### 通知与请求回复

- **向上级汇报：**省略 `target_task_id`，使用 `message_type: progress`（默认）或 `notice`。此形式可带 `subtype`、`correlation_id`，以及不超过 32 KiB 的 JSON 对象 `payload`。主代理没有上级，不能使用此形式。
- **普通定向消息：**提供 `target_task_id`、`message`，可选填 `kind`；省略 `message_type`、`subtype`、`correlation_id` 和 `payload`。纠正或追加工作使用此形式。
- **回复待处理请求：**除 `target_task_id`、`message_type: response` 和 `message` 外，**必须**提供该请求的 `correlation_id`；可选填 `kind`。此形式不接受 `subtype` 与 `payload`。只能向上级汇报的角色不能发送定向回复。

### 长文本控制工具

`done`、`complete` 和 `escalate` 可能携带较长的 Markdown 报告、总结或升级原因。参数仍在流式接收时，TUI 会临时显示 `N chars received`；接收完成后，正文按 Markdown 直接渲染在卡片里。`complete` 还会保留结构化完成信息：修改文件、遗留限制、已知风险、后续建议和 artifact 引用。

这类卡片恒展开，标题行只有工具名：报告本身就是卡片的全部内容。`compact_context`、`delegate`、`question`、`notify`、`write`、`edit`、`apply_patch`、`delete`、`todo_write`、`handoff` 同样如此：没有折叠标记，也不响应折叠键。只有 `read`、`grep`、`glob`、`shell`、`cancel` 以及通用工具调用可以折叠，标题带 `▸` / `▾`。折叠键用法与收起后的样子见[使用指南：TUI 基本交互](./usage_CN.md#tui-基本交互)。

`delegate` 只有一个工具结果，即异步启动句柄。后续 `complete` 调用和 mailbox 更新是独立的 runtime 事件，按稳定的 `task_id` 更新已有委派任务/卡片，不会生成额外的 `delegate` 工具结果。每次 `complete` 报告都会在 owner 视图创建一张 **AGENT COMPLETE** 通知卡；worker 终止失败显示为 **AGENT BLOCKED**，并唤醒直接 owner。

### 委派任务边界

agent 间消息遵守请求边界：目标 busy 时，消息只入队并随其下一次 LLM 请求一并处理，不打断当前请求；空闲但可恢复的目标会被唤醒接收。发给空闲主代理的 progress / notice 不是纯信息：每一条未投递的更新都会保留，并按产出顺序投递；下一次回合之间的处理会把这些待投递更新合并成一批，为投递这批单独唤醒主代理多跑一回合（多一次 LLM 请求），之后它才可能重新静默。mailbox 与协调状态具备持久性：父子请求/响应记录与排队载荷都能跨 compaction 与重启存活，投递跨任务水合保持幂等。

委派状态以 runtime 为准，而不是以模型输出为准。worker 未能调用协调工具（`complete`、`escalate` 或 `notify`）时，会获得一次有界的后续请求；若仍然无法完成，或 provider/模型重试耗尽，Chord 会将其标记为 failed、记录 `risk_alert` 并唤醒 owner。Rehydrate 后的 runtime 可能获得新的 `agent_id`；后续协调应使用稳定的委派 `task_id`。

## MCP 工具

已配置 MCP server 暴露的工具会以 `mcp_<server>_<tool>` 形式注册（例如 `mcp_search_web_search_exa`），权限规则按这个完整名称匹配。用 MCP server 配置里的 `allowed_tools` 可以限制注册哪些远程工具，见[配置：MCP](./configuration_CN.md#mcp)。

## 相关

- [权限与安全](./permissions-and-safety_CN.md)
- [使用指南](./usage_CN.md)
- [扩展与定制](./customization_CN.md)
