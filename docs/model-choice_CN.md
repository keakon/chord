# 按工作选模型

<!-- description: 先决定 Chord 接什么，再按角色分模型。配方和示例页用来演示旗舰怎么接线，选定之后再去复制。 -->

> 口径：2026 年 9 月。套餐会变，买之前、改配置之前以各家官网为准。

还没选定渠道时看这一页。可复制的接线在[模型配置速查](./model-configs_CN.md)和[配置示例](./examples/index_CN.md)。那些片段用当前旗舰，是为了把字段写全；选定之后把模型 ID 换掉即可。

## 你已经在付的，Chord 用得上吗？

Chord 只接受 API key，或 Codex OAuth（`chord auth codex`）。订阅如果只能登录那家自己的应用，这里用不了。

| 你已经有 | 能给 Chord 用吗？ |
| --- | --- |
| 支持第三方客户端使用的编程套餐（例如 ChatGPT Plus / Pro（Codex）、Command Code GOAT、OpenCode Go） | 能 |
| 任何 Chord 支持协议（Chat Completions、Responses、Messages、Generate Content）的模型 API key | 能 |
| Claude Pro / Max、SuperGrok、Muse Code、Google AI Pro / Ultra，或其他只能登录自家客户端的编程套餐 | 不能 |
| GLM Coding Plan | 不能（仅限官方支持的指定工具与产品环境中使用） |

## 四种情况怎么选

### 1. 已经在订别的

表里是「能」，就用它。是「不能」，原来的应用照旧，接着看第 2、3、4 条。Codex：一份套餐就能填满五个角色——orchestrator、explorer、coder 用账号目录里最便宜的模型，expert 和 reviewer 用在额度允许范围内最强的模型。便宜套餐或 API key，分法见第 2 条。

### 2. 少量使用，追求性价比

买一个便宜的官方 API key（例如 Gemini 3.8 Flash、DeepSeek V4.1 Flash 或 GPT-5.6 Luna），或订一份提供标准端点的便宜套餐（例如 Command Code GOAT、OpenCode Go）。调度、搜索、大部分改代码，五个角色全走它。难的决策变多时，再把 expert 和 reviewer 换成更强的模型；便宜套餐没有够强的模型时，给这两个角色另配一个官方 key。

### 3. 大量使用

天天用的话，包月比按 token 计费划算。订 Codex，登录进去：orchestrator、explorer、coder 用账号目录里最便宜的模型，expert 和 reviewer 用在额度允许范围内最强的模型。

### 4. 不太在意账单

把当时能拿到的最强 API 模型——按这份口径是 GPT-6 Astra 和 Claude Fable 5.1——留给 expert 和 reviewer。orchestrator、explorer、coder 用便宜、快的模型就够了，用不到旗舰。

## 哪个角色用什么模型？

调度、机械改代码这些活，便宜模型就能干，用旗舰是浪费。池子按用途拆：便宜快的装一个，你愿意付钱的旗舰装一个；团队示例里叫 `fast` 和 `deep`。

下面这些名字不是内置角色。Chord 自带的是 `builder` 和 `planner`。五人分工来自可选的[团队方案](./examples/examples-team_CN.md)，想要那种布局再去复制。

| 团队示例里的角色 | 建议模型 | 为什么 |
| --- | --- | --- |
| orchestrator | Gemini 3.8 Flash、DeepSeek V4.1 Flash、GPT-5.6 Luna | 分类、派工、合成，每个回合都跑。 |
| explorer | DeepSeek V4.1 Flash、Gemini 3.8 Flash、GPT-5.6 Luna | 只读探路，报告文件在哪，不做判断；DeepSeek 最省，Gemini 读资料更强。 |
| coder | DeepSeek V4.1 Flash、Gemini 3.8 Flash、GPT-5.6 Luna | 改哪、改成什么都已写清，这类机械改动它们都够用。 |
| expert | Claude Fable 5.1、GPT-6 Astra | 根因、架构取舍、并发与热路径，判断错了会变成隐性债。 |
| reviewer | Claude Fable 5.1、GPT-6 Astra | 只抓回归与不变量，不重做设计。 |

只订了套餐、没单独买 API key 时，就从套餐目录里挑：orchestrator、explorer、coder 用最便宜的模型，expert、reviewer 用额度允许范围内最强的模型。Codex 对应 GPT-5.6 Luna 和 GPT-6 Astra（账号里没有 Astra 就用 GPT-5.6 Sol）。

落地就按用途拆池：`deep` 放 expert 和 reviewer 的模型，`fast` 放 explorer 和 coder 的模型。团队示例把 orchestrator 也放在 `deep`；实际使用中它用不到旗舰，放进 `fast` 或单独的便宜池都行。

### 检索用哪个模型？

- **仓库里找文件、读代码**：DeepSeek V4.1 Flash。只读探路用不到闭卷知识，它单价最低、缓存便宜，适合反复读同一批文件。
- **网页资料、PDF、图表**：Gemini 3.8 Flash。读长 PDF、理解图表是它的强项；DeepSeek 闭卷很弱，检索只能靠外部搜索工具补，别让它凭记忆答。
- **只有 Codex 订阅**：仓库探路用 GPT-5.6 Luna；它长上下文弱，超大仓库先收窄范围再派。
- **两类都要、只想要一个模型**：用 Gemini 3.8 Flash。

### coder 可以用便宜的模型吗？

可以，而且默认就该用。coder 适合「改哪、改成什么」已经定下来的活：重命名、机械重构、格式和配置调整、小的局部修复、按固定接口补测试。判断发生在 expert 那边；这类活 DeepSeek V4.1 Flash、Gemini 3.8 Flash、GPT-5.6 Luna 都够用，账单比旗舰低得多。

还需要判断的活就不适合它：问题还没有定论（「查一下为什么」「选个方案」「注意并发」），要动的行为涉及协议、数据模型、并发与生命周期、权限或恢复，或者已经失败过两次。系统级、陌生环境的活（新语言、新构建系统、容器里）也要先把路径和验收写清楚再跑；三个模型里 DeepSeek V4.1 Flash 在这种环境下最弱。

### 没有 GPT 或 Claude 订阅，expert 用什么？

这两家都不用订阅，单买 API key 就能用，所以没有 ChatGPT、Claude 套餐也能选 Fable 5.1 和 Astra；完全不想碰这两家的话，从第 3 条往下看。

1. **Claude Fable 5.1**：用 Anthropic API key 按量付费。架构品味、根因分析、深研是它最强的项，缓存价格也适合反复读同一批文件。
2. **GPT-6 Astra**：单独买 OpenAI API key 就行。它更省 token，适合「问题已经收窄、要一次做对」的场景；缓存更贵，别把整个仓库每轮喂给它。
3. **Muse Spark 1.3**（Meta Model API）：GPT、Claude 渠道之外最强的一个。长程实现、大仓库是它的强项；根因和架构上的判断弱一档，派给它时把 expert 的活拆小、多验证。
4. **GLM-5.3 或 Kimi K3**：开源模型里最强的两个，OpenCode Go、Command Code GOAT 这类开源模型套餐就有。能顶 expert 的活，但架构、并发的终审别交给它们。
5. **上面都没有**：把 expert 的问题压小——让便宜模型复现、缩小范围；等判断错了会变成隐性债时，再回头看前面几条。日常讨论可以让 Gemini 3.8 Flash 先顶一轮，别让它当终审。

reviewer 跟 expert 用同一个模型。只有一份旗舰预算时先给 expert，reviewer 在实质改动后再开。

### orchestrator 需要什么能力？

它每个回合都跑：读任务、分类、派人、收结果、决定纠偏还是升级。要的是：

- 工具调用稳，能读懂 worker 的报告并转述结论；
- 能分清「这题还要不要做产品级决定」：要就派 expert，路径和替换都写死就派 coder，只是找文件在哪就派 explorer；
- 便宜、快。旗舰的推理和品味用在这里是浪费，值得花钱防的只有派错人。

默认用 Gemini 3.8 Flash、DeepSeek V4.1 Flash 或 GPT-5.6 Luna；三者里 DeepSeek 最省。观察到它经常派错人，再换更强的模型；别一上来就用旗舰。

## 决定之后

1. 到[模型配置速查](./model-configs_CN.md)复制对应片段。
2. 需要完整文件布局时，从[配置示例](./examples/index_CN.md)起步，把旗舰 ID 换成你真正选的。
3. 用 `chord doctor models` 确认能通。
