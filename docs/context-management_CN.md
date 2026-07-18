# 上下文管理

Chord 提供两层互补的上下文管理机制：**上下文压缩（Compaction）**调用 LLM 生成摘要并持久化改写会话历史，**上下文剪裁（Reduction）**在每次请求前用启发式规则裁剪 prompt。两者分别作用于持久化历史和单次请求，各司其职。

两者都通过 `config.yaml` 顶层的 `context:` 配置。配置文件本身的组织方式（文件、层级、provider 等）见[配置与认证](./configuration_CN.md)。

## 对比速览

| 特性 | 上下文压缩（Compaction） | 上下文剪裁（Reduction） |
|------|-------------------------|------------------------|
| 做了什么 | 调用 LLM 生成结构化摘要，归档旧历史，用摘要替代原文 | 按规则裁剪本次请求中过时的工具输出 |
| 是否落盘 | ✅ 改写 session 文件 | ❌ session 文件不变 |
| 是否调用模型 | ✅（可配置专用模型池） | ❌（纯启发式规则） |
| 触发时机 | 上下文超过阈值的百分比 / 手动 `/compact` / 异常恢复 | 每次 LLM 请求前自动执行 |
| 典型耗时 | 数秒到数十秒（需等待 LLM 回复） | 毫秒级（内存内规则匹配） |
| 用户感知 | TUI 显示"Compacting context..."进度 | 无感知（静默） |
| loop 模式 | 启用；压缩仍可运行，让长会话继续推进 | 新增消息禁用；详见 [Loop 模式与 Codex 额度冻结](#loop-模式与-codex-额度冻结) |

**两者的关系**：Reduction 是轻量级的第一道防线——每次请求前自动裁剪过时的工具输出，减缓上下文膨胀速度。当 Reduction 仍不够、上下文持续增长到 Compaction 阈值时，Compaction 启动做深度压缩。大多数用户只需关注 Compaction 配置；Reduction 的默认值已经适配常见场景，通常无需调整。

自动压缩主要由 provider 返回的输入 usage 触发。请求级剪裁可能让当前 prompt 变小，但剪裁后得到的本地估算不会取消已经由 provider usage 触发的压缩请求。如果 provider 或网关后续不再返回 usage（或返回 `input_tokens: 0`），Chord 会用最近一次可信的非零 usage 样本和当前会进入上下文的消息 bytes 做保守比例估算，作为同一个自动压缩阈值的兜底信号。

## 上下文压缩（Compaction）

当主会话上下文使用量接近模型上限时，Chord 会自动触发上下文压缩。压缩过程调用 LLM 分析当前对话，生成结构化摘要（目标、进度、关键决策、文件证据等），归档旧消息，用摘要替换对话历史。压缩结果持久保存到磁盘，会话文件体积显著缩小。

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
| `threshold` | 浮点数 | `0.8` | 触发自动压缩的上下文使用率阈值。取值 `0` ~ `1`，例如 `0.8` 表示用量达到可用输入预算的 80% 时触发；设为 `0` 可关闭自动压缩。 |
| `model_pool` | 字符串 | 克隆当前 agent 模型池 | 执行压缩的专用模型池名。建议使用低成本/快速模型以节省开销。 |
| `reserved` | 整数 | `0` | 为 tokenizer 误差、工具 schema 开销、压缩恢复安全等预留的 token 余量。计算触发阈值时先从输入预算中扣除。 |
| `preset` | 字符串 | 自动检测 | 强制指定压缩实现方式，一般无需设置。 |
| `profile` | 字符串 | `auto` | 压缩策略，一般无需设置。 |

### 触发阈值如何计算

以**可用输入预算**为基准。若模型配置了 `limit.input`，以此为准；否则按 `limit.context - 有效请求输出`（其中有效输出取 `max_output_tokens` 与模型 `limit.output` 的较小值）推导。若设置了 `reserved`，再从预算中扣除。TUI 信息面板和底部栏的 `Context` 百分比使用同一输入预算基准，与自动压缩阈值保持对齐。对于会单独报告 prompt cache 写入的 provider，Chord 会把当前 prompt 侧用量按 `input_tokens + cache_write_tokens` 计算，因此新写入缓存的 prompt 片段也会计入显示的上下文负担。

provider usage 是自动触发的权威依据。Chord 不会用请求级剪裁后的本地 token 估算去清除已经触发的自动压缩请求，因为多模态输入、工具 schema、provider/proxy framing 等都可能让本地估算与 provider 统计不一致。唯一的兜底是 usage 缺失场景：Chord 收到可信的非零 `input_tokens` 后，会记录当时会进入上下文的消息 bytes，包括正文、需要回放的 tool-call 参数、thinking blocks 和 reasoning text；如果后续响应缺少 usage 或返回 0，且这些 bytes 已增长，就按比例估算 `input_tokens`，估算值达到 `threshold` 时也会触发自动压缩。这个 byte-calibrated estimate 只用于提前压缩，不用于计费，也不表示精确的上下文窗口用量。

**预留 headroom 示例**：

```yaml
context:
  compaction:
    threshold: 0.8
    reserved: 16000
```

以模型 `input: 272000` 为例，扣除 `reserved` 后可用预算为 `256000`，当上下文达到 `256000 × 0.8 = 204800` tokens 时触发自动压缩。设置合理的 `reserved` 可避免由于 tokenizer 计算误差或工具描述开销导致压缩触发偏晚。

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

每次向 LLM 发送请求前，Chord 会用一套确定性规则检查对话中的工具输出，对过时的大段内容做裁剪。**这只影响本次请求的 prompt，不会改写磁盘上的会话文件。**剪裁决策只使用本地可确定信号（消息数、本地 token 估算、模型输入预算、工具输出 age/bytes），不会依赖 provider 是否返回 prompt cache 命中 token。

剪裁默认启用，通常无需逐项配置，只写以下任一形式即可使用默认参数：

```yaml
context:
  reduction: true
```

```yaml
context:
  reduction: {}
```

不支持 `context.reduction: false`；不写 `context.reduction`，或使用 `true` / `{}`，都会保留默认的请求级剪裁行为。

全部字段及默认值：

```yaml
context:
  reduction:
    confirm_age_turns: 2
    error_age_turns: 3
    high_risk_protect_age_turns: 4
    shell_success_age_turns: 1
    shell_success_bytes: 3000
    read_like_age_turns: 1
    read_like_output_bytes: 3000
    stale_age_turns: 3
    stale_output_bytes: 1500
    wrap_up_grace_requests: 1
    min_tool_results_prune: 6
    min_incremental_saved_tokens: 2048
    high_pressure_usage: 0.80
    force_prune_usage: 0.90
```

未设置或非正数的字段使用以上默认值。项目级 `.chord/config.yaml` 可按字段覆盖全局配置。

> **大多数用户不需要配置这一节。** 内置默认值偏保守，已适配常见场景。基于本地真实会话的统计，剪裁能带来可观节省，且没有系统性破坏 prompt cache 复用；想往某个方向调整时，参考下面的调参思路表。

### 默认行为

- Chord 会在每次 main-model 请求前执行轻量请求级剪裁；普通 prompt-cache 热身不会保护本来可剪裁的工具输出。
- 当 `todo_write` 把所有 TODO 标为 completed/cancelled 后，Chord 会把下一次 main-model 请求视为收尾请求。默认 `wrap_up_grace_requests: 1` 只会在同模型、没有排队用户输入、上下文非高压、且重新估算的节省低于 `min_incremental_saved_tokens` 时，避免低收益的最终 prompt surface 抖动。如果已有稳定的已剪裁前缀，收尾请求会复用该已剪裁前缀，而不是把旧工具输出恢复成原文。用户新提问、模型切换、上下文高压力或可观节省会恢复正常剪裁。
- 较老消息冻结复用：同一 turn 内形成稳定的**已剪裁** surface 后，低压力下只估算新增尾部；如果新增尾部低于 `min_incremental_saved_tokens`，复用上一次已剪裁前缀，只追加当前尾部消息，避免每轮重新扫描历史并减少 prompt surface 抖动。未剪裁前缀不会被用来绕过剪裁。
- 高压力立即剪裁：估算输入达到 `high_pressure_usage` 后不再套用小增量 hysteresis；达到 `force_prune_usage` 后优先控制上下文体积。
- 近期高风险工具输出会优先按真实 user-turn age 保护，再进入普通 age/bytes 剪裁。默认 `high_risk_protect_age_turns: 4` 会把 diff/patch、失败日志、stack trace、权限/安全输出和当前工作集关键证据完整保留约 4 个用户轮次。这是主要的成本/正确性权衡旋钮：调低能让更早轮次的高风险输出提前进入保守摘要，而当前用户轮刚产生、模型正要继续使用的高风险输出始终完整保留。
- 成功 shell 输出在变旧且超过 `shell_success_bytes` 后按低风险噪音处理，并保留输出大小、行数、有代表性的成功信号行（如有）以及尾部片段 fallback；shell 命令本身仍可从关联的 tool call 中获得。近期失败、stack trace、diff 和 warning 密集的构建日志会先由高风险保护或结构化日志摘要处理；较旧输出在不再受近期高风险窗口保护后，后续仍可能被摘要化。
- 大块旧工具输出仍会按 age/bytes 规则剪裁，但在退回通用省略前会尽量保留结构化线索：`read` 保留路径与行范围元数据，`grep` / `glob` / LSP references 保留查询范围和代表命中，JSON 输出保留顶层结构和数量，成功 shell 输出保留大小/信号行上下文，构建 / 测试日志保留关键失败或警告行。旧错误、diagnostics、确认类输出会被压成固定短 marker 或摘要。

### Loop 模式与 Codex 额度冻结

在 loop 模式下，新增消息不会再应用剪裁。如果你在某个 LLM 请求仍在进行时启用 `/loop on`，Chord 会冻结并复用该请求已经准备好的前缀，避免旧历史从“已剪裁形态”翻回完整原始工具输出，从而保持 prompt cache 前缀稳定；loop 期间产生的新消息会保持未剪裁，直到退出 loop 后再恢复普通剪裁策略。切换 loop 模式本身不会新增、删除或重写稳定的 system prompt 文本；否则即使任务上下文没有变化，也会导致 prompt cache 失效。

当当前主 Agent provider 使用 Codex rate-limit surface，且 5h 或 7d 额度窗口剩余不足 10% 时，Chord 会在连续自动 continuation 中临时冻结完整的 LLM-facing request surface。冻结范围包括请求级剪裁结果、已安装的系统提示词和可见工具定义。这样做是有意的：接近额度耗尽时，Codex 只有在上下文表面不变的情况下，才可能沿着 `stop_reason=tool_call` 链继续执行直到 `end_turn`；如果此时上下文形态变化，可能导致 Codex 在额度用尽后无法继续复用当前会话。冻结会在交互边界解除——例如 Agent 回到 idle，或用户发送真实的新消息——因此 MCP / YOLO 等显式用户切换可以在下一次请求重新构建 surface。如果 key 或运行模型发生变化，Chord 也会允许下一次请求重建 surface，因为之前冻结的 surface 已不再匹配当前 Codex 身份。

### 剪裁规则

Chord 会按工具输出类型和时效分类处理。专门摘要会优先于通用旧结果省略，因此旧的大块输出可以保留高价值结构，同时不会改写持久会话历史。

| 类别 | 典型场景 | 年龄阈值 | 大小阈值 | 设计意图 |
|------|----------|----------|----------|----------|
| 确认/权限 | 工具权限确认、用户授权结果 | `confirm_age_turns`（默认 2 轮后） | — | 权限决策很快过时，可较早裁剪 |
| 错误结果 | 工具执行失败的错误信息 | `error_age_turns`（默认 3 轮后） | — | 失败原因可能仍有参考价值，保留稍久 |
| Shell 成功 / 日志 | 成功命令、构建 / 测试 / lint 日志 | `shell_success_age_turns`（默认 1 轮后） | `shell_success_bytes`（默认 3000 字节以上才剪） | 成功输出通常可重新执行；摘要保留大小、行数、有代表性的成功信号行（如有）以及尾部 fallback，命令仍可从关联 tool call 获取；大日志摘要会保留关键失败 / 警告 |
| 读取类 | `read`、文件内容预览 | `read_like_age_turns`（默认 1 轮后） | `read_like_output_bytes`（默认 3000 字节以上才剪） | 文件内容可随时重新读取；摘要保留路径和请求 / 实际显示范围 |
| 搜索类 | `grep`、`glob`、LSP references | `read_like_age_turns`（默认 1 轮后） | `read_like_output_bytes`（默认 3000 字节以上才剪） | 命中列表可重跑；摘要保留范围、数量和代表命中 |
| JSON / 结构化输出 | `shell` 或结构化工具返回的 JSON | 先走类别 gate，再退回旧结果兜底 | 类别对应的大小 gate | 大型结构化内容在通用省略前保留顶层 object key 或 array 数量 |
| 其他旧结果 | 不属于以上类别的旧工具输出 | `stale_age_turns`（默认 3 轮后） | `stale_output_bytes`（默认 1500 字节以上才剪） | 兜底规则，最保守，避免误删不易重建的内容 |

年龄参数说明：

- `*_age_turns` 是**等效年龄**阈值。工具结果会因为后续出现新的用户轮次而变旧，也会因为同一个用户轮次内继续产生很多后续 assistant/tool 消息而变旧。实现上，Chord 会取“该结果之后经过的用户轮次”和“后续消息进展折算出的等效轮次”两者的较大值。例如 `read_like_age_turns: 2` 会比 `1` 多保留一个等效轮次；如果同一轮里后续工具调用已经足够多，它仍可能在同一轮内被裁剪。
- `*_bytes` 是该类别参与裁剪的**最小输出字节数**。小于此值的输出保留完整内容——短输出不需要裁剪。
- `min_tool_results_prune`（默认 6）是 generic stale-output 兜底路径的**安全门槛**：某条结果即使已经达到这条兜底规则要求的年龄和大小，Chord 也会等到会话中至少有这么多条工具结果后，才应用 generic stale 剪裁，避免小会话过早触发这条最保守的兜底裁剪。像 shell-success、read-like、search-like、JSON、build/log 这类按类别定义的摘要路径，仍按各自的 age/size 规则生效。它不控制“后续消息进展”如何折算为等效年龄。
- `wrap_up_grace_requests`（默认 1）在 `todo_write` 报告所有 TODO completed/cancelled 后保护下一次 main-model 请求。它按 LLM 请求次数计数，不按用户轮次计数；如果模型已切换或上下文已进入高压力，则跳过该保护，因为旧 prompt cache 很可能不可用，或上下文安全更重要。
- 近期高风险输出不受上述阈值限制：在真实用户轮次还不足 `high_risk_protect_age_turns` 时，看起来像 diff、失败断言、stack trace、权限/安全错误的结果会保持完整，即使同一轮内后续工具调用本会让它们达到裁剪条件。该保护只按真实用户轮次计数，不计入等效年龄中的“消息进展”部分。

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
| 文件内容经常需要回头查阅 | 调高 `read_like_age_turns`（如 `3`）和 `read_like_output_bytes`（如 `8000`） |
| TODO 完成后的最终回复因 prompt cache 被扰动而更贵 | 保持 `wrap_up_grace_requests: 1`；只有当你的流程通常在 TODO 完成后还会多一次验证请求时才考虑设为 `2` |
| 工具输出都很重要不想丢 | 整体调高各 `*_age_turns` 和 `*_bytes` |

## 相关文档

- [配置与认证](./configuration_CN.md) — 配置文件、层级与完整字段速查表
- [使用指南 — `/compact`](./usage_CN.md#常用本地控制命令)
- [性能](./performance_CN.md)
- [常见问题排查](./troubleshooting_CN.md)
