# 内置工具

本页列出模型可调用的全部内置工具名。在 agent 的 `permission:` 规则、hook 的 `tools:` 过滤器和 skill 的 `allowed_tools` 列表中，请使用这些名称的原样拼写。

`allow` / `ask` / `deny` 的判定方式（包括编排类工具之间的特殊耦合）见[权限与安全](./permissions-and-safety_CN.md)。

## 文件

| 工具 | 用途 |
| --- | --- |
| `read` | 读取本地文件进上下文，支持 1-based 的 `offset` / `limit` 行分页。 |
| `write` | 创建文件，或有意整体替换一个文件。 |
| `edit` | 在现有文件中替换精确文本。 |
| `apply_patch` | 应用 Codex 风格补丁信封（`*** Begin Patch`）：在单次事务性调用中新增、更新、删除或移动一个或多个文件。规则和过滤器中仍接受旧别名 `patch`。 |
| `delete` | 删除整个文件。 |
| `view_image` | 加载本地 PNG/JPEG 进上下文；仅当生效模型池的第一个模型支持图片输入时可用。本地路径权限处理与 `read` 相同。 |

模型每次只会看到 `edit` / `apply_patch` 中的一个（按模型家族选择）；补丁原生模型的文件创建/删除也经由 `apply_patch` 信封而非 `write`/`delete`。详见[编辑工具](./edit-tools_CN.md)。

## 搜索与导航

| 工具 | 用途 |
| --- | --- |
| `grep` | 按正则/字面文本搜索内容，输出有上限；支持多根 `paths` 和 `includes` glob 过滤。 |
| `glob` | 按 glob 模式匹配路径，输出有上限。 |
| `lsp` | 在指定文件位置做语义化的 definition / references / implementation 查询，需要对应 LSP server 覆盖该文件类型。 |

在 TUI 中，`lsp` 卡片会在头部概括查询动作和位置（例如 `find references internal/agent/main.go:54:17`），查询完成后显示位置数量，展开详情可看到每个返回的 `path:line:character` 位置。

## 执行

| 工具 | 用途 |
| --- | --- |
| `shell` | 执行非交互式 shell 命令。 |
| `spawn` | 启动长时间运行的后台进程。 |
| `spawn_status` | 查看 `spawn` 启动进程的生命周期状态。 |
| `spawn_stop` | 停止 `spawn` 启动的进程。 |

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

`save_artifact` 有两种互斥的参数形态：用 `filename` 加 `content`（必要时配 `mode: create / append / overwrite`）写入或更新会话产物；或者改用 `result_type` 加 `result` 参数对——`result` 必须是 JSON object——把载荷作为不可变、内容寻址的结果存入 `artifacts/results/`，返回的 ResultRef（`id`、`result_type`、`rel_path`、`sha256`、`size_bytes`）可以直接作为 `complete` 的 `result_ref` 传入。

## 编排与控制

这些工具控制的是 agent 工作流而不是本地副作用，因此 YOLO 模式**不会**绕过它们的权限——YOLO 消除的是确认文件编辑和 shell 命令的摩擦，不是角色的边界。`handoff`、`delegate`、`cancel` 会带来角色原本没有的能力。非 YOLO 模式下它们走普通权限规则，宽泛的 `"*": allow` 会像授予其它工具一样授予它们——内置 `builder` 必须显式 deny `handoff` 和 `delegate`，正是为了在默认规则下保持单 agent。只有在 YOLO 下通配符才够不着它们：默认拒绝，除非规则直接指名对应工具。`done` 和 `compact_context` 只是结束或收缩当前这段工作，挂载它们的那个运行时模式本身就是授权，纯通配规则不会影响它们。详见[权限与安全](./permissions-and-safety_CN.md)。

| 工具 | 用途 |
| --- | --- |
| `done` | 携带最终 Markdown 报告申请 loop 退出。仅在 loop 运行期间挂载，因此普通会话根本看不到它，完成结果直接用 assistant 正文返回。Loop 退出仍受退出条件和本地确认门控。 |
| `handoff` | 把计划/工作移交给另一个角色执行。 |
| `delegate` | 启动一个委派的 SubAgent 工作流并立即返回它的启动句柄（`task_id` / `agent_id`），不等它完成。调用必须携带 `expected_write_scope`：声明覆盖工作范围的最小 `files` / `path_prefix` / `modules`。这份声明是协调元数据，不是运行时边界——worker 能否改文件完全由角色的权限规则决定（deny 掉 `write` / `edit` / `delete` / `apply_patch` 的角色注册不到这些工具），声明路径之外的调用不会被运行时拦截。诚实声明最窄范围，兄弟任务的叠加提示才有意义：新任务的声明范围与另一个仍活跃的任务重叠时，委派照常启动，句柄会带 `scope_conflict: true`、`suggested_task_id` 和 `suggested_action: serialize_or_worktree`，提示你把两个任务串行执行、用 `notify` 协调共享文件的编辑，或让新 worker 在独立的 git worktree 里工作。只读任务应选择注册不到文件修改工具的角色并传空 scope——空 scope 只对这种角色放行，能写文件的角色必须声明非空范围，否则委派被拒绝。`shell` / `spawn` 这类命令工具不受 scope 约束，可用性由角色的权限规则决定。拒绝 `delegate` 会同时禁用该角色的 `cancel` 与嵌套委派。 |
| `cancel` | 取消一个被委派的 worker；前提是 `delegate` 已启用。 |
| `complete` | SubAgent 侧：携带摘要把当前委派任务标记为完成。 |
| `escalate` | SubAgent 侧：请求父 agent 介入，但不结束自己的任务。 |
| `notify` | 向上级代理或指定子代理发送非阻塞通知。定向消息可唤醒已完成或已失败的子代理，并保留它自己的会话历史；已取消的任务不可恢复。具体参数见下方。 |

### 通知与请求回复

- **向上级汇报：**省略 `target_task_id`，使用 `message_type: progress`（默认）或 `notice`。此形式可带 `subtype`、`correlation_id`，以及不超过 32 KiB 的 JSON 对象 `payload`。主代理没有上级，不能使用此形式。
- **普通定向消息：**提供 `target_task_id`、`message`，可选填 `kind`；省略 `message_type`、`subtype`、`correlation_id` 和 `payload`。纠正或追加工作使用此形式。
- **回复待处理请求：**除 `target_task_id`、`message_type: response` 和 `message` 外，**必须**提供该请求的 `correlation_id`；可选填 `kind`。此形式不接受 `subtype` 与 `payload`。只能向上级汇报的角色不能发送定向回复。


### 长文本控制工具

`done`、`complete` 和 `escalate` 可能携带较长的 Markdown 报告、总结或升级原因。参数仍在流式接收时，TUI 会临时显示 `N chars received`；接收完成后，正文按 Markdown 直接渲染在卡片里。`complete` 还会保留结构化完成信息，例如修改文件、验证命令、限制、风险、后续建议和 artifact 引用。

这类卡片恒展开，标题行只有工具名：报告本身就是卡片的全部内容，折叠成一行预览、再把摘要压回标题，只是把正文里已有的内容重说一遍。`compact_context`（目标、已完成、决策、遗留问题、下一步、状态文件）、`delegate`（描述、worker 句柄、完成信息）、`question`（每个问题、选项与选中项）和 `notify`（target、kind、消息）同样如此：没有折叠标记，`o` / `Enter` / `Space` 对它们不生效。以参数作为索引的卡片——`read`、`write`、`edit`、`apply_patch`、`delete`、`grep`、`glob`、`handoff` 等——保留可折叠正文和标题索引行；`cancel` 也照旧可折叠，并在标题保留 `cancel <task_id> (<原因>)`。

`delegate` 只有一个工具结果，即异步启动句柄。后续 `complete` 调用和 mailbox 更新是独立的 runtime 事件，按稳定的 `task_id` 更新已有委派任务/卡片，不会生成额外的 `delegate` 工具结果。每次 `complete` 报告都会在 owner 视图创建一张 **AGENT COMPLETE** 通知卡；worker 终止失败显示为 **AGENT BLOCKED**，并唤醒直接 owner。

### 委派任务边界

agent 间消息遵守请求边界：目标 busy 时，消息只入队并随其下一次 LLM 请求一并处理，不打断当前请求；目标空闲但可恢复时，Chord 会唤醒它；纯 progress 更新不会强制本来空闲的 agent 启动。mailbox 与协调状态具备持久性：父子请求/响应记录与排队载荷都能跨 compaction 与重启存活，投递跨任务水合保持幂等。


委派状态以 runtime 为准，而不是以模型输出为准。worker 未能调用协调工具（`complete`、`escalate` 或 `notify`）时，会获得一次有界的后续请求；若仍然无法完成，或 provider/模型重试耗尽，Chord 会将其标记为 failed、记录 `risk_alert` 并唤醒 owner。Rehydrate 后的 runtime 可能获得新的 `agent_id`；后续协调应使用稳定的委派 `task_id`。

## MCP 工具

已配置 MCP server 暴露的工具会以 `mcp_<server>_<tool>` 形式注册（例如 `mcp_search_web_search_exa`），权限规则按这个完整名称匹配。用 MCP server 配置里的 `allowed_tools` 可以限制注册哪些远程工具，见[配置 — MCP](./configuration_CN.md#mcp)。

## 相关

- [权限与安全](./permissions-and-safety_CN.md)
- [使用指南](./usage_CN.md)
- [扩展与定制](./customization_CN.md)
