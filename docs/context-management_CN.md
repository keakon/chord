# 上下文管理

Chord 提供两层互补的上下文管理机制：**上下文压缩（Compaction）**调用 LLM 生成摘要并持久化改写会话历史，**上下文剪裁（Reduction）**在每次请求前用启发式规则裁剪 prompt。两者分别作用于持久化历史和单次请求，各司其职。

两者都通过 `config.yaml` 顶层的 `context:` 配置。配置文件本身的组织方式（文件、层级、provider 等）见[配置与认证](./configuration_CN.md)。

## 对比速览

| 特性 | 上下文压缩（Compaction） | 上下文剪裁（Reduction） |
|------|-------------------------|------------------------|
| 做了什么 | 调用 LLM 生成结构化摘要，归档旧历史，用摘要替代原文 | 按规则裁剪本次请求中过时的工具输出 |
| 是否落盘 | ✅ 改写 session 文件 | ❌ session 文件不变 |
| 是否调用模型 | ✅（可配置专用模型池） | ❌（纯启发式规则） |
| 触发时机 | 达到阈值且即将发起下一次主模型请求 / 手动 `/compact` / 异常恢复 | 每次 LLM 请求前自动执行 |
| 典型耗时 | 数秒到数十秒（需等待 LLM 回复） | 毫秒级（内存内规则匹配） |
| 用户感知 | TUI 显示"Compacting context..."进度 | 无感知（静默） |
| loop 模式 | 启用；压缩仍可运行，让长会话继续推进 | 新增消息禁用；详见 [Loop 模式与 Codex 额度冻结](#loop-模式与-codex-额度冻结) |

**两者的关系**：Reduction 是轻量级的第一道防线——每次请求前自动裁剪过时的工具输出，减缓上下文膨胀速度。当 Reduction 仍不够、上下文持续增长到 Compaction 阈值时，Compaction 启动做深度压缩。大多数用户只需关注 Compaction 配置；Reduction 的默认值已经适配常见场景，通常无需调整。

Compaction 在把历史交给摘要模型之前，会先对其应用一次 Reduction 规则以节省摘要调用的开销，并且遵循你配置的 Reduction 参数：调高了保留阈值的会话，其持久摘要也会基于保留更多的输入生成。归档到 `history-N.md` 的原文不受此影响，始终是完整无损的。

自动压缩主要由 provider 返回的输入 usage 触发。请求级剪裁可能让当前 prompt 变小，但剪裁后得到的本地估算不会取消已经由 provider usage 触发的压缩请求。如果 provider 或网关后续不再返回 usage（或返回 `input_tokens: 0`），Chord 会用最近一次可信的非零 usage 样本和当前会进入上下文的消息 bytes 做保守比例估算，作为同一个自动压缩阈值的兜底信号。

主模型正常以 `stop` 结束时，即使刚刚达到阈值，Chord 也不会因为回到 idle 就立刻新建一轮自动压缩。它会保留自动压缩请求，等到下一个 continuation barrier、准备发起主模型请求时再启动压缩。压缩和主模型请求可以并行；如果请求因上下文超限而挂起，Chord 会等压缩应用后再恢复。如果 stop 之前已经有压缩在后台运行，Chord 不会取消它；压缩生成的 draft 仍会在下一个安全的 continuation/idle barrier 应用。

## 上下文压缩（Compaction）

当主会话上下文使用量接近模型上限时，Chord 会准备自动压缩，并在准备下一次主模型请求时启动。压缩过程调用 LLM 分析当前对话，生成结构化摘要（目标、进度、关键决策、文件证据等），归档旧消息，用摘要替换对话历史。压缩结果持久保存到磁盘，会话文件体积显著缩小。

每个 checkpoint 开头都有一段 **session anchors（会话锚点）**：会话最初的请求，以及从你的纠正中提取的长期约束。压缩是递归的——每一轮都会把上一个 checkpoint 当普通历史重新总结——所以凡是交给摘要模型自行决定是否复述的内容，每轮都会衰减一点。锚点不参与这个过程：它们从上一个 checkpoint **原样拷贝**而不是重新生成，并且提示词明确要求摘要模型不要复述、也不得与之矛盾。约束列表有上限，超出时保留最早的若干条（通常是项目级基本规则）和最新的若干条，丢弃中间部分。
会话后来推翻的约束不会被悄悄丢弃：最新的指令会取代旧约束，被取代的条目仍以 `~` 前缀留在锚点块里，模型能看到方向的改变。你在普通消息里明确声明的约束——例如「保持现有 API 行为不变」——与命令式纠正享有同样的锚点地位，因为它们同样是需要跨多轮压缩存活的长期指令，否则每压缩一轮就衰减一点。checkpoint 还会把每个归档的 `history-N.md` 文件连同其内容主题列成一张**历史地图**，需要原文时模型可以直接用 read 工具读取对应归档，而不用猜该打开哪个文件。

面向继续执行的压缩会在 checkpoint 后原样保留一个安全的最近尾部。它优先按完整用户轮次保留（通常是最近两轮），token 预算约为上下文窗口的 5%；当单个用户轮次也超出预算时——这在该轮带完整工具循环时是常态——会退回到"能装下的最长安全后缀"，而不是整个放弃尾部。工具调用与结果不会被拆开；若短会话保留尾部后不足以形成有效摘要，Chord 会安全退回到压缩完整 head。显式 `archival` profile 不保留原始尾部——checkpoint 即压缩后的全部上下文。

### 保留最近消息

除此之外，每个 checkpoint 都会把被归档 head 中最新的**真实用户消息**原样嵌入自身——若会话恰好结束在一条被中断的 assistant 回复上，这条未完成的回复片段也会一并保留——放在 checkpoint 内的 `## Retained Recent Messages` 段，受 `retain_recent_tokens` 这个估计 token 预算约束（内置默认 4096）。继续执行的 profile 会把最近几轮作为原始消息保留在 checkpoint 之后，保留段覆盖的正是它们前面的消息；`archival` profile 没有原始尾部、其余内容只剩摘要，保留段就是最新指令唯一的原文残留。保留段不替代摘要，只把最新的指令边界钉在上下文里，让续写不必先重读归档就能接上。

checkpoint 恢复的关键文件是每次请求从磁盘现读的 request-local overlay；每个 `<file>` 块都会带 SHA-256 revision，以及相对该 checkpoint 首次注入是否已变化的标记。该 overlay 只在稳定剪裁 surface 记录完成后注入，因此不会进入前缀兼容性检查，也不会让增量剪裁复用失效。

**最小配置**（启用自动压缩）：

```yaml
context:
  compaction:
    threshold: 0.8
    model_pool: compact
```

**配置字段说明**：

| 字段 | 类型 | 默认 | 说明 |
|------|------|------|------|
| `threshold` | 浮点数 | `0.8` | 触发自动压缩的上下文使用率阈值。取值 `0` ~ `1`，例如 `0.8` 表示用量达到可用输入预算的 80% 时触发；设为 `0` 可关闭自动压缩。超出 `0` ~ `1` 范围的值（负数、大于 `1`，或 NaN/±Inf）会被拒绝并回退到内置默认值。 |
| `model_pool` | 字符串 | 克隆当前 agent 模型池 | 执行压缩的专用模型池名。**优先选大上下文窗口，而不是单纯选便宜**：摘要输入会被裁剪到压缩模型自身的窗口内，且**从最早的归档消息开始丢**，小窗口模型会让摘要看不到会话是怎么开始的。理想选择是"窗口大且快而便宜"的模型。 |
| `reserved` | 整数 | `0` | 在 `threshold` 留出的比例余量之外，再为 tokenizer 误差、工具 schema 开销、压缩恢复安全等保留的固定 token 余量。通常建议省略（保持 `0`）；非零值会先从输入预算中扣除，再应用 `threshold`。 |
| `preset` | 字符串 | 自动检测 | 强制指定压缩实现方式，一般无需设置。 |
| `profile` | 字符串 | `auto` | 压缩策略，一般无需设置。 |
| `reminder` | 浮点 | `0`（派生） | 上下文压力提醒线（usage 比例）。`0` 表示按 `min(0.60, threshold × 0.90)` 派生；非零值作为提醒线使用。usage 达到 `min(reminder, threshold)`（任一先到）即触发提醒——reminder 设在 `threshold` 之上时，threshold 越线本身就会触发提醒（压缩在越线当次启动，提醒与压缩开始同一次请求）。`threshold: 0` 时提醒一并禁用；提醒无法单独关闭——自动压缩开启时没有只关提醒的选项，只能调高提醒线，或用 `threshold: 0` 一并关闭。超出 `0` ~ `1` 范围的值（负数、大于 `1`，或 NaN/±Inf）会被拒绝并回退到派生默认值。 |
| `model_driven` | 布尔 | `false` | 实验性开关：给主 agent 暴露 `compact_context` 工具，让模型在工作状态充分外化（写入文件或结构化参数）后主动请求 durable context checkpoint。checkpoint 不调用摘要模型，在工具批次收口后的 barrier 处原子应用并暂停下一次主模型请求，随后在同一 turn 的压缩上下文上继续。工具仅 MainAgent 可见、必须单独调用、`state_files` 只作路径引用不读取。低收益请求会被自动跳过。默认关闭。 |
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
`reminder`；模型级 `threshold: 0` 只对该模型禁用自动压缩（提醒一并禁用）。

自动压缩阈值首次越线时，Chord **立即启动** usage-driven 压缩：它在后台异步运行，在下一个 continuation barrier 应用——越线的那次请求与它并行继续执行。请求面的 reminder 只在启用 `model_driven`（`compact_context` 可见）时注入；关闭时自动压缩完全由运行时接管——与 Codex 的 local / remote 两条压缩路径一致，它们从不通知工作模型——会话只是继续跑，直到压缩在 barrier 应用。提醒只在会话继续发出主请求时生效——越线后若 turn 正好收尾，usage-driven 压缩走既有的 end-of-turn 路径。真正与压缩启动并行的那次请求才会附带一次性外化提示（同样仅限 `model_driven` 启用时）；provider 拒绝（oversize）仍然立即强制压缩。切换模型会套用新模型的 per-model 阈值并开启新的提醒窗口。

### 模型驱动上下文 checkpoint（实验性）

设置 `context.compaction.model_driven: true` 后，主 agent 获得 `compact_context` 工具。模型在状态充分外化后单独调用它（同一响应里不能有其他工具调用）——后续需要的事实要么写在 `state_files` 指出的文件里，要么完整表达在 `active_objective` / `completed` / `decisions` / `open_issues` / `next_step` 结构化参数中。runtime 校验请求，等工具批次收口后：

1. 快照对话并归档 head（不调用摘要模型，checkpoint 由确定性构造）；
2. 当预计收益低于保守门槛（2048 tokens 且占 prepared surface 的 10%，可缓存会话还会扣除 prompt-cache 重写成本）时拒绝 reset；距上次成功 apply 不足 3 个主模型请求批次时同样会跳过；
3. 原子应用 checkpoint，快照后追加的内容作为 live tail 保留，并在压缩后的上下文上继续同一 turn。

自动压缩不会把模型的 checkpoint 锁死。当 usage-driven 压缩已在运行（threshold 越线启动了后台 worker，或 draft 已 ready、正在等 continuation barrier）时，与它并行的那次请求仍可提交 `compact_context`。模型是自己挑的边界，所以它的 checkpoint 优先：runtime 丢弃自动 draft，改应用模型 checkpoint。自动压缩是兜底而不是锁——threshold 越线不会夺走正在收尾的模型的 reset 机会。一次性外化提示不会提及这种覆盖（模型不需要知道有自动压缩在跑，只需要知道当前上下文即将结束）；模型之前主动提交的 checkpoint 照常工作。

skip 是正常的策略结果：立即用相同请求重试会被短暂冷却，结果不会改变——模型应等待或继续推进。上下文用量接近自动压缩阈值时，下一次请求可能附带一次性压力提醒：它直接告诉模型为压缩做准备（当前阶段已收口就单独调用 `compact_context`，否则随阶段把 findings 和决定写进本角色可写的项目文件，如 `.chord/notes/` 下的任务笔记或 `.chord/plans/` 下的计划文档），不再引用还剩多少空间。usage-driven 压缩在 threshold 越线当次即启动，与压缩并行的那次请求会附带一次性外化提示。这两个 overlay 都用 `<system-reminder>` 块包裹——与其他所有 harness 注入的运行时消息同一约定——让模型能区分它们和用户写的内容（内存压力信号类研究，如 MemGPT，正是以 system 消息注入这类提醒）。它们都只在启用 `model_driven` 时注入——关闭时模型没有任何外化契约，注入只会变成无法执行的噪音。它们是瞬态的，不会进入对话历史。

启用 `model_driven` 时，主 agent 的系统提示词还会附带一段简短被动的 `Long-session context management` 指引：开头声明 `<system-reminder>` 包裹的消息是 harness 注入的运行时状态（绝非用户所写）且具权威性；随后要求随阶段收口把关键发现和决定写入本角色可写的项目文件——如 `.chord/notes/` 下的任务笔记或 `.chord/plans/` 下的计划文档——让它们能在后续 checkpoint 后存活，只在真正的阶段边界单独调用 `compact_context`，checkpoint 应用后需要精确历史时去读归档的 history 文件。SubAgent 永远不会收到这段指引或该工具。该指引是建议性的，不是强制流程。

压缩是递归的：下一次自动摘要写在一段以 checkpoint 开头的历史之上。会话锚点（原始请求、standing constraints）逐字前向携带，前一个 checkpoint 的结构化正文也一样——摘要模型始终把它作为受保护的输入段收到，应用后的 checkpoint 还会把它逐字追加为 `## Previous Checkpoint` 段。因此 checkpoint 的结构化内容（目标、决策、未决问题、下一步……）从不依赖摘要模型恰好复述它，链式压缩也无法一次摘要一点地侵蚀它。

`state_files` 只是路径引用：Chord 从不读取或注入这些文件，因此该工具无法绕过 Read 权限。checkpoint 的 `Current User Request` 永远来自你的真实消息，不会采用模型参数。工具 success 只表示请求被接受；之后出现的 model-driven `[Context Summary]` checkpoint 才表示 reset 已应用。请求被跳过或失败时会继续使用旧上下文，usage-driven 自动压缩兜底保持生效。

可观测性：TUI 状态栏会把模型请求的 checkpoint 与 usage-driven 压缩区分开显示（「model checkpoint」），并在跳过/失败时短暂展示原因；`/stats` 新增「Context Compaction」分区，按 stage 和 trigger 统计生命周期事件（如 `applied/model_driven`、`skipped/model_driven`），方便观察模型请求重置的频率与实际应用情况。

### 触发阈值如何计算

以**可用输入预算**为基准。若模型配置了 `limit.input`，以此为准；否则按 `limit.context - 有效请求输出`（其中有效输出取 `max_output_tokens` 与模型 `limit.output` 的较小值）推导。若设置了 `reserved`，再从预算中扣除。因此实际触发点为 `(输入预算 - reserved) × threshold`；`reserved` 会和 `threshold` 未使用的比例余量叠加，而不是替代它。TUI 信息面板和底部栏的 `Context` 百分比使用扣除 `reserved` 后的同一输入预算基准，与自动压缩阈值保持对齐。对于会单独报告 prompt cache 写入的 provider，Chord 会把当前 prompt 侧用量按 `input_tokens + cache_write_tokens` 计算，因此新写入缓存的 prompt 片段也会计入显示的上下文负担。

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

当 provider 同时公布"总上下文窗口"和"单独的输入上限"时，已知限制则建议三个字段都写明：

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

每次向 LLM 发送请求前，Chord 会用一套确定性规则检查对话中的工具输出，对过时的大段内容做裁剪。**这只影响本次请求的 prompt，不会改写磁盘上的会话文件。**剪裁决策只使用工具输出类型、实际 main-model 请求批次、大小和本地有效性状态；上下文使用率只影响持久压缩（Compaction），不会改变 Reduction surface。

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
- 已剪裁消息会冻结并逐字节复用，避免每轮重写旧 marker。尚未剪裁的非-read 输出会记录下一次可能跨过规则阈值的 request-batch frontier；只有新增、到期、重复调用命中、工具结果门槛跨越或 surface 失效时才重新分类。小尾部复用不能跳过已经到期的 frontier。`read` 仍按 path/range 的 edit/read 有效性定向分析；仍然有效的读取保持完整，直到后续修改或覆盖读取将其标记为 `truncated=stale` / `truncated=superseded`。
- 较老消息形成稳定 surface 后，只分析新增尾部和到期 frontier；如果新增节省低于 `min_incremental_saved_tokens`，复用上一次已剪裁前缀，只追加当前尾部消息。历史 shape、工具 schema、模型、Reduction 配置或 session 改变时会使该 surface 失效。
- 近期高风险工具输出按实际 main-model request-batch age 保护，再进入普通 age/bytes 剪裁。默认 `high_risk_protect_age_turns: 4` 保护失败日志、stack trace、权限/安全输出和当前工作集关键证据；diff/patch 使用独立的 `diff_protect_age_turns: 12`，避免长审查在形成 findings 前过早丢失核心变更证据。同一 assistant 响应中的并行工具调用共享一个批次，不会因为并行结果数量多而提前老化。
- 成功 shell 输出在变旧且超过 `shell_success_bytes` 后按低风险噪音处理，并保留输出大小、行数、有代表性的成功信号行（如有）以及尾部片段 fallback；shell 命令本身仍可从关联的 tool call 中获得。近期失败、stack trace、diff 和 warning 密集的构建日志会先由高风险保护或结构化日志摘要处理；较旧输出在不再受近期高风险窗口保护后，后续仍可能被摘要化。
- 对不在静态只读白名单中的成功 Shell 调用，Chord 会重新核验稳定/恢复前缀里已有 durable hash 的历史读取。确认文件被替换、删除或 hash 改变后，旧读取会标记为 `truncated=stale`；无法读取的路径和没有 durable hash 的旧会话记录不会被猜测为 stale。
- 大块旧工具输出仍会按 age/bytes 规则剪裁，但在退回通用省略前会尽量保留结构化线索：`read` 保留路径与行范围元数据，`grep` / `glob` / LSP references 保留查询范围、受字节预算约束的位置清单和显式省略标记，JSON 输出保留顶层结构和数量，成功 shell 输出保留大小/信号行上下文，diff/patch 保留文件、hunk、变更数量和有界代表行，构建 / 测试日志保留关键失败或警告行。旧错误、diagnostics、确认类输出会被压成固定短 marker 或摘要。
- 剪裁诊断继续保留聚合的 `reread_after_reduction` 计数，并在前后两次读取都有 durable hash 时进一步区分“同 revision 重读”和“revision 已变化后的必要刷新”。
- 重调证据会反馈到保留策略：当模型对“输出已被剪裁”的调用重新发起完全相同的调用（重读、重搜、只读 shell 重跑）时，该 input 的最新输出在本会话余下时间内免于剪裁，并记录 `recalled_input_protect` 跳过原因。较旧的重复副本仍会折叠为 repeated marker；已判定 stale 的读取仍保留 stale 标记；重跑会改变状态的命令（如测试）不获得豁免——那是在求新鲜结果，不是找回被裁内容。豁免集合是会话内存态，随剪裁缓存在恢复或模型切换时丢弃，并从实时证据重建。

### Loop 模式与 Codex 额度冻结

在 loop 模式下，新增消息不会再应用剪裁。如果你在某个 LLM 请求仍在进行时启用 `/loop on`，Chord 会冻结并复用该请求已经准备好的前缀，避免旧历史从“已剪裁形态”翻回完整原始工具输出，从而保持 prompt cache 前缀稳定；loop 期间产生的新消息会保持未剪裁，直到退出 loop 后再恢复普通剪裁策略。切换 loop 模式本身不会新增、删除或重写稳定的 system prompt 文本；否则即使任务上下文没有变化，也会导致 prompt cache 失效。

如果模型显式支持 Chord 的 request-only 动态工具挂载（`compat.chat_completions.mcp_system_tools_message` 或 `compat.responses.mcp_additional_tools`），那么在请求进行中执行 `/loop on` 时，只要当前冻结的顶层工具表面里还没有 `done`，Chord 就可以在下一次 loop 请求里把 `done` 作为一次性的动态工具声明补进去。这个挂载只作用于当前请求，不会改写冻结的顶层工具定义，因此开启 loop 时更容易保住现有 prompt cache 边界；如果冻结工具表面本来已经有 `done`，Chord 不会重复注入。
不支持这两类 request-only 动态工具挂载的模型仍沿用原来的行为：如果启用 loop 需要改动工具表面，后续请求依然可能因为顶层工具定义变化而打断 prompt cache 复用。

当当前主 Agent provider 使用 Codex rate-limit surface，且 5h 或 7d 额度窗口剩余不足 10% 时，Chord 会在连续自动 continuation 中临时冻结完整的 LLM-facing request surface。冻结范围包括请求级剪裁结果、已安装的系统提示词和可见工具定义。这样做是有意的：接近额度耗尽时，Codex 只有在上下文表面不变的情况下，才可能沿着 `stop_reason=tool_call` 链继续执行直到 `end_turn`；如果此时上下文形态变化，可能导致 Codex 在额度用尽后无法继续复用当前会话。冻结会在交互边界解除——例如 Agent 回到 idle，或用户发送真实的新消息——因此 MCP / YOLO 等显式用户切换可以在下一次请求重新构建 surface。如果 key 或运行模型发生变化，Chord 也会允许下一次请求重建 surface，因为之前冻结的 surface 已不再匹配当前 Codex 身份。

### 剪裁规则

Chord 会按工具输出类型和时效分类处理。专门摘要会优先于通用旧结果省略，因此旧的大块输出可以保留高价值结构，同时不会改写持久会话历史。

| 类别 | 典型场景 | 年龄阈值 | 大小阈值 | 设计意图 |
|------|----------|----------|----------|----------|
| 确认/权限 | 工具权限确认、用户授权结果 | `confirm_age_turns`（默认 2 轮后） | — | 权限决策很快过时，可较早裁剪 |
| 错误结果 | 工具执行失败的错误信息 | `error_age_turns`（默认 3 轮后） | — | 失败原因可能仍有参考价值，保留稍久 |
| Shell 成功 / 日志 | 成功命令、构建 / 测试 / lint 日志 | `shell_success_age_turns`（默认 2 轮后——结果在首个可响应它的请求里就已是 age 1，因此新鲜成功输出总能被完整看到恰好一次）；命中 shell 工具只读白名单的命令（`cat`、`ls`、`git log` 等）使用 `shell_read_only_age_turns`（默认 3 轮后） | `shell_success_bytes`（默认 3000 字节以上才剪） | 成功输出通常可重新执行；只读命令是内容获取——相当于没有有效性追踪的 read——因此保护窗口更长（真实会话统计中相同重调的中位间隔约 3 个请求批次）；摘要保留大小、行数、有代表性的成功信号行（如有）以及尾部 fallback，命令仍可从关联 tool call 获取；大日志摘要会保留关键失败 / 警告 |
| Diff / Patch | `git diff`、unified/combined diff、ApplyPatch 文本 | `diff_protect_age_turns`（默认完整保留 12 轮） | 超过 Shell/读取类字节阈值后才摘要 | 保留文件、hunk、变更数量和有界代表行；不会按源码中的 `error`/`failed` 标识符误判为构建日志 |
| 读取类 | `read`、文件内容预览 | 读取本身无年龄门控——已失效/已被覆盖的读取在状态确定后立即渲染有效性标记（陈旧内容在任何年龄都有误导性，因此跳过年龄门控和各保护分支）；其他读取类输出等到 `read_like_age_turns`（默认 2 轮后） | `read_like_output_bytes`（默认 3000 字节以上才剪） | 被后续局部 edit/apply_patch 的修改范围覆盖（或遇到整文件/未知范围修改）的读取会被裁剪并标记 `truncated=stale`；被后续更大范围读取覆盖的标记 `truncated=superseded`（更新副本就在后文）。仍是该内容现行视图的读取**永不裁剪**，与年龄和大小无关——裁掉它只会逼出重读，或让模型基于摘要猜测 |
| 搜索类 | `grep`、`glob`、LSP references | `read_like_age_turns`（默认 2 轮后） | `read_like_output_bytes`（默认 3000 字节以上才剪） | 命中列表可重跑，但多点修改任务真正依赖的是 `path:line` 清单——摘要在字节预算内保留**完整位置清单**（每个命中文件及其行号），只给前几个文件附代表片段，超出预算才显式标注省略 |
| JSON / 结构化输出 | `shell` 或结构化工具返回的 JSON | JSON 文档等到 `stale_age_turns`（默认 3 轮后）——key/item 骨架是信息损失最大的摘要形态，且取值往往跨多个请求；NDJSON 日志流（如 `go test -json`）沿用所在类别的年龄 | 类别对应的大小 gate | 大型结构化内容在通用省略前保留顶层 object key 或 array 数量 |
| 其他旧结果 | 不属于以上类别的旧工具输出 | `stale_age_turns`（默认 3 轮后） | `stale_output_bytes`（默认 1500 字节以上才剪） | 兜底规则，最保守，避免误删不易重建的内容 |

年龄参数说明：

- `*_age_turns` 保留旧配置名，但单位是**实际 main-model 请求批次**。请求在真正 dispatch 给 provider 前分配递增 batch；失败请求也会留下年龄间隔。同一 assistant 响应中的多个并行工具调用及其结果共享一个 batch，因此并行工具只算 1 轮。旧会话没有 batch 元数据时，Chord 会按后续 user/assistant 响应保守回退。
- `*_bytes` 是该类别参与裁剪的**最小输出字节数**。小于此值的输出保留完整内容——短输出不需要裁剪。
- 仍然“现行”的 `read` 输出**永不裁剪**：其展示范围之后没有与局部 edit/apply_patch 的修改范围重叠，文件没有被整体替换或删除,也没有更晚的读取覆盖同一范围。这类输出是模型对该内容唯一的现行视图，裁掉它要么逼出一次多余的重读（额外请求轮次、破坏 prompt cache），要么让模型基于摘要凭空作答。容量压力由持久 Compaction 负责：每条 read 结果本身已受读取工具的单次输出预算约束，保留的读取只会线性增长，达到阈值后由 Compaction 归档。成功的 `edit`/`apply_patch` 调用参数本身已经保留应用后的 delta，因此结果只保留应用摘要和诊断，不重复回显修改文本；旧会话或无法可靠确定修改范围时，仍保守地使该文件的全部读取失效。
- `min_tool_results_prune`（默认 6）是 generic stale-output 兜底路径的**安全门槛**：某条结果即使已经达到这条兜底规则要求的年龄和大小，Chord 也会等到会话中至少有这么多条工具结果后，才应用 generic stale 剪裁，避免小会话过早触发这条最保守的兜底裁剪。像 shell-success、read-like、search-like、JSON、build/log 这类按类别定义的摘要路径，仍按各自的 age/size 规则生效。它不参与 request-batch age 计算。
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

- [配置与认证](./configuration_CN.md) — 配置文件、层级与完整字段速查表
- [使用指南 — `/compact`](./usage_CN.md#常用本地控制命令)
- [性能](./performance_CN.md)
- [常见问题排查](./troubleshooting_CN.md)
