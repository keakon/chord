# 模型配置

<!-- description: 用内置模型目录接入模型：chord config add、目录刷新、自定义端点借用与网关行为经验。 -->

`chord config add` 从内置模型目录读取已知参数，直接写入 `config.yaml`，包括上下文与输入输出限额、输入模态、推理档位，以及已验证端点的 Responses 字段发送规则。保存时设置 `catalog: false`，模型运行时不再依赖目录补齐。目录数据来自公开的 [chord-models](https://github.com/keakon/chord-models) 仓库：每个 chord 发布版内嵌一份当时的已验证快照，目录刷新可以不等发版带来新数据。已验证模型事实来自引用的文档；官方接入信息提供配置所需的地址和凭据变量，托管绑定另行记录经过验证的端点行为。模型事实或接入信息不代表已完成真实请求验证。**候选条目**保留尚待核实的发现及其来源、作用域，永不成为运行时默认值。

不带参数的 `chord config add` 可以浏览目录；`chord doctor config` 会标明当前生效的是哪份快照、来自哪里——内嵌快照，还是带上游 tag 的刷新缓存。协议与字段语义见[配置参考](./configuration_CN.md)；本页讲怎么把模型接上、怎么保持配置不过期。

## 最快路径：`chord config add`

托管 preset 的 provider（`openai`、`anthropic`、`gemini`、`codex`）一条命令即可：

```bash
chord config add openai/gpt-6.1-sol
```

绑定 `preset` 的请求模型名在添加时按目录解析一次，命令将解析后的模型参数和池引用写入配置，保留你已有的显式设置。其余接入选项交给参数（`--pool` 指定目标池，`--api-key-env` 写入凭据引用），完整说明见 [`chord config add`](./cli_CN.md#chord-config-add)。

思考档位保存在命名 `variants` 中；基础档位保持未设置，除非你已显式配置。命令优先使用端点已验证的变体列表；没有该列表时，只在协议一致的情况下使用模型文档列出的档位。通过 `--variant high` 或 `openai/gpt-6.1-sol@high` 等引用选择档位，未显式选择时使用端点默认值。

自定义网关用别的名字提供目录模型时，显式借用其事实：

```bash
chord config add mygw/claude-gw --url https://gateway.example.com/v1/messages \
  --catalog anthropic/claude-opus-5-5
```

在交互终端中，可以直接运行 `chord config add mygw/gpt-6-sol`。没有目录绑定时，Chord 会列出接近的已验证模型，由你按数字键立即选择，无需回车；按 `m` 可输入完整目录 ID，按 Esc 取消；新 provider 接着填写 API 地址和密钥环境变量。选择后可选择已有模型池或创建新池，并调整推理档位和请求体压缩，查看预览后确认保存。已有 provider 会沿用地址和凭据。输入 `q` 或在保存时选择否即可取消，文件保持原样。

脚本可传 `--no-interactive` 和完整参数。非交互模式未命中时会列出已验证模型及刷新候选，然后返回错误；不会根据名字自动绑定或采纳候选。要接入刚发布的新模型，先让命令从上游仓库刷新目录（显式联网操作）：

```bash
chord config add mygw/gpt-6.2-sol --url ... --refresh-catalog
chord config refresh-catalog   # 或者只刷新、不添加
```

刷新快照按版本号整体替换目录。同一来源、内容一致的同版本快照也可带来候选条目。任何一步失败（网络、快照损坏、schema 不兼容）都回退到当前生效的快照——内置目录的离线可用性不受影响。

刷新影响后续添加操作，以及仍在运行时引用目录的模型。保存为 `catalog: false` 的模型保持原有参数。重复执行 `chord config add`，即使指定 `--catalog`，也会保留已有显式值；需要调整时直接编辑 `config.yaml`。

手写的目录引用可能在启动时和 `chord doctor config` 中收到更新建议。确认在当前目录版本下保留该引用：

```bash
chord config add mygw/claude-gw --keep-current
```

依赖某个模型之前先做端到端验证：

```bash
chord doctor models --model mygw/claude-gw
```

## Gemini 4 Argon

内嵌目录快照 [v2026-11-11.1](https://github.com/keakon/chord-models/tree/v2026-11-11.1) 包含 `google/gemini-4-argon`。已有安装可先刷新目录，再使用 `GEMINI_API_KEY` 接入官方 API：

```bash
chord config refresh-catalog
chord config add google/gemini-4-argon --api-key-env GEMINI_API_KEY
chord doctor models --model google/gemini-4-argon
```

Argon 的输入、思考和输出共享 1,048,576 token 窗口，没有单独公布的输入上限。可选思考变体为 `low`、`medium` 和 `high`，不支持 `minimal`。通过 `google/gemini-4-argon@high` 等引用选择档位，不带变体时使用端点默认值。模型事实保留长输出容量，官方 `gemini` 绑定将 `limit.output` 设为 65,536，约束当前流式请求的思考与输出总量；实际请求还受全局 `max_output_tokens`（默认 64,000）和剩余窗口限制。

启用 `compat.generate_content.automatic_continuation: true` 后，Chord 会携带服务端 checkpoint 继续同一次生成，连接中断时也优先在同一模型、端点和凭据上恢复。恢复失败后，备用模型从生成开始前的可靠历史重新求解，无法继承原来的思考。此能力只作用于原生 `generate-content` 协议，不适用于 OpenAI 兼容接口或 Code Assist。普通多轮工具调用与一次生成跨 HTTP 请求续传是两种不同流程。

当前内嵌目录仍采用单请求上限。需要原生续传时，可显式配置：

```yaml
max_output_tokens: 262144
providers:
  gemini:
    type: generate-content
    api_url: https://generativelanguage.googleapis.com/v1beta
    response_header_timeout: 1200
    stream_idle_timeout: 1200
    models:
      gemini-4-argon:
        catalog: false
        compat:
          generate_content:
            automatic_continuation: true
            single_request_output_limit: 65536
        limit:
          context: 1048576
          output: 1048576
        variants:
          low:
            thinking:
              level: low
          medium:
            thinking:
              level: medium
          high:
            thinking:
              level: high
```

两个 compat 字段分别继承，优先级为模型配置 > provider 配置 > 目录默认。显式 `false` 关闭续传，此时 `single_request_output_limit` 限制单次输出预算；`null` 或空对象不等于关闭。开关未设置时默认关闭，不按模型名称推断。网关必须支持 token 透传才能启用，Chat Completions 的 `native_thinking` 设置不能代替此开关。

全局默认预算仍为 64,000；只有提高 `max_output_tokens` 才会增加总思考与输出预算，实际值仍受模型上限和剩余窗口限制。最终达到 `MAX_TOKENS` 后不会自动增加预算。多跳会重复计费输入，Context 显示的窗口大小与累计计费输入分开计算。本地工具等待最终响应确认并保存后执行。checkpoint 只在当前进程的请求期间有效，重启后不能续传原生思考。

Argon 需要有可用额度的付费 API 账号。免费层额度为 0 时会返回 HTTP 429；Google AI Pro / Ultra 订阅不替代 API 凭据与额度。用上面的 `doctor models` 检查你自己的接入；目录收录不表示账号已获准调用。

## Gemini 4 Carbon

内嵌目录快照 [v2026-11-11.1](https://github.com/keakon/chord-models/tree/v2026-11-11.1) 同样包含 `google/gemini-4-carbon`。Carbon 是 Google 最强的模型，面向复杂多步编码与长程智能体任务；大多数日常软件工程仍以 Argon 为宜。接入与 Argon 同一路由和 `GEMINI_API_KEY`：

```bash
chord config refresh-catalog
chord config add google/gemini-4-carbon --api-key-env GEMINI_API_KEY
chord doctor models --model google/gemini-4-carbon
```

Carbon 的容量规格与 Argon 一致：输入、思考和输出共享 1,048,576 token 窗口，没有单独的输入上限；官方 `gemini` 绑定将流式请求限制在 65,536 token；思考档位为 `low`、`medium`（默认）和 `high`；输入支持文本、图片、视频、音频和 PDF。上一节 Argon 的 `compat.generate_content.automatic_continuation` 配置原样适用，把模型名换成 `gemini-4-carbon` 即可。

Carbon 以 Argon 的首发价上线：2026 年 11 月 30 日前每百万输入/输出 token 2/10 美元，此后为 4/20 美元。免费层额度为 0 时与 Argon 一样返回 HTTP 429；2026-11-11 的冒烟检查已到达 API 但被配额拦截，请用 `doctor models` 自行验证接入。

## 自定义端点：借用目录事实

`chord config add ... --catalog <id>` 会将所选配方复制到模型配置，并写入 `catalog: false`。显式选择优先于 `preset` 的自动绑定和已保存的目录选择，所选模型必须适用于当前 `preset`。自定义端点（无 `preset`）复制限额、模态和上下文压缩建议；与官方使用相同协议时，还会复制模型行为、`compat`、推理变体和 Responses 字段发送规则。`provider` 保留原有地址、凭据和传输设置。

需要在运行时按目录补齐参数，也可以手写目录引用。该模型每次加载都依赖所选目录条目：

```yaml
providers:
  mygw:
    type: messages
    api_url: https://gateway.example.com/v1/messages
    models:
      claude-gw:
        catalog: anthropic/claude-opus-5-5
```

未知参数保持缺失，需按端点文档补齐。费用统计用的 `cost` 应按端点价格显式填写，包括缓存价格；添加模型会保留已有 `cost`。显式配置值始终优先于目录事实；`chord config advise` 也只对你显式声明的值给建议。

刷新得到的**候选条目**会出现在建议列表里，标注被观察到的 provider 作用域和来源。同一 wire 名在多个作用域被观察到时，Chord 并列展示、优先展示与你的端点匹配的作用域，但绝不替你挑选：非匹配作用域的数值只作参考。采纳候选条目意味着把它的观测值作为你自己的显式配置写进配置文件——候选永远不成为默认值。若候选引用了某个已验证模型，借用那个模型（`--catalog <id>`）是经过验证的采纳路径。

## 网关行为经验

已验证 preset 绑定承载了 Chord 对端点的记录；自定义网关可能有不同表现。下面的旋钮在[配置参考](./configuration_CN.md)里有完整语义，这里只给可操作的结论。

### Chat Completions 网关背后的 thinking

Chord 的 `thinking.*` 键与 wire 无关，但把 `/v1/chat/completions` 翻译成原生 API 的网关只按自己认识到的形状读取 thinking 配置。`compat.chat_completions.native_thinking` 决定 Chord 写进 chat 请求体的形状：`gemini` / `gemini-3`（`extra_body.google.thinking_config`）、`anthropic`（`thinking: {type, budget_tokens}`）、`thinking`（DeepSeek、GLM、Kimi K2.x、Doubao 使用的原生 `thinking: {type}` 对象）、`qwen`（`enable_thinking`）；`claude`、`kimi` 等家族名选择相同形状。只有 DeepSeek 路由不需要选择器就会带上 `thinking` 对象。

选择器同时也是 Chord 识别网关模型是 Gemini 还是 Claude 的唯一信号，与模型 ID 叫什么无关：没有它，Chord 不会回写 Gemini 的 thought signature，Gemini 3/4 会以 HTTP 400 拒绝每次工具调用之后的下一个请求。所以网关后面的每个 Gemini 3/4 模型都要钉上 `native_thinking: gemini-3`，哪怕它没配 thinking 块。`chord doctor config` 会对缺选择器的 Gemini 3/4 模型和 thinking 块会被丢弃的网关模型给出警告。

### 跨协议的 reasoning 连续性

要求每次请求带回完整 reasoning 历史的后端，需要 `compat.reasoning_continuity.reasoning_replay: all`——默认值（`current_turn`）只回放当前轮。DeepSeek 的 Chat/Messages 自己就会保留完整历史；保留 thinking 的配方（GLM `clear_thinking: false`、Qwen `preserve_thinking`、Kimi K3 `keep: all`）以及 MiniMax thinking 模式这类硬回放契约，需要显式设置 `reasoning_replay: all`。概念与取舍见[回放契约怎么选](./reasoning_CN.md#决定回放契约)与[回退池里跨协议保留什么](./reasoning_CN.md#跨-provider-回退时保留什么)。

### 压缩阈值调优

自动压缩使用固定模型基数：provider 公布了独立输入上限时使用该值，否则从总窗口中减去模型的最大输出容量。最大输出未知或可占满整个窗口时，以总窗口为基数。修改请求输出上限不会移动压缩阈值。显式设置的模型或全局阈值优先；未设置时采用该模型的目录建议，没有目录建议才使用全局默认值 0.8。机制见[上下文压缩](./context-management_CN.md#上下文压缩compaction)。

目录阈值是调优起点，不是 provider 限制，也不代表已测得的性能最优值。应按实际任务质量、延迟、请求费用和缓存复用效果调整。所选模型的 `config_profile` 和 `cost.notes` 发布在 [chord-models](https://github.com/keakon/chord-models) 仓库；长上下文价格可能高于基础单价。

## 目录配方包含什么

目录配方包含 token 限额和输入模态、推理变体、模型级 `compat` 和有依据的上下文压缩提示。`chord config add` 解析配方后，将模型字段显式保存到配置中；新 `provider` 使用官方 URL，已有自定义地址保持原样。例如为 `sample/model` 指定 `--catalog moonshotai/kimi-k3`，命令会将完整推理历史回放设置复制到 `compat`。全部字段及其来源发布在 [chord-models](https://github.com/keakon/chord-models) 仓库。

网关提供不同协议时，仍继承限额、模态和上下文压缩建议；协议专用参数需按网关的接口配方设置。

compat 按字段继承，显式模型值及 provider 值优先于目录默认值。其他已有模型配置块按用户配置保留，目录只填充缺失的块；将块设为 `null` 会阻止目录重新填充。模型压缩建议只补齐未设置的字段。显式设置全局或模型 `threshold` 时，目录不再填充缺省的 `reminder`，提醒线按有效阈值派生；显式全局 reminder 也优先于目录建议。模型显式值仍优先于全局设置。

请求体压缩和上下文 compaction 是两件事。自动添加的 provider 默认按原文发送请求体，只有 `preset: codex` 例外——它像 Codex 客户端一样发送 `zstd` 压缩的请求体。目录中记录的 Codex `zstd`、Anthropic `gzip` 支持信息仅供参考。要覆盖 preset 默认值，把 provider 的 `compress` 设为 `gzip`、`zstd` 或 `off`；添加模型会保留已有的显式压缩设置。

模型配置可以集中在 `model_templates` 中，通过 YAML 锚点复用。思考档位定义在 `variants` 中，使用 `模型名@档位` 选择。模型文档列出的档位可能多于某个托管端点已验证的变体，使用前需确认端点接受哪些档位。

下面的 Messages 配置让几个 Claude 模型共用参数，并列出它们支持的 `low`、`medium`、`high`、`xhigh` 和 `max` 五个档位。基础配置不设置 `effort`，不带档位的模型引用使用端点默认值。各模型的 `cost` 按端点价格分别添加：

```yaml
model_templates:
  claude-5: &claude-5
    catalog: false
    limit:
      context: 1000000
      output: 128000
    modalities:
      input: [text, image, pdf]
    thinking:
      type: adaptive
      display: summarized
    prompt_cache:
      mode: explicit
      ttl: 5m
      cache_tools: true
    compaction:
      threshold: 0.7
      reminder: 0.6
    variants:
      low:
        thinking:
          effort: low
      medium:
        thinking:
          effort: medium
      high:
        thinking:
          effort: high
      xhigh:
        thinking:
          effort: xhigh
      max:
        thinking:
          effort: max
providers:
  mygw:
    type: messages
    api_url: https://gateway.example.com/v1/messages
    models:
      claude-fable-5-2: *claude-5
      claude-opus-5-5: *claude-5
      claude-sonnet-5-5: *claude-5
model_pools:
  fable:
    - mygw/claude-fable-5-2
```

需要指定档位时，将池引用写成 `mygw/claude-fable-5-2@high` 等形式；配置中必须存在对应的 `variants.high`。用 `chord doctor config` 检查配置和变体是否有效，再用 `chord doctor models --model mygw/claude-fable-5-2` 验证实际接入。
