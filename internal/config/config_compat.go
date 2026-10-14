package config

import (
	"strings"
)

// ModelCompatConfig contains provider/model-specific compatibility toggles.
// All options are opt-in and default to disabled unless explicitly enabled.
// Model-level wire fields override provider-level defaults when both exist.
type ModelCompatConfig struct {
	GenerateContent     *GenerateContentCompatConfig     `json:"generate_content,omitempty" yaml:"generate_content,omitempty"`
	ThinkingToolcall    *ThinkingToolcallCompatConfig    `json:"thinking_toolcall,omitempty" yaml:"thinking_toolcall,omitempty"`
	ReasoningContinuity *ReasoningContinuityCompatConfig `json:"reasoning_continuity,omitempty" yaml:"reasoning_continuity,omitempty"`
	ForcedToolChoice    *ForcedToolChoiceCompatConfig    `json:"forced_tool_choice,omitempty" yaml:"forced_tool_choice,omitempty"`
	RequestOverrides    *RequestOverridesConfig          `json:"request_overrides,omitempty" yaml:"request_overrides,omitempty"`
	ChatCompletions     *ChatCompletionsCompatConfig     `json:"chat_completions,omitempty" yaml:"chat_completions,omitempty"`
	Responses           *ResponsesCompatConfig           `json:"responses,omitempty" yaml:"responses,omitempty"`
	ApplyPatch          *ApplyPatchCompatConfig          `json:"apply_patch,omitempty" yaml:"apply_patch,omitempty"`
	HostedTools         *[]string                        `json:"hosted_tools,omitempty" yaml:"hosted_tools,omitempty"` // hosted_tools catalog entries enabled for this target
}

// ProviderCompatConfig contains provider-level compatibility toggles. Model
// behavior compat belongs under ModelCompatConfig; transport-layer compat lives
// here so the schema reflects the runtime semantics.
type ProviderCompatConfig struct {
	GenerateContent     *GenerateContentCompatConfig     `json:"generate_content,omitempty" yaml:"generate_content,omitempty"`
	ThinkingToolcall    *ThinkingToolcallCompatConfig    `json:"thinking_toolcall,omitempty" yaml:"thinking_toolcall,omitempty"`
	ReasoningContinuity *ReasoningContinuityCompatConfig `json:"reasoning_continuity,omitempty" yaml:"reasoning_continuity,omitempty"`
	ForcedToolChoice    *ForcedToolChoiceCompatConfig    `json:"forced_tool_choice,omitempty" yaml:"forced_tool_choice,omitempty"`
	RequestOverrides    *RequestOverridesConfig          `json:"request_overrides,omitempty" yaml:"request_overrides,omitempty"`
	Usage               *UsageCompatConfig               `json:"usage,omitempty" yaml:"usage,omitempty"`
	Responses           *ResponsesCompatConfig           `json:"responses,omitempty" yaml:"responses,omitempty"`
	ChatCompletions     *ChatCompletionsCompatConfig     `json:"chat_completions,omitempty" yaml:"chat_completions,omitempty"`
	ApplyPatch          *ApplyPatchCompatConfig          `json:"apply_patch,omitempty" yaml:"apply_patch,omitempty"`
	HostedTools         *[]string                        `json:"hosted_tools,omitempty" yaml:"hosted_tools,omitempty"` // hosted_tools catalog entries enabled for this target
}

// ApplyPatchCompatConfig controls the apply_patch tool surface and wire shape.
// Both knobs are three-state: nil falls back to model-name inference (and, for
// freeform, the wire type — only Responses endpoints have a custom tool type),
// so an unconfigured model keeps the conservative inferred behavior and a user
// only sets a knob to override the inference.
type ApplyPatchCompatConfig struct {
	// Enabled controls the tool surface: true keeps apply_patch (hiding edit
	// and the write/delete tools), false falls back to edit. Nil infers from
	// the model name.
	Enabled *bool `json:"enabled,omitempty" yaml:"enabled,omitempty"`
	// Freeform controls the Responses wire shape: true emits apply_patch as a
	// custom tool with a grammar, false emits a JSON function tool. Nil infers
	// from the model name and the wire type (non-Responses endpoints default to
	// false). Hosts that accept Responses requests but reject custom tools have
	// no built-in exception: set Freeform to false there (see docs/edit-tools.md).
	Freeform *bool `json:"freeform,omitempty" yaml:"freeform,omitempty"`
}

// ForcedToolChoiceCompatConfig controls whether request-level forced tool
// choice (loop exit-control tool_choice "required") may be sent to the target.
// Some compatible chat backends reject non-auto tool choice while thinking is
// enabled, so Chord downgrades the forced choice to the server default for
// those targets instead of sending a rejected request. Other backends only
// support "auto" at all (for example a gateway that rejects "required",
// "none" and named choices with a 400); AutoOnly covers those targets by
// downgrading any non-auto choice regardless of reasoning state.
type ForcedToolChoiceCompatConfig struct {
	// SuppressInThinking downgrades forced tool_choice to "auto" whenever
	// reasoning/thinking is active for the request.
	SuppressInThinking *bool `json:"suppress_in_thinking,omitempty" yaml:"suppress_in_thinking,omitempty"`
	// AutoOnly downgrades any non-auto tool_choice to the backend default for
	// targets that only support "auto".
	AutoOnly *bool `json:"auto_only,omitempty" yaml:"auto_only,omitempty"`
}

// UsageCompatConfig overrides provider usage-field semantics when a compatible
// gateway differs from the official wire protocol. Nil uses protocol defaults.
type UsageCompatConfig struct {
	InputIncludesCacheRead  *bool `json:"input_includes_cache_read,omitempty" yaml:"input_includes_cache_read,omitempty"`
	InputIncludesCacheWrite *bool `json:"input_includes_cache_write,omitempty" yaml:"input_includes_cache_write,omitempty"`
}

// ResponsesCompatConfig controls which optional Responses request fields Chord
// emits for a provider. Every knob defaults to the current stable Codex-shaped
// behavior; set one only for compatible gateways that reject a specific field.
type ResponsesCompatConfig struct {
	// SendStore emits the top-level "store" field (default true). When false the
	// field is omitted and the backend default applies — for official Responses
	// backends that default is server-side retention (store=true).
	SendStore *bool `json:"send_store,omitempty" yaml:"send_store,omitempty"`
	// SendReasoningInclude requests include: ["reasoning.encrypted_content"]
	// (default true). Set false for gateways that reject the include parameter.
	SendReasoningInclude *bool `json:"send_reasoning_include,omitempty" yaml:"send_reasoning_include,omitempty"`
	// SendToolChoice emits the default tool_choice: "auto" (default true).
	// An explicit tool choice from model/variant tuning is always sent.
	SendToolChoice *bool `json:"send_tool_choice,omitempty" yaml:"send_tool_choice,omitempty"`
	// SendParallelToolCalls emits parallel_tool_calls when tools are present
	// (default true). Set false for a binding that does not accept this field.
	SendParallelToolCalls *bool `json:"send_parallel_tool_calls,omitempty" yaml:"send_parallel_tool_calls,omitempty"`
	// SendPromptCacheKey emits prompt_cache_key and client_metadata session
	// routing metadata (default true).
	SendPromptCacheKey *bool `json:"send_prompt_cache_key,omitempty" yaml:"send_prompt_cache_key,omitempty"`
	// SendMaxOutputTokens emits max_output_tokens with the effective output cap
	// (default false, preserving the stable Codex request shape). Enable for
	// compatible gateways whose server-side default output is unbounded.
	SendMaxOutputTokens *bool `json:"send_max_output_tokens,omitempty" yaml:"send_max_output_tokens,omitempty"`
	// MCPAdditionalTools mounts runtime MCP function definitions through
	// input[type="additional_tools"] at their conversation anchor instead of
	// the top-level tools array. Default false: compatible gateways must opt in.
	MCPAdditionalTools *bool `json:"mcp_additional_tools,omitempty" yaml:"mcp_additional_tools,omitempty"`
}

// HostedToolConfig is one entry in the top-level hosted_tools catalog: the
// local tool surface the main model sees plus the raw wire declarations used
// on the independent sub-requests that ask the provider to run the tool
// server-side. Chord only places declarations and captures raw result
// payloads, so a new provider-side tool (or a new tool version) stays a
// configuration change instead of Go code. Entries are inert until a
// provider/model enables them through compat.hosted_tools.
type HostedToolConfig struct {
	// Description and Parameters form the local tool surface. Parameters is a
	// JSON Schema object; nil exposes a tool without arguments.
	Description string         `json:"description,omitempty" yaml:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty" yaml:"parameters,omitempty"`
	// Prompt is the sub-request instruction template; {arg} placeholders are
	// replaced with the matching local tool argument value. Empty serializes
	// the arguments as JSON instead.
	Prompt string `json:"prompt,omitempty" yaml:"prompt,omitempty"`
	// ReadOnly and ConcurrencySafe override the local tool traits. Nil keeps
	// the conservative default (false), so a catalog-only tool never silently
	// gains read-only or parallel-execution semantics.
	ReadOnly        *bool `json:"read_only,omitempty" yaml:"read_only,omitempty"`
	ConcurrencySafe *bool `json:"concurrency_safe,omitempty" yaml:"concurrency_safe,omitempty"`
	// RetrySafe permits replay after an attempt whose execution outcome is unknown.
	RetrySafe *bool `json:"retry_safe,omitempty" yaml:"retry_safe,omitempty"`
	// ImagePaths selects base64 images from completed raw result objects.
	ImagePaths []string `json:"image_paths,omitempty" yaml:"image_paths,omitempty"`
	// TimeoutSeconds caps one local execution; 0 uses the shared default.
	TimeoutSeconds int `json:"timeout_s,omitempty" yaml:"timeout_s,omitempty"`
	// Declarations maps a supported provider wire type (messages, responses)
	// to that family's declaration. A family without an entry cannot carry
	// the tool.
	Declarations map[string]HostedToolDeclarationConfig `json:"declarations,omitempty" yaml:"declarations,omitempty"`
	// ModelPool routes this tool's sub-requests to a named top-level
	// model_pools entry instead of the caller's own pool. Empty follows the
	// caller: the main agent runs the tool on the main pool and a subagent on
	// its own pool. The pool must exist at startup, the calling agent must
	// include it in its own model_pools, and compat.hosted_tools still gates
	// which entries inside the pool can serve the tool.
	ModelPool string `json:"model_pool,omitempty" yaml:"model_pool,omitempty"`
}

// HostedToolDeclarationConfig is the per-family wire declaration of a hosted
// tool. Tool and Force are raw wire JSON: Tool is placed in the family's
// declaration slot as-is, and Force becomes tool_choice when the sub-request
// forces the tool (a string such as "required" or a family-specific object).
// Object values may carry {"$arg": "<name>"} placeholders, which are replaced
// with the matching local tool argument at request-build time; a missing or
// empty argument drops the enclosing key.
type HostedToolDeclarationConfig struct {
	Tool    map[string]any    `json:"tool,omitempty" yaml:"tool,omitempty"`
	Force   any               `json:"force,omitempty" yaml:"force,omitempty"`
	Include []string          `json:"include,omitempty" yaml:"include,omitempty"`
	Headers map[string]string `json:"headers,omitempty" yaml:"headers,omitempty"`
}

// ChatCompletionsCompatConfig controls which optional Chat Completions request
// fields Chord emits for a provider.
type ChatCompletionsCompatConfig struct {
	// SendStreamOptions emits stream_options: {include_usage: true} (default
	// true). Set false for gateways that reject stream_options; token usage
	// then stays unreported on streaming responses.
	SendStreamOptions *bool `json:"send_stream_options,omitempty" yaml:"send_stream_options,omitempty"`
	// InferFinishReason derives a normal completion when a compatible gateway
	// ends the stream without emitting finish_reason (default false). Text-only
	// streams finalize as "stop" and tool-call streams as "tool_calls" instead
	// of being marked interrupted.
	InferFinishReason *bool `json:"infer_finish_reason,omitempty" yaml:"infer_finish_reason,omitempty"`
	// RequiresToolResultName emits the paired tool name on tool result messages
	// (default false). Some compatible gateways require the name in addition to
	// tool_call_id.
	RequiresToolResultName *bool `json:"requires_tool_result_name,omitempty" yaml:"requires_tool_result_name,omitempty"`
	// RequiresAssistantAfterToolResult inserts a synthetic assistant message
	// between a tool result and the next user message (default false). Some
	// compatible gateways reject a user message that directly follows tool
	// results.
	RequiresAssistantAfterToolResult *bool `json:"requires_assistant_after_tool_result,omitempty" yaml:"requires_assistant_after_tool_result,omitempty"`
	// MCPSystemToolsMessage mounts runtime MCP function definitions in
	// role:system messages at their conversation anchor instead of the
	// top-level tools array. Default false: only enable it for models that
	// accept the Kimi-compatible dynamic-tool shape.
	MCPSystemToolsMessage *bool `json:"mcp_system_tools_message,omitempty" yaml:"mcp_system_tools_message,omitempty"`
	// KeepReasoningEffort keeps the reasoning request controls when a
	// current-turn assistant tool-call message replays without reasoning
	// content (default false). Chord otherwise reads the missing reasoning
	// content as a backend that does not replay reasoning and strips
	// reasoning_effort and reasoning request overrides for the rest of the
	// turn. Enable it for endpoints that accept reasoning controls without a
	// reasoning-content replay contract, such as Grok on the Chat Completions
	// wire.
	KeepReasoningEffort *bool `json:"keep_reasoning_effort,omitempty" yaml:"keep_reasoning_effort,omitempty"`
	// NativeThinking selects the request shape Chord uses to hand a model's
	// thinking settings to a Chat Completions endpoint that translates the call
	// into the model's native API. Empty (default) uses the protocol-generic
	// Chat Completions request and does not add a native dialect field, except
	// for DeepSeek API model names recognized by modelcompat.ModelNativeFamily,
	// which keep the "thinking" object without a selector, and off/none disables it. Accepted
	// shapes are "gemini"
	// (extra_body.google.thinking_config), "gemini-3"
	// (the same shape plus Gemini 3 signature replay), "anthropic"
	// (thinking:{type,budget_tokens}), "thinking" (the native thinking:{type}
	// object used by DeepSeek, GLM, Kimi K2.x, and Doubao), and "qwen"
	// (enable_thinking). Family aliases (claude, deepseek, glm, kimi, ...) name
	// the same shapes. Nothing is emitted when the model configures no thinking
	// knobs.
	NativeThinking string `json:"native_thinking,omitempty" yaml:"native_thinking,omitempty"`
}

// NativeThinkingValue returns the configured native thinking selector.
func (c *ChatCompletionsCompatConfig) NativeThinkingValue() string {
	if c == nil {
		return ""
	}
	return strings.TrimSpace(c.NativeThinking)
}

// RequestOverridesConfig applies protocol-agnostic patches after Chord builds a
// request. Body values merge recursively; null deletes a field. Renames retain
// Chord's computed value under a provider-specific field name. Header nulls
// remove defaults that are unsupported by a compatible endpoint.
type RequestOverridesConfig struct {
	Body             map[string]any     `json:"body,omitempty" yaml:"body,omitempty"`
	RenameBodyFields map[string]*string `json:"rename_body_fields,omitempty" yaml:"rename_body_fields,omitempty"`
	Headers          map[string]*string `json:"headers,omitempty" yaml:"headers,omitempty"`
}

// ReasoningContinuityCompatConfig controls provider/model-specific replay of
// protocol-native reasoning/thinking continuity payloads.
//
// Supported modes:
//   - "none": disable replay of provider-specific reasoning continuity state.
//   - "openai_visible": replay OpenAI-compatible visible assistant reasoning
//     without injecting provider-specific request fields, and accept portable
//     visible reasoning from other wire families. On the chat wire the visible
//     reasoning is carried as reasoning_content; on the Responses wire the
//     request builder synthesizes reasoning_text items for replayed assistant
//     turns that lost their native reasoning (DeepSeek V4 contract).
//   - "anthropic_unsigned": replay visible, unsigned Anthropic thinking for
//     the same configured provider/model target, and accept portable visible
//     reasoning from other wire families as unsigned thinking blocks.
//
// Other protocols use dedicated runtime handling. Provider-bound opaque
// payloads replay only on compatible wires; when no structured target carrier
// exists, Chord drops the reasoning payload instead of injecting it into
// ordinary assistant content.
type ReasoningContinuityCompatConfig struct {
	Mode string `json:"mode,omitempty" yaml:"mode,omitempty"`
	// Contract selects an endpoint-specific reasoning contract; an explicit
	// value always wins over what the model ID implies. "deepseek" enables the
	// DeepSeek tool-history passback rules on the Chat Completions and Messages
	// wires for an alias whose model name does not identify it; a model ID
	// recognized as DeepSeek by modelcompat.ModelNativeFamily selects the same
	// contract automatically. "gemini-3" enables missing thought-signature repair on the native generate-content endpoint, where
	// a model ID whose final component starts with "gemini-3" or "gemini-4" (see
	// RequiresGeminiThoughtSignature) selects it automatically; a Chat Completions gateway
	// opts in through native_thinking: gemini-3 instead. "none" opts a route out
	// of both inferences, e.g. a deepseek-named third-party route that serves
	// another backend, on the Messages wire as well as Chat Completions.
	Contract string `json:"contract,omitempty" yaml:"contract,omitempty"`
	// ReasoningReplay selects how much historical reasoning/thinking is
	// replayed for the target: "current_turn" (the default) keeps only
	// reasoning after the last user message, "all" replays completed-turn
	// reasoning unchanged (plaintext history plus family-matched native
	// thinking), and "none" strips reasoning everywhere, including the
	// current turn. Completed turns are stripped by default because most
	// thinking backends drop or ignore earlier-turn reasoning server-side
	// while billing it as input; set "all" for endpoints whose contract
	// requires the complete assistant history (Kimi K3 / keep:all, Qwen
	// preserve_thinking, GLM clear_thinking:false; the DeepSeek contract keeps
	// the full history regardless of this field), and "none" only where a
	// probe showed the endpoint accepts a request without current-turn
	// reasoning. Historical reasoning replayed under "all" is billed as input
	// on every request.
	ReasoningReplay string `json:"reasoning_replay,omitempty" yaml:"reasoning_replay,omitempty"`
}

// EffectiveMode returns the configured continuity mode.
func (c *ReasoningContinuityCompatConfig) EffectiveMode() string {
	if c == nil {
		return ""
	}
	return strings.TrimSpace(c.Mode)
}

// ReasoningReplayValue returns the configured reasoning replay window
// ("all", "current_turn", or "none"). An empty value means unset; the
// resolver applies the current_turn default.
func (c *ReasoningContinuityCompatConfig) ReasoningReplayValue() string {
	if c == nil {
		return ""
	}
	return strings.TrimSpace(c.ReasoningReplay)
}

// ReasoningContractValue returns the configured endpoint contract.
func (c *ReasoningContinuityCompatConfig) ReasoningContractValue() string {
	if c == nil {
		return ""
	}
	return strings.TrimSpace(c.Contract)
}

// ThinkingToolcallCompatConfig controls compatibility handling for providers
// that occasionally emit pseudo tool-call templates in reasoning/thinking
// content instead of structured tool_calls.
type ThinkingToolcallCompatConfig struct {
	Enabled *bool `json:"enabled,omitempty" yaml:"enabled,omitempty"`
}

// EnabledValue reports whether thinking-toolcall compatibility is enabled.
// Default is false.
func (c *ThinkingToolcallCompatConfig) EnabledValue() bool {
	if c == nil || c.Enabled == nil {
		return false
	}
	return *c.Enabled
}
