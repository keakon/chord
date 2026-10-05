# 模型配置

<!-- description: 用内置模型目录接入模型：chord config add、目录刷新、自定义端点借用与网关行为经验。 -->

模型能做什么——context / input / output 限额、输入模态、reasoning 档位，以及已验证端点绑定上的 Responses 字段发送规则——都由内置模型目录统一供给。目录数据来自公开的 [chord-models](https://github.com/keakon/chord-models) 仓库：每个 chord 发布版内嵌一份当时的已验证快照，目录刷新可以不等发版带来新数据。已验证模型事实来自引用的文档；官方接入信息提供配置所需的地址和凭据变量，托管绑定另行记录经过验证的端点行为。模型事实或接入信息不代表已完成真实请求验证。**候选条目**保留尚待核实的发现及其来源、作用域，永不成为运行时默认值。

用 `chord config show --catalog` 查看目录，输出会标明展示的是哪份快照、来自哪里——内嵌快照，还是带上游 tag 的刷新缓存。协议与字段语义见[配置参考](./configuration_CN.md)；本页讲怎么把模型接上、怎么保持配置不过期。

## 最快路径：`chord config add`

托管 preset 的 provider（`openai`、`anthropic`、`gemini`、`codex`）一条命令即可：

```bash
chord config add openai/gpt-6.1-sol
```

绑定 preset 的 wire 名直接按目录解析：连模型条目都不写，只写池引用，限额、模态与 reasoning 档位在加载时由已验证绑定自动补齐。其余接线交给参数（`--pool` 指定目标池，`--api-key-env` 写入凭据引用），完整说明见 [`chord config add`](./cli_CN.md#chord-config-add)。

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

目录更新后，已被同家族更新条目取代的目录引用会以 advisory 形式在启动时和 `chord doctor config` 中上报。重跑 `chord config add` 即可换绑，也可以在当前目录版本下确认保留：

```bash
chord config add mygw/claude-gw --keep-current
```

依赖某个模型之前先做一次端到端验证：

```bash
chord doctor models --model mygw/claude-gw
```

## 自定义端点：借用目录事实

`chord config add ... --catalog <id>` 会在模型下写入 `catalog:` 引用。显式选择优先于 preset 的自动绑定和已保存的目录选择，所选模型必须适用于当前 preset。自定义端点（无 preset）继承限额、模态和上下文压缩建议；与官方使用相同协议时，还会继承模型行为、`compat`、reasoning 变体和 Responses 字段发送规则。provider 的地址、凭据和传输设置由用户自己的配置决定。按模型关闭借用用 `catalog: false`：

```yaml
providers:
  mygw:
    type: messages
    api_url: https://gateway.example.com/v1/messages
    models:
      claude-gw:
        catalog: anthropic/claude-opus-5-5
```

目录没有的事实保持缺失，并以诊断形式明确报出——请手动补齐，不要照抄相邻模型的数字。显式配置的值永远优先于目录事实；每个由目录填充的值都会在 `chord config show` 里带 `catalog` 来源层。

刷新得到的**候选条目**会出现在建议列表里，标注被观察到的 provider 作用域和来源。同一 wire 名在多个作用域被观察到时，Chord 并列展示、优先展示与你的端点匹配的作用域，但绝不替你挑选：非匹配作用域的数值只作参考。采纳候选条目意味着把它的观测值作为你自己的显式配置写进配置文件——候选永远不成为默认值。若候选引用了某个已验证模型，借用那个模型（`--catalog <id>`）是经过验证的采纳路径。

## 网关行为经验

已验证 preset 绑定承载了 Chord 对端点的记录；自定义网关可能有不同表现。下面的旋钮在[配置参考](./configuration_CN.md)里有完整语义，这里只给可操作的结论。

### Chat Completions 网关背后的 thinking

Chord 的 `thinking.*` 键与 wire 无关，但把 `/v1/chat/completions` 翻译成原生 API 的网关只按自己认识到的形状读取 thinking 配置。`compat.chat_completions.native_thinking` 决定 Chord 写进 chat 请求体的形状：`gemini` / `gemini-3`（`extra_body.google.thinking_config`）、`anthropic`（`thinking: {type, budget_tokens}`）、`thinking`（DeepSeek、GLM、Kimi K2.x、Doubao 使用的原生 `thinking: {type}` 对象）、`qwen`（`enable_thinking`）；`claude`、`kimi` 等家族名选择相同形状。只有 DeepSeek 路由不需要选择器就会带上 `thinking` 对象。

选择器同时也是 Chord 识别网关模型是 Gemini 还是 Claude 的唯一信号，与模型 ID 叫什么无关：没有它，Chord 不会回写 Gemini 的 thought signature，Gemini 3 会以 HTTP 400 拒绝每次工具调用之后的下一个请求。所以网关后面的每个 Gemini 3 模型都要钉上 `native_thinking: gemini-3`，哪怕它没配 thinking 块。`chord doctor config` 会对缺选择器的 Gemini 3 模型和 thinking 块会被丢弃的网关模型给出警告。

### 跨协议的 reasoning 连续性

要求每次请求带回完整 reasoning 历史的后端，需要 `compat.reasoning_continuity.reasoning_replay: all`——默认值（`current_turn`）只回放当前轮。DeepSeek 的 Chat/Messages 自己就会保留完整历史；保留 thinking 的配方（GLM `clear_thinking: false`、Qwen `preserve_thinking`、Kimi K3 `keep: all`）以及 MiniMax thinking 模式这类硬回放契约，需要显式设置 `reasoning_replay: all`。概念与取舍见[回放契约怎么选](./reasoning_CN.md#决定回放契约)与[回退池里跨协议保留什么](./reasoning_CN.md#跨-provider-回退时保留什么)。

### 压缩阈值调优

自动压缩使用有效输入预算：provider 公布了独立输入上限时使用该值，否则从总窗口中减去本次请求的输出预算。显式设置的模型或全局阈值优先；未设置时采用该模型的目录建议，没有目录建议才使用全局默认值 0.8。机制见[上下文压缩](./context-management_CN.md#上下文压缩compaction)。

目录阈值是调优起点，不是 provider 限制，也不代表已测得的性能最优值。应按实际任务质量、延迟、请求费用和缓存复用效果调整。用 `chord config show --catalog --json` 查看所选模型的 `config_profile` 和 `cost.notes`；长上下文价格可能高于基础单价。

## 目录配方包含什么

目录配方包含 token 限额和输入模态、reasoning 或 thinking 变体、模型级 `compat` 和有依据的上下文压缩提示。preset 绑定和同协议的自定义端点均在加载配置时继承配方。`chord config add` 为新 provider 填入官方 URL，或保留你的自定义地址并写入 `catalog` 引用。例如为 `sample/model` 指定 `--catalog moonshotai/kimi-k3`，即可继承完整 reasoning 历史回放，无需手工复制 compat。用 `chord config show --catalog --json` 可以查看全部字段及其来源。

网关提供不同协议时，仍继承限额、模态和上下文压缩建议；协议专用参数需按网关的接口配方设置。

compat 按字段继承，显式模型值及 provider 值优先于目录默认值。其他已有模型配置块按用户配置保留，目录只填充缺失的块；将块设为 `null` 会阻止目录重新填充。模型压缩建议只补齐未设置的字段。显式设置全局或模型 `threshold` 时，目录不再填充缺省的 `reminder`，提醒线按有效阈值派生；显式全局 reminder 也优先于目录建议。模型显式值仍优先于全局设置。

请求体压缩和上下文 compaction 是两件事。自动添加的 provider 默认不启用请求体压缩，包括官方连接。目录中记录的 Codex `zstd`、Anthropic `gzip` 支持信息仅供参考。确认端点支持后，用户可手动设置 provider 的 `compress: gzip` 或 `compress: zstd`；添加模型会保留已有的显式压缩设置。

模型文档中的推理档位可能多于某个托管端点已验证的变体。Opus 5.5 配方采用官方默认的 `medium` effort；用户的显式配置仍然优先。
