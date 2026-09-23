# 推理与思考

先从[模型配置速查](./model-configs_CN.md)复制所用模型的配方，不必先理解所有协议字段。

- **只想调整思考强度**：找到下表中对应的接入方式，修改它支持的字段。
- **模型调用工具后报思考内容缺失**：查看[回放契约](#决定回放契约)，确认是否需要保留历史思考。
- **只想看译文**：配置[思考翻译](./configuration_CN.md#thinking-附加翻译)，它只影响显示，不改变模型请求中的思考设置。

思考会影响费用：新生成的思考计入输出，随历史再次发送的思考计入输入。字段完整定义见[模型字段参考](./configuration_CN.md#模型字段参考)。

## 按线路选请求键位

| 线路 | Chord 键位 | 返回什么 |
| --- | --- | --- |
| Responses（`type: responses`） | `reasoning.effort`、`reasoning.summary` | 明文 `reasoning_text`，加上加密的 reasoning item |
| Chat Completions（`type: chat-completions`） | `reasoning.effort`，以及各家族特有字段，通过 `compat.request_overrides.body` 发送（`thinking`、`enable_thinking`、`reasoning_split`、`clear_thinking` 等） | 一般是 `reasoning_content`；有些后端把带标签的思考直接写进 `content` |
| Messages（`type: messages`） | `thinking.type`、`thinking.budget_tokens`、`thinking.effort`、`thinking.display` | 带签名的 `thinking` block |
| Gemini（`type: generate-content`） | `thinking.level`、`thinking.budget`、`thinking.include_thoughts` | 思考摘要，加上 thought signature |

`reasoning.effort` 没有本地白名单：Chord 原样透传，由后端决定接受、收敛还是
拒绝。只有 Responses 线路会先归一化空格和大小写，所以那里 `high` 和 `High`
都行。

走 Chat Completions 时，把请求转成模型原生 API 的网关会按自己的形状收到思考配置：
Gemini 用 `extra_body.google.thinking_config`，Claude 用
`thinking: {type, budget_tokens}`，DeepSeek / GLM / Kimi K2.x / Doubao 用
`thinking: {type}`，Qwen 用 `enable_thinking`。形状按模型名推断，也可以用
`compat.chat_completions.native_thinking` 指定；见
[走 Chat Completions 网关的 thinking](./model-configs_CN.md#走-chat-completions-网关的-thinking)。

## 决定回放契约

答案取决于后端是否要求把自己的思考内容再传回去：

1. **不思考**：模型本身不推理，或者你从不开启思考。无需配置。
2. **会返回思考，但不要求回传**：默认配置就够。Chord 首次尝试会乐观回放
   chat 原生 reasoning，被拒后退化为结构化的已完成工具事实。如果后端根本
   不返回 `reasoning_content`，Chord 会判定它无法回放 reasoning，并在本回合
   后续请求中剥离按请求的 reasoning 控制项；只有确认端点接受这些控制项、
   但没有 reasoning 回放契约时（文档里的例子是走 Chat Completions 的 Grok），
   才设置 `compat.chat_completions.keep_reasoning_effort: true`。
3. **后端会校验回传的思考**：设置 `compat.reasoning_continuity.mode:
   openai_visible` 加 `reasoning_replay: all`，让每条 assistant 消息原样
   回传。带 tools 的 DeepSeek、Kimi K3、Qwen `preserve_thinking`、
   GLM `clear_thinking: false` 都属于这一类。第三方中转不保证遵守官方
   契约（有的会拒绝回放的 `reasoning_content`），把 `all` 当成必需前
   先确认实际端点的行为。
4. **Responses、Messages、Gemini**：原生 continuity 自动生效：Chord 会保存
   明文或带签名 / 加密的状态，并在目标线路允许时回放，无需配置。

`reasoning_replay: all` 会让已完成轮次的思考在每次请求中重复回放，后端按
输入计费。默认的 `current_turn` 会剥离已完成轮次，第 3 条不适用时用默认即可。

## 跨 provider 回退时保留什么

可移植的可见 reasoning 只在目标有结构化承载字段时才转换（Chat Completions
的 `openai_visible`、经验证的 Messages 兼容端点的 `anthropic_unsigned`）；
没有承载字段就直接丢弃，不会把思考塞进正文。已完成的工具调用及其结果始终
保持结构化，这是切换 provider 时必须保住的上下文。详见
[跨协议 fallback 的连续性](./model-configs_CN.md#跨协议-fallback-的连续性)。

## 计费与行为提示

- 思考 token 算输出；回放的 reasoning 算输入。
- 已完成轮次的 thinking 默认会被剥离，用来控制请求体积。Anthropic 会在服务端
  过滤历史轮的 thinking 块，只为模型实际看到的块计费，省掉它们不花冤枉钱；
  原样回放历史的端点会为保留的每个 token 计费，所以上面那些契约要显式选
  `all`。
- 有些后端会固定采样参数，或在思考设置变化时让缓存失效：Kimi K3 固定
  `temperature` / `top_p` / penalties，会话中途改 `reasoning_effort` 会让
  prefix cache 失效。
- TUI 的思考翻译（`thinking_translation`）只影响显示，不会写回模型上下文；
  见 [Thinking 附加翻译](./configuration_CN.md#thinking-附加翻译)。

## 按家族查配方

- [Anthropic Claude](./model-configs_CN.md#anthropic-claude)
- [OpenAI Codex OAuth preset](./model-configs_CN.md#codex-oauth-preset)
- [OpenAI GPT（Responses 兼容接口）](./model-configs_CN.md#openai-gptresponses-兼容接口)
- [Google Gemini](./model-configs_CN.md#google-gemini)
- [GLM / BigModel Coding Plan](./model-configs_CN.md#glm--bigmodel-coding-plan)
- [DeepSeek](./model-configs_CN.md#deepseek)
- [Qwen 保留历史思考](./model-configs_CN.md#qwen-保留历史思考)
- [Kimi](./model-configs_CN.md#kimi)
- [Grok](./model-configs_CN.md#grokxai)
- [MiniMax](./model-configs_CN.md#minimaxopenai-兼容接口)
- [小米 MiMo](./model-configs_CN.md#小米-mimoopenai-兼容接口)
- [Meta Muse Spark](./model-configs_CN.md#meta-muse-spark)

请求报 thinking 模式错误时，从
[常见问题排查](./troubleshooting_CN.md#deepseek--openai-兼容-thinking-模式-400)
查起。
