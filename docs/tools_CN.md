# 内置工具

本页列出模型可调用的全部内置工具名。在 agent 的 `permission:` 规则、hook 的 `tools:` 过滤器和 skill 的 `allowed_tools` 列表中，请使用这些名称的原样拼写。

`allow` / `ask` / `deny` 的判定方式（包括编排类工具之间的特殊耦合）见[权限与安全](./permissions-and-safety_CN.md)。

## 文件

| 工具 | 用途 |
| --- | --- |
| `read` | 读取本地文件进上下文。 |
| `write` | 创建文件，或有意整体替换一个文件。 |
| `edit` | 在现有文件中替换精确文本。 |
| `patch` | 对单个已有文件应用 unified diff hunk。 |
| `delete` | 删除整个文件。 |
| `view_image` | 加载本地 PNG/JPEG 进上下文；仅当生效模型池的第一个模型支持图片输入时可用。本地路径权限处理与 `read` 相同。 |

## 搜索与导航

| 工具 | 用途 |
| --- | --- |
| `grep` | 按正则/字面文本搜索内容，输出有上限；支持多根 `paths` 和 `includes` glob 过滤。 |
| `glob` | 按 glob 模式匹配路径，输出有上限。 |
| `lsp` | 在指定文件位置做语义化的 definition / references / implementation 查询，需要对应 LSP server 覆盖该文件类型。 |

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

这些工具控制的是 agent 工作流而不是本地副作用。YOLO 模式**不会**绕过 `handoff`、`delegate`、`cancel`、`done` 的权限；宽泛的 `"*": allow` 规则也不会自动授予它们——角色需要哪一个，就单独配置哪一个。

| 工具 | 用途 |
| --- | --- |
| `done` | 仅当当前 runtime 或工作流明确要求工具化完成信号时发送最终报告，主要用于申请 loop 退出。普通任务完成后必须直接用 assistant 正文返回结果；仅仅完成工作或发现 `done` 可用，都不是调用理由。Loop 退出仍受退出条件和本地确认门控。 |
| `handoff` | 把计划/工作移交给另一个角色执行。 |
| `delegate` | 启动一个委派的 SubAgent 工作流。拒绝它会同时禁用该角色的 `cancel` 和嵌套委派。 |
| `cancel` | 取消一个被委派的 worker；前提是 `delegate` 已启用。 |
| `complete` | SubAgent 侧：携带摘要把当前委派任务标记为完成。 |
| `escalate` | SubAgent 侧：请求父 agent 介入，但不结束自己的任务。 |
| `notify` | 向 owner 或指定的被委派 worker 发送非阻塞通知。 |

## MCP 工具

已配置 MCP server 暴露的工具会以 `mcp_<server>_<tool>` 形式注册（例如 `mcp_search_web_search_exa`），权限规则按这个完整名称匹配。用 MCP server 配置里的 `allowed_tools` 可以限制注册哪些远程工具，见[配置 — MCP](./configuration_CN.md#mcp)。

## 相关

- [权限与安全](./permissions-and-safety_CN.md)
- [使用指南](./usage_CN.md)
- [扩展与定制](./customization_CN.md)
