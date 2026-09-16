# 上下文管理

<!-- description: 长会话如何留在模型上下文窗口内：请求剪裁、持久压缩，以及可选的模型驱动 checkpoint。 -->

Chord 提供两层互补的上下文管理机制：**上下文压缩（Compaction）**调用 LLM 生成摘要并持久化改写会话历史，**上下文剪裁（Reduction）**在每次请求前用启发式规则剪裁 prompt。两者分别作用于持久化历史和单次请求，各司其职。

两者都通过 `config.yaml` 顶层的 `context:` 配置。配置文件本身的组织方式（文件、层级、provider 等）见[配置与认证](./configuration_CN.md)。

大多数会话可以先用默认设置。只想了解日常影响时，先记住三点：

- 剪裁只改变发给模型的当前请求，不改写会话历史。
- 压缩用摘要替换后续上下文，原文归档到 `history-N.md`；摘要不是完整原文，需要细节时仍应查归档。
- 想主动缩短上下文时用 `/compact`。压缩过频繁或请求超限时，再调整下方阈值。

## 对比速览

| 特性 | 上下文压缩（Compaction） | 上下文剪裁（Reduction） |
|------|-------------------------|------------------------|
| 做了什么 | 调用 LLM 生成结构化摘要，归档旧历史，用摘要替代原文 | 按规则剪裁本次请求中过时的工具输出 |
| 是否落盘 | ✅ 改写 session 文件 | ❌ 对话历史不变（被丢弃的载荷会归档） |
| 是否调用模型 | ✅（可配置专用模型池） | ❌（纯启发式规则） |
| 触发时机 | 达到阈值且即将发起下一次主模型请求 / 手动 `/compact` / 异常恢复 | 每次 LLM 请求前自动执行 |
| 典型耗时 | 数秒到数十秒（需等待 LLM 回复） | 毫秒级（内存内规则匹配） |
| 用户感知 | TUI 显示「Compacting context...」进度 | 无感知（静默） |
| loop 模式 | 启用；压缩仍可运行，让长会话继续推进 | 启用；loop 模式不改变剪裁行为，详见 [Loop 模式](#loop-模式) |

**两者的关系**：Reduction 是轻量级的第一道防线——每次请求前自动剪裁过时的工具输出，减缓上下文膨胀速度。当 Reduction 仍不够、上下文持续增长到 Compaction 阈值时，Compaction 启动做深度压缩。大多数用户只需关注 Compaction 配置；Reduction 的默认值已经适配常见场景，通常无需调整。

Compaction 在把历史交给摘要模型之前，会先对其应用一次 Reduction 规则以节省摘要调用的开销，并且遵循你配置的 Reduction 参数：调高了保留阈值的会话，其持久摘要也会基于保留更多的输入生成。归档到 `history-N.md` 的原文不受此影响，始终是完整无损的。

自动压缩主要依据服务商返回的输入用量。缺少用量数据时，Chord 会根据最近的可信用量估算。达到阈值后，通常在下一次模型请求前启动；不会仅因回答结束就另开一轮压缩。已经运行的压缩会继续完成，并在安全时机应用结果。请求因上下文超限暂停时，会等待压缩后再恢复。

## 上下文压缩（Compaction）

当主会话上下文使用量接近模型上限时，Chord 会准备自动压缩，并在准备下一次主模型请求时启动。压缩过程调用 LLM 分析当前对话，生成结构化摘要（目标、进度、关键决策、文件证据等），归档旧消息，用摘要替换对话历史。压缩结果持久保存到磁盘，会话文件体积显著缩小。

**最小配置**（启用自动压缩）：

```yaml
context:
  compaction:
    threshold: 0.8
```

不指定 `model_pool` 时，压缩沿用当前 Agent 的模型池。需要单独选择压缩模型时，将 `model_pool` 设为已经定义的池名，并优先选择上下文窗口足够大的模型。

### 保留最近消息

压缩后的摘要称为 checkpoint。它保留任务目标、关键进展和会话约束，并附上历史归档索引；需要核对原话时，可以读取对应的 `history-N.md`。

- 继续执行的压缩会尽量保留最近的完整对话，通常是最近两轮，预算约为上下文窗口的 5%。单轮过大时保留能装下的安全后缀，不拆开工具调用和结果。显式选择 `archival` 时不保留这个原始尾部。
- 摘要内还会原样保留最近归档的用户消息，受 `retain_recent_tokens` 约束，默认预算为 4096 个估计 token。被中断的回复或模型主动压缩前的相关正文，也可随保留内容一起进入后续上下文。
- 会话约束会随压缩保留；超过容量时优先保留最早和最近的条目。新指令取代旧约束时，旧条目会标记为已被取代。
- 摘要不能覆盖你最新的消息或完成拒绝理由。发生冲突时，当前任务状态、磁盘文件和可核对的工具结果优先于摘要。恢复上下文所用的关键文件会从磁盘重新读取。

保留原文有预算限制，摘要也可能遗漏细节。重要要求应写进项目指令或文件；需要精确历史时查归档，不要仅依赖摘要。

**配置字段说明**：

| 字段 | 类型 | 默认 | 说明 |
|------|------|------|------|
| `threshold` | 浮点数 | `0.8` | 触发自动压缩的上下文使用率阈值。取值 `0` ~ `1`，例如 `0.8` 表示用量达到可用输入预算的 80% 时触发；设为 `0` 可关闭自动压缩。超出 `0` ~ `1` 范围的值（负数、大于 `1`，或 NaN/±Inf）会被拒绝并回退到内置默认值。 |
| `model_pool` | 字符串 | 克隆当前 agent 模型池 | 执行压缩的专用模型池名。**优先选大上下文窗口，而不是单纯选便宜**：摘要输入会被剪裁到压缩模型自身的窗口内，且**从最早的归档消息开始丢**，小窗口模型会让摘要看不到会话是怎么开始的。理想选择是「窗口大且快而便宜」的模型。 |
| `reserved` | 整数 | `0` | 在 `threshold` 留出的比例余量之外，再为 tokenizer 误差、工具 schema 开销、压缩恢复安全等保留的固定 token 余量。通常建议省略（保持 `0`）；非零值会先从输入预算中扣除，再应用 `threshold`。 |
| `preset` | 字符串 | 自动检测 | 强制指定压缩实现方式，一般无需设置。 |
| `profile` | 字符串 | `auto` | 压缩策略，一般无需设置。 |
| `reminder` | 浮点 | `0`（派生） | 上下文压力提醒线（usage 比例）。`0`（默认）按 `min(0.60, threshold × 0.90)` 派生；(0,1] 区间的值显式设置提醒线；`-1` 只关闭压力提醒、自动压缩保持开启（按模型同样可用）。低于 threshold 的 reminder 在 usage 到达 `min(reminder, threshold)`（任一先到）时触发；等于或高于 threshold 的 reminder 不单独触发——usage 只会在已经越线的请求上到达这条线，那些请求本来会带宽限「compaction imminent」提示或外化提示。`threshold: 0` 会一并关闭两者；其它取值（负数、大于 `1`，或 NaN/±Inf）会被拒绝并回退到派生默认值。 |
| `model_driven` | 布尔 | `false` | 实验性开关：给主 agent 暴露 `compact_context` 工具，让模型在工作状态充分外化（写入文件或结构化参数）后主动请求 durable context checkpoint。checkpoint 不调用摘要模型，在工具批次收口后的 barrier 处原子应用并暂停下一次主模型请求，随后在同一 turn 的压缩上下文上继续。工具仅 MainAgent 可见、必须单独调用、`state_files` 与 `planned_state_files` 只作路径引用，工具自身不读取也不校验存在性；reset 后 runtime 会重新载入登记文件与 checkpoint 关键文件的一小段头部，且仅当当前 read 权限规则允许该路径。低收益请求会被自动跳过。默认关闭。 |
| `retain_recent_tokens` | 整数 | `4096`（内置） | 每个压缩 checkpoint 内嵌的最近真实用户消息的估计 token 预算（见上文的「保留最近消息」）；`0` 或缺省使用内置默认值，只算消息正文。需要跨压缩保住更多最近轮次就调大，想让压缩多回收上下文就调小；保留段不替代摘要，只把最新指令边界原样钉住。 |

按模型覆盖写在**模型定义**上（`ModelConfig.compaction`，含 `threshold` 与 `reminder` 两个子字段），可经 `model_templates` 用 `<<:` 共享；**没有** `context.compaction.models` 这张表。

`threshold` / `reminder`（全局或按模型）驱动 usage-driven 压缩路径和上下文配色，对**所有**用户生效，与 `model_driven` 无关——后者只注册 `compact_context` 工具。TUI 的上下文用量显示（侧边栏 Context 数值与进度条、状态栏百分比 pill）用这两条线取色：reminder 以下为绿色，reminder 到 threshold 之间为橙/黄色，达到 threshold 为红色。请求面的 reminder / warning overlay 则只在启用 `model_driven` 时注入（见下文）。

全局行写在 `context.compaction` 下，按模型调参写在模型定义上（`ModelConfig.compaction`），可用模板 `<<:` 复用：

```yaml
context:
  compaction:
    threshold: 0.65          # 全局自动压缩线（兜底）

model_templates:
  luna-cost-first: &luna-cost-first
    limit: {context: 1050000, output: 128000}   # 官方全窗口：不写 input
    compaction: {threshold: 0.25, reminder: 0.2}   # 留在 272K 长上下文计价档之内

providers:
  openai:
    models:
      gpt-5.6-luna: *luna-cost-first
      gpt-5.6-sol:
        limit: {context: 1050000, output: 128000}
        compaction: {threshold: 0.55}   # 质量优先
```

没有 `compaction` 块的模型继承全局 `context.compaction.threshold` /
`reminder`；模型级 `threshold: 0` 只对该模型禁用自动压缩（提醒一并禁用）；模型级 `reminder: -1` 则只关闭该模型的压力提醒，自动压缩保持开启。

自动压缩阈值越线时，Chord 默认**立即启动** usage-driven 压缩：它在后台异步运行，在下一个 continuation barrier 应用，越线之后的那次请求与它并行继续执行；provider 拒绝（oversize）仍然立即强制压缩。

启用 `model_driven`（`compact_context` 可见）时，同一压缩窗口内的首次越线会把启动推迟两个主模型请求：第一次观察到越线的请求和其后一个请求照常发出，模型借此收口当前阶段、提交 model-driven checkpoint 或把状态外化到文件，然后再启动基于摘要的压缩。宽限期内的**每个请求**都会附带「compaction imminent」提示，倒计时写真实剩余数（越线请求本身写「还有 2 个请求」、最后一轮写「还有 1 个请求」），模型即使没看到越线那次的提示，也知道自己正处在最后一轮（一次性提示到最后一轮时早已不在上下文里）。usage 达到可用输入预算的 95% 时宽限直接跳过或提前结束（单次请求拉入大量工具输出不能借宽限一路顶到 provider oversize 拒绝）；越线后 model-driven 请求收口但未应用（skip / failure / cancel）时宽限立即结束：模型已经出手过，安全网从下一个 gate 接管；越线之前收口的请求不消耗宽限。宽限每个窗口只花一次；任何 durable apply、会话切换、恢复或模型变化都会开启新窗口。请求面的 reminder / warning overlay 只在启用 `model_driven` 时注入；关闭时自动压缩完全由运行时接管（与 Codex 的 local / remote 两条压缩路径一致，它们从不通知工作模型），会话只是继续跑，直到压缩在 barrier 应用。提醒只在会话继续发出主请求时生效：越线后若 turn 正好收尾，usage-driven 压缩走既有的 end-of-turn 路径。一次性外化提示只在真正启动压缩的那次请求上出现，宽限期内压缩还没启动、不会提前注入它。切换模型会套用新模型的 per-model 阈值并开启新的提醒窗口；若新模型的窗口更小、当前用量已经越过它的阈值，Chord 会在切换后提前压缩：空闲时立即启动自动压缩，turn 进行中则把下一次主模型请求延后到压缩应用之后，切换后的请求不会越过新模型阈值；压缩应用后显示一行一次性状态提示。

### 模型驱动上下文 checkpoint（实验性）

设置 `context.compaction.model_driven: true` 后，主 agent 获得 `compact_context` 工具。注册该工具是启用功能的一部分：仅含通配符的权限规则（例如 allowlist 式的 `"*": deny` 加少量显式放行的工具）不会隐藏或拦截它：只有匹配 `compact_context` 的非全局工具规则仍然生效（`deny` 会移除工具并给出一次性诊断，`ask` 保留工具但每次调用需确认，`allow` 与默认一致）。像 `compact_*` 这样的窄匹配也算匹配规则。即使角色的 allowlist 没有放行任何文件工具，模型仍可把状态完整写进结构化参数来完成 checkpoint。模型应在「用 checkpoint 替换当前历史」比继续携带历史更划算，且后续需要的事实已经充分外化时单独调用它（同一响应里不能有其他工具调用）：这些事实要么写在 `state_files` 指出的文件里，要么完整表达在 `active_objective` / `completed` / `decisions` / `open_issues` / `next_step` 结构化参数中（`completed` 只记录已验证的结果及验证方式，todo 列表由运行时自动快照、无需复述）。引用已有状态文件前，应先更新其中依赖的内容。结构化参数能完整承载恢复状态时，`state_files` 可以留空，不必为了请求 checkpoint 额外创建或修改文件。checkpoint 是有成本的状态转移，不是例行保存进度。runtime 校验请求，等工具批次收口后：

1. 快照对话并归档 head（不调用摘要模型，checkpoint 由确定性构造）；
2. 当预计收益低于保守门槛（2048 tokens 且占 prepared surface 的 10%）时拒绝 reset；距上次成功 apply 不足 3 个主模型请求批次时同样会跳过；
3. 原子应用 checkpoint，快照后追加的内容作为 live tail 保留，并在压缩后的上下文上继续同一 turn。

checkpoint 只保存继续执行所需的状态，不代表任务完成，也不能代替最终回复。TODO 全部结束只是阶段边界；只剩最终汇报时，模型直接回复，不请求 checkpoint。需要用户回答或确认时，走正常提问或等待流程。压缩期间收到的用户输入和后台结果由后续请求继续处理。

参数和运行状态相同，且上次 checkpoint 后没有新工作或输入时，Chord 会跳过重复请求；checkpoint 重试和上下文提醒不算新进展。有新输入或工作后可以重新评估，但仍需满足应用间隔和收益门槛。

当前回合里最近几组失败的工具批次会作为真实记录重新挂在 checkpoint 卡片之后：被拒绝的调用（例如 runtime 驳回的 `compact_context` 请求）保留错误卡片，对该代做 fork 时也仍会回放，不会只剩卡片里的摘录。更早的失败只留在归档和 checkpoint 的证据包里。只有失败本身保留原文：同批里顺带成功的结果会换成 `[result elided by checkpoint: N bytes]` 标记，附件也不会被带回新上下文——完整输出在归档里。

#### 何时请求 checkpoint

checkpoint 的停点按上下文压力调整，不要求每次都等完整阶段结束：

- 上下文充足时，只有预计节省的后续上下文成本高于 checkpoint 和重新读取状态的成本，才请求压缩。阶段完成只是有利边界，不是必要条件。
- 出现上下文压力提醒时，先完成当前原子操作，写下最低限度的恢复状态；即使阶段仍是 `active` 或 `candidate`，也可以使用 provisional checkpoint。不要把未完成工作写成已完成。
- 出现「即将压缩」或阈值告警时，停止可选探索，在下一个安全停点记录当前目标、已完成工作、具体下一步和未决事项，然后尽快请求 checkpoint。不要打断正在执行的工具、文件写入、子任务或其他操作。

#### 与自动压缩的关系

自动压缩不会把模型的 checkpoint 锁死。当 usage-driven 压缩已在运行（threshold 越线启动了后台 worker，或 draft 已 ready、正在等 continuation barrier）时，与它并行的那次请求仍可提交 `compact_context`。模型是自己挑的边界，所以它的 checkpoint 优先：runtime 丢弃自动 draft，改应用模型 checkpoint。自动压缩是兜底而不是锁——threshold 越线不会夺走正在收尾的模型的 reset 机会。一次性外化提示不会提及这种覆盖（模型不需要知道有自动压缩在跑，只需要知道当前上下文即将结束）；模型之前主动提交的 checkpoint 照常工作。

#### 跳过与上下文提醒

skip 是正常的策略结果：立即用相同请求重试会被短暂冷却，结果不会改变：模型应等待或继续推进。上下文用量高于提醒线的期间，请求会携带压力提醒：每个压缩窗口先给一次完整文案，之后只带一行简短且自包含的短文案：提醒是逐请求重建的瞬态 overlay，重复提醒不能指望完整文案还在上下文里，因此它直接重述动作，告诉模型为压缩做准备（先完成当前原子操作，将恢复状态放入结构化参数或有权限写入的状态文件；还有工作时按工具契约请求 provisional checkpoint，只剩最终回复或需要用户输入时则直接回复或提问），不再引用还剩多少空间。模型在本窗口调用过 `compact_context`（无论那次尝试收口成什么）、usage 回落线下，或 durable apply / 会话切换 / 恢复 / 模型变化开启新窗口后，重发停止。usage-driven 压缩在 threshold 越线当次即启动（启用 `model_driven` 时先经上文所述宽限期推迟两个请求批次），一次性外化提示只出现在真正启动压缩的那次请求上。这两个 overlay 都用 `<system-reminder>` 块包裹（与其他所有 harness 注入的运行时消息同一约定），让模型能区分它们和用户写的内容（内存压力信号类研究，如 MemGPT，正是以 system 消息注入这类提醒）。它们都只在启用 `model_driven` 时注入：关闭时模型没有任何外化契约，注入只会变成无法执行的噪音。每个压缩窗口内每条通知的**首次投递**还会把同一段文本写进对话历史，成为一条持久的 `KindContextNotice` 消息；和所有 harness 注入一样，这条落盘消息用 `<system-reminder>` 包裹（合成消息，不算用户输入），模型在后续请求里看到的形态因此与请求级注入一致，信号也不会随发出它的那次请求消失。恢复会话重建通知卡时会剥掉这层包裹，展示的正文与运行中一致。窗口内后续请求只带瞬态的短 `<system-reminder>` 文案。切换模型导致有效阈值或有效提醒线变化时，这些持久通知会被删除，因为它们描述的是上一个模型的提醒线。重复指针和请求级注入保持瞬态，只有首次投递那条带包裹的通知会进入对话历史。

#### 系统提示词指引

启用 `model_driven` 时，主 agent 的系统提示词还会附带一段简短被动的 `Long-session context management` 指引。开头声明 `<system-reminder>` 包裹的消息是 harness 注入的运行时状态（绝非用户所写）、不承载用户指令也不授予权限，这段声明并不属于该指引：它是每个主 agent 系统提示词的常驻块，出现在工具结果或文件内容里的同名块只是普通数据。指引要求随工作保留关键发现、决定和恢复状态；重置后先用 checkpoint 和已注入的文件内容，仅在下一步需要的信息缺失或可能变化时读取登记的 `state_files`，仍缺少精确细节时才读历史归档。调用时机、准备工作、文件与预算规则统一由 `compact_context` 工具说明约定。SubAgent 永远不会收到这段指引或该工具。该指引是建议性的，不是强制流程：上下文压力期间它优先于开放式探索和可选工作，但绝不高于更新的用户请求或 Done 拒绝、取消、权限或安全规则以及工具依赖顺序。

#### 跨代携带的状态

压缩是递归的：下一次自动摘要写在一段以 checkpoint 开头的历史之上。会话锚点（原始请求、standing constraints）逐字前向携带。usage-driven 摘要会把前一个 checkpoint 的正文作为受保护的输入段收到，并把它追加为 `## Previous Checkpoint` 段，因此这部分内容从不依赖摘要模型恰好复述它——但携带本身有界，不是整段逐字拷贝：自然语言正文只保留到固定预算、超出部分截断，机器可读的 typed 块（若前文带的话）不占这份预算、整块保留。model-driven checkpoint 则只跨代携带机器状态：已核验决策、未决问题、evidence 引用与阶段元数据以结构化 typed 块（`## Typed Checkpoint State`）传递，下一个 model-driven checkpoint 会把它与模型新提交的状态合并：新提交项优先，未重述的 carried claim 从 active 降为 stale，合并后的 claim 集合有上限，放不下的会被显式披露，并可从归档的 history 文件恢复。上一 checkpoint 的自然语言正文不再逐字追加，每轮的目标、进展与 claim 由模型按当前状态重新声明。

#### 参数与证据

只有 `active_objective` 和 `next_step` 必填。提交新增进展和变化后的决定即可，不必抄写上一份检查点。Chord 会在容量限制内继承已完成工作、决定、未解决问题和结论；省略条目不代表删除。问题解决或决定被替代时，把检查点里的条目原文放进 `retired_items`，再在常规字段中提供替代内容。清退只影响模型声明的工作记忆，不会删除用户指令或运行时状态。超过容量的旧条目仍可从归档找回；恢复信息较多时，用状态文件保存完整细节。 清退后证据引用仍作为有界来源记录保留，不会重新激活已清退的结论，也不代表工作完成。

`evidence_refs` 可引用 checkpoint evidence pack 中已知的稳定证据 ID，Chord 会在 barrier 前校验 ID。`claim_kinds` 将每条 claim 标记为 observed、derived、assumed 或 proposed。claim 键是自然语言断言，通常是对 `completed`/`decisions` 中某条结论的浓缩重述：允许改写，从不要求逐字一致，完全独立的 claim 也允许。合并上一 checkpoint 的 claim 时以键为单位：只有新提交沿用同一键重述，旧 claim 才会被覆盖；换个键改写，旧 claim 原样保留，与新 claim 并存。`observed` 必须有 `claim_evidence`；Chord 自动将其中的 ID 汇总到 `evidence_refs`，不必重复填写。证据仍必须真实可解析、包含分类且未失效；引用只证明来源，不代表模型结论已经得到验证。`state_files` 只是当前外部状态的路径引用；`planned_state_files` 用于尚未写入、仅供后续动作参考的路径。两者在构建 checkpoint 时都不会被 Chord 读取或校验存在性（因此该工具无法绕过 Read 权限，也不可能被当成存在性探针使用），只在 reset 后按当前 read 权限规则决定是否自动载入。条目通常写成相对项目根的路径（如 `docs/usage.md`）；绝对路径以及 `~`、`./`、`../` 开头的写法，只要词法解析后落在项目根内也一样接受，并在构建 checkpoint 前统一归一成相对项目根的路径。每个条目始终是模型声明的引用：过期或不存在的路径只在真正读取时才会暴露（read 工具会报告文件缺失），而不是靠 checkpoint 时刻的静默探测。checkpoint 的 `Current User Request` 永远来自你的真实消息，不会采用模型参数。工具 success 只表示请求被接受；之后出现的 model-driven `[Context Summary]` checkpoint 才表示 reset 已应用。请求被跳过或失败时会继续使用旧上下文，usage-driven 自动压缩兜底保持生效。

#### 可观测性

TUI 状态栏会把模型请求的 checkpoint 与 usage-driven 压缩区分开显示（「model checkpoint」），并在跳过/失败时短暂展示原因；`/stats` 新增「Context Compaction」分区，按 stage 和 trigger 统计生命周期事件（如 `applied/model_driven`、`skipped/model_driven`），方便观察模型请求重置的频率与实际应用情况。

### 触发阈值如何计算

以**可用输入预算**为基准。若模型配置了 `limit.input`，以此为准；否则按 `limit.context` 减去模型声明的 `limit.output` 推导（模型声明了自己的输出上限时，例如 Codex 400K 窗口配 128K 输出推出 272K 输入预算）；只有未声明 `limit.output` 的模型才回退到预留有效默认输出上限（`max_output_tokens`，默认 `64000`）。若设置了 `reserved`，再从预算中扣除。因此实际触发点为 `(输入预算 - reserved) × threshold`；`reserved` 会和 `threshold` 未使用的比例余量叠加，而不是替代它。TUI 信息面板和底部栏的 `Context` 百分比使用扣除 `reserved` 后的同一输入预算基准，与自动压缩阈值保持对齐。对于会单独报告 prompt cache 写入的 provider，Chord 会把当前 prompt 侧用量按 `input_tokens + cache_write_tokens` 计算，因此新写入缓存的 prompt 片段也会计入显示的上下文负担。

provider usage 是自动触发的权威依据。Chord 不会用请求级剪裁后的本地 token 估算去清除已经触发的自动压缩请求，因为多模态输入、工具 schema、provider/proxy framing 等都可能让本地估算与 provider 统计不一致。唯一的兜底是 usage 缺失场景：Chord 收到可信的非零 `input_tokens` 后，会记录当时会进入上下文的消息 bytes，包括正文、需要回放的 tool-call 参数、thinking blocks 和 reasoning text；如果后续响应缺少 usage 或返回 0，且这些 bytes 已增长，就按比例估算 `input_tokens`，估算值达到 `threshold` 时也会触发自动压缩。这个 byte-calibrated estimate 只用于提前压缩，不用于计费，也不表示精确的上下文窗口用量。

**额外固定 headroom 示例（仅在需要时）**：

通常只需设置 `threshold`。以模型 `input: 272000` 和 `threshold: 0.8` 为例，不设置 `reserved` 时会在 `272000 × 0.8 = 217600` tokens 触发压缩，已经留下 `54400` tokens（20%）的比例余量。

只有还需要额外的固定余量时，例如阈值设得很高、provider usage 不可靠或工具 schema 特别大，才建议配置非零 `reserved`：

```yaml
context:
  compaction:
    threshold: 0.8
    reserved: 16000
```

此时扣除 `reserved` 后可用预算为 `256000`，当上下文达到 `256000 × 0.8 = 204800` tokens 时触发自动压缩，比仅设置 `threshold: 0.8` 提前 `12800` tokens；TUI 的 `Context` 百分比也以 `256000` 为分母。不确定是否需要额外固定余量时，请保持默认值 `0`。

注意：非零的 `reserved` 无法在项目配置中重置。Chord 会取项目层与全局层中第一个正数的 `reserved`，项目层设置 `reserved: 0` 会落回全局值；如需降低请直接修改全局配置。

### 手动压缩与超长恢复

除自动触发外，你也可通过 TUI 的 `/compact` 命令随时手动压缩。手动压缩与自动压缩使用同一套后台 worker：即使 agent 正在执行任务也可以启动，进度会显示在后台压缩状态槽位，并在下一个安全的 continuation/idle barrier 应用，而不是立刻打断当前 turn。也可使用 `/compact --no` 临时关闭当前会话的后续自动压缩。

如果实际尝试过的所有候选模型都因为上下文长度错误拒绝请求，且自动压缩已启用，Chord 会启动 oversize recovery 压缩，并在压缩应用后重试。若自动压缩已关闭（`threshold: 0` 或 `/compact --no`），Chord 会停止当前 turn 并给出明确错误，而不是继续重试同一个超长 prompt。

### 区分 input/output 上限

当 provider 同时公布「总上下文窗口」和「单独的输入上限」时，已知限制则建议三个字段都写明：

```yaml
providers:
  openai:
    models:
      gpt-5.5:
        limit:
          context: 400000
          input: 272000
          output: 128000
```

降低 `output` 并不会提高 provider 的硬输入上限。若所选模型输入预算较小，或 provider 区分 input/output limit，建议保持自动压缩开启。

## 上下文剪裁（Reduction）

每次向 LLM 发送请求前，Chord 会用一套确定性规则检查对话中的工具输出，对过时的大段内容做剪裁。**这只影响本次请求的 prompt，不会改写磁盘上存储的对话历史。**剪裁决策只使用工具输出类型、实际 main-model 请求批次、大小和本地有效性状态；上下文使用率只影响持久压缩（Compaction），不会改变 Reduction surface。

**每一次有损摘要都会留下还原地址。**当剪裁要摘要一段超过 2000 字节的输出时，会先把完整内容写入会话的 `reduced-artifacts/` 目录，并在标记后追加一行 `Full output saved to <路径>` 引用，因此这一层丢掉的东西都能取回。归档按内容寻址：相同内容共用一个文件，同一输出的多个副本不会各写一份。已经自带恢复途径的摘要不再归档，因为多存一份没有价值：被后文更新读取覆盖的，直接指向那份更新的副本；被 edit 或 apply_patch 改掉而失效的，没动过的部分重读就能拿回，被替换的原文还留在那次编辑自己的参数里；diagnostics 保留结构化正文；确认类输出本身没有载荷。但因整文件写入、删除或编辑工具之外的改动而失效的读取仍会归档——这时重读只能拿到新内容，当时看到的那个版本别处再也没有。

### 工具结果的首次内联预算

普通工具结果进入会话前，还会经过一层独立的内联安全预算。默认预算是 50 KiB。没有超过预算的结果，第一次会完整交给模型；单行较长或行数超过 2000，单独都不会让一个本来不大的结果被迫重新读取。这两个限制只用于已经超过字节预算的结果预览。

超过预算时，Chord 会把完整结果保存到会话的 `tool-outputs/` 目录，并返回有界预览和稳定引用。模型只应在当前预览不足以支持下一步判断时，搜索或读取实际缺失的范围。这层执行时保护和请求级剪裁是两回事：前者决定结果首次是否内联且可恢复，后者只会在后续请求中摘要过时结果。

剪裁默认启用，通常无需逐项配置，只写以下任一形式即可使用默认参数：

```yaml
context:
  reduction: true
```

```yaml
context:
  reduction: {}
```

`context.reduction: false` 完全关闭请求级剪裁（持久 Compaction 不受影响）；`true` / `{}`（或不写 `context.reduction`）保留默认的请求级剪裁行为。

配置层遵循更具体层优先规则。项目级 `true` 或 mapping 会在全局配置为 `false` 时显式重新启用剪裁；项目层省略该值则继承全局设置。

全部字段及默认值：

```yaml
context:
  reduction:
    confirm_age_turns: 2
    error_age_turns: 3
    high_risk_protect_age_turns: 4
    diff_protect_age_turns: 12
    shell_success_age_turns: 2
    shell_success_bytes: 3000
    shell_read_only_age_turns: 3
    read_like_age_turns: 2
    read_like_output_bytes: 3000
    stale_age_turns: 3
    stale_output_bytes: 1500
    wrap_up_grace_requests: 1
    min_tool_results_prune: 6
    min_incremental_saved_tokens: 2048
```

未设置或非正数的字段使用以上默认值。项目级 `.chord/config.yaml` 可按字段覆盖全局配置。

> **大多数用户不需要配置这一节。** 内置默认值偏保守，已适配常见场景。基于本地真实会话的统计，剪裁能带来可观节省，且没有系统性破坏 prompt cache 复用；想往某个方向调整时，参考下面的调参思路表。

### 默认行为

- Chord 会在每次 main-model 请求前执行轻量请求级剪裁；普通 prompt-cache 热身不会保护本来可剪裁的工具输出。
- 当 `todo_write` 把所有 TODO 标为 completed/cancelled 后，Chord 会把下一次 main-model 请求视为收尾请求。默认 `wrap_up_grace_requests: 1` 只会在同模型、没有排队用户输入、且重新估算的节省低于 `min_incremental_saved_tokens` 时，避免低收益的最终 prompt surface 抖动。如果已有稳定的已剪裁前缀，收尾请求会复用该已剪裁前缀，而不是把旧工具输出恢复成原文。用户新提问、模型切换或可观节省会恢复正常剪裁。
- 已剪裁消息会冻结并逐字节复用，避免每轮重写旧 marker。尚未剪裁的非-read 输出会记录下一次可能跨过规则阈值的 request-batch frontier；只有新增、到期、重复调用命中、工具结果门槛跨越或 surface 失效时才重新分类。小尾部复用不能跳过已经到期的 frontier。`read` 仍按 path/range 的 edit/read 有效性定向分析；仍然有效的读取保持完整，直到后续修改或覆盖读取将其标记为 `truncated=stale` / `truncated=superseded`。位于已发送前缀里的读取，其改写同样要过 cache 摊还门，因此标记可能推迟到冷缓存或收益足以回本时再落地。
- 较老消息形成稳定 surface 后，只分析新增尾部和到期 frontier；如果新增节省低于 `min_incremental_saved_tokens`，复用上一次已剪裁前缀，只追加当前尾部消息。历史 shape、工具 schema、模型、Reduction 配置或 session 改变时会使该 surface 失效。
- 近期高风险工具输出按实际 main-model request-batch age 保护，再进入普通 age/bytes 剪裁。默认 `high_risk_protect_age_turns: 4` 保护失败日志、stack trace、权限/安全输出和当前工作集关键证据；diff/patch 使用独立的 `diff_protect_age_turns: 12`，避免长审查在形成 findings 前过早丢失核心变更证据。同一 assistant 响应中的并行工具调用共享一个批次，不会因为并行结果数量多而提前老化。
- 成功 shell 输出在变旧且超过 `shell_success_bytes` 后按低风险噪音处理，并保留输出大小、行数、有代表性的成功信号行（如有）以及尾部片段 fallback；shell 命令本身仍可从关联的 tool call 中获得。近期失败、stack trace、diff 和 warning 密集的构建日志会先由高风险保护或结构化日志摘要处理；较旧输出在不再受近期高风险窗口保护后，后续仍可能被摘要化。
- 对不在静态只读白名单中的成功 Shell 调用，Chord 会重新核验稳定/恢复前缀里已有 durable hash 的历史读取。确认文件被替换、删除或 hash 改变后，旧读取会标记为 `truncated=stale`；无法读取的路径和没有 durable hash 的旧会话记录不会被猜测为 stale。
- 大块旧工具输出仍会按 age/bytes 规则剪裁，但在退回通用省略前会尽量保留结构化线索：`read` 保留路径与行范围元数据，`grep` / `glob` / LSP references 保留查询范围、受字节预算约束的位置清单和显式省略标记，JSON 输出保留顶层结构和数量，成功 shell 输出保留大小/信号行上下文，diff/patch 保留文件、hunk、变更数量和有界代表行，构建 / 测试日志保留关键失败或警告行。旧错误、diagnostics、确认类输出会被压成固定短 marker 或摘要。
- 剪裁诊断继续保留聚合的 `reread_after_reduction` 计数，并在前后两次读取都有 durable hash 时进一步区分「同 revision 重读」和「revision 已变化后的必要刷新」。
- 重调证据会反馈到保留策略：当模型对「输出已被剪裁」的调用重新发起完全相同的调用（重读、重搜、只读 shell 重跑）时，该 input 的最新输出在本会话余下时间内免于剪裁，并记录 `recalled_input_protect` 跳过原因。较旧的重复副本仍会折叠为 repeated marker；已判定 stale 的读取仍保留 stale 标记；重跑会改变状态的命令（如测试）不获得豁免——那是在求新鲜结果，不是找回被裁内容。豁免集合是会话内存态，随剪裁缓存在恢复或模型切换时丢弃，并从实时证据重建。

### Loop 模式

Loop 模式不改变剪裁行为：loop 模式下的请求与普通请求一样，走同一套请求级剪裁和稳定前缀复用逻辑。切换 loop 模式本身不会新增、删除或重写稳定的 system prompt 文本；否则即使任务上下文没有变化，也会导致 prompt cache 失效。

如果模型显式支持 Chord 的 request-only 动态工具挂载（`compat.chat_completions.mcp_system_tools_message` 或 `compat.responses.mcp_additional_tools`），那么在请求进行中执行 `/loop on` 时，只要当前冻结的顶层工具表面里还没有 `done`，Chord 就可以在下一次 loop 请求里把 `done` 作为一次性的动态工具声明补进去。这个挂载只作用于当前请求，不会改写冻结的顶层工具定义，因此开启 loop 时更容易保住现有 prompt cache 边界；如果冻结工具表面本来已经有 `done`，Chord 不会重复注入。
不支持这两类 request-only 动态工具挂载的模型仍沿用原来的行为：如果启用 loop 需要改动工具表面，后续请求依然可能因为顶层工具定义变化而打断 prompt cache 复用。

### 剪裁规则

Chord 会按工具输出类型和时效分类处理。专门摘要会优先于通用旧结果省略，因此旧的大块输出可以保留高价值结构，同时不会改写持久会话历史。

| 类别 | 典型场景 | 年龄阈值 | 大小阈值 | 设计意图 |
|------|----------|----------|----------|----------|
| 确认/权限 | 工具权限确认、用户授权结果 | `confirm_age_turns`（默认 2 轮后） | — | 权限决策很快过时，可较早剪裁 |
| 错误结果 | 工具执行失败的错误信息 | `error_age_turns`（默认 3 轮后） | — | 失败原因可能仍有参考价值，保留稍久 |
| Shell 成功 / 日志 | 成功命令、构建 / 测试 / lint 日志 | `shell_success_age_turns`（默认 2 轮后——结果在首个可响应它的请求里就已是 age 1，因此新鲜成功输出总能被完整看到恰好一次）；命中 shell 工具只读白名单的命令（`cat`、`ls`、`git log` 等）使用 `shell_read_only_age_turns`（默认 3 轮后） | `shell_success_bytes`（默认 3000 字节以上才剪） | 成功输出通常可重新执行；只读命令是内容获取——相当于没有有效性追踪的 read——因此保护窗口更长（真实会话统计中相同重调的中位间隔约 3 个请求批次）；摘要保留大小、行数、有代表性的成功信号行（如有）以及尾部 fallback，命令仍可从关联 tool call 获取；大日志摘要会保留关键失败 / 警告 |
| Diff / Patch | `git diff`、unified/combined diff、ApplyPatch 文本 | `diff_protect_age_turns`（默认完整保留 12 轮） | 超过 Shell/读取类字节阈值后才摘要 | 保留文件、hunk、变更数量和有界代表行；不会按源码中的 `error`/`failed` 标识符误判为构建日志 |
| 读取类 | `read`、文件内容预览 | 读取本身无年龄门控——已失效/已被覆盖的读取跳过年龄门控和各保护分支（陈旧内容在任何年龄都有误导性）；其他读取类输出等到 `read_like_age_turns`（默认 2 轮后） | `read_like_output_bytes`（默认 3000 字节以上才剪） | 被后续局部 edit/apply_patch 的修改范围覆盖（或遇到整文件/未知范围修改）的读取会被剪裁并标记 `truncated=stale`；被后续更大范围读取覆盖的标记 `truncated=superseded`（更新副本就在后文）。仍是该内容现行视图的读取**永不剪裁**，与年龄和大小无关——裁掉它只会逼出重读，或让模型基于摘要猜测。由于标记替换的是更早请求已经发送过的字节，改写同样要过 cache 摊还门：在省下的 token 无法覆盖尾部重算成本时，完整读取保留原位，标记推迟到冷缓存或收益足以回本时再落地 |
| 搜索类 | `grep`、`glob`、LSP references | `read_like_age_turns`（默认 2 轮后） | `read_like_output_bytes`（默认 3000 字节以上才剪） | 命中列表可重跑，但多点修改任务真正依赖的是 `path:line` 清单——摘要在字节预算内保留**完整位置清单**（每个命中文件及其行号），只给前几个文件附代表片段，超出预算才显式标注省略 |
| JSON / 结构化输出 | `shell` 或结构化工具返回的 JSON | JSON 文档等到 `stale_age_turns`（默认 3 轮后）——key/item 骨架是信息损失最大的摘要形态，且取值往往跨多个请求；NDJSON 日志流（如 `go test -json`）沿用所在类别的年龄 | 类别对应的大小 gate | 大型结构化内容在通用省略前保留顶层 object key 或 array 数量 |
| 其他旧结果 | 不属于以上类别的旧工具输出 | `stale_age_turns`（默认 3 轮后） | `stale_output_bytes`（默认 1500 字节以上才剪） | 兜底规则，最保守，避免误删不易重建的内容 |

年龄参数说明：

- `*_age_turns` 保留旧配置名，但单位是**实际 main-model 请求批次**。请求在真正 dispatch 给 provider 前分配递增 batch；失败请求也会留下年龄间隔。同一 assistant 响应中的多个并行工具调用及其结果共享一个 batch，因此并行工具只算 1 轮。旧会话没有 batch 元数据时，Chord 会按后续 user/assistant 响应保守回退。
- `*_bytes` 是该类别参与剪裁的**最小输出字节数**。小于此值的输出保留完整内容——短输出不需要剪裁。
- 仍然「现行」的 `read` 输出**永不剪裁**：其展示范围之后没有与局部 edit/apply_patch 的修改范围重叠，文件没有被整体替换或删除,也没有更晚的读取覆盖同一范围。这类输出是模型对该内容唯一的现行视图，裁掉它要么逼出一次多余的重读（额外请求轮次、破坏 prompt cache），要么让模型基于摘要凭空作答。容量压力由持久 Compaction 负责：每条 read 结果本身已受读取工具的单次输出预算约束，保留的读取只会线性增长，达到阈值后由 Compaction 归档。成功的 `edit`/`apply_patch` 调用参数本身已经保留应用后的 delta，因此结果只保留应用摘要和诊断，不重复回显修改文本；旧会话或无法可靠确定修改范围时，仍保守地使该文件的全部读取失效。
- `min_tool_results_prune`（默认 6）是 generic stale-output 兜底路径的**安全门槛**：某条结果即使已经达到这条兜底规则要求的年龄和大小，Chord 也会等到会话中至少有这么多条工具结果后，才应用 generic stale 剪裁，避免小会话过早触发这条最保守的兜底剪裁。像 shell-success、read-like、search-like、JSON、build/log 这类按类别定义的摘要路径，仍按各自的 age/size 规则生效。它不参与 request-batch age 计算。
- `wrap_up_grace_requests`（默认 1）在 `todo_write` 报告所有 TODO completed/cancelled 后保护下一次 main-model 请求。它按 LLM 请求次数计数；模型切换时跳过该保护。
- 近期高风险输出不受普通类别阈值限制：在 request-batch age 还不足 `high_risk_protect_age_turns` 时，看起来像 diff、失败断言、stack trace、权限/安全错误的结果会保持完整。同一批并行工具不会互相增加年龄。

### 调参思路

当你重视 prompt cache 稳定性、且会在多个轮次中反复围绕同一批活跃文件工作时，保持默认即可。如果主要问题是工具密集型会话很快顶到上下文上限，可以下调字节阈值，例如 `read_like_output_bytes: 2500`；成本优先的配置还可以缩短高风险保护窗口：

```yaml
context:
  reduction:
    high_risk_protect_age_turns: 1
```

| 你遇到的情况 | 建议 |
|--------------|------|
| Prompt cache 复用良好，但中等大小的读取/日志仍然太容易改变请求前缀 | 进一步调高 `read_like_output_bytes` 和 `shell_success_bytes` |
| 每次对话很短但工具输出特别多 | 降低 `min_tool_results_prune`（如 `4`） |
| Prompt 中权限确认信息过多 | 降低 `confirm_age_turns`（如 `1`） |
| 构建/测试日志经常需要回头看 | 进一步调高 `shell_success_bytes`（如 `16000`） |
| 文件内容经常需要回头查阅 | 无需调参：仍然有效的读取始终保留，直到被修改或被更新读取覆盖 |
| TODO 完成后的最终回复因 prompt cache 被扰动而更贵 | 保持 `wrap_up_grace_requests: 1`；只有当你的流程通常在 TODO 完成后还会多一次验证请求时才考虑设为 `2` |
| 工具输出都很重要不想丢 | 整体调高各 `*_age_turns` 和 `*_bytes` |

## 相关文档

- [配置与认证](./configuration_CN.md)：配置文件、层级与完整字段速查表
- [使用指南：`/compact`](./usage_CN.md#常用本地控制命令)
- [性能](./performance_CN.md)
- [常见问题排查](./troubleshooting_CN.md)
