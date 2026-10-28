# 扩展与定制

先选择你想改变的行为，不需要一次配置所有扩展：

| 想做什么 | 使用哪项扩展 |
| --- | --- |
| 让 Agent 遵守项目规则 | [仓库指令](#仓库指令) |
| 给不同任务分配模型和权限 | [自定义 Agents](#自定义-agents) |
| 按需加载专业知识或操作步骤 | [Skills](#skills) |
| 自动通知、检查或处理工具结果 | [Hooks](#hooks) |
| 获取代码诊断 | [LSP](#lsp) |
| 接入外部工具 | [MCP](#mcp) |
| 保存常用提示词 | [自定义命令](#自定义-slash-commands) |

## 仓库指令

项目需要给自动化 agent 提供长期有效的规则时，可以添加 `AGENTS.md`，例如编码规范、验证命令、安全要求或仓库专属审查准则。

会话开始时，Chord 从当前工作目录向上查找，直到项目根目录，再按从根目录到当前目录的顺序加载 `AGENTS.md`。从项目根启动时，先加载根目录的文件。之后只有**读取**会触发发现：某个子目录下的文件第一次被读取时，该目录（到会话目录之间）的 `AGENTS.md` 才补进来，注入下一次请求；写入、编辑、打补丁、删除和 shell 都不触发。目录规则只是提示词：不阻断、不取消、不延迟，也不以任何方式改变工具调用；需要强制执行的约束请写成权限规则或 hook，不要写在规则正文里。主 Agent 和子 Agent 分别维护规则快照；更深目录的规则只影响其子树，不覆盖更高优先级的指令。

Shell 只检查实际工作目录，不解析命令里的路径或 `cd`。项目外目录、MCP 和模型服务端原生工具不在自动发现范围内。`AGENTS.md` 若是符号链接，会跟随到它的目标，无论目标位于哪里（例如仓库旁边一个共享规则仓库）：规则仍归属于放链接的那个目录。显式点名链接或目标的 `read` deny 会阻止规则加载；通配默认 deny 不参与判断，普通白名单不会把项目规则整体藏起来。每层目录的观察结果在当前工作目录绑定期间保持固定；恢复会话或切换工作目录时重新检查。上下文压缩会保留有效规则的完整正文。规则文件读不出来（IO 错误、超过 64 KiB、非 UTF-8）时按不存在处理：不会让会话失败，也不会影响任何工具调用。大小和编码拒绝会按路径记一次日志；I/O 错误不记录。

Python 项目中，Chord 会沿同一路径寻找最近的有效虚拟环境，每层依次检查 `.venv`、`venv`、`env`，并提示 Agent 优先使用其中的解释器。工作目录、平台和虚拟环境信息会在上下文压缩后继续提供，无需重复说明。

## 自定义 Agents

可覆盖或新增角色配置：

- 全局：`~/.config/chord/agents/`
- 项目级：`.chord/agents/`

支持 `.md`（YAML frontmatter 加 Markdown prompt 正文）以及 `.yaml` / `.yml`（纯 YAML，通过 `prompt` 或 `system_prompt` 配置 prompt）。

常见用途：为不同角色设置不同模型链和权限，或增加专门的 reviewer、backend、frontend、docs 等角色；也可以通过 `prompt_preset` 让自定义角色复用内置角色 prompt 块，并用 `prompt_append` 在其基础上补充而非替换。

完整 Agent 配置字段、示例和委派选项见 [配置与认证：Agent 配置](./configuration_CN.md#agent-配置)。

要让子 Agent 运行在外部 coding agent 进程中，见[外部 coding agent 子 Agent](./external-agents_CN.md)。

## Skills

Chord 默认从以下目录发现 Skills：

- `.chord/skills/`
- `.agents/skills/`
- `~/.config/chord/skills/`
- `skills.paths` 中配置的额外目录

运行时不会把所有 skill 正文预先注入 system prompt；任务明显匹配时，模型才会调用 `skill` 工具按需加载。

TUI 侧边栏的 **SKILLS** 区块只显示当前已发现的 skills，每行一个图标、不带状态文字。图标形状表示状态：

| 图标 | 含义 |
| --- | --- |
| `○` | 空闲：模型可加载，正文未加载，`paths` 也未命中 |
| `◎` | 相关：某次项目文件读取命中了它的 `paths`，模型已收到（或即将收到）名称与描述通知 |
| `●` | 已加载：正文真正进入本 Agent 的上下文，`skill` 工具加载和自己用 `/skill` 加载都算 |
| `↻` | 需重载：正文曾加载但已被压缩归档，重新加载后才能使用 |
| `◌` | 仅显式加载：模型看不到它，只有 `/skill` 能加载正文 |
| `⊘` | 被拒绝：角色 ruleset 拒绝该技能（仅选择器行） |

图标颜色是强调色：正文在上下文中为绿色，压缩后需重载为琥珀色，其余保持暗色。加载失败不会标记，未发现/不存在的 skill 也不显示（直至被发现）。

每个 Agent 只能看到和加载自己权限允许的技能。加载状态分别记录：主 Agent 加载过某个技能，不代表子 Agent 也已加载。恢复子任务时，Chord 会恢复其加载记录，并按当前权限显示。

最小结构示例：

```text
.chord/skills/
└── go-expert/
    └── SKILL.md
```

`SKILL.md` 示例：

```markdown
---
name: go-expert
description: Go language development expert
resources:
  - references/style.md
---

遵循 Effective Go 和 Go Code Review Comments。
```

`description` 是模型在 `Available Skills` 列表里判断技能是否匹配时看到的文本，触发条件写在这里；超过 1024 字符，Chord 会截断并补 `...`。

### 自己加载 skill

`/skill <name> [args]` 可以就地加载一个 skill。Chord 把这行当普通用户消息提交，紧接着把 skill 正文作为 `skill` 工具结果追加进同一回合，模型不用自己决定调用工具就能拿到正文。名字之后的内容原样作为该 skill 的参数，替换正文里的 `${CHORD_SKILL_ARGS}`：

```text
/skill go-expert 审一下 parser 包
```

这行跟着当前聚焦的 Agent，子 Agent 也可以用同样的方式载入技能。只敲 `/skill` 则打开选择器，列出当前 Agent 可加载的全部技能（只留给显式加载的排在前面），选中后回填带尾随空格的 `/skill <name>`，参数接着输入即可。被 ruleset 拒绝的技能显示为 Disabled 并给出原因，名字不存在则弹 toast 拒绝。

这样合成的加载与模型自己加载等价：记进会话，技能显示为已加载，继续会话时恢复该状态；持久压缩把这对消息归档后同样会清掉，与重启一致。

### 让技能不进模型目录

frontmatter 里写 `disable-model-invocation: true` 会让技能不进模型目录：`Available Skills` 列表和 `skill` 工具列表里都没有它，模型即使点名也加载不了；某个角色配的技能全是这种时，它连 `skill` 工具都不会注册。你仍可以用 `/skill <name>` 自己加载，面板里标 `◌`。skill 目录下的 `chord.yaml` sidecar 可以双向覆盖这个字段。ruleset 依旧生效，被它拒绝的技能双方都用不了；`chord doctor skills` 的可见性只看 ruleset，不看这个字段，所以这类技能在那里照样报 `visible`，尽管它从不到达模型。

### 声明式资源

skill 正文依赖同目录下的文件时，在 frontmatter 的可选字段 `resources`
里按相对 skill 根目录的路径列出来。只校验已声明的条目，正文里顺带提到
的其他路径会被忽略。

- 条目必须留在 skill 根目录内，且最终是普通文件。缺失、逃出根目录（`..`、
  绝对路径、指向根外的 symlink）、目录和设备文件都会判失败。
- 空文件只告警：读出来等于没读，但很容易被忽略。
- 资源问题不会隐藏 skill。缺资源的 skill 照常加载、照常可见；`skill` 工具
  会在正文前加一段告警块，TUI 卡片上也有标记。
- doctor 的结论是检查那一刻的快照，不是担保。运行期每次加载都会重新 stat，
  检查完再删掉的文件，运行时照样告警。

正文里 `${CHORD_SKILL_DIR}/<path>` 这种字面量会按同一规则顺带检查一遍，
最多记告警。正文散文里的裸相对路径不扫描。

设置 `paths` 后，本地 `read` 成功读取匹配的项目文件时，Chord 会向模型提示相关技能：

```yaml
paths:
  - "internal/tui/**"
  - "**/*.go"
```

模式相对于当前 checkout 根目录，区分大小写，多项之间为“或”。只使用 `/` 分隔，目录写成 `dir/**`；绝对路径、`..`、否定模式或非法 glob 会禁用该技能的自动发现，`chord doctor skills` 会说明原因。最终有效 `paths` 缺省或为空时是无条件技能；`**` 则等待任意项目文件读取。sidecar 中非空的 `paths` 会覆盖 frontmatter，缺省或 `[]` 不覆盖。

条件技能不会提前列入启动目录。匹配后，Chord 在整轮工具回执完成后提示技能名称和描述，并限制提示数量与长度；这不会加载正文、执行技能或授予权限。模型仍可在匹配前按名称调用获准的技能；用户也可通过 `/skill` 显式加载获准的条件技能或仅手动技能。选择器与侧边栏使用同一套图标：路径命中或已提示为 `◎`，正文可用为 `●`，压缩后需重新加载为 `↻`。

只有成功的本地文本读取会触发匹配，包括局部行读取。Shell、grep、glob、图片/PDF 工具、MCP 和服务端原生工具不触发。符号链接按当前 checkout 内的真实目标匹配。各 Agent 的发现状态独立；上下文压缩保留激活元数据，并可补发简短提示，但不会自动恢复技能正文。

## 诊断 skills

`chord doctor skills` 负责回答“配了却没生效”的 skill 卡在哪里。它复用运行
时的发现顺序和解析器，只是把无效文件和被遮蔽的同名文件也各留一行，而不
是静默跳过。加 `--json` 可输出机器可读报告。

每行有四个独立维度：

- `integrity`：`passed` 表示运行时会保留该文件；`failed` 表示会被跳过
 （YAML 写坏了、缺 `name`/`description`、文件读不出）。
- `load`：正文能不能读出来（`passed`/`failed`/`not_run`）。
- `visibility`：`builder` ruleset 下是否可见
 （`visible`/`denied`/`not_checked`）。这一维只看 ruleset：
 `disable-model-invocation` 不属于它，因此只留给显式加载的技能照样报
 `visible`。
- `resources`：已声明资源的健康状况
  （`passed`/`failed`/`warning`/`none`）。
- `shadowed`：是否有更高优先目录的同名 skill 占位。

退出码沿用 `doctor` 家族：有 skill 的 integrity 或 load 失败时返回 `1`，
检查本身跑不起来时返回 `2`。配置读不出，或扫描出问题（目录不可读、悬空
符号链接、扫描路径不是目录）都属于这一类：这些问题会列在报告里（`--json`
下是 `scan_issues`），报告照常输出。被拒绝、被遮蔽、资源问题、一共没配
skill 都不影响退出码。`--strict` 会在 ruleset 不可用或某项检查没跑成时
也返回 `1`。能加载、能读出，不代表模型会选用它。

## Hooks

Hooks 让你在运行时的明确节点（工具调用前、LLM 调用后、idle 时、工具批量完成后等）运行外部命令，用途包括通知、审计、自动化检查、工具结果清洗。

agent idle 时弹桌面通知的简单示例：

```yaml
hooks:
  on_idle:
    - name: notify-idle
      command: ["osascript", "-e", "display notification \"Chord 已空闲\" with title \"Chord\""]
```

完整 14 个触发点列表、JSON envelope 协议、sync / automation / observer 三类差异及更多示例，见专门的 [Hooks](./hooks_CN.md) 页面。

## LSP

LSP 可在写文件后返回语义级诊断。

典型配置：

```yaml
lsp:
  gopls:
    command: gopls
    file_types: [".go"]
    root_markers: ["go.work", "go.mod", ".git"]
    options:
      gopls:
        staticcheck: true
        analyses:
          minmax: true
          rangeint: true
          slicescontains: true
  pyright:
    command: pyright-langserver
    args: ["--stdio"]
    file_types: [".py", ".pyi"]
  typescript:
    command: typescript-language-server
    args: ["--stdio"]
    file_types: [".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs"]
    root_markers: ["tsconfig.json", "jsconfig.json", "package.json", ".git"]
  rust-analyzer:
    command: rust-analyzer
    file_types: [".rs"]
    root_markers: ["Cargo.toml", "rust-project.json"]
```

`options` 是 Chord 应答服务器 `workspace/configuration` 请求时返回的 workspace settings，键名就是 section 名：gopls 的 `staticcheck`、`analyses` 等设置要放在 `gopls` 键下，Pyright 的设置用 `python`、`python.analysis`。平铺在顶层不会送到服务器——section 找不到对应键时，Chord 返回空对象。`init_options` 只作为 LSP 初始化元数据发送，并不是 gopls settings 的正确位置。可用的 analyzer 名称及其默认值取决于本机安装的 gopls 版本。较新的 gopls 已默认启用大多数 `modernize` analyzer；显式设为 `true` 可以记录并保留项目依赖的检查，设为 `false` 则可关闭单项检查。修改 Go 文件后，Chord 会透传 gopls 的 information 和 hint 诊断，但在默认最多 10 条的输出额度内，error 和 warning 会优先展示。staticcheck 与 analyzer 的结果要比编辑结果等待的类型检查晚一秒以上。它们给编辑过的文件带来新的 error 或 warning 时，Chord 会把它们排成持久的 LSP follow-up，在后续请求里投递，并显示 `LSP DIAGNOSTICS` 卡片；同一文件已经发过的问题，要消失后再出现才会重发。错过了单次编辑等待时间的问题由 follow-up 补送，不会拖住整个回合。

gopls 的设置是一层平铺的 map。gopls 文档里的 `build`、`ui` 只是分组名，不是键：`directoryFilters` 要直接写在 `gopls` 下（也可以写成带点的 `build.directoryFilters`），嵌套的 `build:` 会被 gopls 当作未知设置拒绝。`directoryFilters` 限定 gopls 从哪些目录加载包，适合排除体积大且不含 Go 代码的子目录，例如前端的 `web/`。配置的清单会替换默认的 `["-**/node_modules"]`，需要保留这一项；以 `.` 或 `_` 开头的目录本来就会跳过。这些过滤规则不作用于 gopls 自己的文件监听（`fileWatcher`）。`fileWatcher` 保持默认的 `off` 即可：`fsnotify` 会监听工作区里的每个目录，在 macOS 上可能耗尽文件描述符。

```yaml
lsp:
  gopls:
    command: gopls
    file_types: [".go"]
    options:
      gopls:
        staticcheck: true
        analyses:
          ST1000: false
```

只想关掉个别噪音检查，不必放弃整个 staticcheck：保留 `staticcheck: true`，在 `analyses` 里把对应 analyzer 设为 `false` 就行。例如 `ST1000: false` 会去掉 staticcheck 的包注释告警，其它 staticcheck 检查照常运行；`staticcheck: false` 才是关闭整个 staticcheck 集合。gopls v0.23 没有 `checks` 选项（本机支持哪些设置可查 `gopls api-json`），老示例里的 `checks: ["all", "-ST1000"]` 写法不生效，能用的开关只有 `analyses` 里的单项设置。这些设置只在语言服务器启动时读取，改完要重启 Chord。

gopls 还会把分析结果持久化到磁盘缓存，使用同一份 gopls 二进制的项目共用这份缓存：语言服务器重启后，未改动的包直接复用结果，只重算你改过的包及其依赖者。`maxFileCacheBytes` 调整的是该缓存的软上限（默认 1GB；超过 5 天未访问的条目无论预算多大都会被清掉），调大可以让更多结果跨重启保留；`env` 里的 `GOMAXPROCS` 则限制编辑后那阵多核 CPU 峰值背后的分析并行度，代价是分析墙钟时间变长。

```yaml
lsp:
  gopls:
    command: gopls
    file_types: [".go"]
    env:
      GOMAXPROCS: "4"
    options:
      gopls:
        maxFileCacheBytes: 4000000000
```

`env` 里的条目会附加到语言服务器进程继承的环境上。其中一项与 gopls daemon 共享有关：`-remote=auto` 模式下，gopls 通过 `XDG_RUNTIME_DIR` 定位 daemon socket，未设置时回退到会话的临时目录——用不同 `TMPDIR` 启动的会话会各自起一个 daemon，临时目录被销毁重建后，运行中的 daemon 就找不到了。给 gopls 配置 `XDG_RUNTIME_DIR` 指向一个已存在的用户级目录（Linux 上是 `/run/user/<uid>`）即可跨会话共用同一个 daemon；Windows 不读这个变量，自动启动的 daemon 使用 TCP 回环地址。

这种 LSP 反馈是编辑后的增量检查，不能替代 CI 中的全仓门禁。若项目要在 CI 中采用独立的 `modernize` 命令，应先清理并审查现有发现，再固定命令版本，而不是使用 `@latest`；部分建议修复（例如把 `omitempty` 改为 `omitzero`）会有意改变序列化行为，必须人工审查。

需要先在本机安装对应语言服务器才能使用。对于 Pyright，未配置 Python 解释器时，Chord 会从 LSP workspace root 向上寻找最近的有效虚拟环境，不越过该文件所属仓库或工作区的边界；类 Unix 查找 `.venv/bin/python`、`venv/bin/python` 和 `env/bin/python`，Windows 查找对应的 `Scripts\python.exe`。同一 workspace root 的发现结果会随 LSP client 缓存，避免重复探测。

TypeScript 服务器还要求能跑起一个 TypeScript：`typescript-language-server` 会加载工作区 `node_modules` 里的 `lib/tsserver.js`，工作区里没有才退回全局安装。两处都取不到时，服务器在 `initialize` 阶段直接失败，Chord 把该 server 标为启动失败（信息面板红点），原因写进日志。给它一个 TypeScript 有两条路：

- 装项目依赖（`pnpm install`、`npm install` 等），让服务器用工作区自己的 TypeScript，诊断和项目实际编译用的版本一致。
- 或者用 `init_options.tsserver.fallbackPath` 指向另一个安装；它只在工作区没有可用 TypeScript 时生效（`tsserver.path` 则始终优先）：

```yaml
lsp:
  typescript:
    command: typescript-language-server
    args: ["--stdio"]
    file_types: [".ts", ".tsx", ".js", ".jsx"]
    init_options:
      tsserver:
        # 指向仍提供 tsserver.js 的安装，例如 .../lib/tsserver.js
        fallbackPath: /path/to/typescript/lib/tsserver.js
```

TypeScript 7 不再随包提供 `lib/tsserver.js`（语言服务并入了原生编译器），锁定 TypeScript 7 的工作区要把 `fallbackPath` 指向仍提供该文件的版本（6.x 及更早），代价是诊断来自旧版编译器。每个 TypeScript 服务器实际加载的版本、以及服务器对此发出的警告，Chord 都会写进日志。

`file_types` 决定语言服务器处理哪些文件，`root_markers` 决定工作区根目录。省略 `root_markers` 时，gopls 使用 `go.work`、`go.mod`，TypeScript/JavaScript 使用 `tsconfig.json`、`jsconfig.json`、`package.json`，Pyright 使用 `pyrightconfig.json`、`pyproject.toml`、`requirements.txt`。显式配置会覆盖这些默认标记。

Chord 从被编辑文件所在目录向上查找最近的匹配标记，以该目录作为语言服务器的工作区根。查找不会越过文件所属的 Git 仓库、worktree 或会话已绑定的工作目录；没有匹配标记时使用这个边界。外部非 Git 项目需要能匹配 `root_markers`，否则跳过 LSP。

编辑独立 worktree 或其他仓库（例如 `../companion`）中的文件时，也会按目标项目启动语言服务器，无需切换当前工作目录。所有实例沿用当前会话的 LSP 配置，不自动加载外部项目的 Chord 配置。不同根目录使用独立实例，工具结果只附带本次编辑所用实例的诊断；侧栏用目录名区分外部或多个工作区。

切换会话工作目录会保留其他工作区的实例与诊断。每个服务器名最多保留 8 个存活实例，超出后关闭最久未使用的实例；再次写入对应工作区时按需重启。单实例的打开文档超过 256 个时，后台会关闭较早使用的文档；闲置文档也会关闭。没有打开文档且持续闲置的实例会释放，后续编辑时按需重启。后台也会清理目录已被移除的实例。

对于已打开或已有诊断记录的文件，Chord 在后台监测外部修改、原子替换和删除，并定期补查。内容变化后会撤销旧的内存文档和诊断，让语言服务器重新读取磁盘；文件删除后会清理相关诊断和侧栏计数，会话历史仍然保留。监测范围限于活跃工作区中的相关文件和目录，不递归扫描整个仓库，处理过程不额外显示提示。

Chord 根据配置中的服务器名或可执行文件名识别类型：`gopls`、`typescript` / `typescript-language-server`、`pyright` / `pyright-langserver`、`basedpyright` / `basedpyright-langserver`，也支持 Windows 可执行文件后缀。使用自定义包装脚本时，保留这些服务器名，或显式设置 `root_markers`。

通常无需手动设置 `python.pythonPath`。只有需覆盖自动发现逻辑或改用自定义解释器路径，才需设置它。`python.analysis` 也是按需启用的 Pyright 行为调优项，如调整类型检查严格度。这类配置请使用嵌套 `options`：

```yaml
lsp:
  pyright:
    command: pyright-langserver
    args: ["--stdio"]
    file_types: [".py", ".pyi"]
    options:
      python.analysis:
        typeCheckingMode: strict
```

确需显式覆盖解释器时，在同样的嵌套 `options` 下添加 `python.pythonPath`：

```yaml
lsp:
  pyright:
    command: pyright-langserver
    args: ["--stdio"]
    file_types: [".py", ".pyi"]
    options:
      python:
        pythonPath: .venv/bin/python
```

## MCP

MCP 服务器把外部工具或远端数据源暴露给模型。

```yaml
mcp:
  exa:
    url: https://mcp.exa.ai/mcp
```

可通过 `allowed_tools` 只暴露部分工具，减少 token 开销。详见 [配置与认证](./configuration_CN.md#mcp)。

本地模式下 MCP 会在 TUI 启动后异步连接。自动启动的 server 仍在后台连接，但第一次 LLM 请求会等待：每个自动启动的 server 要么连接成功，要么明确失败后才会继续。

对于不是每轮对话都需要的 MCP，建议设置 `manual: true`：启动时保持禁用，不连接该 server，也不把它的工具描述加入默认 LLM 工具上下文，从而降低平时的上下文开销。需要使用时，再通过 `/mcp`（菜单）或 `/mcp enable <server>` 手动启用。

在 TUI 中，按 `Ctrl+O` 可打开 MCP 选择器。Agent 运行中也可以打开它查看 server 状态并切换手动 server。运行中做出的变更会在下一次模型请求生效，因此当前正在进行的请求会继续使用它启动时的工具表面和 prompt。

只有 `manual: true` 的 server 才能在运行时修改状态。自动启动的 server 会作为默认工具上下文的一部分保持只读，不受 `/mcp enable|disable` 影响。

## 自定义 slash commands

可在 `config.yaml` 中定义项目级或全局级 slash commands，将常用模板或操作包装为快捷入口。

```yaml
commands:
  /review: "请审查当前 diff 中的代码变更，关注正确性和安全性。"
  /commit: "请根据当前 staged 变更生成一条简洁的 commit message。"
```

输入 `/review` 后，如果出现自动补全列表，先用 `Tab` 或 `Enter` 接受补全，再按 `Enter`；Chord 会将对应文本作为用户消息发送给模型。自定义命令也会出现在 `/` 自动补全列表中。

## 通知

Hooks 或桌面通知配置可以在以下场景提醒你：权限确认、问题等待输入、agent 完全停止。

## 相关文档

- [配置与认证](./configuration_CN.md)
- [权限与安全](./permissions-and-safety_CN.md)
- [常见问题排查](./troubleshooting_CN.md)
