# Reasoning and thinking

Start with a [recipe for your model](./model-configs.md); you do not need to understand every protocol field first.

- **To change thinking effort**: find your connection type in the table below and adjust its supported fields.
- **If a request fails after a tool call because thinking is missing**: use the replay-contract guidance below to determine whether historical thinking must be preserved.
- **To read a translation**: configure [thinking translation](./configuration.md#appended-thinking-translation). It affects display only, not the model's thinking request settings.

Thinking affects cost: newly generated thinking counts as output, and thinking sent again with history counts as input. See the [model field reference](./configuration.md#model-field-reference) for full field definitions.

## Request keys by wire family

| Wire family | Chord keys | What comes back |
| --- | --- | --- |
| Responses (`type: responses`) | `reasoning.effort`, `reasoning.summary` | Plaintext `reasoning_text` plus encrypted reasoning items |
| Chat Completions (`type: chat-completions`) | `reasoning.effort`, plus family-specific fields sent through `compat.request_overrides.body` (`thinking`, `enable_thinking`, `reasoning_split`, `clear_thinking`, ...) | Usually `reasoning_content`; some backends inline tagged thinking in `content` |
| Messages (`type: messages`) | `thinking.type`, `thinking.budget_tokens`, `thinking.effort`, `thinking.display` | Signed `thinking` blocks |
| Gemini (`type: generate-content`) | `thinking.level`, `thinking.budget`, `thinking.include_thoughts` | Thought summaries plus thought signatures |

`reasoning.effort` has no local whitelist: Chord passes the value through and
lets the backend accept, clamp, or reject it. Only the Responses wire normalizes
whitespace and casing first, so `high` and `High` both work there.

On Chat Completions, a gateway that translates the call into the model's native
API receives the thinking settings as that API's own field: Gemini as
`extra_body.google.thinking_config`, Claude as `thinking: {type, budget_tokens}`,
DeepSeek / GLM / Kimi K2.x / Doubao as `thinking: {type}`, and Qwen as
`enable_thinking`. The shape is inferred from the model name and can be forced
with `compat.chat_completions.native_thinking`; see
[Thinking behind a Chat Completions gateway](./model-configs.md#thinking-behind-a-chat-completions-gateway).

## Decide the replay contract

The answer depends on whether the backend requires its own reasoning content back:

1. **No thinking**: the model does not reason, or you never turn thinking on.
   Nothing to configure.
2. **Thinking comes back, but the backend does not require it again**: the
   default is enough. Chord replays chat-native reasoning optimistically on the
   first attempt and degrades to structured completed tool facts if the target
   rejects it. If the backend never returns `reasoning_content` at all, Chord
   reads it as replay-incompatible and drops per-request reasoning controls for
   the rest of the turn; set
   `compat.chat_completions.keep_reasoning_effort: true` only for endpoints
   that accept those controls without a reasoning-content contract (Grok on
   Chat Completions is the documented case).
3. **The backend validates the replayed reasoning**: set
   `compat.reasoning_continuity.mode: openai_visible` plus
   `preserve_history: true` so every assistant message goes back unchanged.
   This is the contract for DeepSeek when a request carries tools, Kimi K3,
   Qwen `preserve_thinking`, and GLM `clear_thinking: false`.
4. **Responses, Messages, and Gemini**: native continuity is automatic. Chord
   captures the plaintext or signed/encrypted state and replays it where the
   wire allows; nothing to configure.

`preserve_history: true` replays completed-turn thinking on every request, which
the backend bills as input. Leave it off unless the contract in step 3 applies.

## What crosses a fallback pool

Portable visible reasoning is converted into the target's structured carrier
when one exists (`openai_visible` on Chat Completions, `anthropic_unsigned` on
verified Messages-compatible endpoints); otherwise it is dropped rather than
pasted into assistant content. Completed tool calls and their results stay
structured, and that is the part which must survive a provider switch. See
[Cross-protocol fallback continuity](./model-configs.md#cross-protocol-fallback-continuity).

## Cost and behavior notes

- Thinking tokens are output tokens; replayed reasoning is input tokens.
- Completed-turn reasoning is stripped by default because most backends drop it
  server-side while still billing it.
- Some backends pin sampling or invalidate caches when thinking settings change:
  Kimi K3 fixes `temperature` / `top_p` / penalties and drops the prefix cache
  when `reasoning_effort` changes mid-session.
- Thinking translation for the TUI (`thinking_translation`) is display-only and
  is never written back into model context; see
  [Appended thinking translation](./configuration.md#appended-thinking-translation).

## Recipes by family

- [Anthropic Claude](./model-configs.md#anthropic-claude)
- [OpenAI Codex OAuth preset](./model-configs.md#codex-oauth-preset)
- [OpenAI GPT (Responses)](./model-configs.md#openai-gpt-responses)
- [Google Gemini](./model-configs.md#google-gemini)
- [GLM / BigModel Coding Plan](./model-configs.md#glm--bigmodel-coding-plan)
- [DeepSeek](./model-configs.md#deepseek)
- [Qwen preserved thinking](./model-configs.md#qwen-preserved-thinking)
- [Kimi](./model-configs.md#kimi)
- [Grok](./model-configs.md#grok-xai)
- [MiniMax](./model-configs.md#minimax-openai-compatible)
- [Xiaomi MiMo](./model-configs.md#xiaomi-mimo-openai-compatible)
- [Meta Muse Spark](./model-configs.md#meta-muse-spark)

When a request fails with a thinking-mode error, start from
[Troubleshooting](./troubleshooting.md#deepseek--openai-compatible-thinking-mode-400s).
