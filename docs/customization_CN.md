# 扩展与定制

先选择你想改变的行为，不需要一次配置所有扩展：

| 想做什么 | 使用哪项扩展 |
| --- | --- |
| 让 Agent 遵守项目规则 | [仓库指令](#仓库指令) |
| 给不同任务分配模型和权限 | [自定义 Agents](#自定义-agents) |
| 按需加载专业知识或操作步骤 | [Skills](#skills) |
| 自动通知、检查或处理工具结果 | [Hooks](#hooks) |
| 获取代码诊断、定义和引用 | [LSP](#lsp) |
| 接入外部工具 | [MCP](#mcp) |
| 保存常用提示词 | [自定义命令](#自定义-slash-commands) |

## 仓库指令

项目需要给自动化 agent 提供长期有效的规则时，可以添加 `AGENTS.md`，例如编码规范、验证命令、安全要求或仓库专属审查准则。

会话开始时，Chord 从当前工作目录向上查找，直到项目根目录，再按从根目录到当前目录的顺序加载 `AGENTS.md`。从项目根启动时，只加载根目录的文件。主 Agent 和子 Agent 都会收到这些规则；它们不覆盖更高优先级的指令。

Python 项目中，Chord 会沿同一路径寻找最近的有效虚拟环境，每层依次检查 `.venv`、`venv`、`env`，并提示 Agent 优先使用其中的解释器。工作目录、平台和虚拟环境信息会在上下文压缩后继续提供，无需重复说明。

## 自定义 Agents

可覆盖或新增角色配置：

- 全局：`~/.config/chord/agents/`
- 项目级：`.chord/agents/`

支持 `.md`（YAML frontmatter 加 Markdown prompt 正文）以及 `.yaml` / `.yml`（纯 YAML，通过 `prompt` 或 `system_prompt` 配置 prompt）。

常见用途：为不同角色设置不同模型链和权限，或增加专门的 reviewer、backend、frontend、docs 等角色；也可以通过 `prompt_preset` 让自定义角色复用内置角色 prompt 块，并用 `prompt_append` 在其基础上补充而非替换。

完整 Agent 配置字段、示例和委派选项见 [配置与认证：Agent 配置](./configuration_CN.md#agent-配置)。

## Skills

Chord 默认从以下目录发现 Skills：

- `.chord/skills/`
- `.agents/skills/`
- `~/.config/chord/skills/`
- `skills.paths` 中配置的额外目录

运行时不会把所有 skill 正文预先注入 system prompt；任务明显匹配时，模型才会调用 `skill` 工具按需加载。

TUI 侧边栏的 **SKILLS** 区块只显示当前已发现的 skills。字形表示模型可见性：`○` / `●` 是模型可加载的，`◌` 表示只留给显式加载。颜色表示加载状态，skill 正文真正进入本 Agent 的上下文后才变绿，`skill` 工具加载和自己用 `/skill` 加载都算。加载失败不会标记，未发现/不存在的 skill 也不显示（直至被发现）。

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

frontmatter 的 `paths` 字段目前只解析、不生效：skill 的可见性只按权限
过滤，不按文件模式匹配。

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

LSP 可在写文件后返回语义级诊断，并提供 `definition` / `references` / `implementation` 等能力。

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

`options` 是 Chord 应答服务器 `workspace/configuration` 请求时返回的 workspace settings，键名就是 section 名：gopls 的 `staticcheck`、`analyses` 等设置要放在 `gopls` 键下，Pyright 的设置用 `python`、`python.analysis`。平铺在顶层不会送到服务器——section 找不到对应键时，Chord 返回空对象。`init_options` 只作为 LSP 初始化元数据发送，并不是 gopls settings 的正确位置。可用的 analyzer 名称及其默认值取决于本机安装的 gopls 版本。较新的 gopls 已默认启用大多数 `modernize` analyzer；显式设为 `true` 可以记录并保留项目依赖的检查，设为 `false` 则可关闭单项检查。修改 Go 文件后，Chord 会透传 gopls 的 information 和 hint 诊断，但在默认最多 10 条的输出额度内，error 和 warning 会优先展示。

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

这种 LSP 反馈是编辑后的增量检查，不能替代 CI 中的全仓门禁。若项目要在 CI 中采用独立的 `modernize` 命令，应先清理并审查现有发现，再固定命令版本，而不是使用 `@latest`；部分建议修复（例如把 `omitempty` 改为 `omitzero`）会有意改变序列化行为，必须人工审查。

需要先在本机安装对应语言服务器才能使用。对于 Pyright，未配置 Python 解释器时，Chord 会从 LSP workspace root 向上寻找最近的有效虚拟环境，不越过项目根；类 Unix 查找 `.venv/bin/python`、`venv/bin/python` 和 `env/bin/python`，Windows 查找对应的 `Scripts\python.exe`。同一 workspace root 的发现结果会随 LSP client 缓存，避免重复探测。

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

`file_types` 决定语言服务器处理哪些文件，`root_markers` 决定工作区根目录。省略 `root_markers` 时，TypeScript/JavaScript 使用 `tsconfig.json`、`jsconfig.json` 和 `package.json`，Pyright 使用 `pyrightconfig.json`、`pyproject.toml` 和 `requirements.txt`，两类服务器不会把另一种语言的标记当作项目边界。其他服务器回退到 Chord 项目根；显式配置 `root_markers` 会覆盖默认标记。

Chord 根据配置中的服务器名或可执行文件名识别类型：`typescript` / `typescript-language-server`、`pyright` / `pyright-langserver`、`basedpyright` / `basedpyright-langserver`，也支持 Windows 可执行文件后缀。使用自定义包装脚本时，保留这些服务器名，或显式设置 `root_markers`。目录查找始终限制在项目根内。

对匹配的文件，Chord 会按以下规则确定该语言服务器的 workspace root：从文件所在目录向上，取最近一个包含任一 `root_markers` 的目录（不越过项目根）；没有匹配则回退到项目根。发现是按文件进行的，所以不同文件可能落在不同的根上，同一个服务器名也能按根各起一个实例。这样嵌套前端工程（例如仓库根本身是后端项目、前端在 `frontend/` 子目录）就能得到 root 定位到该子包的语言服务器，直接在包内找它的 `node_modules`、`tsconfig.json` 等包级配置。单个服务器名最多保留 8 个存活实例；monorepo 中标记目录超出这个数量时，最久未使用的实例会被关闭，下次读取其根下的文件时再重启。

Python、TypeScript 和 JavaScript 都会按文件发现最近的 workspace root；同一服务器名可以为不同根目录缓存独立实例。

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
