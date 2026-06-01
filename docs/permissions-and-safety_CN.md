# 权限与安全

Chord 是一个可读取文件、修改文件、执行命令并调用外部工具的 coding agent。公开使用前，应先明确其权限模型和安全边界。

## 核心原则

- 默认把高风险能力设为 `ask`
- 对明显危险或不需要的能力使用 `deny`
- 仅对低风险、可预期的动作使用 `allow`
- 把 API keys 放在 `auth.yaml` 或环境变量里，不要写进项目文件

## 权限模型

常见权限状态：

- `allow`：自动允许
- `ask`：执行前要求确认
- `deny`：直接拒绝

在 TUI 确认框中，`A` 用于打开当前工具调用的“添加规则”选择界面；进入该界面后，再按 `Enter` 才会保存所选规则并允许这次调用。

权限可在 Agent 配置中定义。推荐从下面这套个人开发模板开始，再按项目风险收紧或放宽：

```yaml
permission:
  "*": allow
  Handoff: deny
  Delegate: deny
  Delete: ask
  WebFetch:
    "http://localhost:8000/*": ask
    "http://169.254.169.254/*": deny
  Shell:
    "sudo *": ask
    "rm *": ask
    "rmdir *": ask
    "mv *": ask
    "git add *": ask
    "git checkout *": ask
    "git clean *": ask
    "git commit *": ask
    "git push *": ask
    "git reset *": ask
    "git restore *": ask
    "git tag *": ask
```

这套配置的含义：默认允许大多数工具；禁用 `Handoff` 与 `Delegate`；删除文件、选定的 WebFetch URL pattern、以及常见高风险 shell/git 命令需要确认。权限规则按「最后匹配优先」生效，因此 `WebFetch` 和 `Shell` 下更具体的规则会覆盖顶层 `"*": allow`。适合单人、可信工作区；共享仓库、团队服务或自动化 headless 部署应进一步收紧。

### 特殊权限语义

大多数工具都按上面的 `allow` / `ask` / `deny` 字面含义执行，但少数编排工具有意带有额外联动，使权限设置与 Chord 能安全运行的工作流保持一致：

- `Handoff` 和 `Done` 会被当作控制 gate。设为 `deny` 会隐藏或禁用对应工作流；设为 `allow` 或 `ask` 都会让工作流可用，真正交接 / 完成时 Chord 仍可能显示本地确认（例如 loop 的 `Done` 确认）。也就是说，`ask` 不是这两个工具的“更强工作流模式”，它主要表示工具保持可见 / 可用，同时保留 Chord 内建确认 gate。这个取舍可以避免模型看到一个可用控制工具却最终无法完成，同时仍防止静默切换角色或过早退出 loop。
- `Delegate` 控制的是一组委派工作流。如果 `Delegate` 为 `deny`，Chord 也会禁用通过 `Cancel` 取消 SubAgent、从 SubAgent 中隐藏嵌套的 `Delegate` / `Cancel`，并把 SubAgent 的 `Notify` 限制为只通知自己的 owner，而不是任意指定目标。原因是取消或定向通知其他委派任务本身属于管理 delegated workstreams；如果禁用了 `Delegate` 却允许这些片段，会形成一个不完整但仍可干扰委派工作的控制面。
- 因此 `Cancel` 依赖 `Delegate`：即使配置了 `Cancel: allow`，只要 `Delegate` 被禁用，`Cancel` 仍会被拒绝。若希望某个角色能取消委派工作，需要同时启用 `Delegate` 和 `Cancel`。
- `Question: ask` 会被归一化为 `allow`。`Question` 工具本身就是向用户提出结构化问题并等待回答；如果在提问前再加一次权限确认，只会产生重复弹窗，并不能降低最终决策风险。
- YOLO 不会绕过 `Handoff`、`Delegate`、`Cancel` 或 `Done`；即使普通文件 / shell / web 权限被绕过，这些控制工具的权限仍会执行。YOLO 下，宽泛的 `"*": allow` 规则本身不会授予这些受保护工具；如果角色需要使用它们，请分别直接配置对应工具权限。

> 权限属于 Agent 级配置，不是简单的全局开关。

对于 `Shell`，像 `"git *": allow` 这样的具体 `allow` pattern 不会自动放行包含未引用 shell 分隔符（`;`、`&&`、`||`、`|`、`&` 或换行）的复合命令。这类调用会继续匹配后续规则，通常回到 `ask` 或 `deny`。这只是安全兜底，不是 shell 沙箱；`Shell: allow` 或 `Shell: { "*": allow }` 这类宽泛规则只应给完全可信的角色使用。

## Shell 与 shell 风险

`Shell` 能执行系统命令，应格外谨慎。`Shell` 和 `Spawn` 都是刻意设计的非交互工具：Chord 不会把模型可控的 stdin 接入子进程；Unix 子进程会在没有 controlling TTY 的环境中运行；高置信的交互式命令会在执行前被拒绝。普通 stdin 读取（如 shell `read`/`select`）会看到 EOF，而不是等待模型输入；如果命令需要输入，请通过 pipe、here-doc、文件或参数显式提供。登录向导、终端编辑器、pager / 全屏 TUI、密码提示、以及需要 `/dev/tty` 的命令，应在真实终端中手动执行，或改写为显式提供输入/参数的非交互命令。

`Shell` / `Spawn` 的平台说明：

- 在 Unix 上，Chord 会把子进程放到新的 session 中，并在超时/取消时按进程组清理。
- 在 Windows 上，Chord 仍然保持 `Shell` / `Spawn` 非交互，但这里没有与 Unix `setsid` / 进程组控制完全等价的路径；超时/取消时会退回到直接终止进程，对后代进程的清理可能不如 Unix 完整。

常见改写方式：

- 用 `git commit -m "message"` 或 `git commit -F file` 代替会打开编辑器的 `git commit`
- amend 时如果要保留现有提交信息，使用明确不会打开编辑器的形式，如 `git commit --amend --no-edit` 或 `git commit --amend -C HEAD`
- 避免在 `Shell` / `Spawn` 中运行交互式 Git patch 流程（`git add -p`、`git commit -p`、`git stash -p`）；改为显式指定 pathspec，或在真实终端中手动执行
- 容器命令不要分配 TTY（如 `docker exec -it`、`docker run -t`、`podman run -t`、`kubectl exec -it`），除非你是在真实终端中手动运行
- 用 `npm init -y` / `--yes`，或显式提供所有必要选项
- 需要 sudo 非交互失败时用 `sudo -n`，避免等待密码提示
- 命令确实支持非交互 stdin 时，用 pipe 或 here-doc 显式提供输入

建议：

- 默认把文件删除、批量改写、网络下载、数据库操作保留为 `ask` 或 `deny`
- 如需管控本地/内网服务或敏感 endpoint，使用 `WebFetch` URL pattern，例如 `WebFetch: { "http://localhost:8000/*": ask }`
- 仅对少量可预期的开发命令设置 `allow`
- 不要把权限匹配理解为安全沙箱

**重要**：Chord 的权限匹配是产品层面的风险控制，不是操作系统级隔离或安全沙箱。

## 文件修改风险

`Edit`、`Write`、`Delete` 都会直接改动工作区文件。`Edit` 用于修改一个已有文件的局部内容，`Write` 用于创建文件或明确完整替换文件，`Delete` 用于删除整个文件。`Read` 和 `Grep` 虽然是只读工具，但它们仍会访问本地文件系统路径，并且会刻意拒绝标准流设备文件（如 `/dev/stdin`、`/dev/stdout`、`/dev/stderr` 等）这类受限 device-style 路径，而不会把它们当作普通文件处理。

执行 `Edit` 前，目标文件必须已经被系统观察过：可以来自 `Read`、之前成功的 `Write` / `Edit`，或系统解析的 `@file` mention。如果文件在观察后发生变化，只要工具仍能验证当前内容，Chord 会提示警告而不是直接拒绝。有风险的非空写前内容会备份到当前会话目录，工具结果会包含备份路径。空文件和无风险的连续 agent-owned 编辑不会创建备份。备份上限为每 path 10 个、每 session 200 个、单文件 10 MiB、每 session 总计 50 MiB；如果必须备份但超过这些上限或因其他原因失败，编辑仍可继续，但工具结果会说明未创建备份及原因。删除/清理会话时，这些备份会随会话目录一起删除。

建议：

- 在重要仓库中配合 Git 使用，方便回滚
- 对生产配置、部署脚本、密钥文件保持 `ask`
- 对生成文件或测试工件目录做更细粒度规则

## 凭据与配置

- API keys 建议放在 `~/.config/chord/auth.yaml`
- 也可通过环境变量引用
- 不要将真实密钥写入示例配置、脚本或项目仓库
- 为 `auth.yaml` 设置严格权限，如 `chmod 600 ~/.config/chord/auth.yaml`

## Headless 模式安全边界

`chord headless` 适合作为 bot / gateway 的底层控制面，但它本身不负责多租户隔离、浏览器安全边界或权限托管。

接入聊天平台、自动化系统或团队服务时，应在外层额外控制：

- 允许访问哪些工作目录
- 允许调用哪些命令
- 谁可以批准高风险操作
- 事件如何审计与留痕

## 网络与外部集成

Chord 支持接入：

- provider API
- LSP
- MCP
- Hooks
- 本地 shell 命令

这些能力都会扩大运行时边界。接入前建议逐项确认：

- 是否真的需要该能力
- 它会读写哪些资源
- 出错时如何回滚或停用
- 是否会把敏感数据带到外部服务

## 使用建议

- 初次使用时，从最小 provider 配置和最小权限开始
- 在个人仓库中先观察一段时间，再逐步放宽权限
- 在共享仓库或团队环境中，不要默认全局 `allow`
- 对自动化 Hook 和 MCP 工具做最小权限暴露

## 相关文档

- [配置与认证](./configuration_CN.md)
- [扩展与定制](./customization_CN.md)
- [Headless 集成](./headless_CN.md)
