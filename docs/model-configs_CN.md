# 模型配置速查

当你已经确定要用哪一类 provider / model，只想要一段可复制的起始配置时，用这一页。字段语义和完整 schema 仍以[配置与认证](./configuration_CN.md)为准；完整的多文件工作站 / 团队布局示例见[配置示例](./examples/index_CN.md)。

> **按模型调压缩。** 本页每一份 recipe 都是把模型接进 `model_pools` /
> `providers` 的接线配置。想按模型分别调整上下文自动压缩，在
> 模型自身定义或模板上加 `compaction` 块即可（详见[上下文压缩](./context-management_CN.md#上下文压缩compaction)）：
>
> ```yaml
> model_templates:
>   luna-full-window: &luna-full-window
>     limit: {context: 1050000, output: 128000}   # 官方全窗口：不写 input
>     compaction: {threshold: 0.25, reminder: 0.2}   # 把用量留在 272K 长上下文计价档之下
>
> providers:
>   openai:
>     models:
>       gpt-5.6-luna: *luna-full-window
> ```
>
> 没有 `compaction` 块的模型继承全局 `context.compaction.threshold`；
> `reminder` 未设置时按 `min(0.60, threshold × 0.90)` 派生；`reminder: -1`
> 则只关闭该模型的压力提醒，自动压缩保持开启。这两个字段调
> 的是 usage-driven 自动压缩路径，**无论是否启用 `model_driven` 都生效**。
> 本页给出的建议把模型的 `threshold` 调到可靠工作窗口的**上沿**（压缩把
> 上下文维持在该区间内），需要时可把 `reminder` 设在它下方一点。某模型的
> 长上下文可靠性没有依据可写时，省略 `compaction` 块、让它用全局默认即可。

## OpenAI Responses 兼容接口：GPT-5.4 / GPT-5.5 / GPT-5.6

GPT-5.6 片段默认使用 Codex 档位配额（`1000000` context / `872000` input /
`128000` output——2026-09 服务端档位，872K 输入 + 128K 输出 = 1M），因为许多
Responses 中转开放的是 Codex 受限窗口，而不是完整的 OpenAI API 窗口。账号或
中转若仍是旧档位，回落 `400000 / 272000 / 128000`。如果账号或网关明确支持
GPT-5.6 完整 API 窗口，可按下方说明手动启用 1.05M 上下文。价格块使用
OpenAI API 费率；中转收费不同时需要自行覆盖。Codex OAuth 使用下方单独的
preset 配置。使用 API key 的 provider 需要在
`~/.config/chord/auth.yaml` 中配置同名条目：

```yaml
openai:
  - "$OPENAI_API_KEY"
```

### GPT-5.4

```yaml
providers:
  openai:
    type: responses
    api_url: https://api.openai.com/v1/responses
    supported_service_tiers: [fast, slow]
    models:
      gpt-5.4:
        limit:
          context: 1050000
          input: 950000
          output: 128000
        cost:
          input: 2.5
          output: 15
          cache_read: 0.25
        reasoning:
          summary: auto
        variants:
          high:
            reasoning:
              effort: high
          xhigh:
            reasoning:
              effort: xhigh
        modalities:
          input: [text, image, pdf]

model_pools:
  default:
    - openai/gpt-5.4@high
```

验证：

```bash
chord doctor models --model openai/gpt-5.4@high
```

### GPT-5.5

```yaml
providers:
  openai:
    type: responses
    api_url: https://api.openai.com/v1/responses
    supported_service_tiers: [fast, slow]
    models:
      gpt-5.5:
        limit:
          context: 400000
          input: 272000
          output: 128000
        cost:
          input: 5
          output: 30
          cache_read: 0.5
        reasoning:
          summary: auto
        variants:
          high:
            reasoning:
              effort: high
          xhigh:
            reasoning:
              effort: xhigh
        modalities:
          input: [text, image, pdf]

model_pools:
  default:
    - openai/gpt-5.5@high
```

验证：

```bash
chord doctor models --model openai/gpt-5.5@high
```

### GPT-5.6 alias（`gpt-5.6` → Sol）

5.6 三个档位共用相同的窗口、reasoning、variants 和 modalities，所以公共
内容收进 `&gpt-5-6-base` anchor，各档位只需添加自己的 `cost` 块（只维护
永久牌价，限时促销价不在这里维护）。

```yaml
model_templates:
  gpt-5.6-base: &gpt-5-6-base
    # Codex 订阅 2026-09 服务端档位：872K input + 128K output = 1M
    # （max_context_window=872000）。账号/中转仍是旧档位时回落
    # 400000/272000/128000；直连官方 API 用后面的 full-window 模板。
    limit:
      context: 1000000
      input: 872000
      output: 128000
    reasoning:
      effort: medium
      summary: auto
    variants:
      low:
        reasoning:
          effort: low
      high:
        reasoning:
          effort: high
      xhigh:
        reasoning:
          effort: xhigh
      max:
        reasoning:
          effort: max
    modalities:
      input: [text, image, pdf]

providers:
  openai:
    type: responses
    api_url: https://api.openai.com/v1/responses
    models:
      gpt-5.6:
        <<: *gpt-5-6-base
        cost:
          input: 5
          output: 30
          cache_read: 0.5
          cache_write: 6.25
          input_tiers:
            - above_input_tokens: 272000
              input: 10
              output: 45
              cache_read: 1
              cache_write: 12.5

model_pools:
  default:
    - openai/gpt-5.6@high
```

如果你要固定价格 / 行为，直接改用明确模型 ID：

### GPT-5.6 Sol

```yaml
providers:
  openai:
    type: responses
    api_url: https://api.openai.com/v1/responses
    models:
      gpt-5.6-sol:
        <<: *gpt-5-6-base
        cost:
          input: 5
          output: 30
          cache_read: 0.5
          cache_write: 6.25
          input_tiers:
            - above_input_tokens: 272000
              input: 10
              output: 45
              cache_read: 1
              cache_write: 12.5
```

### GPT-5.6 Terra

```yaml
providers:
  openai:
    type: responses
    api_url: https://api.openai.com/v1/responses
    models:
      gpt-5.6-terra:
        <<: *gpt-5-6-base
        cost:
          input: 2
          output: 12
          cache_read: 0.2
          cache_write: 2.5
          input_tiers:
            - above_input_tokens: 272000
              input: 4
              output: 18
              cache_read: 0.4
              cache_write: 5
```

### GPT-5.6 Luna

```yaml
providers:
  openai:
    type: responses
    api_url: https://api.openai.com/v1/responses
    models:
      gpt-5.6-luna:
        <<: *gpt-5-6-base
        cost:
          input: 0.2
          output: 1.2
          cache_read: 0.02
          cache_write: 0.25
          input_tiers:
            - above_input_tokens: 272000
              input: 0.4
              output: 1.8
              cache_read: 0.04
              cache_write: 0.5
```

要点：

- GPT-5.6 示例对 Codex 订阅用 `1000000 / 872000 / 128000`（2026-09 服务端
  档位：872K input + 128K output = 1M，对应 `max_context_window=872000`）；
  账号/中转若仍是旧档位则回落 `400000 / 272000 / 128000`。
- 使用 OpenAI 官方 API，或已确认网关开放完整 API 窗口时，把 `context`
  改成 `1050000` 并删除 `input`。Chord 按 `context` 减去模型声明的
  `limit.output` 推导可用输入预算（此模板为 128000，得 922000）；只有模型
  未声明 `limit.output` 时才回退到默认 64000 输出上限。不要继续保留
  `input: 272000`：超过 272K 是长上下文计价阈值，不是完整 API 的输入硬上限。
- `gpt-5.6` 当前会解析到 Sol，因此它的 `cost` 应按 Sol 费率填写。
- GPT-5.6 API 可用的 reasoning effort 包括 `none`、`low`、`medium`、`high`、`xhigh`、`max`。
- Responses 在启用 reasoning 时默认使用 `reasoning.summary: auto`；如果不希望 Chord 请求可读 reasoning 摘要，请显式配置 `reasoning.summary: none`。
- Chord 当前尚未暴露 GPT-5.6 的 `reasoning.mode: pro`。

验证：

```bash
chord doctor models --model openai/gpt-5.6@max
```

#### GPT-5.6 的压缩调优

先分清两件事再定阈值：**模型跑在哪个窗口**——Codex/中转的 272K 输入
配额，还是官方 API 的 1.05M 全窗口——以及**阈值为什么调**：保质量、
避开 272K 长上下文计价档，还是把窗口当容量用。同一比例在两个窗口下的
触发点相差近 4 倍，针对一个窗口调出来的配方不能直接搬给另一个。

**长上下文质量**（OpenAI 公布的 MRCR v2 8-needle 数据）：Sol/Terra 在
256K–512K 段保持 91.5% / 89.6%，到 512K–1M 段降到 73.8% / 72.5%；Luna
两段都是 41.3%——是悬崖而不是缓坡。区间是平均值，只能当"质量大致从哪
里开始下滑"的粗略参照，不能当精确拐点用。

**计费**（官方 OpenAI API）：prompt 输入**超过** 272K（正好 272000 不算）
时，**整次请求**按长上下文费率计费——输入 / 缓存读取 / 缓存写入都是 2
倍、输出 1.5 倍，不是只对超出部分计价。中转和 Codex OAuth 自己定价，这
条不一定适用。Chord 的费用统计按完整 prompt 选档，但自动压缩不认识价格
档：它只按用量比例触发，所以"请求不超过 272K"是调参目标，不是保证——
触发比较的是上一次 provider 返回的 usage，一次大工具结果就可能把下一次
请求推过线，启用 `model_driven` 时宽限期还会让越线后的请求照常发出。给
272K 线留点余量；另外每次压缩都要调用摘要模型并丢失原始上下文，阈值压
得过低省下的输入费可能还抵不上压缩开销。

##### 官方 API 全窗口（1.05M）

删掉 `input`，可用输入预算由 `context` 减去模型声明的 `limit.output` 推出：
此模板声明 `output: 128000`，得 922K，与全局请求输出上限（默认 64K）无关
——只有模型未声明 `limit.output` 时才预留默认 64000。272K 计价线约占预算
的 29%，所以成本优先档落在 0.2 区间，不是笔误。

成本优先（Sol/Terra/Luna 共用：把用量留在 272K 计价档内，同时避开 Luna
的 256K+ 崩塌区）：

```yaml
model_templates:
  gpt-5.6-full-cost: &gpt-5.6-full-cost
    <<: *gpt-5-6-base
    limit:
      context: 1050000      # 官方全窗口：不写 input
      output: 128000
    compaction:
      threshold: 0.25       # 约 231K–247K 触发，低于 272K 计价线
      reminder: 0.2
```

质量优先（Sol/Terra；Luna 没有可以瞄准的强长上下文区段）：

```yaml
model_templates:
  gpt-5.6-sol-quality: &gpt-5.6-sol-quality
    <<: *gpt-5-6-base
    limit:
      context: 1050000
      output: 128000
    compaction:
      threshold: 0.55       # 约 507K–542K 触发；0.5–0.65 都合理
```

上面示例里的 `reminder` 可以省略：省略时按 `min(0.60, threshold × 0.90)`
派生（质量优先模板对应 0.50）。超过约 0.65 后触发点进入约 600K–640K，
已经在 Sol/Terra 只有 ~73% 的区段里；0.7（约 645K–690K）是容量优先选
择，等于明确接受长上下文计费和部分质量损失，0.8（约 738K–789K）更甚。
别把旧的 Luna 0.3 配方搬到这里：该窗口下 0.3 在约 277K–296K 才触发，
已经越过计价线。

##### Codex 订阅通道：窗口由服务端目录控制，配置前先实测

走 Codex 订阅端点（`preset: codex` 或 `/codex/responses` 中转）时，
ChatGPT 账号拿到的窗口来自服务端模型目录（`context_window` /
`max_context_window`）。Codex 把上下文算作**输入 + 输出**：目录里的
`max_context_window = 872000` 是 1M 档的输入侧——872K 输入 + 128K 输出
= 1M，正是官方 `model_context_window: 1000000` 配置要的值。95% 因子只是
客户端把 raw 转成 usable 输入（约 828.4K），Codex 自己的自动压缩默认在
解析后 raw 窗口的 90%（约 784.8K），都不是"总窗口被钳制"。

目录值历史上多次变动、账号间也不一致：输入侧长期是 272K（400K 档 =
272K + 128K），2026-08 中旬放开到 872K 上限，2026-09 初服务端开始下发
扩大后的档位。账号可能滞后，且 `/status` 在首个请求前可能显示配置值、
请求后才回落真实值。因此：

- 设 `limit.input` 前先实测该端点实际接受的输入量（配候选值跑长会话，
  观察日志是否出现 `context_length_exceeded` / oversize 拒绝）。
- 订阅端点按服务端状态取值：2026-09 放开后为 `input: 872000`（1M 档）；
  若你的账号/中转仍是旧档位则回落 `input: 272000`。`threshold` 与窗口
  解耦：它是"在可用预算的多少比例处压缩"，按质量/成本权衡选——但 API
  的 >272K 输入整单 2× 计价悬崖与窗口无关，若你的路由适用该计价，触发
  线仍应压在悬崖内。
- 只有直连 OpenAI API（`api.openai.com/v1/responses`）才适用官方 922K
  输入上限（1.05M 窗口 − 128K 输出）；仍建议留出压缩与单批暴涨余量。

其余规则不变：`compaction` 写在模型模板上，引用它的 provider 都会继
承；`reminder` 省略时按 `min(0.60, threshold × 0.90)` 派生；这两个字段
调 usage-driven 自动压缩，与 `model_driven` 是否开启无关。用 `gpt-5.6`
别名（解析到 Sol）时，把 `compaction` 加到该别名对应的模板上。

## OpenAI Responses 兼容接口：GPT-6 Astra

GPT-6 Astra 是 OpenAI 当前的旗舰模型（模型 ID `gpt-6-astra`）：1,050,000
上下文窗口，最大输出 128,000，可用输入 922,000（不设 `input` 时由
`context` 减去 `output` 推出）。reasoning effort 支持 `low`、`medium`、
`high`、`xhigh`、`max`，**没有 `none`**。标准定价每 1M token：输入 $10 /
输出 $50 / 缓存读取 $1 / 缓存写入 $12.50；prompt 输入超过 272K 时整次请求
按输入/缓存 2×、输出 1.5× 计费。与 GPT-5.6 不同，Astra 没有 Sol/Terra/Luna
分档——`gpt-6-astra` 是单一模型 ID，所以配方里也没有档位 variants。使用
API key 的 provider 需要在 `~/.config/chord/auth.yaml` 中配置同名条目：

```yaml
openai:
  - "$OPENAI_API_KEY"
```

### GPT-6 Astra

基础模板默认带 cost-first 的 `compaction` 块：272K 是计价悬崖（整次请求
重定价，不是只对超出部分计价），把用量压在悬崖下面是最大的成本杠杆，而
Astra 在远低于悬崖的区间仍保持满格长上下文质量（OpenAI 公布 MRCR v2
8-needle 在 256K–512K 段 100%）。只有当你愿意接受 2× 长上下文费率时，才
把 `threshold` 调过 0.29。

```yaml
model_templates:
  gpt-6-astra-base: &gpt-6-astra-base
    limit:
      context: 1050000
      output: 128000        # 官方全窗口：不写 input；可用输入按 922K 推出
    cost:
      input: 10
      output: 50
      cache_read: 1
      cache_write: 12.5
      input_tiers:
        - above_input_tokens: 272000
          input: 20
          output: 75
          cache_read: 2
          cache_write: 25
    reasoning:
      effort: medium
      summary: auto
    variants:
      low:
        reasoning:
          effort: low
      high:
        reasoning:
          effort: high
      xhigh:
        reasoning:
          effort: xhigh
      max:
        reasoning:
          effort: max
    modalities:
      input: [text, image, pdf]
    compaction:
      threshold: 0.25       # 约 231K 触发，低于 272K 计价悬崖
      reminder: 0.2

providers:
  openai:
    type: responses
    api_url: https://api.openai.com/v1/responses
    models:
      gpt-6-astra: *gpt-6-astra-base

model_pools:
  default:
    - openai/gpt-6-astra@high
```

验证：

```bash
chord doctor models --model openai/gpt-6-astra@high
```

要点：

- 这段针对**官方 OpenAI API**，所以声明完整 `1050000` 窗口、不写
  `input`：Chord 按 `context` 减去模型自己声明的 `limit.output` 推出可用
  输入预算（`1050000 − 128000 = 922000`），只在模型未声明 `limit.output`
  时才预留默认 `64000` 输出上限。超过 272K 在这里是计价阈值，不是输入硬
  上限，所以不要写 `input: 272000`。
- Codex 受限窗口是另一种配额，见下方 [Codex OAuth preset](#codex-oauth-preset)
  的 Codex 档位示例。不要把这段 API 窗口直接搬到 Codex provider 上。
- API 可用的 reasoning effort 是 `low`、`medium`、`high`、`xhigh`、`max`，
  用 `openai/gpt-6-astra@max` 这样的引用选 variant。GPT-6 Astra 没有
  `none` effort。
- Responses 在启用 reasoning 时默认用 `reasoning.summary: auto`；不希望
  Chord 请求可读摘要时显式设 `reasoning.summary: none`。

#### GPT-6 Astra 的压缩调优

上面的基础模板已经带了 cost-first 的 `compaction`（0.25/0.2）。272K 计价
悬崖是硬约束，质量天花板却不是——OpenAI 公布 GPT-6 Astra 在 MRCR v2
8-needle 的 256K–512K 段 100%、512K–1M 段 96.3%，是缓坡而不是 GPT-5.6 Sol
那种悬崖（Sol 在 512K–1M 掉到 73.8%）。所以 Astra 在 cost-first 之外调
阈值，权衡的是价格/容量，不是保质量。

**成本优先**（基础模板，0.25/0.2）：约 231K 触发，低于 272K 悬崖。推荐
默认——2× 重定价比任何其他杠杆都大，触发点又稳稳落在满格质量区段里。

**质量优先 / 容量优先**（接受 2× 长上下文费率）：因为 Astra 在约 512K 之前
没有质量悬崖，阈值可以推到 GPT-5.6 Sol 不敢碰的位置，仍处在高质量区段。
0.6–0.7（约 553K–645K）能买到很大的窗口，MRCR 还在 96% 以上；0.7–0.8
（约 645K–738K）更偏容量，质量代价更明显。覆盖基础模板的 `compaction`：

```yaml
model_templates:
  gpt-6-astra-quality: &gpt-6-astra-quality
    <<: *gpt-6-astra-base
    compaction:
      threshold: 0.65      # 约 600K 触发；接受 2× 长上下文费率
```

省略 `reminder` 时按 `min(0.60, threshold × 0.90)` 派生。在 Codex 受限
provider 上（其窗口由服务端控制、Astra 尚未实测），把 `compaction` 写到
那个 provider 的模型条目上，阈值按实测窗口调，而不是按 API 全窗口。

## Codex OAuth preset

当你要使用 ChatGPT/Codex OAuth，而不是 API key 时，用这个配置。Codex 使用
独立的模型配额；与上方 API key 示例的区别不只是 provider preset 和认证方式。

本节使用以下 Codex 模型限制：

| 模型 | `limit.context` | `limit.input` | `limit.output` |
| --- | ---: | ---: | ---: |
| GPT-6 Astra | 1,000,000 | 872,000 | 128,000 |
| GPT-5.4 | 1,050,000 | 950,000 | 128,000 |
| GPT-5.5 | 400,000 | 272,000 | 128,000 |
| GPT-5.6 Sol / Terra / Luna | 1,000,000 | 872,000 | 128,000 |

三个字段都要保留：`context` 表示 Codex 开放的输入加输出总窗口，`input`
和 `output` 则是其中各自独立的硬上限。两个独立上限不必相加等于
`context`；输入接近上限时，留给输出的空间自然会变少。

```yaml
providers:
  codex:
    preset: codex
    type: responses
    models:
      gpt-6-astra:
        limit:
          context: 1000000
          input: 872000
          output: 128000
        variants:
          high:
            reasoning:
              effort: high
          xhigh:
            reasoning:
              effort: xhigh
          max:
            reasoning:
              effort: max
      gpt-5.5:
        limit:
          context: 400000
          input: 272000
          output: 128000
        variants:
          high:
            reasoning:
              effort: high
          xhigh:
            reasoning:
              effort: xhigh
          max:
            reasoning:
              effort: max
      gpt-5.4:
        limit:
          context: 1050000
          input: 950000
          output: 128000
      gpt-5.6-sol:
        limit:
          context: 1000000
          input: 872000
          output: 128000

model_pools:
  default:
    - codex/gpt-6-astra@high
    - codex/gpt-5.5@high
```

登录：

```bash
chord auth codex
```

要点：

- 同时使用 API key 和 Codex OAuth 时，因为凭据和模型配额不同，应保留两个 provider，并分别配置模型限制。
- GPT-6 Astra 正在上线后头几周内向 Codex 推出（需要 Codex CLI 0.153.0
  或更新版本），Codex 订阅窗口官方尚未公布。配方沿用 GPT-5.6 Sol 的
  `1000000 / 872000 / 128000` 作为保守起点；上线后请按账号的服务端目录
  核对，并把三个字段都调成实测窗口再用于长会话。
- GPT-5.4 使用 `1050000 / 950000 / 128000`，分别对应 1.05M 总窗口、Codex 的有效输入预算（约为窗口的 90%；显式声明的输入始终按原值使用，与 `output` 不满足加和关系时也不会被钳制到 `context - output` 以内）和模型最大输出。
- `gpt-5.6-sol`、`gpt-5.6-terra`、`gpt-5.6-luna` 都使用 `1000000 /
  872000 / 128000`（Codex 订阅 2026-09 服务端档位；旧档位账号回落
  `400000 / 272000 / 128000`）。
- 这些数值跟随当前 Codex 模型目录，未来 Codex 版本可能调整。后端配额变化时，要同时更新三个字段。

## Anthropic Claude

在 `~/.config/chord/auth.yaml` 中配置：

```yaml
anthropic:
  - "$ANTHROPIC_API_KEY"
```

```yaml
model_templates:
  claude-opus: &claude-opus
    limit:
      context: 1000000
      output: 128000
    cost:
      input: 5
      output: 25
      cache_read: 0.5
      cache_write: 6.25
      cache_write_1h: 10
    thinking:
      type: adaptive
      display: summarized
    variants:
      high:
        thinking:
          effort: high
      xhigh:
        thinking:
          effort: xhigh
    modalities:
      input: [text, image, pdf]

providers:
  anthropic:
    type: messages
    api_url: https://api.anthropic.com/v1/messages
    models:
      claude-opus-5: *claude-opus
      claude-opus-4.8: *claude-opus
      claude-opus-4.7: *claude-opus

model_pools:
  default:
    - anthropic/claude-opus-5@high
```

Opus 5、4.8、4.7 的上下文窗口（1M）、最大输出（128K）、定价、adaptive
thinking 与输入模态完全一致，因此共用同一个 `&claude-opus` 模板，只有
模型 ID 不同。用不到的型号可以删掉，`model_pools` 指向你想用的模型即可
（例如 `anthropic/claude-opus-5@high`）。

如果想要更低成本的 Claude 配置，可沿用同样结构，改为 `claude-sonnet-5`、`cost: {input: 2, output: 10}`，并按需把 `output` 调低（例如 64000）做保守的本地分配。Sonnet 5 的 $2 / $10（每百万 token）定价已于 2026 年 8 月转为永久。

### Claude Fable 5.1

`claude-fable-5-1`（2026 年 9 月发布）沿用了 Fable 5 的 $10 / $50（每百万 token 输入 / 输出）费率，但缓存读取降到每百万 token $0.25——是基础输入价的 0.025x，而不是常见的 0.1x 乘数——所以 `cache_read` 要填 0.25，不要按比例填成 1.0。它与 Fable 5 一样是 1M 上下文、128K 最大输出、adaptive thinking，并支持 PDF。

```yaml
model_templates:
  claude-fable-5.1: &claude-fable-5-1
    limit:
      context: 1000000
      output: 128000
    cost:
      input: 10
      output: 50
      cache_read: 0.25
      cache_write: 12.5
      cache_write_1h: 20
    thinking:
      type: adaptive
      display: summarized
    variants:
      high:
        thinking:
          effort: high
      xhigh:
        thinking:
          effort: xhigh
    modalities:
      input: [text, image, pdf]

providers:
  anthropic:
    type: messages
    api_url: https://api.anthropic.com/v1/messages
    models:
      claude-fable-5-1: *claude-fable-5-1

model_pools:
  default:
    - anthropic/claude-fable-5-1@high
```

`claude-fable-5` 仍可用，费率相同，只有缓存读取是 $1.0。

#### Claude 5 的压缩调优

Claude 5 全系（Fable 5.1、Opus 5、Sonnet 5）都是 1M 上下文、128K 最大输出、全窗口统一按 token 计费。MRCR v2 8-needle 显示 Opus 级模型即使到 1M 仍能保持 ~76%（当前所有模型族里最平坦的曲线），可靠窗口确实很大。Opus 4.7 时代的模型为换取"拒绝而非编造"牺牲了检索准确率；Opus 5 和 Fable 5.1 恢复了强长上下文检索。默认 `threshold: 0.8` 对这类模型是合理起点；如果跑数小时的 agentic 长会话，0.7 能让模型避开 512K 以上的轻度退化带。

```yaml
# 直接在既有 claude-fable-5-1 模板上加 compaction，引用它的 provider 全部继承
model_templates:
  claude-fable-5-1: &claude-fable-5-1
    limit: {context: 1000000, output: 128000}
    compaction: {threshold: 0.7}   # 针对数小时 agentic 长会话调低到 0.7
```

`reminder` 故意省略：缺省派生为 `min(0.60, 0.7×0.9) = 0.60`，对这些模型是
合理的提前量——只有想更早/更晚提示时才显式设置。Opus 5 等同一可靠档的
模型加同样一行即可。

注意 Opus 4.7 起换了 tokenizer：同样文本在 Claude 5 模型上比老模型多约 30% token，所以在老模型上感觉合适的上下文预算要相应下调。

## Google Gemini

在 `~/.config/chord/auth.yaml` 中配置：

```yaml
gemini:
  - "$GEMINI_API_KEY"
```

```yaml
providers:
  gemini:
    api_url: https://generativelanguage.googleapis.com/v1beta/models
    models:
      gemini-3.5-flash:
        limit:
          context: 1048576
          output: 65536
        modalities:
          input: [text, image, pdf]
        thinking:
          budget: -1
          level: high
      gemini-3.7-flash:
        limit:
          context: 1048576
          output: 65536
        modalities:
          input: [text, image, pdf]
        thinking:
          level: high

model_pools:
  default:
    - gemini/gemini-3.5-flash
```

要点：

- `api_url` 保持在 `/models` 基础路径即可；Chord 会自动追加 `/{model}:streamGenerateContent?alt=sse`。
- `type` 可以省略；Chord 会根据 `/models` 路径自动识别 Gemini。
- Gemini 3.7 Flash（2026 年 8 月 GA）是目前的主力模型：促销价每百万 token $0.75 / $3.75 到 2026 年底，2027 年起 $1.50 / $7.50。它的 thinking 级别只有 `low` / `medium` / `high`——不支持 `minimal`，且 `thinking_budget` 已废弃，所以上面模板省略了 `budget`。Gemini 3.5 / 3.6 Flash 仍可用旧模板。

### Gemini 的压缩调优

Gemini 3.x 是目前前沿模型里长上下文悬崖最陡的：128K 处很强（MRCR v2 8-needle 84.9%），到 1M 崩到 ~26%——所以尽管窗口标称 1M，可靠窗口其实只有 128K–200K 左右。Gemini 会话应远早于此压缩：把该模型的 `threshold` 调到可用预算的 ~0.15–0.25（1M 窗口约合 150K–250K），`reminder` 设在它下方一点，让模型在自动压缩前先收到压力提示并有机会主动 reset。

```yaml
# 在每个 Gemini 模型模板上加 compaction；引用该模板的 provider 全部继承
model_templates:
  gemini-3.1-pro: &gemini-3.1-pro
    limit: {context: 1048576, output: 65536}
    compaction: {threshold: 0.2, reminder: 0.15}
  gemini-3.7-flash: &gemini-3.7-flash
    limit: {context: 1048576, output: 65536}
    compaction: {threshold: 0.25, reminder: 0.2}
```

Gemini 在超过 200K 输入时也按整请求更高档计费（全请求进高价档），所以赶在 200K 前压缩既保质量又省钱。如果工作负载确实需要长上下文，建议改用 GPT-5.6 Sol / Claude 5 这类模型，而不是把 Gemini 硬推到它的可靠区间之外。

## GLM-5.2 / BigModel Coding Plan

在 `~/.config/chord/auth.yaml` 中配置：

```yaml
bigmodel:
  - "$BIGMODEL_API_KEY"
```

下面三个模板是 GLM-5.x 系列通用基础：`chat` 模板的
`thinking.type: enabled` + `clear_thinking: false` 与 `reasoning.effort`
取值落在 GLM-5.2（动态思考，effort 支持 `max`/`xhigh`/…/`none`）和
GLM-5.3 / 5.3-Flash（强制思考，effort 仅 `max`/`high`/`low`）的交集上，
所以同一套模板可直接用于 5.2、5.3 与 5.3-Flash。`glm-5.2-*` 命名取自引入
这些 compat 设置的模型，不代表只适用于 5.2。

```yaml
model_templates:
  glm-5.2-chat: &glm-5-2-chat
    limit:
      context: 1000000
      output: 128000
    reasoning:
      effort: max
    compat:
      request_overrides:
        rename_body_fields:
          max_completion_tokens: max_tokens
        body:
          thinking:
            type: enabled
            clear_thinking: false
      reasoning_continuity:
        mode: openai_visible
        preserve_history: true

  glm-5.2-messages: &glm-5-2-messages
    limit:
      context: 1000000
      output: 128000
    thinking:
      type: adaptive
      effort: max
    compat:
      request_overrides:
        headers:
          anthropic-beta: null
      reasoning_continuity:
        mode: anthropic_unsigned

  glm-5.2-responses: &glm-5-2-responses
    limit:
      context: 1000000
      output: 128000
    reasoning:
      effort: max

  glm-5.3-chat: &glm-5-3-chat
    <<: *glm-5-2-chat
    variants:
      low:
        reasoning:
          effort: low
      high:
        reasoning:
          effort: high
      max:
        reasoning:
          effort: max

  glm-5.3-flash: &glm-5-3-flash
    <<: *glm-5-3-chat
    modalities:
      input: [text, image, pdf]

providers:
  bigmodel:
    type: chat-completions
    api_url: https://open.bigmodel.cn/api/coding/paas/v4/chat/completions
    models:
      glm-5.2: *glm-5-2-chat
      glm-5.3: *glm-5-3-chat
      glm-5.3-flash: *glm-5-3-flash

  bigmodel-messages:
    type: messages
    api_url: https://open.bigmodel.cn/api/anthropic/v1/messages
    models:
      glm-5.2: *glm-5-2-messages

  glm-responses:
    type: responses
    api_url: https://example.com/v1/responses
    models:
      glm-5.2: *glm-5-2-responses

model_pools:
  default:
    - bigmodel/glm-5.2
```

要点：

- Chat Completions 需要 `thinking.type: enabled`、`reasoning_effort` 和
  `max_tokens`。`request_overrides` 添加 GLM 思考字段并重命名动态计算的输出
  上限字段；`openai_visible` 会原样回放原生 `reasoning_content`，并把其他
  wire family 的可移植可见 reasoning 转成 `reasoning_content`。
- Messages 兼容接口使用 `thinking` 和 `output_config.effort`。除非对应接口
  明确支持，否则应关闭 Anthropic beta header。兼容 Messages 接口可能返回
  无签名 thinking，而非 Claude 风格的签名块，不能仅凭 wire 格式推断签名
  回放能力。只有明确验证 endpoint 接受自身无签名 thinking 的工具循环后，
  才配置 `anthropic_unsigned`；启用后，Chord 也能把其他 wire family 的可移植
  可见 reasoning 映射成该 target 的无签名 `thinking` block。
- GLM 的 `/responses` 由网关自行实现。只有网关明确说明支持 OpenAI
  Responses 映射时，才单独使用仅含 `reasoning.effort` 的模板。
- GLM-5.3（2026 年 8 月 GA）沿用 GLM-5.2 的纯文本规格（1M 上下文、128K
  最大输出），可以直接复用上面的 GLM-5.2 模板，只需把 provider `models`
  里的模型 ID 换成 `glm-5.3`。
- GLM-5.3 和 5.3-Flash 的 `reasoning_effort` 只支持 `low` / `high` / `max`
  三档（GLM-5.2 另外支持 `xhigh` / `medium` / `minimal` / `none` 并映射），
  所以 `glm-5.3-chat` 模板额外加了这三档 `variants`；5.3 和 5.3-Flash 请用
  `glm-5.3@low|high|max` 这类引用，不要套用 5.2 的 effort 取值集合。
- GLM-5.3-Flash（2026 年 8 月发布）是 GLM-5 系列首个原生多模态模型：
  支持图像 / 视频 / 文件输入，上下文 1M、最大输出 128K。PDF 输入官方支持：
  GLM Chat Completion API 接受 `file` content block，`file` 对象可选
  `file_id` / `file_url` / `file_data`（Base64 的 `data:<MIME>;base64,...`
  URL），单文件 ≤ 50M，支持 pdf/txt/word/jsonl/xlsx/pptx 等格式；这与 Chord
  的 chat-completions PDF 负载（`type: file` + `filename` + `file_data`）
  完全一致，无需兼容配置。文本参数与 GLM-5.3 一致，所以从上面的 Chat
  Completions 模板继承，只需补 `modalities.input`。`thinking.type` 只支持
  `enabled`（无法关闭思考），chat 模板已配置好。第三方中转可能只实现了
  旧的仅 URL 形式 `file_url`，依赖 Base64 `file_data` 前先确认中转支持。

### GLM-5.x 的压缩调优

GLM-5.2/5.3 标称 1M 窗口，但独立的长上下文评测把 GLM/Qwen 这一类开放权重
模型的可靠工作窗口放在 200K–256K 左右（大约是标称 1M 的 20–25%）。如果你在
GLM 上跑长探索型会话，请在可用预算的四分之一附近压缩：

```yaml
# 给你已经在用的 glm 模板（glm-5.2-chat / glm-5.2-messages /
# glm-5.3-chat ...）加上 compaction；引用该模板的模型条目全部继承。
model_templates:
  glm-5.2-chat: &glm-5-2-chat
    limit: {context: 1000000, output: 128000}
    compaction: {threshold: 0.25, reminder: 0.2}
```

上面的配方里 GLM-5.2 由多个 provider 提供（`bigmodel` chat、
`bigmodel-messages`、`glm-responses`）；每个引用该模板的模型条目都会拿到同一份
`compaction`。如果你的工作负载本来就短，省略 `compaction` 块、让模型用全局
默认即可。

## DeepSeek V4（Flash / Pro）

在 `~/.config/chord/auth.yaml` 中配置：

```yaml
deepseek:
  - "$DEEPSEEK_API_KEY"
```

`deepseek-v4-pro` 与 `deepseek-v4-flash` 走同一套 API，协议层完全一致，
共用下面按 wire family 命名的模板；两个模型都支持 Responses API。

```yaml
model_templates:
  deepseek-v4-chat: &deepseek-v4-chat
    limit:
      context: 1000000
      output: 64000
    reasoning:
      effort: high
    variants:
      low:
        reasoning:
          effort: low
      high:
        reasoning:
          effort: high
      max:
        reasoning:
          effort: max
    compat:
      request_overrides:
        rename_body_fields:
          max_completion_tokens: max_tokens
        body:
          thinking:
            type: enabled
      reasoning_continuity:
        mode: openai_visible
      forced_tool_choice:
        suppress_in_thinking: true

  deepseek-v4-messages: &deepseek-v4-messages
    limit:
      context: 1000000
      output: 64000
    thinking:
      type: adaptive
      effort: high
    variants:
      low:
        thinking:
          effort: low
      high:
        thinking:
          effort: high
      max:
        thinking:
          effort: max
    compat:
      request_overrides:
        headers:
          anthropic-beta: null
      reasoning_continuity:
        mode: anthropic_unsigned

  deepseek-v4-responses: &deepseek-v4-responses
    limit:
      context: 1000000
      output: 64000
    reasoning:
      effort: high
    variants:
      low:
        reasoning:
          effort: low
      high:
        reasoning:
          effort: high
      max:
        reasoning:
          effort: max
    compat:
      responses:
        send_reasoning_include: false
        send_max_output_tokens: true
      reasoning_continuity:
        mode: openai_visible

providers:
  deepseek:
    type: chat-completions
    api_url: https://api.deepseek.com/v1/chat/completions
    models:
      deepseek-v4-pro: *deepseek-v4-chat
      deepseek-v4-flash: *deepseek-v4-chat

  deepseek-messages:
    type: messages
    api_url: https://api.deepseek.com/anthropic/v1/messages
    models:
      deepseek-v4-pro: *deepseek-v4-messages
      deepseek-v4-flash: *deepseek-v4-messages

  deepseek-responses:
    type: responses
    api_url: https://api.deepseek.com/v1/responses
    models:
      deepseek-v4-pro: *deepseek-v4-responses
      deepseek-v4-flash: *deepseek-v4-responses

model_pools:
  default:
    - deepseek/deepseek-v4-flash@high
```

要点：

- DeepSeek Chat thinking 使用 `thinking.type`、顶层 `reasoning_effort` 和
  `max_tokens`。`request_overrides` 提供请求形状差异；thinking + 工具调用
  循环中，`openai_visible` 会原样返回 assistant 的 `reasoning_content`。
  DeepSeek 在启用 thinking 时会拒绝 forced tool choice，所以模板会把 loop
  强制的 `tool_choice: required` 降级为后端默认选择。
- DeepSeek Responses 支持 `tool_choice: required`，因此模板保留 loop 的强制
  工具选择。该接口直接返回明文 `reasoning_text`，因此无需请求加密 reasoning；
  它支持 `max_output_tokens`，模板会继续显式发送上限。其他不支持的字段会被
  DeepSeek 静默忽略。
- DeepSeek Messages 支持 `output_config.effort`；Chord 从
  `thinking.effort` 生成该字段。兼容接口应关闭 Anthropic beta header。
  DeepSeek 的 Anthropic 兼容接口可能返回无签名 `thinking`，而不是 Claude
  风格的签名块。`anthropic_unsigned` 会原生回放同 provider/model 的无签名
  thinking，也能把其他 wire family 的可移植可见 reasoning 转为无签名
  `thinking` block；如果 target 仍拒绝该形状，严格兼容级别会丢弃 reasoning
  carrier，但保留工具轮次。
- 第三方 `/responses` 端点由网关自行实现；只有网关明确说明映射方式时，
  才使用 `reasoning.effort` 和 `openai_visible`。
- 对兼容网关，请使用该网关 / 账号实际公开的模型 ID 和限制。见
  [常见问题排查 — DeepSeek / OpenAI 兼容 thinking 模式 400](./troubleshooting_CN.md#deepseek--openai-兼容-thinking-模式-400)。

补充：

- 官方定价页标注的最大输出为 384K；这里 `limit.output: 64000` 是保守的
  本地分配，与 pro 保持一致。需要更长输出时按需调大。
- flash 与 pro 都支持 Responses API（`api.deepseek.com/v1/responses`）。
  响应中的 `output_tokens_details.reasoning_tokens` 由 Chord 按标准 reasoning
  回显处理，无需额外配置。
- `reasoning_effort` 官方支持 `low` / `high` / `max`（默认 `high`）。
  `xhigh` 会被映射到 `high`，`medium` 映射到 `high`，所以模板只定义
  `low` / `high` / `max` 三个 variant。
- 模型间有差异时（例如某个型号默认思考强度不同），用 YAML 锚点继承
  并覆盖差异部分即可，例如：

```yaml
  deepseek-v4-pro-chat: &deepseek-v4-pro-chat
    <<: *deepseek-v4-chat
    reasoning:
      effort: max
```

  这样 pro 的默认思考强度为 `max`，flash 保持 `high`，其余字段（limit、
  compat、variants）全部复用。

- flash 定价约为 pro 的 1/3（off-peak、无缓存命中时：输入 $0.22 /
  输出 $0.66 每百万 token；peak 价约翻倍，缓存命中时输入低至 $0.007），
  适合高频 / 低成本场景。见 [DeepSeek 官方定价](https://api-docs.deepseek.com/quick_start/pricing/)。

### DeepSeek V4 Flash Vision（实验版）

`deepseek-v4-flash-vision-exp` 是 Flash 的视觉变体：文本能力与思考行为
和 Flash 一致，额外支持图像输入（JPEG / PNG / GIF / WebP；内嵌、URL
或 Files API 均可）。全站只有这个模型收图—— flash 和 pro 传图会返回
`400`（"This model does not support image"）。官方标注为 experimental，价格
与 flash 相同：图片按输入 token 计费，单张最多计 384 token（详见定价页与
[官方 Vision 指南](https://api-docs.deepseek.com/guides/vision/)）。Chat Completions、
Responses 和 Anthropic 兼容接口都支持图像输入，三条 wire
family 各自继承对应的 V4 模板，只加 `modalities`：

```yaml
model_templates:
  deepseek-v4-vision-chat: &deepseek-v4-vision-chat
    <<: *deepseek-v4-chat
    modalities:
      input: [text, image]

  deepseek-v4-vision-messages: &deepseek-v4-vision-messages
    <<: *deepseek-v4-messages
    modalities:
      input: [text, image]

  deepseek-v4-vision-responses: &deepseek-v4-vision-responses
    <<: *deepseek-v4-responses
    modalities:
      input: [text, image]

providers:
  deepseek:
    type: chat-completions
    api_url: https://api.deepseek.com/v1/chat/completions
    models:
      deepseek-v4-pro: *deepseek-v4-chat
      deepseek-v4-flash: *deepseek-v4-chat
      deepseek-v4-flash-vision-exp: *deepseek-v4-vision-chat

  deepseek-messages:
    type: messages
    api_url: https://api.deepseek.com/anthropic/v1/messages
    models:
      deepseek-v4-pro: *deepseek-v4-messages
      deepseek-v4-flash: *deepseek-v4-messages
      deepseek-v4-flash-vision-exp: *deepseek-v4-vision-messages

  deepseek-responses:
    type: responses
    api_url: https://api.deepseek.com/v1/responses
    models:
      deepseek-v4-pro: *deepseek-v4-responses
      deepseek-v4-flash: *deepseek-v4-responses
      deepseek-v4-flash-vision-exp: *deepseek-v4-vision-responses
```

给 `image_url`（Chat）或 `input_image`（Responses）设 `detail`
（`low` / `high` / `original` / `auto`，`high` 与 `original` 等价）可以按请求控制
图像处理方式与 token 消耗。Chord 目前对每张图片都发送 `auto`，暂不暴露按请求
调整 `detail` 的能力；图片以 inline base64 传入，不支持外部 URL 或 Files API
`file_id`。[`view_image`](./tools_CN.md) 工具能把本地图片加载进上下文，但只有
把这个模型放在 `messages` 或 `responses` provider 的池首才行：
`chat-completions`（`deepseek`）provider 能在用户消息里收图，却无法在 tool result 里返回图片。

#### DeepSeek V4 的压缩调优

DeepSeek V4 Pro/Flash 标称 1M 窗口，但 MLA 架构在长距离上退化明显：独立的
multi-needle 评测中 V4 Pro 在 1M 处只有约 41%（8-needle），而单 needle 约
78%——这种陡降和 Gemini 的悬崖如出一辙。在 1M 窗口上，可靠工作窗口大约
200K。V4 即使全部缓存未命中也远比同级模型便宜，因此频繁压缩的代价比在高端
模型上低得多——尽早压、多压几次：

```yaml
# 上面的配方里 deepseek-v4-chat / deepseek-v4-messages /
# deepseek-v4-responses 已经共用同一个基模板；把 compaction 加在共享模板上，
# 每个 deepseek-v4-pro / deepseek-v4-flash 条目都会继承。
model_templates:
  deepseek-v4-chat: &deepseek-v4-chat
    limit: {context: 1000000, output: 128000}
    compaction: {threshold: 0.25, reminder: 0.2}
```

DeepSeek 的缓存命中价是业界最低的（$0.0036/M），因此一次能保住可缓存前缀的
压缩，在重复读取场景下几乎是免费的。短的交互式会话保持全局默认即可，只有真正
跑长时间 agentic 任务时才需要单独给模型条目调参。

## Qwen 保留历史思考

Qwen 通过 `reasoning_content` 返回可见思考，但大多数型号默认忽略历史
消息里的该字段。只有模型文档明确支持 `preserve_thinking` 时才应开启
回放（目前主要是 Qwen 3.6/3.7 Max、Plus 系列）；较早的 Qwen 3/3.5
即使会输出思考，也应保持 continuity 关闭。

```yaml
model_templates:
  qwen-preserved: &qwen-preserved
    limit:
      context: 1000000
      output: 65536
    compat:
      request_overrides:
        body:
          enable_thinking: true
          preserve_thinking: true
      reasoning_continuity:
        mode: openai_visible
        preserve_history: true

providers:
  qwen:
    type: chat-completions
    api_url: https://dashscope-intl.aliyuncs.com/compatible-mode/v1/chat/completions
    models:
      qwen3.7-plus: *qwen-preserved

model_pools:
  default:
    - qwen/qwen3.7-plus
```

请按账号和区域文档替换上下文限制及 endpoint。`preserve_thinking: true`
时，历史思考会计入输入 token 和费用；`preserve_history: true` 让 Chord
不在客户端剥离这段历史。

## Kimi K3

Kimi K3 是当前旗舰思考模型，提供 1M token 上下文、始终启用思考，目前
只接受 `reasoning_effort: max`，并要求多轮对话和工具调用循环完整回传
assistant 消息（包括 `reasoning_content`）。不要发送 K2.x 的 `thinking`
参数，也不要显式发送 `temperature` 等固定采样字段。

```yaml
model_templates:
  kimi-k3: &kimi-k3
    limit:
      context: 1048576
      output: 131072
    reasoning:
      effort: max
    compat:
      chat_completions:
        mcp_system_tools_message: true
      reasoning_continuity:
        mode: openai_visible
        preserve_history: true

  kimi-k2.7-code: &kimi-k2-7-code
    limit:
      context: 262144
      output: 32768
    compat:
      reasoning_continuity:
        mode: openai_visible
        preserve_history: true

  kimi-k2.6-thinking: &kimi-k2-6-thinking
    limit:
      context: 262144
      output: 32768
    compat:
      request_overrides:
        body:
          thinking:
            type: enabled
            keep: all
      reasoning_continuity:
        mode: openai_visible
        preserve_history: true

providers:
  kimi:
    type: chat-completions
    api_url: https://api.moonshot.ai/v1/chat/completions
    models:
      kimi-k3: *kimi-k3
      kimi-k2.7-code: *kimi-k2-7-code
      kimi-k2.6: *kimi-k2-6-thinking

model_pools:
  default:
    - kimi/kimi-k3
```

`mcp_system_tools_message` 是显式的模型能力开关，不靠模型名猜测。开启后，
运行时 manual MCP 声明会固定在原对话位置，不再改写顶层工具列表。不接受
「只带 `tools`、不带 `content` 的 `role: system` 消息」的网关应保持关闭；
fallback 池能力不一致时，Chord 会自动采用各模型都能接受的顶层工具形态。

K2.7 Code 是 256K 上下文、面向编码的纯思考型号；它的 thinking 和
`keep: all` 行为固定，因此模板不发送 `thinking` 对象。K2.6 是 256K
上下文的通用混合思考型号，所以显式设置这两个字段。K2.5 不支持保留
历史思考，而且已对新用户进入退场阶段；新配置应优先使用 K3。

对于所有使用 `openai_visible` 的模板（DeepSeek、GLM、受支持的 Qwen 和
Kimi），Chord 首次会把原生 reasoning 乐观回放给任何 Chat Completions
目标，因此 Kimi K2.6/K2.7→K3 这类官方允许的同 provider 升级和同模型跨
provider fallback 都能保留连续性。服务端会丢弃更早轮次 reasoning 的
后端（DeepSeek）不设 `preserve_history`，Chord 会在回放前剥离已完成
轮次的 reasoning，避免为其付费；preserved-thinking 模板（GLM
`clear_thinking: false`、Qwen `preserve_thinking`、Kimi K3 / `keep: all`）
设置 `preserve_history: true`，完整 assistant 历史会原样回放。若目标拒绝原生 reasoning，Chord 只会
删除或转换不兼容的 reasoning 负载；已完成且成对的工具调用和结果仍会保留。
当目标连结构化形状也不接受时，严格降级会把已完成的动作历史文本化，而
不会把外部工具事实静默删除。

### 跨协议 fallback 的连续性

当模型池在 Chat Completions、Responses、Messages 或 Gemini 之间切换时，
Chord 会保留已完成工具轮次中可迁移的部分：

- 已完成且成对的调用与结果会尽量转换为目标协议的结构化工具表示；
- 与工具轮次绑定的可见 reasoning（`reasoning_content`、无签名 thinking
  文本、Responses reasoning summary 或 Gemini thought 文本）只有在目标提供
  结构化 reasoning carrier（`openai_visible` 或 `anthropic_unsigned`）时才会
  转换；否则会被丢弃，而不会注入普通 assistant 正文；
- Claude signature、Responses 加密 reasoning、Gemini thought signature 等
  provider 专属 opaque 状态不会被伪造，也不会复制到不兼容协议；
- 若目标拒绝合成后的结构化形状，严格兼容降级会把完整调用/结果历史文本化，
  而不是静默删除。

纯 reasoning-only 历史不会转换为 fallback 文本。这样可以把跨协议上下文
集中在与动作相关的状态上，避免为和工具轮次无关的旧思考链重复付费。

## Grok 4.6（xAI Responses）

xAI 推荐通过 Responses API 使用 Grok。Grok 4.6 支持文本和图片输入、
function calling、structured output、reasoning，并提供 500K 上下文。xAI
也接受 PDF 附件，以 `input_file` 提供公开 `file_url` 或已上传的 `file_id`
即可，服务端会自动启用 `attachment_search` 工具；但 Chord 发送 PDF 用的是
inline base64 `file_data`，xAI 的 Responses API 不接受非图片的 inline 字节，
所以这里的 `modalities.input` 不声明 `pdf`。Grok 4.6 通过
`response.reasoning_text.*` 流事件返回 reasoning text；Chord 会把这些事件
映射到统一 thinking stream，同时保存有序 Responses output item 以延续工具
调用状态。

```yaml
model_templates:
  grok-4.6: &grok-4-6
    limit:
      context: 500000
    reasoning:
      effort: high
    modalities:
      input: [text, image]

providers:
  xai:
    type: responses
    api_url: https://api.x.ai/v1/responses
    models:
      grok-4.6: *grok-4-6

model_pools:
  default:
    - xai/grok-4.6
```

xAI 只公布了 Grok 4.6 的 500K 总上下文窗口，没有再给出更低的独立模型输出
上限，因此这里省略 `limit.output`。Chord 不会向 xAI 发送
`max_output_tokens`，由 API 在剩余上下文内安排输出；本地从 `limit.context`
推导输入预算时，仍会预留默认的 `64000` 输出预算。只有明确需要发送固定上限时，
才配置 `limit.output`、提高 Chord 的 `max_output_tokens`，并设置
`compat.responses.send_max_output_tokens: true`。

可使用 `grok-4.6` 作为模型 ID。不要配置
`openai_visible`：xAI Responses 使用原生有序 output / reasoning 状态，而非
Chat Completions 的 `reasoning_content`。`reasoning.effort` 支持 `low`、
`medium`、`high`、`xhigh`（仅 Grok 4.6 可用，不支持该档位的模型会按 `high`
处理）；`high` 是默认值，且 reasoning 不可关闭。

## 如何验证任意一份配置

复制完配置后，先跑一个定向检查：

```bash
chord doctor models --model provider/model
```

然后再验证你实际要用的 variant，例如：

```bash
chord doctor models --model openai/gpt-5.6@max
chord doctor models --model codex/gpt-5.5@max
chord doctor models --model anthropic/claude-opus-5@high
```
