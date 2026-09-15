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

## OpenAI GPT（Responses 兼容接口）

GPT-5.4 / GPT-5.5 / GPT-5.6 / GPT-6 Astra 片段使用 OpenAI 模型页公布的
档位：GPT-5.4 / 5.6 / 6 为 `1050000 / 922000 / 128000`（1.05M 总窗口；
922K 输入预算由 `context` 减 `output` 推导——这些模型不公布独立输入上
限），API 与当前 Codex 目录一致；GPT-5.5 保持 `400000 / 272000 / 128000`。
账号或中转仍是旧档位时，相应模型回落 `400000 / 272000 / 128000`。价格块
使用 OpenAI API 费率；中转收费不同时需要自行覆盖。Codex OAuth 使用下方
单独的 preset 配置。使用 API key 的 provider 需要在
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
          input: 922000
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
    - openai/gpt-5.4@xhigh
```

验证：

```bash
chord doctor models --model openai/gpt-5.4@xhigh
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
    - openai/gpt-5.5@xhigh
```

验证：

```bash
chord doctor models --model openai/gpt-5.5@xhigh
```

### GPT-5.6（Sol / Terra / Luna）

5.6 家族有三个模型：`gpt-5.6-sol`、`gpt-5.6-terra` 和 `gpt-5.6-luna`。三者
共用相同的窗口、reasoning、variants 和 modalities，这部分公共内容收进
`&gpt-5-6-base` 锚点，各模型条目只需再补自己的 `cost` 块。

```yaml
model_templates:
  gpt-5.6-base: &gpt-5-6-base
    # 1.05M 模型页档位，API 与当前 Codex 目录一致：1050000 总窗口 /
    # 922000 输入预算（`context` 减 `output` 推导，不公布独立输入上限）
    # / 128000 输出。账号/中转仍是旧档位时回落 400000/272000/128000。
    limit:
      context: 1050000
      input: 922000
      output: 128000
    reasoning:
      effort: medium
      summary: auto
    variants:
      low:
        reasoning:
          effort: low
      medium:
        reasoning:
          effort: medium
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

model_pools:
  default:
    - openai/gpt-5.6-sol@xhigh
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

model_pools:
  default:
    - openai/gpt-5.6-terra@max
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

model_pools:
  default:
    - openai/gpt-5.6-luna@max
```

要点：

- 5.6 示例直接声明模型页窗口 `1050000 / 922000 / 128000`：922K 输入预算
  由 `context` 减 `output` 推导（这些模型不公布独立输入上限），无需显式
  `input`。只有 400K 档模型（上面的 GPT-5.5 / 5.2）才保留 `input: 272000`。
  账号/中转仍是旧 Codex 档位时，5.6 各档回落 `400000 / 272000 / 128000`。
- GPT-5.6 API 可用的 reasoning effort 包括 `none`、`low`、`medium`、`high`、`xhigh`、`max`。
- Responses 在启用 reasoning 时默认使用 `reasoning.summary: auto`；如果不希望 Chord 请求可读 reasoning 摘要，请显式配置 `reasoning.summary: none`。
- Chord 当前尚未暴露 GPT-5.6 的 `reasoning.mode: pro`。

验证：

```bash
chord doctor models --model openai/gpt-5.6-sol@xhigh
```

#### GPT-5.6 的压缩调优

先分清两件事再定阈值：**模型按哪个预算跑**——上面示例用的
1.05M / 922K 档位，还是账号/中转仍是旧目录时的 `400000 / 272000`
回落档——以及**阈值为什么调**：保质量、避开 272K 长上下文计价档，还是
把窗口当容量用。触发点 = `threshold × usable input budget`，同一比例在
两种预算下的触发点相差很大，针对一种预算调出来的配方不能直接搬给另一种。

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

成本优先（Sol/Terra/Luna 共用：把用量留在 272K 计价档内，同时避开 Luna
的 256K+ 崩塌区）：

```yaml
model_templates:
  gpt-5.6-cost-first: &gpt-5-6-cost-first
    <<: *gpt-5-6-base
    compaction:
      threshold: 0.25       # 0.25 × 922K ≈ 231K，低于 272K 计价线
      reminder: 0.2
```

质量优先（Sol/Terra；Luna 没有可以瞄准的强长上下文区段）：

```yaml
model_templates:
  gpt-5.6-quality-first: &gpt-5-6-quality-first
    <<: *gpt-5-6-base
    compaction:
      threshold: 0.55       # ≈ 507K；0.5–0.65 都合理
```

上面示例里的 `reminder` 可以省略：省略时按 `min(0.60, threshold × 0.90)`
派生（质量优先模板对应 0.50）。超过约 0.65 后触发点进入约 600K–640K，
已经在 Sol/Terra 只有 ~73% 的区段里；0.7（约 645K–690K）是容量优先选
择，等于明确接受长上下文计费和部分质量损失，0.8（约 738K–789K）更甚。
别把旧的 Luna 0.3 配方搬到这里：该窗口下 0.3 在约 277K–296K 才触发，
已经越过计价线。

##### Codex 订阅通道：窗口由服务端目录控制，配置前先实测

走 Codex 订阅端点（`preset: codex` 或 `/codex/responses` 中转）时，
ChatGPT 账号实际拿到的窗口来自服务端模型目录（`context_window` /
`max_context_window`），不是模型页：目录值历史上多次变动、账号间也不
一致（输入侧出现过低至 272K 的档位，而模型页宣传 1.05M）。`/status`
在首个请求前可能显示配置值、请求后才回落真实值。因此：

- 在长会话依赖 1.05M 档位前先实测该端点实际接受的输入量（配候选
  `limit` 跑长会话，观察日志是否出现 `context_length_exceeded` /
  oversize 拒绝）。
- 账号/中转仍是旧档位时，为该 provider 回落 `400000 / 272000 / 128000`。
- `threshold` 与窗口解耦：它是"在可用预算的多少比例处压缩"，按质量/成本
  权衡选——但 API 的 >272K 输入整单 2× 计价悬崖与窗口无关，若你的路由
  适用该计价，触发线仍应压在悬崖内。留足余量：触发比较的是上一次
  provider 返回的 usage，一次大工具结果就可能把下一次请求推过线。

其余规则不变：`compaction` 写在模型模板上，引用它的 provider 都会继
承；`reminder` 省略时按 `min(0.60, threshold × 0.90)` 派生；这两个字段
调 usage-driven 自动压缩，与 `model_driven` 是否开启无关。写在
`&gpt-5-6-base` 这类共用模板上的 `compaction` 会作用于所有合并它的模型；
只想调某一档时，为该档单独建一个模板。

### GPT-6 Astra

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
      medium:
        reasoning:
          effort: medium
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
    - openai/gpt-6-astra@medium
```

验证：

```bash
chord doctor models --model openai/gpt-6-astra@medium
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
  用 `openai/gpt-6-astra@medium` 这样的引用选 variant。GPT-6 Astra 没有
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

当你要使用 ChatGPT/Codex OAuth，而不是 API key 时，用这个配置。Codex OAuth
与上方 API key 示例的区别只在 provider preset 和认证方式——模型窗口与 API
一致。

本节使用的模型档位：

| 模型 | `limit.context` | `limit.input` | `limit.output` |
| --- | ---: | ---: | ---: |
| GPT-6 Astra | 1,050,000 | 922,000 | 128,000 |
| GPT-5.4 | 1,050,000 | 922,000 | 128,000 |
| GPT-5.5 | 400,000 | 272,000 | 128,000 |
| GPT-5.6 Sol / Terra / Luna | 1,050,000 | 922,000 | 128,000 |

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
          context: 1050000
          input: 922000
          output: 128000
        variants:
          medium:
            reasoning:
              effort: medium
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
          input: 922000
          output: 128000
      gpt-5.6-sol:
        limit:
          context: 1050000
          input: 922000
          output: 128000

model_pools:
  default:
    - codex/gpt-6-astra@medium
    - codex/gpt-5.5@xhigh
```

登录：

```bash
chord auth codex
```

要点：

- 同时使用 API key 和 Codex OAuth 时，因为凭据和模型配额不同，应保留两个 provider，并分别配置模型限制。
- GPT-6 Astra 正在上线后头几周内向 Codex 推出（需要 Codex CLI 0.153.0
  或更新版本），Codex 订阅窗口官方尚未公布。配方沿用 GPT-5.6 Sol 的
  `1050000 / 922000 / 128000` 作为保守起点；上线后请按账号的服务端目录
  核对，并把三个字段都调成实测窗口再用于长会话。
- GPT-5.4、GPT-5.6 Sol / Terra / Luna 与 GPT-6 Astra 使用模型页档位
  `1050000 / 922000 / 128000`（922K 输入预算由 `context` 减 `output` 推导，
  这些模型不公布独立输入上限）。API 的 >272K 整单 2× 计价悬崖若适用于
  你的路由仍照常生效。账号/中转的服务端目录仍是旧档位时，回落
  `400000 / 272000 / 128000`。
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
model_templates:
  # Gemini 3.x Flash 共用形状：1M 窗口、`level` 控制的 thinking。
  gemini-flash: &gemini-flash
    limit:
      context: 1048576
      output: 65536
    modalities:
      input: [text, image, pdf]
    thinking:
      level: high

providers:
  gemini:
    api_url: https://generativelanguage.googleapis.com/v1beta/models
    models:
      gemini-3.8-flash: *gemini-flash

model_pools:
  default:
    - gemini/gemini-3.8-flash
```

要点：

- `api_url` 保持在 `/models` 基础路径即可；Chord 会自动追加 `/{model}:streamGenerateContent?alt=sse`。
- `type` 可以省略；Chord 会根据 `/models` 路径自动识别 Gemini。
- Gemini 3.8 Flash（2026 年 9 月 2 日 GA）是目前的主力模型：1M token 上下文、最大 64K 输出，thinking 级别为 `low` / `medium`（官方默认）/ `high`。它不支持 `minimal`，且 `thinking_budget` 已废弃，所以上面模板只用 `level`；模板固定用 `high` 服务 agentic 场景——日常任务降到 `medium` / `low` 可以省延迟和 token。
- Gemini 3.5 / 3.6 Flash 也是同一套结构，并且仍然接受 `minimal`；Flash-Lite 系列则以 `minimal` 为默认值。Gemini 3.1 Pro 只接受 `low` / `medium` / `high`，同样不支持 `minimal`，所以不要把一个 `minimal` variant 套用到整个家族。

### Gemini 的压缩调优

Gemini 长上下文表现随档位差异极大，没有统一的压缩规则：

- **Gemini 3.1 Pro** 的多针长上下文确实弱（公开 MRCR v2 8-needle 检索约 0.26），所以要保留激进压缩：`threshold` 取可用预算的约 0.2、`reminder` 约 0.15（1M 窗口约合 150K–210K）。
- **Gemini 3.8 Flash、Flash-Lite** 为 1M 窗口设计，长上下文表现很好，激进提前压缩只会丢掉它们还能用的上下文。Flash 用全局默认（`threshold` 0.8）或直接不写该模板块即可。3.8 Flash 靠更高的 token 消耗换取更好的准确率，所以长时间 agentic 任务里用量上涨属于正常现象——不是该提前压缩的信号。

```yaml
# 按模型分别配 Gemini 的 compaction；引用该模板的 provider 全部继承。
model_templates:
  gemini-pro: &gemini-pro
    limit: {context: 1048576, output: 65536}
    compaction: {threshold: 0.2, reminder: 0.15}
    thinking:
      include_thoughts: true
    variants:
      high: {thinking: {level: "high"}}
      medium: {thinking: {level: "medium"}}
      low: {thinking: {level: "low"}}
    modalities: {input: [text, image, pdf]}
```

计费提醒：只有 **Gemini 3.1 Pro** 在超过 200K 输入后进入更高输入档（整请求按高价档计费）；Gemini 3.8 Flash 与 Flash-Lite 在任何上下文长度下都是平价，所以 Flash 没有为省钱而提前压缩的理由——只有当你的工作负载确实出现质量退化时才压。如果你既要长可靠窗口、又要 Pro 级质量，那才是该换用 GPT-5.6 Sol / Claude 5 这类模型的场景。

## GLM / BigModel Coding Plan

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
    - bigmodel/glm-5.3-flash
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
- 示例默认池优先选 `glm-5.3-flash`——Coding Plan 主力、原生多模态输入。
  纯文本场景把池条目换成 `bigmodel/glm-5.3`；需要 GLM-5.2 更宽的 effort
  档位（`xhigh` / `medium` / `minimal` / `none`）时也可以保留
  `bigmodel/glm-5.2`——上面的 provider `models` 仍把它列为可选文本模型，
  沿用同一套模板。

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

## DeepSeek

在 `~/.config/chord/auth.yaml` 中配置：

```yaml
deepseek:
  - "$DEEPSEEK_API_KEY"
```

`deepseek-flash` 就是 DeepSeek-V4.1-Flash：1M 上下文、默认开启思考，三条
wire family 都原生支持图像输入。官方 API 上，旧的 `deepseek-v4-flash` 与
`deepseek-v4-flash-vision-exp` 仍被接受，会路由到这里并按 Flash 价格计费；
`deepseek-v4-pro` 的请求也会在 2026-09-14 12:00（北京时间）之后路由过来，
直到 V4.1-Pro 发布。

```yaml
model_templates:
  deepseek-v4.1-chat: &deepseek-v4-1-chat
    limit:
      context: 1000000
      output: 64000
    modalities:
      input: [text, image]
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
        preserve_history: true
      forced_tool_choice:
        suppress_in_thinking: true

  deepseek-v4.1-messages: &deepseek-v4-1-messages
    limit:
      context: 1000000
      output: 64000
    modalities:
      input: [text, image]
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
        preserve_history: true

  deepseek-v4.1-responses: &deepseek-v4-1-responses
    limit:
      context: 1000000
      output: 64000
    modalities:
      input: [text, image]
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
        preserve_history: true

providers:
  deepseek:
    type: chat-completions
    api_url: https://api.deepseek.com/v1/chat/completions
    models:
      deepseek-flash: *deepseek-v4-1-chat

  deepseek-messages:
    type: messages
    api_url: https://api.deepseek.com/anthropic/v1/messages
    models:
      deepseek-flash: *deepseek-v4-1-messages

  deepseek-responses:
    type: responses
    api_url: https://api.deepseek.com/v1/responses
    models:
      deepseek-flash: *deepseek-v4-1-responses

model_pools:
  default:
    - deepseek/deepseek-flash@high
```

要点：

- DeepSeek Chat thinking 使用 `thinking.type`、顶层 `reasoning_effort` 和
  `max_tokens`。`request_overrides` 提供请求形状差异；thinking + 工具调用
  循环中，`openai_visible` 会原样返回 assistant 的 `reasoning_content`。
  请求带 tools 时，DeepSeek 要求后续每一轮都完整回传历史
  `reasoning_content`，否则返回 `400`，所以模板设置 `preserve_history: true`
  让 Chord 在本地保留已完成轮次的思考；不带 tools 时该字段会被忽略。
  DeepSeek 在启用 thinking 时会拒绝 forced tool
  choice，所以模板会把 loop 强制的 `tool_choice: required` 降级为后端默认
  选择。
- DeepSeek Responses 支持 `tool_choice: required`，因此模板保留 loop 的强制
  工具选择。该接口直接返回明文 `reasoning_text`，因此无需请求加密 reasoning；
  它支持 `max_output_tokens`，模板会继续显式发送上限。接口不支持的字段
  （`store`、`background`、`previous_response_id` 等）会被静默忽略；流以
  `response.completed` / `incomplete` / `failed` 事件结束，没有
  `data: [DONE]`。
- DeepSeek Messages 支持 `output_config.effort`；Chord 从 `thinking.effort`
  生成该字段。兼容接口应关闭 Anthropic beta header——它只对 Files API 生效。
  `thinking.budget_tokens` 会被接受但忽略：思考深度由 effort 值决定，不是
  token 预算。DeepSeek 的 Anthropic 兼容接口可能返回无签名 `thinking`，
  而不是 Claude 风格的签名块。`anthropic_unsigned` 会原生回放同
  provider/model 的无签名 thinking，也能把其他 wire family 的可移植可见
  reasoning 转为无签名 `thinking` block；如果 target 仍拒绝该形状，严格兼容
  级别会丢弃 reasoning carrier，但保留工具轮次。
- 三个 wire family 都能收图，图片按输入 token 计费（官方上限为单图 1024
  token）。接口支持 inline base64、外部 URL 与 Files API `file_id`，按文件
  内容识别格式（JPEG / PNG / GIF / WebP），且图片只能出现在 user 消息中：
  system 或 assistant 消息带图会返回 `400`。请求限制：请求体不超过
  48 MiB、单请求最多 600 张图、图片总量不超过 64 MiB（用 `file_id` 时
  200 MiB）、单边不超过 8192 px（单请求图片达到 15 张时降到 4096 px）。
  Chord 固定用 inline base64，所以在 Chord 内部用不了外部 URL 或 `file_id`。
  - `image_url`（Chat）或 `input_image`（Responses）上的 `detail` 接受
    `low` / `high` / `original`（与 `high` 等价）/ `auto`；Chord 在
    Responses 上发送 `auto`，在 Chat 上不带该字段，也不提供按请求配置。
  - [`view_image`](./tools_CN.md) 工具能把本地图片加载进上下文，但只有把
    这个模型放在 `messages` 或 `responses` provider 的池首才行：
    `chat-completions`（`deepseek`）provider 能在用户消息里收图，却无法在
    tool result 里返回图片。
- 第三方 `/responses` 端点由网关自行实现；只有网关明确说明映射方式时，
  才使用 `reasoning.effort` 和 `openai_visible`。
- 对兼容网关，请使用该网关 / 账号实际公开的模型 ID 和限制。见
  [常见问题排查 — DeepSeek / OpenAI 兼容 thinking 模式 400](./troubleshooting_CN.md#deepseek--openai-兼容-thinking-模式-400)。

补充：

- 官方定价页标注的最大输出为 384K；这里 `limit.output: 64000` 是保守的
  本地分配，需要更长输出时按需调大。
- `reasoning_effort`（Chat）与 `output_config.effort`（Messages）接受
  `low` / `high` / `max`，Responses 的 `reasoning.effort` 还接受 `none`
  （关闭思考）；默认值是 `high`。其余取值由后端重映射：`medium` 和
  `xhigh` 映射到 `high`——所以模板只定义 `low` / `high` / `max`
  三个 variant。
- Responses API 位于 `api.deepseek.com/v1/responses`；响应中的
  `output_tokens_details.reasoning_tokens` 由 Chord 按标准 reasoning 回显
  处理，无需额外配置。
- Flash 价格（每百万 token，off-peak | peak）：缓存命中 $0.003 | $0.006，
  缓存未命中 $0.15 | $0.30，输出 $0.60 | $1.20。peak 时段为 UTC 周一至
  周五 01:00–04:00 与 06:00–10:00。见
  [DeepSeek 官方定价](https://api-docs.deepseek.com/quick_start/pricing/)。

### 仍在提供 V4 一代模型的网关

有些 provider 仍在提供 V4 一代的权重，模型 ID 是 `deepseek-v4-flash` 或
`deepseek-v4-pro`。这一代只收文本：flash 和 pro 传图会返回 `400`（一代里
只有实验性的 `deepseek-v4-flash-vision-exp` 收图），所以这类条目不能声明
`image` 输入。直接复用 `*deepseek-v4-1-chat` 会把 `image` 一起继承过来，
用 `modalities` 覆盖，或者干脆按纯文本另建模板：

```yaml
# 网关仍在提供 V4 一代模型时：模板照抄，只保留文本。
model_templates:
  deepseek-v4-chat: &deepseek-v4-chat
    <<: *deepseek-v4-1-chat
    modalities:
      input: [text]

providers:
  deepseek-gateway:
    type: chat-completions
    api_url: https://example.com/v1/chat/completions
    models:
      deepseek-v4-flash: *deepseek-v4-chat
```

### DeepSeek V4.1 Flash 的压缩调优

DeepSeek V4.1 Flash 标称 1M 窗口，但长距离可靠性是这个家族的短板：上一代
V4 的独立 multi-needle 评测中，V4 Pro 在 1M 处只有约 41%（8-needle），
单 needle 约 78%——这种陡降和 Gemini 3.1 Pro 的悬崖如出一辙。V4.1 目前
没有公开的长上下文评测，在此之前仍按同样的口径处理：把可靠工作窗口按
约 200K 对待，尽早压缩。Flash 家族即使全部缓存未命中，也远比同级模型便宜
得多，所以频繁压缩的代价比在高端模型上低——尽早压、多压几次：

```yaml
# 给上面配方里的 deepseek-v4.1-chat / -messages / -responses 模板加上
# compaction；引用这些模板的模型条目会全部继承。
model_templates:
  deepseek-v4.1-chat: &deepseek-v4-1-chat
    limit: {context: 1000000, output: 128000}
    compaction: {threshold: 0.25, reminder: 0.2}
```

DeepSeek 的缓存命中价是业界最低的（$0.003/M），因此一次能保住可缓存前缀的
压缩，在重复读取场景下几乎是免费的。短的交互式会话保持全局默认即可，只有
真正跑长时间 agentic 任务时才需要单独给模型条目调参。

## Qwen 保留历史思考

Qwen 通过 `reasoning_content` 返回可见思考，但大多数型号默认忽略历史
消息里的该字段。只有模型文档明确支持 `preserve_thinking` 时才应开启
回放——目前是 Qwen 3.8 Max，3.7 Max / Plus / Flash，以及 3.6 Max
preview / Plus（含带日期的快照版本）。请以官方支持列表为准；较早的
Qwen 3/3.5 即使会输出思考，也应保持 continuity 关闭。

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

## Kimi

Kimi K3 是当前旗舰思考模型，提供 1M token 上下文、始终启用思考，
`reasoning_effort` 接受 `low` / `high` / `max`（默认 `max`）；会话中途
切换 effort 会让 prefix cache 失效，所以不要频繁改。它要求多轮对话和
工具调用循环完整回传 assistant 消息（包括 `reasoning_content`）。不要
发送 K2.x 的 `thinking` 参数，也不要显式发送采样字段：K3 把
`temperature` 固定为 1.0、`top_p` 固定为 0.95、penalties 固定为 0。

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
provider fallback 都能保留连续性。工具模式契约要求完整 reasoning 历史的
后端（DeepSeek）和 preserved-thinking 模板（GLM `clear_thinking: false`、
Qwen `preserve_thinking`、Kimi K3 / `keep: all`）都设置
`preserve_history: true`，完整 assistant 历史会原样回放。若目标拒绝原生 reasoning，Chord 只会
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

## Grok（xAI）

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

### Chat Completions

Grok 4.6 也能走 OpenAI 兼容的 `/v1/chat/completions`，官方仍在维护这条线路，
只是建议新集成改用 Responses。它同样接受 `reasoning_effort`（`low`、`medium`、
`high` 默认、`xhigh`）；reasoning 模型不接受 `stop`、`presence_penalty`、
`frequency_penalty`，`max_tokens` 已弃用，应改用 `max_completion_tokens`。

网关是否回传 `reasoning_content` 各不相同。网关一直不回传时，Chord 回放
assistant tool call 没有 reasoning content 可用，会按「该后端无法回放
reasoning」处理，从出现工具调用的下一次请求起剥离 `reasoning_effort`——按请求
设置的 effort 就只对每个回合的首个请求生效。想让 effort 和 reasoning 请求覆盖项
在整个回合都保持生效，就用 `compat.chat_completions.keep_reasoning_effort: true`：

```yaml
model_templates:
  grok-4.6: &grok-4-6
    limit:
      context: 500000
      output: 64000
    reasoning:
      effort: high
    compat:
      chat_completions:
        keep_reasoning_effort: true
    variants:
      low:
        reasoning:
          effort: low
      medium:
        reasoning:
          effort: medium
      high:
        reasoning:
          effort: high
      xhigh:
        reasoning:
          effort: xhigh
    modalities:
      input: [text, image]

providers:
  grok-gateway:
    type: chat-completions
    api_url: https://example.com/v1/chat/completions
    models:
      grok-4.6: *grok-4-6

model_pools:
  default:
    - grok-gateway/grok-4.6@xhigh
```

`openai_visible` 依然不用配：Grok 不要求回放 `reasoning_content`。缓存命中
取决于粘性路由：xAI 在 Chat Completions 上接受 `prompt_cache_key` 并映射为
`x-grok-conv-id`；网关两者都不透传时，每个请求都会以缓存未命中重发。

## MiniMax（OpenAI 兼容接口）

在 `~/.config/chord/auth.yaml` 中配置：

```yaml
minimax:
  - "$MINIMAX_API_KEY"
```

OpenAI 兼容端点是 `https://api.minimax.io/v1/chat/completions`。`MiniMax-M3`
是支持多模态的旗舰，窗口 1M token；M2.x 系列（`MiniMax-M2.7`、
`MiniMax-M2.5`、`MiniMax-M2.1`、`MiniMax-M2` 及各自的 `-highspeed` 变体）
只收文本，窗口 204,800 token。M3 默认开启思考（不传 `thinking` 即为
adaptive），M2.x 始终开启、无法关闭；只有 M3 接受
`thinking: {type: disabled}` 跳过思考。

```yaml
model_templates:
  minimax-m3: &minimax-m3
    limit:
      context: 1000000
    modalities:
      input: [text, image]

  minimax-m2x: &minimax-m2x
    limit:
      context: 204800
    modalities:
      input: [text]

providers:
  minimax:
    type: chat-completions
    api_url: https://api.minimax.io/v1/chat/completions
    models:
      MiniMax-M3: *minimax-m3
      MiniMax-M2.7: *minimax-m2x

model_pools:
  default:
    - minimax/MiniMax-M3
```

MiniMax 只公布了上下文窗口，没有公布输出上限，所以模板不写 `limit.output`，
沿用全局上限。M3 还能收视频，但 Chord 的 chat 线路只发送文本和图片。

默认请求形状下，思考内容会带标签写在 assistant 的 `content` 里，官方要求这段
content 完整保留。Chord 会原样回放 assistant content，所以默认形状无需额外
配置；代价是思考会作为普通正文显示，而不是进入 reasoning 展示区。

想让思考走 reasoning 通道，就设置 `reasoning_split: true`：接口会把思考放进
`reasoning_content`，同时返回 `reasoning_details`。Chord 能解析并回放
`reasoning_content`，但没有 `reasoning_details` 的对应实现，而官方要求两者都
完整保留。只有当你的端点接受纯 `reasoning_content` 回放时，才用下面这种形状：

```yaml
# 结构化 reasoning：MiniMax 把思考移到 reasoning_content。
model_templates:
  minimax-m3-split: &minimax-m3-split
    <<: *minimax-m3
    compat:
      request_overrides:
        body:
          reasoning_split: true
      reasoning_continuity:
        mode: openai_visible
        preserve_history: true
```

## Meta Muse Spark

`~/.config/chord/auth.yaml` 配好 key：

```yaml
meta:
  - "$MODEL_API_KEY"
```

Muse Spark 1.3 是 Meta 的 agentic/编程模型，跑在 Meta Model API 上：上下文
1,048,576 token，官方参考配置的输出上限 131,072，输入支持文本、图片和 PDF
（接口还收 video 和 audio，但 Chord 的 Responses wire 发不出去），思考始终
开启，effort 可取 `minimal` / `low` / `medium` / `high` / `xhigh` / `max`。
三个兼容面里要选 Responses：只有它跨轮携带思考，Chord 会把思考作为加密
reasoning item 回放。

```yaml
model_templates:
  muse-spark-1.3: &muse-spark-1-3
    limit:
      context: 1048576
      output: 131072
    reasoning:
      effort: high
      summary: auto
    variants:
      minimal:
        reasoning:
          effort: minimal
      low:
        reasoning:
          effort: low
      medium:
        reasoning:
          effort: medium
      high:
        reasoning:
          effort: high
      xhigh:
        reasoning:
          effort: xhigh
      max:              # 仅 Standard 档
        reasoning:
          effort: max
    modalities:
      input: [text, image, pdf]

providers:
  meta:
    type: responses
    api_url: https://api.meta.ai/v1/responses
    models:
      muse-spark-1.3: *muse-spark-1-3

model_pools:
  default:
    - meta/muse-spark-1.3@xhigh
```

- 默认请求形状不需要 `compat`。Responses provider 本来就会发
  `include: ["reasoning.encrypted_content"]` 加 `store: false`，正是 Meta 推荐
  的 stateless 重放组合；回放 reasoning item 时 Chord 会显式带上 `summary`
  字段，这是 Meta 的硬性要求。`prompt_cache_key` 默认发送且受支持，
  `client_metadata` 则接受后忽略。
- Muse Spark 始终思考，`reasoning.effort: none` 会返回 `HTTP 400`，不要加
  `none` variant。`max` 仅 Standard 档提供。
- 示例池从 `@xhigh` 起步；要拉满思考就换 `@max`，日常想快一点可以降到
  `@medium` 或 `@low`。
- `muse-spark-1.3-contributor` 是同一模型的低价档，代价是允许 Meta 用你的
  prompt 和 completion 训练；能接受这个交换再用，而且该档没有 `max`。
- `cost` 是可选项，这份配方不写，Chord 也就不会估算这个模型的花费；想统计
  成本就按你账号的费率补上 `cost` 块。
- `limit.output` 取 Meta 参考配置里的 `131072`。Chord 在 Responses 上默认不
  发 `max_output_tokens`；想让 Chord 显式执行这个上限，设
  `compat.responses.send_max_output_tokens: true`。
- Meta 的发布评测显示长上下文检索基本不衰减（MRCR v2 8-needle 在 256K–512K
  是 98.5，512K–1M 是 98.1），没有已知的质量悬崖要压，沿用全局 compaction
  阈值即可。
- 另有 Messages 兼容端点（`https://api.meta.ai/v1/messages`）给 Anthropic
  形态的客户端用；本页只写 Responses 路径。Chat Completions 不跨轮携带思考，
  agentic 场景不推荐。

验证：

```bash
chord doctor models --model meta/muse-spark-1.3@xhigh
```

## 如何验证任意一份配置

复制完配置后，先跑一个定向检查：

```bash
chord doctor models --model provider/model
```

然后再验证你实际要用的 variant，例如：

```bash
chord doctor models --model openai/gpt-5.6-sol@xhigh
chord doctor models --model codex/gpt-5.5@xhigh
chord doctor models --model anthropic/claude-opus-5@high
```
