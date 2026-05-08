# Headless

`chord headless` 是 Chord 的轻量控制面入口，适合 bot、gateway、自动化脚本接入。

## 它是什么

- 无 TUI
- 通过 stdio 交互
- 输入是 JSON 命令（每行一条）
- 输出是 JSON envelope（每行一条）

适合作外层集成，但**不**自带浏览器前端、多租户隔离、完整权限托管。

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

每个出站 envelope 形状：

```json
{ "type": "<事件类型>", "payload": { ... } }
```

收到的第一行**永远**是 `{"type": "ready", ...}`——拿到它再发其他命令。

## 命令

下列命令通过 stdin 发送。未识别的 `type` 会返回 `error` envelope。

### `subscribe`

订阅你想推送的事件类型。默认不订阅（你只能收到自己命令的请求/响应 envelope，直到订阅为止）。

```json
{"type": "subscribe", "events": ["activity", "assistant_message", "idle", "tool_result"]}
```

响应：

```json
{"type": "subscribe_response", "payload": {"events": ["activity", "assistant_message", "idle", "tool_result"]}}
```

可订阅类型：`activity`、`assistant_message`、`idle`、`confirm_request`、`question_request`、`error`、`agent_done`、`info`、`toast`、`tool_result`、`assistant_rollback`、`todos`。

### `status`

请求当前后端状态快照。

```json
{"type": "status"}
```

响应：

```json
{
  "type": "status_response",
  "payload": {
    "session_id": "20260508120000000",
    "busy": false,
    "phase": "",
    "phase_detail": "",
    "pending_confirm": null,
    "pending_question": null,
    "last_error": "",
    "last_outcome": "completed",
    "updated_at": "2026-05-08T12:00:00Z"
  }
}
```

### `send`

给 agent 发用户消息。slash 命令的语义与 TUI 一致；裸 `/models` 在 headless 下被当作 `/models status`，因为没有 TUI 浮层可弹。

```json
{"type": "send", "content": "请总结这个项目的目录结构。"}
```

如果存在挂起的 `confirm_request` 或 `question_request`，而用户走 `send` 发了普通消息（不是 `confirm` / `question`），Chord 会**自动取消**挂起的交互，让新消息能被消费。

### `models`

查看或修改模型池。

```json
{"type": "models", "action": "status"}
```

```json
{"type": "models", "action": "set_current_role", "pool": "thinking"}
```

响应：

```json
{
  "type": "models_response",
  "payload": {
    "ok": true,
    "status": "current role: thinking\n..."
  }
}
```

`status` 是与 `/models status` 一样的纯文本快照。

### `confirm`

回应一个挂起的 `confirm_request`。`request_id` 来自请求本身。

```json
{
  "type": "confirm",
  "request_id": "r-…",
  "action": "allow",
  "final_args_json": "{\"path\":\"...\"}",
  "edit_summary": "",
  "deny_reason": "",
  "rule_pattern": "Bash:^git status$",
  "rule_scope": "session"
}
```

`action` 的取值跟随模型/运行时给出的选项（`allow`、`deny`、`allow_once`、…）。可选的 `rule_pattern` + `rule_scope`（`session` / `project` / `user_global`）会同时安装一条 permission 规则；不要规则就两个字段都省略。

### `question`

回答一个挂起的 `question_request`。

```json
{"type": "question", "request_id": "r-…", "answers": ["yes"], "cancelled": false}
```

多选题在 `answers` 里传多个字符串。传 `"cancelled": true` 表示不回答，直接关掉。

### `cancel`

取消当前轮次（等价于 TUI 里两次 `Esc`）。

```json
{"type": "cancel"}
```

## 事件

下列事件在 stdout 推送。下面列出默认会发的 + 可订阅的。**未识别字段**视为不透明，避免未来服务端升级时客户端炸掉。

### 默认必发（无需订阅）

| 类型                  | 时机                                                                                          | 关键 payload 字段                                                                                          |
| --------------------- | --------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------- |
| `ready`               | 服务端启动完成、可接受命令                                                                    | `session_id`，进入 worktree 时还有 `name`、`branch`、`path`、`repo_root`                                   |
| `subscribe_response`  | 对 `subscribe` 命令的响应                                                                     | `events`                                                                                                   |
| `status_response`     | 对 `status` 命令的响应                                                                        | 见 [`status`](#status)                                                                                     |
| `models_response`     | 对 `models` 命令的响应                                                                        | `ok`、`message`、`status`                                                                                  |
| `error`               | 命令解析或执行错误                                                                            | `message`                                                                                                  |

### 可订阅

| 类型                    | 时机                                                                                                | 关键 payload 字段                                                                                                  |
| ----------------------- | --------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------ |
| `activity`              | Agent 进入新的阶段                                                                                  | `agent_id`、`type`（`connecting`、`streaming`、`compacting` 等）、`detail`                                         |
| `assistant_message`     | 一条完整的 assistant 消息已准备好被消费                                                             | `agent_id`、`text`、`tool_calls`                                                                                   |
| `idle`                  | Agent 等待用户输入                                                                                  | `last_outcome`（`completed` / `cancelled` / `error`）                                                              |
| `tool_result`           | 一次工具执行完成                                                                                    | `call_id`、`name`、`status`（`success` / `error` / `cancelled`）、`agent_id`                                       |
| `confirm_request`       | 工具需要显式确认                                                                                    | `request_id`、`tool_name`、`args_json`、`needs_approval`、`already_allowed`、`timeout_ms`                          |
| `question_request`      | 模型反问用户                                                                                        | `request_id`、`tool_name`、`question`、`options`、`option_details`、`default_answer`、`multiple`、`timeout_ms`     |
| `agent_done`            | SubAgent 完成任务                                                                                   | `agent_id`、`task_id`、`summary`                                                                                   |
| `assistant_rollback`    | 丢弃流式 assistant 输出（主要给流式 UI 用）                                                         | `agent_id`、`reason`                                                                                               |
| `info`                  | 运行时信息                                                                                          | `agent_id`、`message`                                                                                              |
| `toast`                 | 临时通知，TUI 显示、headless 一般可忽略                                                             | `agent_id`、`message`、`level`（`info` / `warn` / `error`）                                                        |
| `todos`                 | 替换性的 todo 列表                                                                                  | `todos[]`，每项 `{title, status}`                                                                                  |
| `error`                 | 运行时错误                                                                                          | `agent_id`、`message`                                                                                              |

`assistant_message.text` 空只发生在异常情况——Chord 会记一条 warning，gateway 集成通常应该跳过空消息而不是把空 text 转发给下游。

## 通过 `send` 走 slash 命令

为了方便从只有单一输入框的聊天平台接入，headless 也支持通过 `send` 触发以下 slash 命令：

- `/models status`、`/models <pool>`、`/models --agent <name> <pool>`
- `/help`、`/stats`、`/diagnostics`、`/compact`、`/loop on`、`/loop off`

裸 `/models` 在 headless 下被当作 `/models status`。某些 slash 命令仅 TUI 可用（如 `/new`、`/resume`——需要交互式选择器）；在 headless 下尝试会返回 `error` envelope，提示 "X is only available in local TUI mode"。

## 最小 Python 客户端

```python
import json
import subprocess
import threading

proc = subprocess.Popen(
    ["chord", "headless", "-d", "/path/to/project"],
    stdin=subprocess.PIPE,
    stdout=subprocess.PIPE,
    stderr=subprocess.DEVNULL,
    bufsize=1,
    text=True,
)

def reader():
    for line in proc.stdout:
        ev = json.loads(line)
        print("<-", ev["type"], ev.get("payload"))

threading.Thread(target=reader, daemon=True).start()

def send(cmd: dict) -> None:
    proc.stdin.write(json.dumps(cmd) + "\n")
    proc.stdin.flush()

# 等到 ready（第一行永远是 ready），再订阅、再发消息
send({"type": "subscribe",
      "events": ["activity", "assistant_message", "idle", "tool_result"]})
send({"type": "send", "content": "请总结这个项目的目录结构。"})
```

生产环境别忘了同时处理 `confirm_request`（用 `confirm` 回）和 `question_request`（用 `question` 回）——agent 会一直挂着等。

## chord-gateway —— 推荐的 headless 消费方式

如果你想从聊天界面（飞书、微信等）调用 Chord，或者搭一个多用户网关，通常**不需要**从零实现 headless 协议。配套项目 [keakon/chord-gateway](https://github.com/keakon/chord-gateway) 已经把它包好了，并补齐了协议有意留白的部分：

- 进程生命周期：按会话 spawn / 重启 `chord headless`，回收闲置进程
- 多租户隔离：每用户工作目录、审计日志、限流
- 聊天平台适配：飞书 / 微信 webhook、消息分片、图片转发
- 权限 UX：把 `confirm_request` 和 `question_request` 渲染成内联消息，把回复反映射成 `confirm` / `question` 命令
- 协议层之上的重连辅助

本页这套 headless 协议是更底层的契约，给"chord-gateway 还没覆盖到、需要自己实现某些东西"的接入方用。如果你的目标只是"让人在手机上和 Chord 聊天"，请直接从 chord-gateway 起步，遇到具体障碍再下沉到 headless。

## 适用场景

- 由外层 gateway 管理进程生命周期
- 由外层系统决定哪些事件展示给终端用户
- 在外层强制做工作目录、权限、审计、租户隔离控制

## 不能取代

`chord headless` 不是：

- 浏览器前端
- 多租户安全边界
- 完整权限沙箱

## 相关

- [使用指南](./usage_CN.md)
- [CLI — chord headless](./cli_CN.md#chord-headless)
- [权限与安全](./permissions-and-safety_CN.md)
- [常见问题排查](./troubleshooting_CN.md)
