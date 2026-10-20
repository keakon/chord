# 外部 coding agent 子 Agent

只有子 Agent 可以使用外部 coding agent；主 Agent 必须使用 Chord 内部运行时。任务记录、邮箱、权限决策和完成信封仍由 Chord 负责；外部进程负责模型调用与工具执行，Chord 同时保存正文和工具记录。

当前支持 Claude Code 的 `stream-json` 模式。

## 准备工作

- 安装 Claude Code CLI，并完成登录或在 `backend.env` 中提供 API 凭据。
- 需要 Unix 系统；外部进程在子 Agent 的工作目录中运行，Chord 管理其进程组和退出。
- 受管传输面向 Claude Code 的 `stream-json` 协议本身，不锁定具体版本，也不在启动前校验或拦截版本。

## 配置

在 `~/.config/chord/agents/` 中创建 `mode: subagent` 的 Agent 文件，加入 `backend` 块；项目级定义放在 `.chord/agents/`，会覆盖同名全局定义。

```markdown
---
name: claude-reviewer
description: 在外部 Claude Code 会话里执行审查。
mode: subagent
backend:
  kind: claude-code
  command: claude
  launch_args:
    - --model
    - <model>
permission:
  "*": deny
  read: allow
  grep: allow
  glob: allow
  question: allow
  shell:
    "*": ask
    "git status --short --branch": allow
    "git diff --stat": allow
---

你是审查子 Agent。用文件路径和行号报告问题。
```

将 `<model>` 替换为所需模型，也可省略 `--model`。Markdown 正文与 `prompt_append` 通过初始化协议追加为系统指令，保留 Claude 默认提示词；初始化成功后才提交任务。

### 字段说明

| 字段 | 作用 |
| --- | --- |
| `kind` | 后端适配器，目前只有 `claude-code`。 |
| `command` | CLI 可执行文件，默认 `claude`，按 `PATH` 解析。 |
| `launch_args` | 已支持的普通参数，数组传递，不经过 shell。 |
| `env` | 认证、模型和网络环境变量，值支持从 Chord 进程环境做 `${VAR}` 展开，不保存展开后的凭据。 |

外部 backend 不能声明 `model_pools`、`mcp`、`prompt_preset`、`variant`、`temperature` 或 `max_tokens`。

### 保留参数

协议、会话标识和权限入口由 Chord 管理：

```text
-p --output-format stream-json --verbose --input-format stream-json
[--session-id <uuid> | --resume <uuid>] --permission-prompt-tool stdio
--permission-mode default --safe-mode --restricted
--setting-sources "" --strict-mcp-config --mcp-config '{"mcpServers":{}}'
```

`launch_args` 支持 `--model`、`--effort`、`--max-turns`、`--max-budget-usd`、`--include-partial-messages`、`--replay-user-messages`，以及追加禁用工具的 `--disallowedTools` / `--disallowed-tools`。长选项可以使用 `--flag=value`。

覆盖协议、系统提示词、权限模式或会话选择的参数会被拒绝。`--allowedTools`、权限绕过、位置参数、`--add-dir`、CLI 自建 worktree、后台入口、设置源、插件和 MCP 参数都不支持；未分类参数同样报错。

用户追加的禁用工具与角色派生的禁用集合合并，不能解除角色限制。环境变量覆盖仅支持 Anthropic 凭据、模型与地址、Claude OAuth token、HTTP 代理和 CA 证书配置；运行期注入变量会被拒绝。

## 权限

每条 `can_use_tool` 请求都会翻译成 Chord 的工具名和参数，再用 worker 角色的 ruleset 判断：

- `allow`：Chord 直接应答，不打扰用户。
- `deny`：Chord 拒绝执行并保存工具终态。
- `ask`：弹出绑定到该子 Agent 的确认框。超时、取消或持久化失败不批准执行；编辑参数需要外部 Agent 重新发起调用。

受管工具集合包括 Read、Edit、Write、NotebookEdit、Bash、Glob、Grep、WebFetch、WebSearch、TodoWrite、Agent、AskUserQuestion 和 ExitPlanMode。没有 Chord 映射的工具需要使用其原名声明明确角色规则，通配 `allow` 不会放行它们。`AskUserQuestion` 使用 Chord 问题界面并回填答案；`ExitPlanMode` 始终请求确认，不切换 Chord 主角色。

Claude 自身仍可先拒绝工具。当前受管模式下 TodoWrite 不可用，ExitPlanMode 因不在计划模式而拒绝；Chord 不会绕过这些限制。

Chord 禁用项目和用户自定义、hooks、外部 MCP、插件及 sandbox 自动批准。外部命令和管理员策略仍是受信任边界，角色权限不是操作系统沙箱。shell 规则采用最后匹配优先；宽泛的 `git *` 不等于只读权限，`go test` 也会执行仓库代码。

## 额度与凭据

| 路线 | 要做 | 不要做 |
| --- | --- | --- |
| CLI 登录（`claude auth login`） | 保留正常 HOME 和登录凭据。 | 用空的 API key 覆盖登录；改写 HOME。 |
| API key | 为目标 Console 组织设置 `ANTHROPIC_API_KEY`。 | 在 Agent 文件里写明文 key；把 Chord 参数当作额度选择器。 |

凭据资格、可用额度和计费规则以对应账户当前政策为准。Chord 不提供额度，也不保证不同凭据路线可以叠加；请在 CLI 与账户控制台确认实际认证和用量。

## 常用配方

只读审查：

```yaml
backend:
  launch_args:
    - --disallowedTools
    - Edit,Write
permission:
  "*": deny
  read: allow
  grep: allow
  shell:
    "*": ask
    "git status --short --branch": allow
    "git diff --stat": allow
```

限制批量运行：

```yaml
backend:
  launch_args:
    - --max-turns
    - "30"
    - --max-budget-usd
    - "5"
```

## 与原生子 Agent 的差异

- 外部模型调用不经过 Chord 模型池和 key rotation，也不计入 Chord 用量统计。
- 取消时，Chord 发送 `interrupt` 并关闭输入流，给 CLI 短暂时间写完转录并退出；仍未退出才终止进程组。已发生的修改不会撤销，缺少回执的工具显示结果未知。
- Chord 被强制终止后，外部进程组可能继续运行。重新委派前，应检查并停止残留的 CLI 和子进程；恢复检查不会清理孤儿进程。
- 不支持暂停。需要停下时直接停止任务再重开。
- 每次只执行一个输入。运行中收到的消息持久化排队；封口后的消息等旧进程退出并结算后，在同一任务的新 attempt 中处理。完成后的定向消息也会恢复会话；取消的任务必须重新委派。
- Hooks、Skills 和上下文压缩不作用于外部进程；结果契约会作用：声明了 `result_schema` 时，该契约随角色指令在初始化阶段一并送给外部进程，最终答复必须是满足它的单个 JSON 对象。答复不是 JSON 对象，或缺少 schema 要求的字段时，任务带着违规项失败。
- 删除 Chord 会话不会删除用户 HOME 下的外部会话转录；Chord 会列出因此留下的 session id。
- 外部子 Agent 只接受文本消息，不接受图片或 PDF。
- 恢复要求外部会话仍存在、已证明旧进程退出、没有结果未知的输入，且命令、参数、环境变量键、角色指令及工作目录与保存的绑定一致。凭据从当前环境重新取得。崩溃后未证明进程退出，或输入结果未知时，Chord 拒绝自动重放。
- Chord 的工作区租约只对 Chord 自己执行的工具做串行化。外部后端的工具在 CLI 进程内执行，因此外部 worker 与其他写入者之间没有串行化保证；可能重叠的工作请使用独立检出。
- 完成信封给出检出路径、分支和基线 commit，并在启动时工作区干净的情况下附带相对该基线的 diff 统计。Chord 不追踪外部 Agent 改了哪些文件：工作区是共享的，其他进程也可能写入，请自行对比报告里的基线 commit。
