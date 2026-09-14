# 推理与思考

思考配置分三块：请求侧的开关、响应侧承载思考的字段，以及决定「下一轮是否必须
回传已完成思考」的回放契约。这页把三者对齐到各协议线路，说明怎么判断该用哪种
continuity 模式，并给出各家族的配方入口。字段级语义见
[配置与认证 — 模型字段参考](./configuration_CN.md#模型字段参考)。

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

## 决定回放契约

只需要问一个问题：后端要求把自己的思考内容再传回去吗？

1. **不思考**——模型本身不推理，或者你从不开启思考。无需配置。
2. **会返回思考，但不要求回传**——默认配置就够。Chord 首次尝试会乐观回放
   chat 原生 reasoning，被拒后退化为结构化的已完成工具事实。如果后端根本
   不返回 `reasoning_content`，Chord 会判定它无法回放 reasoning，并在本回合
   后续请求中剥离按请求的 reasoning 控制项；只有确认端点接受这些控制项、
   但没有 reasoning 回放契约时（文档里的例子是走 Chat Completions 的 Grok），
   才设置 `compat.chat_completions.keep_reasoning_effort: true`。
3. **后端会校验回传的思考**——设置 `compat.reasoning_continuity.mode:
   openai_visible` 加 `preserve_history: true`，让每条 assistant 消息原样
   回传。带 tools 的 DeepSeek、Kimi K3、Qwen `preserve_thinking`、
   GLM `clear_thinking: false` 都属于这一类。
4. **Responses、Messages、Gemini**——原生 continuity 自动生效：Chord 会保存
   明文或带签名 / 加密的状态，并在目标线路允许时回放，无需配置。

`preserve_history: true` 会让已完成轮次的思考在每次请求中重复回放，后端按
输入计费。第 3 条不适用时不要开。

## 跨 provider 回退时保留什么

可移植的可见 reasoning 只在目标有结构化承载字段时才转换（Chat Completions
的 `openai_visible`、经验证的 Messages 兼容端点的 `anthropic_unsigned`）；
没有承载字段就直接丢弃，不会把思考塞进正文。已完成的工具调用及其结果始终
保持结构化，这是切换 provider 时必须保住的上下文。详见
[跨协议 fallback 的连续性](./model-configs_CN.md#跨协议-fallback-的连续性)。

## 计费与行为提示

- 思考 token 算输出；回放的 reasoning 算输入。
- 已完成轮次的 thinking 默认会被剥离，因为多数后端在服务端同样丢弃它，却
  照常收输入费。
- 有些后端会固定采样参数，或在思考设置变化时让缓存失效：Kimi K3 固定
  `temperature` / `top_p` / penalties，会话中途改 `reasoning_effort` 会让
  prefix cache 失效。
- TUI 的思考翻译（`thinking_translation`）只影响显示，不会写回模型上下文；
  见 [Thinking 附加翻译](./configuration_CN.md#thinking-附加翻译)。

## 按家族查配方

- [Anthropic Claude](./model-configs_CN.md#anthropic-claude)
- [OpenAI Codex OAuth preset](./model-configs_CN.md#codex-oauth-preset)
- [OpenAI GPT-6 Astra](./model-configs_CN.md#gpt-6-astra)
- [Google Gemini](./model-configs_CN.md#google-gemini)
- [GLM-5.2 / BigModel Coding Plan](./model-configs_CN.md#glm-52--bigmodel-coding-plan)
- [DeepSeek V4.1 Flash](./model-configs_CN.md#deepseek-v41-flash)
- [Qwen 保留历史思考](./model-configs_CN.md#qwen-保留历史思考)
- [Kimi K3](./model-configs_CN.md#kimi-k3)
- [Grok 4.6](./model-configs_CN.md#grok-46xai)
- [MiniMax M3 / M2.x](./model-configs_CN.md#minimax-m3--m2xopenai-兼容接口)

请求报 thinking 模式错误时，从
[常见问题排查](./troubleshooting_CN.md#deepseek--openai-兼容-thinking-模式-400)
查起。
