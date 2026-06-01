# Hooks

Hooks 让你在 Chord 生命周期的明确节点运行外部命令——工具即将执行、LLM 调用返回、Agent 进入 idle 等。常见用途是通知、审计、自动化门禁、批量操作后的检查。

本页是完整参考。更高层的用法建议见 [扩展与定制](./customization_CN.md)。

## Hook 是怎么跑起来的

某个注册的触发点命中时，Chord 会：

1. **启动配置好的命令**（`shell` 行 或 `argv` 列表二选一）。
2. **从 stdin 发送 JSON envelope**（见 [Envelope](#envelope)）。
3. **设置一组 `CHORD_HOOK_*` 环境变量**（见 [环境变量](#环境变量)）。
4. **把工作目录设为项目根**。
5. **读 hook 的 stdout**，按触发点的类别解析为 sync result、automation result 或纯文本（见下文）。
6. **施加超时**（默认 30 秒，可按 hook 配置）。

stdout 不是合法 JSON 时记录为解析失败；非零退出码记录为执行失败。Hook 失败**永远不会**让 Chord 崩溃。

## Hook 类别

14 个触发点按类别分为三类，决定 hook 能返回什么：

| 类别            | 触发点                                                                          | 行为                                                                                                                |
| --------------- | ------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------- |
| **sync（同步）** | `on_tool_call`、`on_before_llm_call`、`on_before_tool_result_append`            | 同步拦截。stdout 输出 `{"action": "continue\|block\|modify", "message": "...", "data": {...}}`。`block` 终止动作；`modify` 用 `data` 替换下游载荷。 |
| **automation**  | `on_tool_batch_complete`                                                        | 异步任务。stdout 输出 `{"status": "...", "summary": "...", "body": "...", "severity": "...", "append_context": bool, "notify": bool}`。结果可选地拼回上下文。 |
| **observer**    | 其余 10 个（`on_idle`、`on_session_start`、`on_after_llm_call` 等）             | stdout 仅写入日志，无法 block 或 modify。纯副作用。                                                                 |

## 触发点

| 触发点                            | 类别        | 触发时机                                                                              | 常见 `data` 字段                                            |
| --------------------------------- | ----------- | ------------------------------------------------------------------------------------- | ----------------------------------------------------------- |
| `on_session_start`                | observer    | 创建或恢复一个会话                                                                    | session 元数据                                              |
| `on_session_end`                  | observer    | 会话正常关闭                                                                          | session 元数据、统计                                        |
| `on_before_llm_call`              | sync        | 即将给模型发请求                                                                      | `model`、`messages`                                         |
| `on_after_llm_call`               | observer    | 模型响应（含重试）完成后                                                              | `model`、`usage`、`error`（失败时）                         |
| `on_tool_call`                    | sync        | 工具实际执行前                                                                        | `tool_name`、`args`、`timeout_ms`                           |
| `on_tool_result`                  | observer    | 工具返回后                                                                            | `tool_name`、`output`、`error`                              |
| `on_before_tool_result_append`    | sync        | 工具结果即将被追加到上下文（最后一个改/脱敏机会）                                     | `tool_name`、`output`、`error`                              |
| `on_tool_batch_complete`          | automation  | 一轮中多个工具批量完成时（典型场景：编辑批量）                                        | `changed_files`、`tool_calls`                               |
| `on_before_compress`              | observer    | 上下文压缩开始前                                                                      | `reason`、当前 `usage`                                      |
| `on_after_compress`               | observer    | 上下文压缩完成后                                                                      | `reason`、压缩前后的 `usage`                                |
| `on_idle`                         | observer    | Agent 切到 idle（一轮结束，等待用户输入）                                             | `agent_id`                                                  |
| `on_wait_confirm`                 | observer    | 工具需要用户确认（permission 为 `ask`）                                               | `tool_name`、`args`                                         |
| `on_wait_question`                | observer    | 模型反问，等待回答                                                                    | `question`                                                  |
| `on_agent_error`                  | observer    | Agent 报错（LLM 错、工具失败等）                                                      | `error`、`error_kind`                                       |

`data` 内部具体字段会随版本演进。为了保证平稳集成：没用到的字段当作不透明对待，只依赖你真正需要的 key。

## Envelope

每次 hook 都从 stdin 收到这份 JSON：

```json
{
  "point": "on_tool_call",
  "timestamp": "2026-05-08T12:00:00.000Z",
  "session_id": "20260508120000000",
  "turn_id": 7,
  "agent_id": "main",
  "agent_kind": "main",
  "project_root": "/path/to/project",
  "selected_model": "anthropic/claude-opus-4.7",
  "running_model": "anthropic/claude-opus-4.7",
  "data": {
    "tool_name": "Shell",
    "args": { "command": "git status" }
  }
}
```

## 环境变量

除了 stdin，Chord exec 之前会注入下列变量（值为空时不设置）：

| 变量                              | 来源                                                              |
| --------------------------------- | ----------------------------------------------------------------- |
| `CHORD_HOOK_POINT`                | Envelope `point`                                                  |
| `CHORD_HOOK_SESSION_ID`           | Envelope `session_id`                                             |
| `CHORD_HOOK_TURN_ID`              | Envelope `turn_id`                                                |
| `CHORD_HOOK_AGENT_ID`             | Envelope `agent_id`                                               |
| `CHORD_HOOK_AGENT_KIND`           | Envelope `agent_kind`                                             |
| `CHORD_HOOK_PROJECT_ROOT`         | Envelope `project_root`                                           |
| `CHORD_HOOK_SELECTED_MODEL`       | Envelope `selected_model`                                         |
| `CHORD_HOOK_RUNNING_MODEL`        | Envelope `running_model`                                          |
| `CHORD_HOOK_TOOL_NAME`            | 便捷字段：从 `data.tool_name` 提取                                |
| `CHORD_HOOK_TIMEOUT_MS`           | 便捷字段：从 `data.timeout_ms` 提取                               |
| `CHORD_HOOK_ERROR_KIND`           | 便捷字段：从 `data.error_kind` 提取                               |

你在 hook 配置里 `environment:` 下写的所有键值也会原样注入。

## stdout 协议

### Sync hook

```json
{
  "action": "continue",
  "message": "可选的人类可读注释",
  "data": null
}
```

- `continue`（stdout 为空时的默认）— 让动作继续。
- `block` — 终止动作；`message` 显示给用户。
- `modify` — 用 `data` 替换下游的载荷。`data` 的形状须匹配该触发点的原始载荷（如 `on_tool_call` 时 `data` 应是改过的 tool args）。

### Automation hook（`on_tool_batch_complete`）

```json
{
  "status": "success",
  "summary": "linted 12 files, 0 issues",
  "body": "详情...",
  "severity": "info",
  "append_context": false,
  "notify": false
}
```

- `status`：`success` 或 `failed`。
- `severity`：`info`、`warning`、`error`。默认 `info`；`status == failed` 时默认 `error`。
- `append_context: true` 让 Chord 把结果拼进下一次 LLM 调用。
- `notify: true` 把 summary 抛给用户。

### Observer hook

stdout 以纯字符串形式写入日志，没有 schema——想 print 什么就 print 什么，方便排错就行。

## HookDef 字段

```yaml
hooks:
  on_tool_call:
    - name: audit-shell
      command: ["./scripts/audit-shell.sh"]   # 或：shell: "./scripts/audit-shell.sh"
      timeout: 10                             # 秒，默认 30
      tools: ["Shell"]                         # glob 匹配 tool 名
      paths: ["src/**/*.go"]                  # glob 匹配相关路径
      agents: ["main", "reviewer"]            # glob 匹配 agent 名
      agent_kinds: ["main", "subagent"]       # 精确匹配
      models: ["anthropic/*"]                 # glob 匹配 selected/running model
      min_changed_files: 0                    # 至少 N 个文件改动才跑
      only_on_error: false                    # 仅 payload 含错误时跑
      join: background                        # 仅 automation：background | before_next_llm
      result: notify_only                     # 仅 automation：ignore | notify_only | append_on_failure | always_append
      result_format: summary                  # 仅 automation：summary | tail | full
      max_result_lines: 50                    # 仅 automation
      max_result_bytes: 4096                  # 仅 automation
      debounce_ms: 0
      concurrency: ""                         # 串行化 key
      retry_on_failure: 0
      retry_delay_ms: 0
      environment:
        AUDIT_LEVEL: strict                   # 原样注入
```

所有 filter 是 AND 关系：每个非空 filter 都满足时才跑。

## 示例

### 1. idle 时通知（observer）

```yaml
hooks:
  on_idle:
    - name: notify-idle
      command:
        - osascript
        - -e
        - 'display notification "Chord is idle" with title "Chord"'
```

副作用即通知，stdout 被丢弃。

### 2. 拒绝危险 shell 命令（sync）

```yaml
hooks:
  on_tool_call:
    - name: deny-rm-rf
      tools: ["Shell"]
      shell: |
        # 从 stdin 读 envelope，按需 block
        jq -e '.data.args.command | test("^rm -rf|^sudo")' \
          && echo '{"action":"block","message":"已拒绝危险命令"}' \
          || echo '{"action":"continue"}'
```

`jq` 从 stdin 读 envelope；命中正则就输出 `block`，Chord 终止该工具调用。

### 3. 编辑批量后跑 lint（automation）

```yaml
hooks:
  on_tool_batch_complete:
    - name: golangci-lint
      tools: ["Edit", "Write", "Delete"]
      paths: ["**/*.go"]
      min_changed_files: 1
      shell: |
        out=$(golangci-lint run ./... 2>&1) || status=failed
        cat <<JSON
        {
          "status": "${status:-success}",
          "summary": "golangci-lint",
          "body": $(jq -Rs . <<<"$out"),
          "append_context": ${status:+true,$0}false
        }
        JSON
      result: append_on_failure
      result_format: tail
      max_result_lines: 80
      join: before_next_llm
```

lint 失败时把截断后的 tail 拼进下一次 LLM 上下文，让模型据此调整。

### 4. 脱敏工具输出里的 API key（sync, modify）

```yaml
hooks:
  on_before_tool_result_append:
    - name: redact-keys
      tools: ["Shell", "WebFetch", "Read"]
      shell: |
        envelope=$(cat)
        redacted=$(jq '.data.output |= (gsub("sk-[A-Za-z0-9_-]{20,}"; "sk-REDACTED"))' <<<"$envelope")
        echo "{\"action\":\"modify\",\"data\": $(jq '.data' <<<"$redacted")}"
```

## 调试 hook

启动 Chord 前设 `CHORD_HOOK_DEBUG=1`——每次 hook 调用都会记录输入、输出、退出码、耗时。详见 [环境变量](./environment_CN.md#开发与调试)。

Hook 行为反常时：

1. 看 `chord.log` 里 `hook execution status=failed/timed_out`。
2. 拿同样的 envelope 从 stdin 喂给命令手动复现。
3. 检查 stdout 是不是合法 JSON（`echo "$out" | jq .`）。

## 相关

- [扩展与定制](./customization_CN.md)：更高层的食谱
- [配置与认证](./configuration_CN.md)：完整 `config.yaml` schema
- [环境变量](./environment_CN.md)：`CHORD_HOOK_DEBUG`
- [权限与安全](./permissions-and-safety_CN.md)：什么时候用 hook、什么时候用 permission 规则
