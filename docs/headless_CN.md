# Headless mode

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

CLI flag：`-d/--session-dir`、`-c/--continue`、`-r/--resume`、`-w/--worktree`。详见 [CLI — `chord headless`](./cli_CN.md#chord-headless)。

## 协议格式

- **stdin**：每行一条 JSON 命令
- **stdout**：每行一条 JSON envelope。其他诊断输出走 stderr，**不要**把 stderr 当协议解析。

每个出站 envelope 的结构：

```json
{ "type": "<event-type>", "payload": { ... } }
```

你收到的第一行一定是 `{"type": "ready", ...}`；在它之前不要发送其他命令。

## 命令

向 stdin 发送以下命令。未知命令会收到 `error` envelope。

### `subscribe`

选择你想接收的推送事件类型。**如果从未发送 `subscribe`，Chord 默认会转发所有可订阅事件。** 一旦发送 `subscribe`，默认行为就会被替换成显式 allowlist。

```json
{"type": "subscribe", "events": ["activity", "assistant_message", "idle", "done_completion"]}
```

响应：

```json
{"type": "subscribe_response", "payload": {"events": ["activity", "assistant_message", "idle", "done_completion"]}}
```

可订阅事件类型：`activity`、`assistant_message`、`idle`、`confirm_request`、`question_request`、`error`、`agent_done`、`info`、`toast`、`done_completion`、`assistant_rollback`、`todos`。

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
    "pending_handoff": null,
    "last_error": "",
    "last_outcome": "completed",
    "updated_at": "2026-05-08T12:00:00Z"
  }
}
```

### `send`

向 agent 发送用户消息。slash 命令的行为与 TUI 一致；裸 `/models` 会被当作 `/models status`，因为 headless 没有 TUI overlay。

```json
{"type": "send", "content": "请总结一下项目结构。"}
```

如果当前有待处理的 `confirm_request` 或 `question_request`，而用户发送了普通消息（不是下面的 `confirm` / `question`），Chord 会先自动关闭该待处理交互，再消费这条新消息。

### `models`

查看或切换模型池。

```json
{"type": "models", "action": "status"}
```

```json
{"type": "models", "action": "set_current_model_pool", "pool": "thinking"}
```

响应：

```json
{
  "type": "models_response",
  "payload": {
    "ok": true,
    "status": "Model pool: thinking\n..."
  }
}
```

`status` 是与 `/models status` 一致的纯文本快照。

### `confirm`

处理一个待决的 `confirm_request`。使用请求里的 `request_id`。

```json
{
  "type": "confirm",
  "request_id": "r-…",
  "action": "allow",
  "final_args_json": "{\"path\":\"...\"}",
  "edit_summary": "",
  "deny_reason": "",
  "rule_pattern": "Shell:^git status$",
  "rule_scope": "session"
}
```

`action` 要与模型/运行时提供的选项一致（如 `allow`、`deny`、`allow_once` 等）。可选的 `rule_pattern` + `rule_scope`（`session` / `project` / `user_global`）会在这次答复的同时安装一条权限规则；两者都省略时表示一次性决策。`session` 只在当前会话内生效；`project` 写入当前项目的 `.chord/agents/<role>.yaml`；`user_global` 写入用户配置目录的 `agents/<role>.yaml`（默认 `~/.config/chord/agents/<role>.yaml`）。

### `question`

回答一个待决的 `question_request`。

```json
{"type": "question", "request_id": "r-…", "answers": ["yes"], "cancelled": false}
```

多选题时可在 `answers` 里传多个字符串。若只想关闭问题而不作答，传 `"cancelled": true`。

### `cancel`

取消当前 turn（等价于在 TUI 里按两次 `Esc`）。

```json
{"type": "cancel"}
```

## 事件

你会在 stdout 收到这些事件。下表覆盖了默认发出的响应类事件，以及可订阅的推送事件。对未知字段请保持宽容，把它们当作未来扩展字段，不要因为新字段而让客户端崩掉。

### 总是会发出（不需要订阅）

| 类型                 | 何时出现                                     | 主要 payload 字段 |
| -------------------- | -------------------------------------------- | ----------------- |
| `ready`              | 服务启动完成，可以接受命令                   | `session_id`，以及可选 worktree 信息：`name`、`branch`、`path`、`repo_root` |
| `subscribe_response` | 响应 `subscribe`                             | `events` |
| `status_response`    | 响应 `status`                                | 见 [`status`](#status) |
| `models_response`    | 响应 `models`                                | `ok`、`message`、`status` |
| `error`              | 命令解析或执行错误                           | `message` |

### 可订阅推送事件

| 类型                 | 何时出现                                     | 主要 payload 字段 |
| -------------------- | -------------------------------------------- | ----------------- |
| `activity`           | Agent 进入新阶段                             | `agent_id`、`type`（如 `connecting`、`streaming`、`compacting`） 、`detail` |
| `assistant_message`  | 一条完整 assistant 消息可供消费              | `agent_id`、`text`、`tool_calls` |
| `idle`               | Agent 再次可接收输入                         | `last_outcome`（`completed` / `cancelled` / `error`） |
| `done_completion`   | 非 loop 模式下 Done 工具完成并给出最终报告 | `call_id`、`report`、`reason`、`status`、`agent_id`、`mode` |
| `confirm_request`    | 某个工具需要显式确认                         | `request_id`、`tool_name`、`args_json`、`needs_approval`、`already_allowed`、`timeout_ms` |
| `question_request`   | 模型向用户提问                               | `request_id`、`tool_name`、`question`、`options`、`option_details`、`default_answer`、`multiple`、`timeout_ms` |
| `agent_done`         | 某个 SubAgent 完成任务                       | `agent_id`、`task_id`、`summary` |
| `assistant_rollback` | 丢弃尚未提交的流式 assistant 输出            | `agent_id`、`reason` |
| `info`               | 运行时信息消息                               | `agent_id`、`message` |
| `toast`              | TUI 中的瞬时通知；headless 可以忽略          | `agent_id`、`message`、`level`（`info` / `warn` / `error`） |
| `todos`              | 替换当前 todo 列表                           | `todos[]`，元素结构为 `{id, content, status, active_form}`；当启用 Delegate workflow 且各项分别对应不同的活跃委派工作流、并使用唯一 `active_form` 时，允许同时存在多个 `in_progress`。 |
| `error`              | 运行时错误                                   | `agent_id`、`message` |

`assistant_message.text` 只有在非常异常的情况下才会为空。Chord 遇到这种情况会记 warning；gateway 集成通常应跳过空消息，而不是继续向下游转发空文本。

## 通过 `send` 兼容 slash 命令

为方便接入只有单一文本输入的聊天表面，headless 也支持通过 `send` 发送这些 slash 命令：

- `/models status`、`/models <pool>`、`/models --agent <name> <pool>`
- `/help`、`/stats`、`/compact`、`/loop on`、`/loop off`（仅当当前 MainAgent 角色可使用 `Done` 工具时）

裸 `/models` 会被当作 `/models status`。部分 slash 命令是 TUI 专用的（例如 `/new`、`/resume` 需要交互式 picker）；在 headless 模式下尝试调用时，会返回 `error` envelope，说明“X 仅在本地 TUI 模式可用”。

## 最小 Python 客户端示例

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

# 等待 ready（第一行一定是 ready），然后订阅并发送消息。
send({"type": "subscribe",
      "events": ["activity", "assistant_message", "idle", "done_completion"]})
send({"type": "send", "content": "Summarize the project structure."})
```

生产环境中还需要处理 `confirm_request`（通过 `confirm` 回答）和 `question_request`（通过 `question` 回答）；在它们得到答复前，agent 会阻塞等待。

## chord-gateway：推荐的 headless 消费方式

如果你想把 Chord 接到聊天表面（飞书、微信等）或搭建多用户 gateway，通常**不需要**自己从零实现 headless 协议。配套项目 [keakon/chord-gateway](https://github.com/keakon/chord-gateway) 已经对其做了封装，并补上了协议刻意留给外层处理的部分：

- 进程生命周期：按 session 拉起 / 重启 `chord headless`，并回收空闲进程。
- 多租户隔离：按用户隔离工作目录、审计日志、限流。
- 聊天平台适配：飞书 / 微信 webhook、消息分段、图片转发。
- 权限交互：把 `confirm_request` / `question_request` 渲染成聊天回复，再映射回 `confirm` / `question` 命令。
- 基于以上 wire format 的重连辅助。

本页描述的是更底层的协议契约，适合那些需要 `chord-gateway` 之外能力的集成方。如果你的目标是“让人能在手机上和 Chord 对话”，优先从 chord-gateway 开始；只有在你有明确理由时，再直接下沉到 headless 协议。

## 适合的用法

- 让外层 gateway 管理进程生命周期。
- 让外层系统决定哪些事件展示给最终用户。
- 在外层实现工作目录、权限、审计、多租户边界控制。

## 它不替代什么

`chord headless` 不是：

- 浏览器应用
- 多租户安全边界
- 完整权限沙箱

更高层的部署方式，见 [chord-gateway](https://github.com/keakon/chord-gateway)。
