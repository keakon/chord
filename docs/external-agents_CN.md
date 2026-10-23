# 外部 coding agent 子 Agent

只有子 Agent 可以使用外部 coding agent；主 Agent 必须使用 Chord 内部运行时。任务记录、邮箱、权限决策和完成信封仍由 Chord 负责；外部进程负责模型调用与工具执行，Chord 同时保存正文和工具记录。

当前支持 Claude Code 的 `stream-json` 模式。

## 准备工作

- 安装 Claude Code CLI，并完成登录或在 `backend.env` 中提供 API 凭据。
- 支持 Linux 和 macOS。外部进程在子 Agent 的工作目录中运行，每个 worker 的进程组由独立监督进程持有，Chord 只保留业务管道。其他 Unix 系统缺少崩溃恢复回执所需的进程启动标识。
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
--tools <受管工具> --settings <受管设置 JSON>
```

`launch_args` 支持 `--model`、`--effort`、`--max-turns`、`--max-budget-usd`、`--include-partial-messages`、`--replay-user-messages`，以及追加禁用工具的 `--disallowedTools` / `--disallowed-tools`。长选项可以使用 `--flag=value`。

覆盖协议、系统提示词、权限模式或会话选择的参数会被拒绝。`--allowedTools`、权限绕过、位置参数、`--add-dir`、CLI 自建 worktree、后台入口、设置源、插件和 MCP 参数都不支持；未分类参数同样报错。

用户追加的禁用工具与角色派生的禁用集合合并，不能解除角色限制。环境变量覆盖仅支持 Anthropic 凭据、模型与地址、Claude OAuth token、HTTP 代理和 CA 证书配置；运行期注入变量会被拒绝。

## 权限

每条 `can_use_tool` 请求都会翻译成 Chord 的工具名和参数，再用 worker 角色的 ruleset 判断：

- `allow`：Chord 直接应答，不打扰用户。
- `deny`：Chord 拒绝执行并保存工具终态。
- `ask`：弹出绑定到该子 Agent 的确认框。超时、取消或持久化失败不批准执行；五分钟内无人回答按拒绝处理；编辑参数需要外部 Agent 重新发起调用。

路径规则适用于文件工具，包括 NotebookEdit 的 `notebook_path`。路径模式要嵌套在工具名下面；顶层键始终匹配工具名，不匹配参数。例如：

```yaml
permission:
  "*": deny
  NotebookEdit:
    "notes/*.ipynb": allow
```

上面的规则只放行 `notes/` 下的 notebook，与 Edit 的同路径规则效果一致。`question` 规则若判定为 `ask`，按 `allow` 处理：问题界面本身就是确认。

受管工具集合包括 Read、Edit、Write、NotebookEdit、Bash、Glob、Grep、WebFetch、WebSearch、TodoWrite、Agent、AskUserQuestion 和 ExitPlanMode。Claude 的 `Task` 写法按 `Agent` 的别名处理。没有 Chord 映射的工具需要使用其原名声明明确角色规则，通配 `allow` 不会放行它们。`AskUserQuestion` 使用 Chord 问题界面，每次最多回答四个问题并回填给 Claude。`ExitPlanMode` 只有在角色规则把它判定为 allow 或 ask 时才会请求用户确认，且不切换 Chord 主角色；规则判定为 deny，或 allowlist 里没有点名它的规则时，该工具会被直接隐藏。在 `default` 权限模式下，Claude 可能在请求宿主授权之前就按不可用拒绝它。

角色规则判定为禁用的工具会通过 `--disallowedTools` 传给 CLI，不会出现在 Claude 自己的工具列表里；保留下来的调用仍逐次经过确认。判定沿用通用的规则匹配顺序：当最后一条匹配该工具名的规则是通配 deny 时才隐藏该工具，因此带具体路径 pattern 的 deny 规则不会隐藏它。所以通配 `deny` 兜底要写在具体放行规则之前，正如上面的示例。

Claude 自身也可能先于宿主授权拒绝工具——某些工具在 CLI 自己的受管模式下不可用。Chord 不会绕过这些限制。

Chord 禁用项目和用户自定义、hooks、外部 MCP、插件及 sandbox 自动批准。逐调用授权依赖 CLI 在执行前请求 `can_use_tool`；事后发现未授权结果，不能证明没有发生副作用。外部命令和管理员策略仍是受信任边界，角色权限不是操作系统沙箱。shell 规则采用最后匹配优先；宽泛的 `git *` 不等于只读权限，`go test` 也会执行仓库代码。

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

- 外部模型调用不经过 Chord 模型池和 key rotation，也不计入 Chord 用量统计。查看该 worker 时，信息面板用 BACKEND 区显示后端类型（例如 `claude-code`）代替 MODEL 区，并隐藏 USAGE、TIME、SKILLS、LSP、MCP 区——后两者描述的是 Chord 自己的集成，外部 CLI 有自己的一套；状态栏省略模型、档位与用量胶囊；`/models` 会说明模型池不适用于外部 Agent。侧栏会在该 worker 的名字后加 `[external]` 后缀。
- 停止时，Chord 发送 `interrupt` 并关闭输入流，给 CLI 短暂时间写完转录并退出；仍未退出才终止进程组。已发生的修改不会撤销，缺少回执的工具显示结果未知。
- 停止预算耗尽后仍无法证明进程组清理完成时，任务以失败收口并释放运行槽位，但仍拒绝恢复。运行时上限统计 Chord 准入的 worker，不包括可能残留的进程。
- 每个外部 worker 都有一个独立监督进程持有其进程组。Chord 被强制终止后，监督进程发现生命通道断开即清理该进程组，并在状态目录的 `external-supervisor/` 下留一条以运行 ID 命名的生命周期回执。监督进程为每次运行写回执，包括 Chord 自己收尾并确认清理的运行；读到退出报告并不代表 Chord 已把它持久化。超过 30 天的回执会被清理。Chord 与监督进程同时被终止时仍无法保证清理，回执可能缺失，也可能来自一次尚未证明清理结束的运行。恢复不会清理孤儿进程，只接受该任务最后一次启动那一次运行的回执。
- 退出 Chord 时外部 worker 仍在运行的话，Chord 只短暂等待停止序列，剩余拆除由监督进程在 Chord 退出后完成。下次启动时，尚未结算的任务记为被会话退出中断，已记录的终态结果保持不变。
- 不支持暂停。需要停下时直接停止任务再重开。
- 切换会话、开始执行计划或接管暂停会话时，Chord 先停止仍在运行的外部 worker，等它结算后再切换；旧任务记为已取消，不会在新会话里自动恢复。
- 每次只执行一个输入。运行中收到的消息持久化排队；封口后的消息等旧进程退出并结算后，在同一任务的新 attempt 中处理。完成或失败后的定向消息都会恢复会话；取消的任务必须重新委派。
- Hooks、Skills 和上下文压缩不作用于外部进程；结果契约会作用：声明了 `result_schema` 时，该契约随角色指令在初始化阶段一并送给外部进程，最终答复必须是满足它的单个 JSON 对象。答复不是 JSON 对象，或缺少 schema 要求的字段时，任务带着违规项失败。
- 删除 Chord 会话不会删除用户 HOME 下的外部会话转录；Chord 会列出因此留下的 session id。
- 外部子 Agent 只接受文本消息，不接受图片或 PDF。
- 恢复要求外部会话仍存在、已证明旧进程退出、没有结果未知的输入，且命令、参数、环境变量键、角色指令不变，工作目录仍存在且与保存的绑定一致。凭据从当前环境重新取得。崩溃后，任务在每次启动前先持久化监督进程的运行 ID；恢复时按该 ID 精确读取回执，只有回执记录了已证明的进程组清理才允许继续。回执缺失、损坏、版本不符或未证明清理时一律拒绝恢复。
- Chord 的工作区租约只对 Chord 自己执行的工具做串行化。外部后端的工具在 CLI 进程内执行，因此外部 worker 与其他写入者之间没有串行化保证；可能重叠的工作请使用独立检出。
- 完成信封给出检出路径、分支和基线 commit，并在启动时工作区干净的情况下附带相对该基线的 diff 统计。启动时工作区已脏或 Chord 无法检查时，信封会说明原因并且不附 diff 统计，不会把未知状态当成干净。Chord 不追踪外部 Agent 改了哪些文件：工作区是共享的，其他进程也可能写入，请自行对比报告里的基线 commit。
