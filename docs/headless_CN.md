# Headless

`chord headless` 是 Chord 的轻量控制面入口，适合 bot、gateway、自动化脚本接入。

## 它是什么

- 无 TUI
- 通过 stdio 交互
- 输入是 JSON 命令（每行一条）
- 输出是 JSON envelope（每行一条）

适合做外层集成，但**不**自带浏览器前端、多租户隔离、完整权限托管。

## 启动

```bash
chord headless
# 或
go run ./cmd/chord/ headless
```

CLI flag：`-d/--session-dir`、`-c/--continue`、`-r/--resume`、`--worktree`。详见 [CLI — `chord headless`](./cli_CN.md#chord-headless)。

## 协议格式

- **stdin**：每行一条 JSON 命令
- **stdout**：每行一条 JSON envelope。其他诊断输出走 stderr，**不要**把 stderr 当协议解析。

每个出站 envelope 的形状：

```json
{
  "type": "event_type",
  "payload": { … },
  "timestamp": 1715760000000
}
```

### 命令

输入命令（每行一条）：

```json
{ "command": "start", "payload": { "required_agent": "builder" } }
{ "command": "message", "payload": { "content": "Hello!" } }
{ "command": "cancel" }
{ "command": "resume", "payload": { "session": "20260428064910975" } }
{ "command": "models", "payload": { "pool": "fast" } }
{ "command": "terminate" }
```

终态命令：

- `terminate`：请求 headless 优雅退出。Chord 将 `agent` 转为 idle（必要时先取消活动 turn），发送一个 `terminated` envelope，然后正常退出。

### Events

关键是 **`ready` event**：

```json
{
  "type": "ready",
  "payload": {
    "sid": "20260502015258426",
    "project_path": "/home/user/my-project",
    "debug": false,
    "worktree": {
      "name": "feat-auth",
      "branch": "chord/feat-auth",
      "path": "/home/user/.local/state/chord/worktrees/…",
      "repo_root": "/home/user/my-project"
    }
  },
  "timestamp": 1715760000000
}
```

- `sid` 是当前 session ID。
- `worktree` 仅在 `chord headless --worktree` 模式下出现；`worktree.name` 是从 `--worktree <name>` 传入的名称；`worktree.branch` 是该任务对应的 git 分支；`worktree.path` 是该 worktree 在文件系统上的位置；`worktree.repo_root` 是主仓库路径。

`ready` 之后，可以发送 `start` 或 `message` 来推进。

还有工具调用、idle 状态、turn 生命周期和错误类型等事件（后面会列出完整表）。

拿 JSON envelope 做集成时，尽量用 `type` 做路由，别死依赖 `payload` 结构；新增字段只可能加在 `payload` 的新 key 上，不会拆旧 key。

### 命令：详细说明

- **`start`**：当前系统 prompt / AGENTS.md / skill 均未加载时，会先做一次默认初始化（等于发送一个空首条消息）；若已有初始化，`start` 是 no-op。
- **`message`**：发送一条用户消息。payload 中的 `content` 为必填字符串。
- **`cancel`**：取消当前 turn（不影响 idle 会话）。
- **`resume`**：恢复指定 session（需要 `payload.session`）。
- **`models`**：切换模型池或查看当前状态。带上 `payload.pool` 切换池；带上 `payload.agent` 和 `payload.pool` 设指定 agent；都不带则返回当前模型池状态。
- **`terminate`**：优雅退出，agent 转为 idle 后 channel 关闭。

### Envelope 类型清单

| type                     | 发生时机                                                                 | 关键 payload                                                        |
| ------------------------ | ------------------------------------------------------------------------ | ------------------------------------------------------------------- |
| `ready`                  | headless 启动后就绪，sid / project / worktree 信息已就绪                 | `sid`、`project_path`、`debug`、`worktree`（仅在 worktree 模式）    |
| `llm_request`            | LLM 请求开始                                                             | `model`、`provider`、`sequence`（一轮内递增）                       |
| `streaming_start`        | LLM 开始返回正文                                                         | `model`、`provider`                                                 |
| `streaming_chunk`        | 每个 streaming delta                                                     | `delta`、`type`（`text` / `reasoning`）                             |
| `streaming_complete`     | LLM streaming 结束                                                       | `model`、`provider`                                                 |
| `tool_call`              | 工具被调用                                                               | `tool`、`id`、`params`                                              |
| `tool_result`            | 某个工具执行完成                                                         | `tool`、`id`、`status`（`success` / `error` / `cancelled`）         |
| `tool_batch`             | 一个 turn 内按需发送的跨模型响应聚合工具批处理                           | `tools: [{tool, id, status}]`                                       |
| `compaction`             | 上下文压缩开始                                                           | `threshold`、`budget`                                               |
| `compaction_complete`    | 上下文压缩完成                                                           | `summary_length`                                                    |
| `idle`                   | agent 回到 idle 状态（turn 完成、会话就绪）                              | 无（或旧版 notifications 子对象）                                   |
| `messaged`               | 确认收到用户消息（非 streaming 事件）                                    | `content`                                                           |
| `error`                  | 不可恢复的 headless 错误（写 stdout，然后退出）                          | `error`（字符串）                                                   |
| `terminated`             | 接收到 `terminate` 命令后优雅退出                                        |                                                                     |

### 超时建议

- `start` 后设 10–60 秒超时等待 `llm_request` 或具体输出事件。
- `message` 后按正常用户交互超时等待（通常 5–300 秒）。
- 没有活跃 `llm_request` 时，每 30–60 秒可以期待一个 `idle`（也可能一直 idle 直到下一条 `message`）。

### 与 chord-gateway 的关系

[chord-gateway](https://github.com/keakon/chord-gateway) 会：

- 管理 Chord headless 进程生命周期
- 做 websocket / ChatOps 协议适配
- 基于 `idle` 事件做 `✅` active-对话状态推送
- 处理会话列表 / resume / 模型池选择等面向聊天用户的逻辑

如果你想**不用 chord-gateway 直接集成**，以 stdin/stdout JSONL 为主线就够了。遇到事件消费不全、被去重、连接状态不同步等问题时，先看 gateway 是否做了干预，不要把 gateway 行为当成 Chord 协议本身。

## 适配方必读：下一步

- `ready` 事件是你集成的起点——从它拿到 `sid`、`project_path`、可选的 `worktree` 信息，再推进。
- idle 检测：收到 `idle` 后，Chord 可以安全接收下一条 `message`。
- 工具调用最好原样记录，别擅自修改 tool schema——改动会污染模型的后续工具使用。
- `terminate` 是退出命令：任何中间层只要发了 `terminate`，Chord 会尽可能把 agent 拉到 idle 然后退出，不会自主恢复。
- 没有 `/help`、`/stats`、`/diagnostics`、`/compact`、`/loop on`、`/loop off` 在 headless 下可用的明确保证——每个命令是否可用取决于该 headless 构建中具体 TUI 无关部分的实现，集成方不要默认这些可用。
- Chord headless 不负责多租户隔离、安全沙箱、浏览器安全边界。接入聊天平台、自动化系统或团队服务时，应在外层额外控制访问范围、权限批准和审计。

## 相关文档

- [CLI — chord headless](./cli_CN.md#chord-headless)
- [权限与安全](./permissions-and-safety_CN.md)
- [常见问题排查](./troubleshooting_CN.md)
