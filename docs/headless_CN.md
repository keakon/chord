# Headless mode

`chord headless` 是 Chord 的轻量控制面入口，适合 bot、gateway、自动化脚本接入。

## 它是什么

- 无 TUI
- 通过 stdio 交互
- 输入是 JSON 命令（每行一条）
- 输出是 JSON envelope（每行一条）

适合做外层集成，但**不**自带浏览器前端、多租户隔离、完整权限托管。

> **协议稳定性：** Chord 还在 1.0 之前，headless 协议在版本之间可能变化。请把未知的 envelope 字段和事件类型当作不透明数据处理，固定集成所测试过的 Chord 版本，并在升级前查看[变更记录](https://github.com/keakon/chord/blob/main/CHANGELOG_CN.md)。

## 启动

```bash
chord headless
# 或
go run ./cmd/chord/ headless
```

CLI flag：`-d/--session-dir`、`-c/--continue`、`-r/--resume`、`-w/--worktree`。详见 [CLI：`chord headless`](./cli_CN.md#chord-headless)。

## 协议格式

- **stdin**：每行一条 JSON 命令
- **stdout**：每行一条 JSON envelope。其他诊断输出走 stderr，**不要**把 stderr 当协议解析。

每个出站 envelope 的结构：

```json
{ "type": "<event-type>", "payload": { ... } }
```

携带状态的 envelope（事件循环推送、命令路径上的 `role_change` / `handoff_cancelled` 公告、以及 `status_response` 快照）会多带一个单调递增的 `seq`（`{ "type": "<event-type>", "seq": 12, "payload": { ... } }`）。推送按 `seq` 顺序发出，但 `status_response` 快照是在命令路径上拷贝再发出的，所以它可能比之后更新的推送晚到。任何改了缓存状态的突变都会递增 `seq`，即使网关没订阅对应的推送、或者这次突变根本没有推送（`send` 自动关掉待决 confirm / question，或显式回复 `confirm` / `question` / `handoff`），因此之后的 `status_response` 一定比突变前拷的那份新。把 `status_response` 合并进缓存状态的集成方，必须丢掉 `seq` 比已见 `seq` 更小的快照。每条 `status_response` 都带非零的 `seq`，包括首次推送前的快照。版本号只在当前进程内有效；新进程发出 `ready` 时，清空已记录的最大版本号。

你收到的第一行一定是 `{"type": "ready", ...}`；在它之前不要发送其他命令。

## 先跑通一次交互

配置好模型后，在终端启动 `chord headless`，然后依次操作：

1. 等待 stdout 输出 `ready`，确认控制面已就绪。
2. 在 stdin 输入 `{"type":"status"}` 并回车，确认收到 `status_response`。
3. 输入 `{"type":"send","content":"Explain this project without changing files."}` 并回车，发送一个只读任务。
4. 查看返回事件。遇到确认、问题或交接请求时，按下方对应命令回复；不要把“正在等你回答”误判为任务卡住。

实际集成中，保持进程 stdin 打开，逐行读取 stdout，并单独收集 stderr。无需先实现事件过滤：未发送 `subscribe` 时会收到所有可订阅事件。

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

可订阅事件类型：`activity`、`assistant_message`、`idle`、`confirm_request`、`question_request`、`notification`、`handoff_request`、`handoff_cancelled`、`role_change`、`error`、`agent_started`、`agent_notify`、`agent_done`、`info`、`toast`、`done_completion`、`local_shell_result`、`assistant_rollback`、`todos`、`compaction_status`、`session_switched`、`background_result`、`context_notice`。

### `status`

请求当前后端状态快照。

```json
{"type": "status"}
```

响应：

```json
{
  "type": "status_response",
  "seq": 1,
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
    "current_role": "builder",
    "updated_at": "2026-05-08T12:00:00Z"
  }
}
```

`session_id` 跟的是当前实际会话，不是启动时的快照。进程不重启、直接换会话时（执行 handoff plan、`/resume <id>`、`/new`），Chord 会更新这个跟踪值，并用一条显式的 `session_switched` 推送告诉订阅方；仅凭缓存值变化不能视为网关已看到新会话。会话没换的恢复（启动回放、持久压缩重写）只刷新时间戳，不推送。即使没订阅 `session_switched`，跟踪值照样会更新，所以 `status_response` 永远报实际运行的那个会话。

### `send`

向 agent 发送用户消息。slash 命令的行为与 TUI 一致；裸 `/models` 会被当作 `/models status`，因为 headless 没有 TUI overlay。

```json
{"type": "send", "content": "请总结一下项目结构。"}
```

如果当前有待处理的 `confirm_request`、`question_request` 或 `handoff_request`，而用户发送了普通消息（不是下面的 `confirm`、`question` 或 `handoff`），Chord 会先自动关闭该待处理交互，再消费这条新消息。待决的 `confirm_request` 会按空理由自动拒绝，待决的 `question_request` 会自动取消；这两类关闭没有专门的取消事件，看下一次 `status_response` 里 `pending_confirm` / `pending_question` 已清空就知道不用再等。被关闭的交互不会在下一次 `status_response` 中继续显示为待决；如果被关闭的是 `handoff_request`，Chord 还会向订阅了 `handoff_cancelled` 的客户端推送该事件，和 [`handoff`](#handoff) 一节里 runtime 主动取消的路径一致。

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

### `role`

查询或切换当前主角色——TUI Shift+Tab 的远程等价。`list` 返回当前角色与有序的主模式角色列表（builder 恒第一、planner 若配置则第二、自定义角色按字母序）；`set` 切换角色并保留会话上下文，与 TUI 循环一致。

```json
{"type": "role", "action": "list"}
```

```json
{"type": "role", "action": "set", "role": "planner"}
```

响应：

```json
{
  "type": "role_response",
  "payload": {
    "ok": true,
    "role": "planner",
    "roles": [
      {"name": "builder", "current": false},
      {"name": "planner", "current": true}
    ]
  }
}
```

`list` 把当前角色放在 `role`，完整列表放在 `roles`，其中 `current: true` 的那一项就是当前角色。`set` 切换到指定角色并返回切换后的状态，切换立即生效——即使还有回合在跑也一样。有 `handoff_request` 待决时 `set` 会被拒绝（`resolve the pending handoff before switching role`），切到已是当前的角色（`already the active role: <name>`）、未知角色名、只作为 SubAgent 定义存在的角色同样会被拒绝。失败响应带 `ok: false` 和面向人的 `message`，原样展示给用户即可。订阅了 `role_change` 时，切换成功还会收到一条 `role_change` 推送；当前角色也会出现在 `status_response` 的 `current_role` 里。注意：`role_change` 事件与 `role_response` 由不同路径写出、先后顺序不定，客户端应以 `role_response` 为准。

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
  "rule_pattern": "shell:^git status$",
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

### `handoff`

处理一个待决的 `handoff_request`。批准会用选定 agent 执行已保存的 plan；拒绝会把拒绝原因追加到对话上下文，并让 planner 基于该上下文继续。

```json
{"type": "handoff", "request_id": "handoff-…", "action": "accept", "agent": "builder", "pool": "thinking"}
```

```json
{"type": "handoff", "request_id": "handoff-…", "action": "deny", "deny_reason": "请先补充发布步骤。"}
```

`action` 可用 `accept` / `allow`（或空 action）表示批准，`deny` / `reject` 表示带原因拒绝；`cancel` 关闭待决 handoff，不执行 plan，也不追加拒绝消息。`agent` 默认使用请求里的默认 agent；可选的 `pool` 会在执行前切换该 agent 的模型池。

待决 handoff 依附于发起它的回合与会话。只要 Chord 在没有 client 决策的情况下丢弃它，即会话切换、更新的回合开始，或 `send` 新消息时自动关闭（见 [`send`](#send)），都会向订阅了 `handoff_cancelled` 的客户端推送该事件；随后 `status_response.pending_handoff` 为 `null`，集成方据此停止等待，而不是继续展示一个 agent 早已放弃的审批提示。

### `local_shell`

从 headless client 侧执行本地 shell 命令，并收到一个 `local_shell_result` 事件。该命令主要用于 gateway 暴露 `!` 风格本地命令的场景。

```json
{"type": "local_shell", "command": "git status --short"}
```

也可以用 `content` 字段作为后备命令内容。输出会合并 stdout 和 stderr，并带有输出上限和超时。

> 安全说明：`local_shell` 会在 `chord headless` 进程环境中执行 `bash -c`。它是直接的本地命令执行协议能力，不是模型工具请求，也不能替代沙箱。任何向用户暴露该能力的 gateway 都必须自行实现认证、授权、审计、命令过滤和租户隔离。

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
| `role_response`      | 响应 `role`                                  | `ok`、`message`、`role`、`roles[]`（元素含 `name`、`current`） |
| `error`              | 命令解析或执行错误                           | `message`，可选 `code`（例如 `stdin_line_too_long`） |

### 可订阅推送事件

| 类型                 | 何时出现                                     | 主要 payload 字段 |
| -------------------- | -------------------------------------------- | ----------------- |
| `activity`           | Agent 进入新阶段                             | `agent_id`、`type`（如 `connecting`、`streaming`、`compacting`） 、`detail` |
| `assistant_message`  | 一条完整 assistant 消息可供消费              | `agent_id`、`task_id`、`agent_type`、`parent_agent_id`、`text`、`tool_calls`；main agent 的委托字段为空 |
| `idle`               | 主 agent 与所有 SubAgent 均已全局静默，可再次接收输入 | `last_outcome`（`completed` / `cancelled` / `error`）、`suppress_user_notification`（除非 agent 在上一次 idle 事件后运行过，否则为 `true`） |
| `done_completion`   | Done 工具完成并给出最终报告。只在 loop 运行期间产生——`done` 仅在此时挂载；`mode` 字段目前恒为 `normal` | `call_id`、`report`、`reason`、`status`、`agent_id`、`mode` |
| `confirm_request`    | 某个工具需要显式确认                         | `request_id`、`agent_id`、`tool_name`、`args_json`、`needs_approval`、`already_allowed`、`needs_approval_rules`、`already_allowed_rules`、`timeout_ms` |
| `question_request`   | 模型向用户提问                               | `request_id`、`agent_id`、`tool_name`、`question`、`options`、`option_details`、`default_answer`、`multiple`、`timeout_ms` |
| `notification`       | agent 需要用户注意，但等待点不是标准 modal 请求 | `reason`、`message` |
| `handoff_request`    | planner 已保存 handoff plan，需要 client 批准或拒绝执行 | `request_id`、`plan_path`、`plan_text`、`plan_error`、`agents[]`，元素包含 `{name, default, model_pools, current_model_pool}`；没有合法目标时 `agents` 为空列表 |
| `handoff_cancelled`  | 待决 handoff 在 client 决策前被丢弃——更新的回合、会话切换或 `send` 自动关闭接管了它 | `request_id`、`reason`（`superseded`） |
| `role_change`        | 当前主角色已切换（经 TUI Shift+Tab 或 `role set` 命令） | `role` |
| `local_shell_result` | `local_shell` 命令的执行结果                 | `command`、`output`、`failed`、`error` |
| `agent_started`      | 某个委托的 SubAgent runtime 开始运行（包括 parked task 的按需 rehydrate） | `agent_id`、`previous_agent_id`（rehydrate 时存在）、`task_id`、`agent_type`、`description`、`parent_agent_id`、`parent_task_id` |
| `agent_notify`       | 某个 agent 向 owner 或指定委派工作流发送非阻塞更新 | `agent_id`、`task_id`、`agent_type`、`parent_agent_id`、`parent_task_id`、`target_agent_id`、`target_task_id`、`kind`、`subtype`、`message` |
| `agent_done`         | 某个 SubAgent 完成任务                       | `agent_id`、`task_id`、`agent_type`、`parent_agent_id`、`parent_task_id`、`summary` |
| `assistant_rollback` | 丢弃尚未提交的流式 assistant 输出            | `agent_id`、`reason` |
| `info`               | 运行时信息消息                               | `agent_id`、`message` |
| `toast`              | TUI 中的瞬时通知；headless 可以忽略          | `agent_id`、`message`、`level`（`info` / `warn` / `error`） |
| `todos`              | 替换当前 todo 列表                           | `todos[]`，元素结构为 `{id, content, status, active_form}`；启用 `todo_write` 时，多个独立且正在处理的工作流可以同时为 `in_progress`，但必须使用唯一的 `active_form`。 |
| `compaction_status`  | 压缩生命周期事件：`started` 与终态（`succeeded`、`skipped`、`failed`、`cancelled`） | `status`、`trigger`（`manual`、`usage_driven`、`length_recovery`、`oversize_driven`、`model_driven`、`model_downshift`）、`reason`、`plan_id`（有界压缩计划标识，用于把终态与产生它的具体计划关联）。进度类遥测不转发。 |
| `session_switched`   | 当前会话换了，但进程没重启（执行 handoff plan、`/resume <id>`、`/new`） | `session_id`（换完之后的新会话） |
| `background_result`  | 后台任务结束后的持久结果；JOB RESULT 卡片唯一的推送通道——它通常在回合 `idle` 之后才落盘，后面不会再有 `assistant_message` 总结它 | `target_agent_id`、`message_index`、`content` |
| `context_notice`     | 持久的上下文压力提醒，headless 没有别的通道能收到它 | `level`、`message`、`message_index` |
| `error`              | 运行时错误                                   | `agent_id`、`message`，可选 `code` |

如果 stdin 上的单行输入超过协议行长度限制，Chord 会输出带 `code: "stdin_line_too_long"` 的 `error` envelope，并继续读取后续行。集成方应在存在 `code` 时用它做错误分类，把 `message` 作为面向人的诊断信息。

静默重试不会推送。TUI 只记在错误面板里的那次重试不会产生 `error` envelope，也不会动 `last_error` / `last_outcome`（`status_response` 与 `idle` 里看到的）——中途重试一次、最后恢复成功的回合，`idle` 里看到的仍然是 `completed`。真正失败时总会跟一条非静默错误，集成方只管看那一条。

纯工具调用轮次（包括 SubAgent 调用 `Complete`）的 `assistant_message.text` 可能为空。Chord 会记 warning 便于观测；gateway 集成应跳过空消息，并以 `agent_done.summary` 作为权威的 SubAgent 完成内容。

进入静止状态的 SubAgent 可能释放 live runtime，但 task 与 transcript 会持久保留。后续获授权的定向通知可用新的 `agent_id` rehydrate 该任务；集成方应使用稳定的 `task_id` 路由，并根据 `agent_started.previous_agent_id` 替换 runtime 级标签。

`idle` 是全局静默信号，不是单次请求完成信号。只要任一 agent 仍在运行、内部事件或需要处理的 mailbox 消息仍在排队、Handoff 决策还没完成，或某个 SubAgent 还有等待下一请求消费的输入，Chord 就不会发出 `idle`。目标 busy 时，排队消息会在下一个请求边界处理；可恢复但未运行的目标会先被唤醒。发往主 inbox 的 progress / notice 快照是可处理的工作，不是纯信息：主代理空闲时，Chord 会把待投递的更新合并成一批、按到达顺序投递，在发出全局 idle 之前先唤醒主代理投递完这批——每来一批都会多一次主回合与 LLM 请求，idle 也要等这批投递收尾才发出。`suppress_user_notification` 不改变 idle 状态收口，只告诉面向用户的集成：当这次静默之前并没有真实的 agent 工作（例如启动、会话 / model pool / MCP 切换或其它用户主动导航）时，不要发出通用完成提醒。除非 agent 在上一次 idle 事件之后确实运行过（主回合、loop 执行或活跃的 SubAgent 工作），否则该字段为 `true`。`notification` 则用于 runtime 明确等待用户输入的提醒；当前 `reason="user_input_required"` 覆盖权限、Question、Handoff 和 loop 决策。

## 通过 `send` 兼容 slash 命令

为方便接入只有单一文本输入的聊天表面，headless 也支持通过 `send` 发送这些 slash 命令：

- `/models status`、`/models <pool>`、`/models --agent <name> <pool>`
- `/role status`、`/role <name>`：查询或切换当前主角色（与 `role` 协议命令同一操作）
- `/help`、`/stats`、`/compact`、`/loop on`、`/loop off`（仅当当前 MainAgent 角色可使用 `done` 工具时）

裸 `/models` 会被当作 `/models status`，裸 `/role` 会被当作 `/role status`。`/new` 和 `/resume <id>` 在 headless 里可用，会在进程内换会话（并推送 `session_switched`）。裸 `/resume` 仍然需要 TUI 的选择器，`/export` 这类命令也是；这些会返回 `error` envelope，说明「X 仅在本地 TUI 模式可用」。

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

生产环境中还需要处理 `confirm_request`（通过 `confirm` 回答）、`question_request`（通过 `question` 回答）和 `handoff_request`（通过 `handoff` 回答）；在它们得到答复前，agent 会阻塞等待。

## chord-gateway：推荐的 headless 消费方式

如果你想把 Chord 接到聊天表面（飞书、微信等）或搭建多用户 gateway，通常**不需要**自己从零实现 headless 协议。配套项目 [keakon/chord-gateway](https://github.com/keakon/chord-gateway) 已经对其做了封装，并补上了协议刻意留给外层处理的部分：

- 进程生命周期：按 session 拉起 / 重启 `chord headless`，并回收空闲进程。
- 多租户隔离：按用户隔离工作目录、审计日志、限流。
- 聊天平台适配：飞书 / 微信 webhook、消息分段、图片转发。
- 权限交互：把 `confirm_request` / `question_request` 渲染成聊天回复，再映射回 `confirm` / `question` 命令。
- 基于以上 wire format 的重连辅助。

本页描述的是更底层的协议契约，适合那些需要 `chord-gateway` 之外能力的集成方。如果你的目标是「让人能在手机上和 Chord 对话」，优先从 chord-gateway 开始；只有在你有明确理由时，再直接下沉到 headless 协议。

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

## 相关

- [使用指南](./usage_CN.md)
- [CLI：chord headless](./cli_CN.md#chord-headless)
- [权限与安全](./permissions-and-safety_CN.md)
- [常见问题排查](./troubleshooting_CN.md)
