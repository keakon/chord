# 按工作选模型

<!-- description: 按任务难度选择具体模型，再配置协调、执行和审查角色；包含四个任务档位与团队配方。 -->

> 选型日期：2026 年 10 月 9 日。下面的候选顺序是建议的试用顺序，不是 Chord 实测排名。购买或改配置前，请确认官方型号、价格和账号可用性。

日常开发从均衡档开始，涉及并发、权限或恢复时用可靠性优先档。明确的机械任务可以降档；小任务由单个主代理直接完成，不必启动整套团队。接入模型用 [`chord config add`](./cli_CN.md#chord-config-add)，限额、模态和 reasoning 档位由目录补齐；完整布局见[配置示例](./examples/index_CN.md)。

## 你已经在付的，Chord 用得上吗？

Chord 只接受 API key，或 Codex OAuth（`chord auth codex`）。订阅如果只能登录那家自己的应用，这里用不了。

| 你已经有 | 能给 Chord 用吗？ |
| --- | --- |
| 支持第三方客户端使用的编程套餐（例如 ChatGPT Plus / Pro（Codex）、Command Code GOAT、OpenCode Go） | 能 |
| 任何 Chord 支持协议（Chat Completions、Responses、Messages、Generate Content）的模型 API key | 能 |
| Claude Pro / Max、SuperGrok、Muse Code、Google AI Pro / Ultra，或其他只能登录自家客户端的编程套餐 | 不能 |
| GLM Coding Plan | 不能（仅限官方支持的指定工具与产品环境中使用） |

已有可用套餐时，先从账号实际提供的模型中选择。包月是否划算取决于额度、限流和任务量；按量 API 还要计算重试、审查及返工成本。只有一份强模型预算时，优先覆盖会影响整个任务的判断，不固定按角色名称分配。

## 按任务选档位

这些档位是配置建议，Chord 不会根据任务难度自动切换。价格档位也不等于能力档位；同一模型可以承担多个角色。

### 经济档：明确、局部、容易验证

适合定位文件和调用方、固定规则的重命名、配置调整，以及按已确定接口补测试。

1. GPT-6 Luna：结构化提取、机械执行和已确定计划的调度。
2. DeepSeek V4.1 Flash：仓库探索、重复读取和明确规格的代码工作。
3. Gemini 3.8 Flash：包含 PDF、图表或截图的资料任务优先考虑它。

协调者使用这一档时，目标、依赖和验收应已确定。需要重新拆解需求、选择行为或解决设计分歧时，转交更强模型。根因分析和安全敏感判断不按机械任务处理。

### 均衡档：日常开发

适合常规功能、多文件修改、已有稳定复现的 bug，以及接口大体明确的重构。

1. GPT-6.1 Sol：日常主代理和常规实现的起始选择；从 `medium` 开始，复杂规划或审查再试 `high`。
2. Claude Sonnet 5.5：常规 coder 或主代理的候选；实现任务可从 `high` 开始，再测试降低 effort 是否影响质量。
3. Claude Opus 5.5：需求更模糊、调用链更长或返工成本较高时，用于协调、实现或审查。

coder 可以决定局部实现细节；行为、公共接口或安全边界仍需负责人确认。没有必要让每次修改都经过 explorer、expert、coder、reviewer 的完整接力。

### 可靠性优先档：复杂代码库与高风险修改

适合跨子系统改动、间歇性故障、并发与生命周期、权限、持久化恢复和性能热路径。

1. Claude Opus 5.5：协调、复杂实现和审查的默认候选，先试 `high`。
2. GPT-6 Astra：用于重要设计决策、困难根因或独立调查路线，按需调用。
3. GPT-6.1 Sol：控制成本的候选，从 `high` 开始，在本项目任务上验证后再扩大职责。

复杂部分可由 expert 直接调查、实现并验证。结论尚未消除实现歧义时，强制转交便宜 coder 容易增加交接和返工成本。

### 攻坚档：已有路线无法收敛或失败代价很高

先检查失败是否来自环境、错误假设或验收不明确，再决定是否提高模型能力。

1. GPT-6 Astra：承担完整的高难度调查或设计任务。
2. Claude Fable 5.1：用于困难推理或第二条独立调查路线。
3. Claude Opus 5.5：提高 effort 后重试有证据支持的新路线；更贵的型号不保证更好的结果。

保留常规 coder 和 explorer，仅把困难部分交给攻坚模型。需要第二意见时，让两条路线先独立收集证据，再比较结论。

## 团队角色与模型池

Chord 自带 `builder` 和 `planner`。下面的角色来自可选的[团队方案](./examples/examples-team_CN.md)，需要时再创建；同一模型可以用于多个独立会话。

| 角色 | 默认池 | 职责 |
| --- | --- | --- |
| orchestrator | `deep` | 保留完整需求，拆解任务，协调设计，纠偏并验收整体交付；小任务直接完成。 |
| coder | `coding` | 实现范围明确的改动并验证，发现行为或接口未确定时反馈。 |
| explorer | `fast` | 定位文件、调用关系和测试，返回可追溯证据及不确定性。 |
| reviewer | `deep` | 根据原始需求和实际改动独立审查，检查回归与验证缺口。 |
| expert | `deep`，按需换攻坚模型 | 处理根因、设计和复杂实现，必要时直接完成专项任务。 |

### 协调者需要多强？

执行已确定计划的调度员，可以使用经过验证的中等或轻量模型。开放式团队负责人还要识别遗漏约束、判断任务依赖、处理矛盾报告和决定何时完成，应从均衡档或可靠性优先档建立基线。

有 expert 不会自动消除协调者的判断工作。expert 必须收到相关目标和约束，并能反馈计划与证据的冲突。协调者需要保留报告里的限制，不能把“局部测试通过”变成“整体完成”。

### 三套起始配方

| 配方 | `deep` | `coding` | `fast` |
| --- | --- | --- | --- |
| 省钱、规格明确 | GPT-6.1 Sol | GPT-6 Luna | GPT-6 Luna |
| 日常开发 | GPT-6.1 Sol | Claude Sonnet 5.5 | GPT-6 Luna |
| 复杂开发 | Claude Opus 5.5 | Claude Sonnet 5.5 | GPT-6 Luna |

[团队示例](./examples/examples-team_CN.md)采用复杂开发配方。只有 Codex 时，从账号目录中选择 Sol 承担协调、实现和审查，Luna 承担探索及机械任务；Astra 在可用时用于攻坚。只有 Anthropic API 时，可以让 Sonnet 承担日常实现和探索，Opus 承担复杂判断。API key 不要求订阅该厂商的聊天应用。

按任务将探索模型替换为 DeepSeek V4.1 Flash 或 Gemini 3.8 Flash。只使用其他渠道时，也按同样职责筛选，先让候选模型完成本项目的代表性任务，再决定是否承担协调或终审。

模型池中的多个条目提供请求失败时的回退，不会按难度自动选模，也不会因为答案错误而自动升级。需要换档时，明确选择对应角色或模型池。不要把三个候选全部放入一个池，就把它当作任务路由器。

## 调整与验证

- 同一组真实任务上比较单代理、强协调者配中档执行者，以及中等协调者配相同执行者。每次只改变一个因素；结果接近时重复运行。
- 记录整体验收成功率、错误宣告完成、人工纠正次数、总费用和耗时。token 单价不能代表含重试、审查和返工的任务成本。
- 从少量 worker 开始。并行写入需要独立交付与明确的共享决策；文件不同仍可能存在语义依赖。`expected_write_scope` 是协调声明，实际权限由角色规则决定。
- 审查要看原始需求、实际改动和验证证据，不能只读实现者摘要。不同模型可以提供另一种视角，但不保证错误相互独立。
- 先使用模型支持的 `medium` 或 `high` 档位，按结果调整。不同厂商的 effort 名称不代表相同计算量；`max` 不保证更好的成本或质量。
- 用 `chord doctor models --pool <池名>` 检查接入。它不验证上述任务成功率。GPT-6.1 Sol 的工具任务应使用 Responses 接入；配置方式见[模型配置](./model-configs_CN.md)。

## 资料与适用范围

型号与接入能力以 [OpenAI 模型文档](https://developers.openai.com/api/docs/models)、[Anthropic 模型文档](https://platform.claude.com/docs/en/models/overview)、[Gemini 3.8 Flash 模型卡](https://deepmind.google/models/model-cards/gemini-3-8-flash/)和 [DeepSeek V4.1 Flash 模型卡](https://huggingface.co/deepseek-ai/DeepSeek-V4.1-Flash)为准。[Artificial Analysis](https://artificialanalysis.ai/models)可用于比较通用能力、速度和成本，其分数不直接代表 Chord 的协调成功率。

2026 年的公开经验支持按职责分配模型，但没有给出适用于所有任务的协调者门槛：

- [Cursor 的多代理工程实验](https://cursor.com/blog/agent-swarm-model-economics)展示了强规划者与便宜执行者的组合，尚未覆盖全部模型配对。
- [小模型协作研究](https://arxiv.org/abs/2601.11327)发现协调者推理很重要，实验主要涉及问答和工具任务。
- [软件修复中的 manager/worker 实验](https://arxiv.org/abs/2603.26458)支持强指导者的价值，但模型配对和弱协调者样本有限。
- [DeOrch](https://arxiv.org/abs/2610.07556)展示了专门训练的小协调模型；结果不能直接推广到未经协调训练的便宜通用模型。

这些资料用于提出候选配置。是否降档，仍要用自己的代码库和验收条件验证。
