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
| `save_artifact` | 在会话 artifacts 目录下保存或更新会话产物（报告、任务图、日志等）。 |
| `read_artifact` | 按会话相对路径读取会话产物。 |

## 编排与控制

这些工具控制的是 agent 工作流而不是本地副作用，因此 YOLO 模式**不会**绕过它们的权限——YOLO 消除的是确认文件编辑和 shell 命令的摩擦，不是角色的边界。`handoff`、`delegate`、`cancel` 会带来角色原本没有的能力，宽泛的 `"*": allow` 不会自动授予它们，角色需要哪一个就单独配置哪一个。`done` 和 `compact_context` 只是结束或收缩当前这段工作，挂载它们的那个运行时模式本身就是授权，纯通配规则不会影响它们。详见[权限与安全](./permissions-and-safety_CN.md)。

| 工具 | 用途 |
| --- | --- |
| `done` | 携带最终 Markdown 报告申请 loop 退出。仅在 loop 运行期间挂载，因此普通会话根本看不到它，完成结果直接用 assistant 正文返回。Loop 退出仍受退出条件和本地确认门控。 |
| `handoff` | 把计划/工作移交给另一个角色执行。 |
| `delegate` | 启动一个委派的 SubAgent 工作流，并立即返回启动句柄（`task_id` / `agent_id`）；不会等待任务完成。调用必须带上 `expected_write_scope`：只做研究时设为 `read_only: true`，可能修改工作区时声明覆盖任务所需内容的最窄 `files`、`path_prefix` 或 `modules` 范围；空范围会被拒绝。拒绝 `delegate` 会同时禁用该角色的 `cancel` 和嵌套委派。 |
| `cancel` | 取消一个被委派的 worker；前提是 `delegate` 已启用。 |
| `complete` | SubAgent 侧：携带摘要把当前委派任务标记为完成。 |
| `escalate` | SubAgent 侧：请求父 agent 介入，但不结束自己的任务。 |
| `notify` | 向 owner 或指定的被委派 worker 发送非阻塞通知。`message_type: response` 配合 `target_task_id` 和可选的 `correlation_id` 可向被委派 worker 发送结构化回复；`payload` 接受不超过 32 KiB 的 JSON 对象。 |
| `notify_peer` | SubAgent 侧：向同一个直接 owner 的存活兄弟任务发送非阻塞通知。它不会授予对 peer 的控制权——需要回复或决策时请使用 owner 中转的 `escalate` / `notify`。 |

### 长文本控制工具

`done`、`complete` 和 `escalate` 可能携带较长的 Markdown 报告、总结或升级原因。参数仍在流式接收时，TUI 会临时显示 `N chars received`；接收完成后，展开工具卡即可看到按 Markdown 渲染的正文。`complete` 还会保留结构化完成信息，例如修改文件、验证命令、限制、风险、后续建议和 artifact 引用。

`delegate` 只有一个工具结果，即异步启动句柄。后续 `complete` 调用和 mailbox 更新是独立的 runtime 事件，按稳定的 `task_id` 更新已有委派任务/卡片，不会生成额外的 `delegate` 工具结果。每次 `complete` 报告都会在 owner 视图创建一张 **AGENT COMPLETE** 通知卡；worker 终止失败显示为 **AGENT BLOCKED**，并唤醒直接 owner。

agent 间消息遵守请求边界：目标 busy 时，消息只入队并随其下一次 LLM 请求一并处理，不打断当前请求；目标空闲但可恢复时，Chord 会唤醒它；纯 progress 更新不会强制本来空闲的 agent 启动。mailbox 与协调状态具备持久性：父子请求/响应记录、peer 路由与排队载荷都能跨 compaction 与重启存活，投递跨任务水合保持幂等。`notify_peer` 只会发送给同一个直接 owner 下的存活兄弟任务。

委派的写入范围既是并发声明，也是执行边界。只读工作必须设置 `read_only: true`；可能修改工作区时，至少要声明一个文件、路径前缀或模块。带范围的 worker 不能执行任意 Shell 命令，嵌套委派也不能声明比父任务更宽的范围。只声明任务确实需要的路径，这样互不相关的委派工作才能并行。

委派状态以 runtime 为准，而不是以模型输出为准。worker 未能调用协调工具（`complete`、`escalate` 或 `notify`）时，会获得一次有界的后续请求；若仍然无法完成，或 provider/模型重试耗尽，Chord 会将其标记为 failed、记录 `risk_alert` 并唤醒 owner。Rehydrate 后的 runtime 可能获得新的 `agent_id`；后续协调应使用稳定的委派 `task_id`。

## MCP 工具

已配置 MCP server 暴露的工具会以 `mcp_<server>_<tool>` 形式注册（例如 `mcp_search_web_search_exa`），权限规则按这个完整名称匹配。用 MCP server 配置里的 `allowed_tools` 可以限制注册哪些远程工具，见[配置 — MCP](./configuration_CN.md#mcp)。

## 相关

- [权限与安全](./permissions-and-safety_CN.md)
- [使用指南](./usage_CN.md)
- [扩展与定制](./customization_CN.md)
