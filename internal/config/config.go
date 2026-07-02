package config

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// Provider type constants
const (
	ProviderTypeChatCompletions       = "chat-completions"
	ProviderTypeChatCompletionsLegacy = "chat_completions"
	ProviderTypeMessages              = "messages"
	ProviderTypeResponses             = "responses"
	ProviderTypeGenerateContent       = "generate-content"
)

const (
	AuthSchemeAnthropicAPIKey = "anthropic-api-key"
	AuthSchemeBearer          = "bearer"
	AuthSchemeAPIKey          = "api-key"
)

// Config is the top-level configuration for the chord agent.
type Config struct {
	Providers      map[string]ProviderConfig `json:"providers" yaml:"providers"`                         // LLM providers
	ModelPools     map[string][]string       `json:"model_pools,omitempty" yaml:"model_pools,omitempty"` // reusable model pool definitions
	Context        ContextConfig             `json:"context" yaml:"context"`                             // context compression settings
	Skills         SkillsConfig              `json:"skills" yaml:"skills"`                               // additional skill paths
	ConfirmTimeout int                       `json:"confirm_timeout" yaml:"confirm_timeout"`             // confirmation timeout in seconds (0 = infinite, default)
	Diff           DiffConfig                `json:"diff" yaml:"diff"`                                   // TUI diff rendering options
	// DesktopNotification, when true, enables terminal idle notifications in local TUI (terminal unfocused).
	// Chord auto-selects the terminal OSC protocol by environment (for example, OSC 777 vs OSC 9).
	// YAML: desktop_notification: true
	DesktopNotification *bool `json:"desktop_notification,omitempty" yaml:"desktop_notification,omitempty"`
	// PreventSleep, when true, prevents macOS idle sleep while any agent is active (non-idle). YAML: prevent_sleep: true
	// Only effective in the local TUI.
	PreventSleep        *bool                      `json:"prevent_sleep,omitempty" yaml:"prevent_sleep,omitempty"`
	KeyMap              map[string][]string        `json:"keymap,omitempty" yaml:"keymap,omitempty"`                             // custom key bindings (snake_case action → key list)
	Commands            map[string]string          `json:"commands,omitempty" yaml:"commands,omitempty"`                         // custom slash commands: "/cmd" → text to send as message
	IMESwitchTarget     string                     `json:"ime_switch_target,omitempty" yaml:"ime_switch_target,omitempty"`       // English IM key (e.g. com.apple.keylayout.ABC); switch/restore use im-select or im-select.exe by platform
	LogLevel            string                     `json:"log_level" yaml:"log_level"`                                           // log verbosity: "debug", "info" (default), "warn", "error"
	Paths               PathsConfig                `json:"paths" yaml:"paths,omitempty"`                                         // user-level state/cache/logs path overrides
	Maintenance         MaintenanceConfig          `json:"maintenance" yaml:"maintenance,omitempty"`                             // optional cleanup/status checks
	LSP                 LSPConfig                  `json:"lsp" yaml:"lsp"`                                                       // LSP server config
	Diagnostics         DiagnosticsConfig          `json:"diagnostics" yaml:"diagnostics,omitempty"`                             // post-tool diagnostics config
	MCP                 MCPConfig                  `json:"mcp,omitempty" yaml:"mcp,omitempty"`                                   // MCP server configs
	Hooks               HookConfig                 `json:"hooks" yaml:"hooks,omitempty"`                                         // lifecycle hook configs
	MaxOutputTokens     int                        `json:"max_output_tokens" yaml:"max_output_tokens"`                           // global output token cap (0 = use DefaultOutputTokenMax)
	StreamRetryRounds   int                        `json:"stream_retry_rounds" yaml:"stream_retry_rounds"`                       // hard cap on public LLM retry rounds (0 = keep retrying until success/cancel)
	Proxy               string                     `json:"proxy,omitempty" yaml:"proxy,omitempty"`                               // global proxy URL (http/https/socks5), empty = no proxy
	WebFetch            WebFetchConfig             `json:"web_fetch" yaml:"web_fetch,omitempty"`                                 // WebFetch-specific options
	Worktree            WorktreeConfig             `json:"worktree" yaml:"worktree,omitempty"`                                   // git worktree integration options
	ThinkingTranslation *ThinkingTranslationConfig `json:"thinking_translation,omitempty" yaml:"thinking_translation,omitempty"` // optional thinking translation enhancement
}

// WebFetchConfig controls WebFetch runtime behavior.
type WebFetchConfig struct {
	// UserAgent overrides the default browser-like User-Agent. Nil = inherit builtin default;
	// non-nil empty string explicitly resets to builtin default when merging project config.
	UserAgent *string `json:"user_agent,omitempty" yaml:"user_agent,omitempty"`
	// Proxy specifies the proxy URL for WebFetch requests. Nil = use global proxy;
	// non-nil empty string = "direct" (explicitly disable proxy); non-empty = use that proxy.
	// Supported schemes: http, https, socks5.
	Proxy *string `json:"proxy,omitempty" yaml:"proxy,omitempty"`
}

type MaintenanceConfig struct {
	SizeCheckOnStartup bool  `json:"size_check_on_startup" yaml:"size_check_on_startup"`
	WarnStateBytes     int64 `json:"warn_state_bytes" yaml:"warn_state_bytes"`
	WarnCacheBytes     int64 `json:"warn_cache_bytes" yaml:"warn_cache_bytes"`
}

// SkillsConfig configures additional skill directories.
type SkillsConfig struct {
	Paths []string `json:"paths" yaml:"paths"` // additional skill directory paths
}

// DiffConfig controls TUI diff rendering behavior.
type DiffConfig struct {
	InlineMaxColumns int `json:"inline_max_columns,omitempty" yaml:"inline_max_columns,omitempty"` // hard cutoff for one-line inline diff; <=0 uses default 200
}

// LSPConfig holds LSP server configurations keyed by server name (e.g. gopls, pyright).
// YAML: lsp.gopls: { command: "gopls", ... } (no "servers" layer).
type LSPConfig map[string]LSPServerConfig

// LSPServerConfig configures a single LSP server.
type LSPServerConfig struct {
	Disabled    bool              `json:"disabled,omitempty" yaml:"disabled,omitempty"`
	Command     string            `json:"command,omitempty" yaml:"command,omitempty"`
	Args        []string          `json:"args,omitempty" yaml:"args,omitempty"`
	Env         map[string]string `json:"env,omitempty" yaml:"env,omitempty"`
	FileTypes   []string          `json:"file_types,omitempty" yaml:"file_types,omitempty"`
	RootMarkers []string          `json:"root_markers,omitempty" yaml:"root_markers,omitempty"`
	InitOptions map[string]any    `json:"init_options,omitempty" yaml:"init_options,omitempty"`
	Options     map[string]any    `json:"options,omitempty" yaml:"options,omitempty"`
	Timeout     int               `json:"timeout,omitempty" yaml:"timeout,omitempty"` // seconds, default 30
}

// MCPServerConfig defines an MCP server connection.
// Either Command (stdio transport) or URL (HTTP transport) must be set.
type MCPServerConfig struct {
	Command      string   `json:"command,omitempty" yaml:"command,omitempty"`             // executable (for stdio transport)
	Args         []string `json:"args,omitempty" yaml:"args,omitempty"`                   // command arguments
	Env          []string `json:"env,omitempty" yaml:"env,omitempty"`                     // optional environment variables
	URL          string   `json:"url,omitempty" yaml:"url,omitempty"`                     // HTTP URL (for HTTP transport)
	AllowedTools []string `json:"allowed_tools,omitempty" yaml:"allowed_tools,omitempty"` // optional remote MCP tool allowlist
	Manual       bool     `json:"manual,omitempty" yaml:"manual,omitempty"`               // when true, do not auto-start; must be enabled via /mcp or shortcut
}

// MCPConfig holds MCP server configurations keyed by server name.
// YAML: mcp.exa: { url: "...", ... } (no "servers" layer).
type MCPConfig map[string]MCPServerConfig

// HookConfig holds hook definitions grouped by trigger point. Each trigger
// point maps to an ordered list of hook entries that are executed sequentially.
type HookConfig struct {
	OnToolCall               []HookEntry `json:"on_tool_call,omitempty" yaml:"on_tool_call,omitempty"`
	OnToolResult             []HookEntry `json:"on_tool_result,omitempty" yaml:"on_tool_result,omitempty"`
	OnBeforeToolResultAppend []HookEntry `json:"on_before_tool_result_append,omitempty" yaml:"on_before_tool_result_append,omitempty"`
	OnBeforeLLMCall          []HookEntry `json:"on_before_llm_call,omitempty" yaml:"on_before_llm_call,omitempty"`
	OnAfterLLMCall           []HookEntry `json:"on_after_llm_call,omitempty" yaml:"on_after_llm_call,omitempty"`
	OnBeforeCompress         []HookEntry `json:"on_before_compress,omitempty" yaml:"on_before_compress,omitempty"`
	OnAfterCompress          []HookEntry `json:"on_after_compress,omitempty" yaml:"on_after_compress,omitempty"`
	OnSessionStart           []HookEntry `json:"on_session_start,omitempty" yaml:"on_session_start,omitempty"`
	OnSessionEnd             []HookEntry `json:"on_session_end,omitempty" yaml:"on_session_end,omitempty"`
	OnIdle                   []HookEntry `json:"on_idle,omitempty" yaml:"on_idle,omitempty"`
	OnWaitConfirm            []HookEntry `json:"on_wait_confirm,omitempty" yaml:"on_wait_confirm,omitempty"`
	OnWaitQuestion           []HookEntry `json:"on_wait_question,omitempty" yaml:"on_wait_question,omitempty"`
	OnAgentError             []HookEntry `json:"on_agent_error,omitempty" yaml:"on_agent_error,omitempty"`
	OnToolBatchComplete      []HookEntry `json:"on_tool_batch_complete,omitempty" yaml:"on_tool_batch_complete,omitempty"`
}

// HookCommand is a union type that accepts either a shell string or an argv list.
type HookCommand struct {
	Shell string
	Args  []string
}

func (c HookCommand) IsZero() bool {
	return c.Shell == "" && len(c.Args) == 0
}

func (c *HookCommand) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.ScalarNode:
		c.Shell = value.Value
		c.Args = nil
		return nil
	case yaml.SequenceNode:
		var args []string
		if err := value.Decode(&args); err != nil {
			return err
		}
		c.Shell = ""
		c.Args = args
		return nil
	default:
		return fmt.Errorf("hook command must be a string or string array")
	}
}

func (c HookCommand) MarshalYAML() (any, error) {
	if len(c.Args) > 0 {
		return c.Args, nil
	}
	return c.Shell, nil
}

// HookEntry defines a single external command hook.
type HookEntry struct {
	Name            string            `json:"name,omitempty" yaml:"name,omitempty"`
	Command         HookCommand       `json:"command" yaml:"command"`
	Timeout         int               `json:"timeout,omitempty" yaml:"timeout,omitempty"` // seconds, default 30
	Tools           []string          `json:"tools,omitempty" yaml:"tools,omitempty"`
	Paths           []string          `json:"paths,omitempty" yaml:"paths,omitempty"`
	Agents          []string          `json:"agents,omitempty" yaml:"agents,omitempty"`
	AgentKinds      []string          `json:"agent_kinds,omitempty" yaml:"agent_kinds,omitempty"`
	Models          []string          `json:"models,omitempty" yaml:"models,omitempty"`
	MinChangedFiles int               `json:"min_changed_files,omitempty" yaml:"min_changed_files,omitempty"`
	OnlyOnError     bool              `json:"only_on_error,omitempty" yaml:"only_on_error,omitempty"`
	Join            string            `json:"join,omitempty" yaml:"join,omitempty"`
	Result          string            `json:"result,omitempty" yaml:"result,omitempty"`
	ResultFormat    string            `json:"result_format,omitempty" yaml:"result_format,omitempty"`
	MaxResultLines  int               `json:"max_result_lines,omitempty" yaml:"max_result_lines,omitempty"`
	MaxResultBytes  int               `json:"max_result_bytes,omitempty" yaml:"max_result_bytes,omitempty"`
	DebounceMS      int               `json:"debounce_ms,omitempty" yaml:"debounce_ms,omitempty"`
	Concurrency     string            `json:"concurrency,omitempty" yaml:"concurrency,omitempty"`
	RetryOnFailure  int               `json:"retry_on_failure,omitempty" yaml:"retry_on_failure,omitempty"`
	RetryDelayMS    int               `json:"retry_delay_ms,omitempty" yaml:"retry_delay_ms,omitempty"`
	Environment     map[string]string `json:"environment,omitempty" yaml:"environment,omitempty"`
}

// Key rotation and order constants for ProviderConfig.
const (
	// KeyRotation controls when to switch to the next key.
	KeyRotationOnFailure  = "on_failure"  // stick with current key; switch only on failure (default)
	KeyRotationPerRequest = "per_request" // pick a (possibly different) key for every request

	// KeyOrder controls which key is chosen among candidates.
	KeyOrderSequential = "sequential" // pick the next key in list order (default for non-Codex providers)
	KeyOrderRandom     = "random"     // pick uniformly at random among available keys
	KeyOrderSmart      = "smart"      // Codex-aware ordering: prefer non-soft-cooled keys, then never-used / higher headroom
)

const (
	OAuthProfileOpenAICodex       = "openai_codex"
	ProviderPresetCodex           = "codex"
	ProviderPresetAzure           = "azure"
	CompactionPresetGeneric       = "generic"
	CompactionPresetCodex         = "codex"
	CompactionProfileAuto         = "auto"
	CompactionProfileContinuation = "continuation"
	CompactionProfileArchival     = "archival"
)

// ProviderConfig specifies an LLM provider and its available models.
type ProviderConfig struct {
	Type                      string                 `json:"type" yaml:"type"`                                                                   // "chat-completions" | "messages" | "responses"
	APIURL                    string                 `json:"api_url" yaml:"api_url"`                                                             // complete API URL (e.g., "https://api.openai.com/v1/chat/completions")
	TokenURL                  string                 `json:"token_url,omitempty" yaml:"token_url,omitempty"`                                     // OAuth2 token endpoint for refresh_token grant
	ClientID                  string                 `json:"client_id,omitempty" yaml:"client_id,omitempty"`                                     // OAuth2 client_id (required by some providers, e.g. openai: app_EMoamEEZ73f0CkXaXp7hrann)
	Preset                    string                 `json:"preset,omitempty" yaml:"preset,omitempty"`                                           // e.g. "codex" for official ChatGPT/Codex OAuth, "azure" for Azure OpenAI Responses
	Store                     *bool                  `json:"store,omitempty" yaml:"store,omitempty"`                                             // provider-level Responses storage preference; nil defaults to false
	ResponsesWebsocket        *bool                  `json:"responses_websocket,omitempty" yaml:"responses_websocket,omitempty"`                 // whether to prefer Responses WebSocket transport; nil = preset default (codex:true, others:false)
	ResponseHeaderTimeout     int                    `json:"response_header_timeout,omitempty" yaml:"response_header_timeout,omitempty"`         // initial provider HTTP response-header timeout in seconds (0 = built-in default)
	StreamIdleTimeout         int                    `json:"stream_idle_timeout,omitempty" yaml:"stream_idle_timeout,omitempty"`                 // per-stream idle timeout in seconds (0 = built-in defaults)
	WebSocketHandshakeTimeout int                    `json:"websocket_handshake_timeout,omitempty" yaml:"websocket_handshake_timeout,omitempty"` // Responses WebSocket handshake timeout in seconds (0 = built-in default)
	RateLimit                 int                    `json:"rate_limit" yaml:"rate_limit"`                                                       // requests per minute (0 = no limit)
	UserAgent                 string                 `json:"user_agent,omitempty" yaml:"user_agent,omitempty"`                                   // optional User-Agent override for provider/model HTTP requests
	AuthScheme                string                 `json:"auth_scheme,omitempty" yaml:"auth_scheme,omitempty"`                                 // optional auth scheme override for request header selection
	Proxy                     *string                `json:"proxy,omitempty" yaml:"proxy,omitempty"`                                             // per-provider proxy URL; nil = inherit global, non-nil (incl. "") = override
	Compat                    *ProviderCompatConfig  `json:"compat,omitempty" yaml:"compat,omitempty"`                                           // provider-level compat defaults (model-level can override model compat only)
	OfficialAPI               *bool                  `json:"official_api,omitempty" yaml:"official_api,omitempty"`                               // true for direct official provider endpoints; false for aggregating/proxy gateways
	SupportedServiceTiers     []ServiceTier          `json:"supported_service_tiers,omitempty" yaml:"supported_service_tiers,omitempty"`         // provider-level default non-standard tiers; model-level can override
	Models                    map[string]ModelConfig `json:"models" yaml:"models"`
	KeyRotation               string                 `json:"key_rotation" yaml:"key_rotation"`             // "on_failure" (default) | "per_request"
	KeyOrder                  string                 `json:"key_order" yaml:"key_order"`                   // "sequential" (default, non-Codex) | "random" | "smart" (default for preset: codex)
	Compress                  bool                   `json:"compress,omitempty" yaml:"compress,omitempty"` // enable gzip request compression for this provider
}

// ModelModalities declares which input modalities a model supports.
type ModelModalities struct {
	Input []string `json:"input,omitempty" yaml:"input,omitempty"` // "text", "image", "pdf"
}

// ModelConfig specifies a model and its parameters.
type ModelConfig struct {
	Name                  string                  `json:"name" yaml:"name"`
	Limit                 ModelLimit              `json:"limit" yaml:"limit"`
	Modalities            *ModelModalities        `json:"modalities,omitempty" yaml:"modalities,omitempty"`
	SupportedServiceTiers []ServiceTier           `json:"supported_service_tiers,omitempty" yaml:"supported_service_tiers,omitempty"` // explicit non-standard tiers supported by this model
	Thinking              *ThinkingConfig         `json:"thinking,omitempty" yaml:"thinking,omitempty"`
	Reasoning             *ReasoningConfig        `json:"reasoning,omitempty" yaml:"reasoning,omitempty"`
	Text                  *TextConfig             `json:"text,omitempty" yaml:"text,omitempty"`
	ParallelToolCalls     *bool                   `json:"parallel_tool_calls,omitempty" yaml:"parallel_tool_calls,omitempty"` // nil = Responses request default false; non-nil = explicit override
	PromptCache           *PromptCacheConfig      `json:"prompt_cache,omitempty" yaml:"prompt_cache,omitempty"`
	Compat                *ModelCompatConfig      `json:"compat,omitempty" yaml:"compat,omitempty"`
	Cost                  *ModelCost              `json:"cost,omitempty" yaml:"cost,omitempty"`
	Store                 *bool                   `json:"store,omitempty" yaml:"store,omitempty"` // model-level Responses storage preference; nil inherits provider/default false
	Variants              map[string]ModelVariant `json:"variants,omitempty" yaml:"variants,omitempty"`
}

// EffectiveResponsesWebsocket returns whether Responses WebSocket should be used.
// Provider override has highest priority; otherwise preset:codex defaults to enabled.
func EffectiveResponsesWebsocket(preset string, providerValue *bool) bool {
	if providerValue != nil {
		return *providerValue
	}
	return strings.EqualFold(strings.TrimSpace(preset), ProviderPresetCodex)
}

// EffectiveStore resolves the configured Responses storage preference.
// Model-level setting takes priority over provider-level; both nil defaults to false.
func EffectiveStore(providerStore, modelStore *bool) bool {
	if modelStore != nil {
		return *modelStore
	}
	if providerStore != nil {
		return *providerStore
	}
	return false
}

// NormalizeAuthScheme trims, lowercases, and validates a provider auth scheme.
// Empty input stays empty to allow later default inference.
func NormalizeAuthScheme(raw string) (string, error) {
	scheme := strings.TrimSpace(strings.ToLower(raw))
	switch scheme {
	case "":
		return "", nil
	case AuthSchemeAnthropicAPIKey, AuthSchemeBearer, AuthSchemeAPIKey:
		return scheme, nil
	default:
		return "", fmt.Errorf("unsupported auth_scheme %q", raw)
	}
}

// EffectiveAuthScheme resolves the request authentication scheme for a provider.
// An explicit provider auth_scheme override wins; otherwise transport defaults
// are inferred from preset/type/url shape.
func EffectiveAuthScheme(preset, providerType, apiURL, authScheme string) string {
	if normalized, err := NormalizeAuthScheme(authScheme); err == nil && normalized != "" {
		return normalized
	}
	if strings.EqualFold(strings.TrimSpace(preset), ProviderPresetAzure) {
		return AuthSchemeAPIKey
	}
	providerType = strings.TrimSpace(strings.ToLower(providerType))
	switch providerType {
	case ProviderTypeMessages:
		return AuthSchemeAnthropicAPIKey
	case ProviderTypeResponses, ProviderTypeChatCompletions:
		return AuthSchemeBearer
	}
	if APIURLPathHasSuffix(apiURL, "/messages") {
		return AuthSchemeAnthropicAPIKey
	}
	if APIURLPathHasSuffix(apiURL, "/responses") || APIURLPathHasSuffix(apiURL, "/chat/completions") {
		return AuthSchemeBearer
	}
	return ""
}

// SplitProviderModelRef splits a model reference base into provider and model parts.
// The input should be in the form "provider/model" without any @variant suffix.
func SplitProviderModelRef(base string) (provider, model string) {
	parts := strings.SplitN(strings.TrimSpace(base), "/", 2)
	if len(parts) != 2 {
		return strings.TrimSpace(base), ""
	}
	return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
}

// CanonicalModelRef formats a provider/model reference with an optional @variant suffix.
func CanonicalModelRef(provider, model, variant string) string {
	ref := strings.TrimSpace(provider) + "/" + strings.TrimSpace(model)
	if strings.TrimSpace(variant) != "" {
		ref += "@" + strings.TrimSpace(variant)
	}
	return ref
}

// LookupConfiguredModel validates that provider/model exists in the configured providers.
func LookupConfiguredModel(providers map[string]ProviderConfig, providerName, modelName string) (ProviderConfig, ModelConfig, error) {
	providerName = strings.TrimSpace(providerName)
	modelName = strings.TrimSpace(modelName)
	providerCfg, ok := providers[providerName]
	if !ok {
		return ProviderConfig{}, ModelConfig{}, fmt.Errorf("provider %q not found in config", providerName)
	}
	modelCfg, ok := providerCfg.Models[modelName]
	if !ok {
		return ProviderConfig{}, ModelConfig{}, fmt.Errorf("model %q not found in provider %q", modelName, providerName)
	}
	return providerCfg, modelCfg, nil
}

// ValidateConfiguredVariant validates that the named variant exists for the model.
// An empty variant is always accepted.
func ValidateConfiguredVariant(modelCfg ModelConfig, providerName, modelName, variantName string) error {
	variantName = strings.TrimSpace(variantName)
	if variantName == "" {
		return nil
	}
	if _, ok := modelCfg.Variants[variantName]; !ok {
		return fmt.Errorf("variant %q not found for model %q in provider %q", variantName, modelName, providerName)
	}
	return nil
}

// LookupConfiguredModelVariant validates that provider/model[/@variant] exists in the configured providers.
func LookupConfiguredModelVariant(providers map[string]ProviderConfig, providerName, modelName, variantName string) (ProviderConfig, ModelConfig, error) {
	providerCfg, modelCfg, err := LookupConfiguredModel(providers, providerName, modelName)
	if err != nil {
		return ProviderConfig{}, ModelConfig{}, err
	}
	if err := ValidateConfiguredVariant(modelCfg, providerName, modelName, variantName); err != nil {
		return ProviderConfig{}, ModelConfig{}, err
	}
	return providerCfg, modelCfg, nil
}

// ResolveConfiguredModelRef parses and validates a configured model reference.
// It requires the ref to be in the form "provider/model[@variant]".
// The returned variant is parsed from the ref but intentionally not validated;
// callers that need strict variant checking should call ValidateConfiguredVariant
// or LookupConfiguredModelVariant on the returned model.
func ResolveConfiguredModelRef(providers map[string]ProviderConfig, rawRef string) (providerName, modelName, variantName string, providerCfg ProviderConfig, modelCfg ModelConfig, err error) {
	baseRef, variantName := ParseModelRef(strings.TrimSpace(rawRef))
	baseRef = strings.TrimSpace(baseRef)
	variantName = strings.TrimSpace(variantName)
	if baseRef == "" {
		return "", "", "", ProviderConfig{}, ModelConfig{}, fmt.Errorf("model reference must not be empty")
	}
	if !strings.Contains(baseRef, "/") {
		return "", "", variantName, ProviderConfig{}, ModelConfig{}, fmt.Errorf("model reference %q must include a provider; use provider/model[@variant]", rawRef)
	}

	providerName, modelName = SplitProviderModelRef(baseRef)
	providerCfg, modelCfg, err = LookupConfiguredModel(providers, providerName, modelName)
	if err != nil {
		return providerName, modelName, variantName, ProviderConfig{}, ModelConfig{}, err
	}
	return providerName, modelName, variantName, providerCfg, modelCfg, nil
}

// SupportsInput reports whether the model accepts the given input modality.
// When Modalities is unset, defaults to ["text"] only: image/pdf support must be
// declared explicitly so unconfigured models never receive binary parts they
// cannot process.
func (m *ModelConfig) SupportsInput(modality string) bool {
	if m.Modalities == nil {
		return modality == "text"
	}
	return slices.Contains(m.Modalities.Input, modality)
}

func serviceTierSet(rawTiers []ServiceTier) map[ServiceTier]bool {
	var out map[ServiceTier]bool
	add := func(tier ServiceTier) {
		if out == nil {
			out = make(map[ServiceTier]bool, 2)
		}
		out[tier] = true
	}
	for _, raw := range rawTiers {
		switch NormalizeServiceTier(string(raw)) {
		case ServiceTierFast:
			add(ServiceTierFast)
		case ServiceTierSlow:
			add(ServiceTierSlow)
		}
	}
	return out
}

// SupportedServiceTierSet returns the non-standard service tiers this model can accept.
// Explicit model supported_service_tiers wins; provider supported_service_tiers is
// the next default; preset: codex defaults to fast+slow because the first-party
// transport supports OpenAI service_tier fast/flex.
func (m *ModelConfig) SupportedServiceTierSet(providerPreset string, providerTiers []ServiceTier) map[ServiceTier]bool {
	if m != nil && len(m.SupportedServiceTiers) > 0 {
		return serviceTierSet(m.SupportedServiceTiers)
	}
	if len(providerTiers) > 0 {
		return serviceTierSet(providerTiers)
	}
	if strings.EqualFold(strings.TrimSpace(providerPreset), ProviderPresetCodex) {
		return serviceTierSet([]ServiceTier{ServiceTierFast, ServiceTierSlow})
	}
	return nil
}

// ModelCompatConfig contains provider/model-specific compatibility toggles.
// All options are opt-in and default to disabled unless explicitly enabled.
type ModelCompatConfig struct {
	ThinkingToolcall    *ThinkingToolcallCompatConfig    `json:"thinking_toolcall,omitempty" yaml:"thinking_toolcall,omitempty"`
	ReasoningContinuity *ReasoningContinuityCompatConfig `json:"reasoning_continuity,omitempty" yaml:"reasoning_continuity,omitempty"`
}

// ProviderCompatConfig contains provider-level compatibility toggles. Model
// behavior compat belongs under ModelCompatConfig; transport-layer compat lives
// here so the schema reflects the runtime semantics.
type ProviderCompatConfig struct {
	ThinkingToolcall    *ThinkingToolcallCompatConfig    `json:"thinking_toolcall,omitempty" yaml:"thinking_toolcall,omitempty"`
	ReasoningContinuity *ReasoningContinuityCompatConfig `json:"reasoning_continuity,omitempty" yaml:"reasoning_continuity,omitempty"`
}

// ReasoningContinuityCompatConfig controls provider/model-specific replay of
// protocol-native reasoning/thinking continuity payloads.
//
// Supported modes:
//   - "none": disable replay of provider-specific reasoning continuity state.
//   - "openai_visible": replay OpenAI-compatible assistant reasoning_content
//     and enable provider-specific preserved-thinking request flags where
//     required (for example GLM Preserved Thinking).
//
// Other protocols use dedicated runtime handling and default-safe stripping
// when switching to an incompatible target wire format.
type ReasoningContinuityCompatConfig struct {
	Mode string `json:"mode,omitempty" yaml:"mode,omitempty"`
}

// EffectiveMode returns the configured continuity mode.
func (c *ReasoningContinuityCompatConfig) EffectiveMode() string {
	if c == nil {
		return ""
	}
	return strings.TrimSpace(c.Mode)
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

// ModelLimit defines token limits for a model. Context is the total request
// window when known, Input is the input-side hard/usable budget when providers
// expose split limits, and Output is the requested-output hard cap.
type ModelLimit struct {
	Context int `json:"context" yaml:"context"`
	Input   int `json:"input,omitempty" yaml:"input,omitempty"`
	Output  int `json:"output" yaml:"output"`
}

// EffectiveInputBudget returns the input-side budget for request sizing and
// automatic compaction. When limit.input is configured it is authoritative;
// otherwise reserve the effective requested-output budget from the total context
// window so input + output can still fit inside limit.context.
func (l ModelLimit) EffectiveInputBudget(outputCapSetting, defaultOutputCap int) int {
	if l.Input > 0 {
		return l.Input
	}
	if l.Context <= 0 {
		return 0
	}
	outputBudget := l.EffectiveOutputBudget(outputCapSetting, defaultOutputCap)
	if outputBudget <= 0 {
		return l.Context
	}
	budget := l.Context - outputBudget
	if budget < 1 {
		return 1
	}
	return budget
}

// EffectiveOutputBudget returns the requested-output budget after applying the
// global output cap default and the model's own limit.output cap.
func (l ModelLimit) EffectiveOutputBudget(outputCapSetting, defaultOutputCap int) int {
	budget := outputCapSetting
	if budget <= 0 {
		budget = defaultOutputCap
	}
	if l.Output > 0 && (budget <= 0 || l.Output < budget) {
		budget = l.Output
	}
	if budget < 0 {
		return 0
	}
	return budget
}

// ThinkingConfig controls extended thinking for Anthropic models.
// Type selects one of three mutually-exclusive modes:
//   - "enabled": manual mode; parses Budget and optional Display
//   - "adaptive": adaptive mode; parses Effort and optional Display
//   - "disabled": thinking disabled; no extra thinking fields are parsed
//
// Budget belongs to manual mode only.
// Effort belongs to adaptive mode only.
// Display is valid only for enabled/adaptive modes.
type ThinkingConfig struct {
	Type            string `json:"type,omitempty" yaml:"type,omitempty"`
	Budget          int    `json:"budget,omitempty" yaml:"budget,omitempty"`
	Effort          string `json:"effort,omitempty" yaml:"effort,omitempty"`
	Display         string `json:"display,omitempty" yaml:"display,omitempty"`
	IncludeThoughts *bool  `json:"include_thoughts,omitempty" yaml:"include_thoughts,omitempty"`
	Level           string `json:"level,omitempty" yaml:"level,omitempty"`
}

// EffectiveType returns the configured thinking type.
func (t *ThinkingConfig) EffectiveType() string {
	if t == nil {
		return ""
	}
	return t.Type
}

// PromptCacheConfig controls Anthropic prompt caching strategy.
// Mode: "off" | "auto" | "explicit" (default when empty: "explicit", preserving existing behaviour).
// TTL: "" (5-minute default) | "1h".
// CacheTools: when true, the last tool definition gets a cache breakpoint.
type PromptCacheConfig struct {
	Mode       string `json:"mode,omitempty" yaml:"mode,omitempty"`
	TTL        string `json:"ttl,omitempty" yaml:"ttl,omitempty"`
	CacheTools *bool  `json:"cache_tools,omitempty" yaml:"cache_tools,omitempty"`
}

// CacheToolsEnabled reports whether tool schema caching is enabled.
// Defaults to false.
func (p *PromptCacheConfig) CacheToolsEnabled() bool {
	if p == nil || p.CacheTools == nil {
		return false
	}
	return *p.CacheTools
}

// ReasoningConfig controls OpenAI reasoning models (o1, o3, o4-mini).
// When set, reasoning_effort is sent in the request and temperature is omitted.
type ReasoningConfig struct {
	Effort  string `json:"effort" yaml:"effort"`                       // e.g. "low" | "medium" | "high" | "xhigh"
	Summary string `json:"summary,omitempty" yaml:"summary,omitempty"` // "auto" | "concise" | "detailed" | ""
}

// ModelVariant defines a named parameter preset for a model.
// For Anthropic models: set Thinking to override type/budget/effort/display.
// For OpenAI Responses API models: set Reasoning to override effort/summary.
type ModelVariant struct {
	Thinking          *ThinkingConfig    `json:"thinking,omitempty" yaml:"thinking,omitempty"`
	Reasoning         *ReasoningConfig   `json:"reasoning,omitempty" yaml:"reasoning,omitempty"`
	Text              *TextConfig        `json:"text,omitempty" yaml:"text,omitempty"`
	ParallelToolCalls *bool              `json:"parallel_tool_calls,omitempty" yaml:"parallel_tool_calls,omitempty"` // nil = inherit model default; non-nil = override Responses request hint
	PromptCache       *PromptCacheConfig `json:"prompt_cache,omitempty" yaml:"prompt_cache,omitempty"`
}

// TextConfig controls output presentation knobs shared by GPT-5 family APIs.
type TextConfig struct {
	Verbosity string `json:"verbosity,omitempty" yaml:"verbosity,omitempty"` // "low" | "medium" | "high"
}

// EffectiveTextVerbosity returns the configured text verbosity.
func (m ModelConfig) EffectiveTextVerbosity() string {
	if m.Text != nil && m.Text.Verbosity != "" {
		return m.Text.Verbosity
	}
	return ""
}

// EffectiveTextVerbosity returns the configured text verbosity.
func (v ModelVariant) EffectiveTextVerbosity() string {
	if v.Text != nil && v.Text.Verbosity != "" {
		return v.Text.Verbosity
	}
	return ""
}

// EffectiveThinkingType returns the resolved thinking type for a model.
func (m ModelConfig) EffectiveThinkingType() string {
	return m.Thinking.EffectiveType()
}

// EffectiveThinkingEffort returns the Anthropic effort setting for a model.
func (m ModelConfig) EffectiveThinkingEffort() string {
	if m.Thinking != nil {
		return m.Thinking.Effort
	}
	return ""
}

// EffectiveThinkingDisplay returns the Anthropic thinking display setting.
func (m ModelConfig) EffectiveThinkingDisplay() string {
	if m.Thinking != nil {
		return m.Thinking.Display
	}
	return ""
}

// EffectiveThinkingType returns the resolved thinking type for a variant.
func (v ModelVariant) EffectiveThinkingType() string {
	return v.Thinking.EffectiveType()
}

// EffectivePromptCacheMode returns the prompt cache mode, defaulting to "explicit".
func (m ModelConfig) EffectivePromptCacheMode() string {
	if m.PromptCache != nil && m.PromptCache.Mode != "" {
		return m.PromptCache.Mode
	}
	return "explicit"
}

// ServiceTier identifies the user-facing service tier abstraction.
type ServiceTier string

const (
	ServiceTierStandard ServiceTier = "standard"
	ServiceTierFast     ServiceTier = "fast"
	ServiceTierSlow     ServiceTier = "slow"
)

// NormalizeServiceTier folds arbitrary input into one of the supported tiers.
// Unknown or empty values default to standard.
func NormalizeServiceTier(raw string) ServiceTier {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case string(ServiceTierFast):
		return ServiceTierFast
	case string(ServiceTierSlow):
		return ServiceTierSlow
	default:
		return ServiceTierStandard
	}
}

// ServiceTierMultipliers stores optional per-tier price multipliers.
type ServiceTierMultipliers struct {
	Fast float64 `json:"fast,omitempty" yaml:"fast,omitempty"`
	Slow float64 `json:"slow,omitempty" yaml:"slow,omitempty"`
}

// ModelCostInputTier overrides the flat top-level prices above a strict token threshold.
type ModelCostInputTier struct {
	AboveInputTokens int64   `json:"above_input_tokens" yaml:"above_input_tokens"`
	Input            float64 `json:"input" yaml:"input"`
	Output           float64 `json:"output" yaml:"output"`
	CacheRead        float64 `json:"cache_read" yaml:"cache_read"`
	CacheWrite       float64 `json:"cache_write" yaml:"cache_write"`
	CacheWrite1h     float64 `json:"cache_write_1h" yaml:"cache_write_1h"`
}

// ResolvedModelCost captures the final prices after input-tier selection and service-tier multipliers.
type ResolvedModelCost struct {
	Input                 float64     `json:"input"`
	Output                float64     `json:"output"`
	CacheRead             float64     `json:"cache_read"`
	CacheWrite            float64     `json:"cache_write"`
	CacheWrite1h          float64     `json:"cache_write_1h"`
	ServiceTier           ServiceTier `json:"service_tier"`
	ServiceTierMultiplier float64     `json:"service_tier_multiplier"`
	InputTierAboveTokens  int64       `json:"input_tier_above_tokens"`
}

// ModelCost records per-token pricing for cost tracking (USD per 1M tokens).
type ModelCost struct {
	Input                  float64                 `json:"input" yaml:"input"`
	Output                 float64                 `json:"output" yaml:"output"`
	CacheRead              float64                 `json:"cache_read" yaml:"cache_read"`
	CacheWrite             float64                 `json:"cache_write" yaml:"cache_write"`
	CacheWrite1h           float64                 `json:"cache_write_1h" yaml:"cache_write_1h"`
	ServiceTierMultipliers *ServiceTierMultipliers `json:"service_tier_multipliers,omitempty" yaml:"service_tier_multipliers,omitempty"`
	InputTiers             []ModelCostInputTier    `json:"input_tiers,omitempty" yaml:"input_tiers,omitempty"`
}

func effectiveCacheWritePrice(input, cacheWrite float64) float64 {
	if cacheWrite > 0 {
		return cacheWrite
	}
	return input
}

func effectiveCacheWrite1hPrice(input, cacheWrite, cacheWrite1h float64) float64 {
	if cacheWrite1h > 0 {
		return cacheWrite1h
	}
	return effectiveCacheWritePrice(input, cacheWrite)
}

// ResolvePricing returns the final token prices for a given billable input size and service tier.
func (c *ModelCost) ResolvePricing(billableInputTokens int64, tier ServiceTier) ResolvedModelCost {
	if c == nil {
		return ResolvedModelCost{ServiceTier: NormalizeServiceTier(string(tier)), ServiceTierMultiplier: 1}
	}
	resolved := ResolvedModelCost{
		Input:                 c.Input,
		Output:                c.Output,
		CacheRead:             c.CacheRead,
		CacheWrite:            effectiveCacheWritePrice(c.Input, c.CacheWrite),
		CacheWrite1h:          effectiveCacheWrite1hPrice(c.Input, c.CacheWrite, c.CacheWrite1h),
		ServiceTier:           NormalizeServiceTier(string(tier)),
		ServiceTierMultiplier: 1,
		InputTierAboveTokens:  -1,
	}
	for _, inputTier := range c.InputTiers {
		if inputTier.AboveInputTokens < billableInputTokens && inputTier.AboveInputTokens > resolved.InputTierAboveTokens {
			resolved.Input = inputTier.Input
			resolved.Output = inputTier.Output
			resolved.CacheRead = inputTier.CacheRead
			resolved.CacheWrite = effectiveCacheWritePrice(inputTier.Input, inputTier.CacheWrite)
			resolved.CacheWrite1h = effectiveCacheWrite1hPrice(inputTier.Input, inputTier.CacheWrite, inputTier.CacheWrite1h)
			resolved.InputTierAboveTokens = inputTier.AboveInputTokens
		}
	}
	if c.ServiceTierMultipliers != nil {
		switch resolved.ServiceTier {
		case ServiceTierFast:
			if c.ServiceTierMultipliers.Fast > 0 {
				resolved.ServiceTierMultiplier = c.ServiceTierMultipliers.Fast
			}
		case ServiceTierSlow:
			if c.ServiceTierMultipliers.Slow > 0 {
				resolved.ServiceTierMultiplier = c.ServiceTierMultipliers.Slow
			}
		}
	}
	if resolved.ServiceTierMultiplier != 1 {
		resolved.Input *= resolved.ServiceTierMultiplier
		resolved.Output *= resolved.ServiceTierMultiplier
		resolved.CacheRead *= resolved.ServiceTierMultiplier
		resolved.CacheWrite *= resolved.ServiceTierMultiplier
		resolved.CacheWrite1h *= resolved.ServiceTierMultiplier
	}
	return resolved
}

// ContextConfig controls automatic context compression behavior.
type ContextConfig struct {
	Compaction CompactionConfig       `json:"compaction" yaml:"compaction,omitempty"`
	Reduction  ContextReductionConfig `json:"reduction" yaml:"reduction,omitempty"`
}

// ContextReductionConfig controls request-level context pruning.
type ContextReductionConfig struct {
	ConfirmAgeTurns         int     `json:"confirm_age_turns,omitempty" yaml:"confirm_age_turns,omitempty"`
	ErrorAgeTurns           int     `json:"error_age_turns,omitempty" yaml:"error_age_turns,omitempty"`
	HighRiskProtectAgeTurns int     `json:"high_risk_protect_age_turns,omitempty" yaml:"high_risk_protect_age_turns,omitempty"`
	ShellSuccessAgeTurns    int     `json:"shell_success_age_turns,omitempty" yaml:"shell_success_age_turns,omitempty"`
	ShellSuccessBytes       int     `json:"shell_success_bytes,omitempty" yaml:"shell_success_bytes,omitempty"`
	ReadLikeAgeTurns        int     `json:"read_like_age_turns,omitempty" yaml:"read_like_age_turns,omitempty"`
	ReadLikeOutputBytes     int     `json:"read_like_output_bytes,omitempty" yaml:"read_like_output_bytes,omitempty"`
	StaleAgeTurns           int     `json:"stale_age_turns,omitempty" yaml:"stale_age_turns,omitempty"`
	StaleOutputBytes        int     `json:"stale_output_bytes,omitempty" yaml:"stale_output_bytes,omitempty"`
	WrapUpGraceRequests     int     `json:"wrap_up_grace_requests,omitempty" yaml:"wrap_up_grace_requests,omitempty"`
	MinToolResultsPrune     int     `json:"min_tool_results_prune,omitempty" yaml:"min_tool_results_prune,omitempty"`
	MinIncrementalTokens    int     `json:"min_incremental_saved_tokens,omitempty" yaml:"min_incremental_saved_tokens,omitempty"`
	HighPressureUsage       float64 `json:"high_pressure_usage,omitempty" yaml:"high_pressure_usage,omitempty"`
	ForcePruneUsage         float64 `json:"force_prune_usage,omitempty" yaml:"force_prune_usage,omitempty"`
}

// CompactionConfig controls durable compaction backend, output profile, and
// input-budget reservation used by auto-compaction / oversize recovery.
type CompactionConfig struct {
	Threshold float64 `json:"threshold,omitempty" yaml:"threshold,omitempty"`
	Preset    string  `json:"preset,omitempty" yaml:"preset,omitempty"`
	Profile   string  `json:"profile,omitempty" yaml:"profile,omitempty"`
	Reserved  int     `json:"reserved,omitempty" yaml:"reserved,omitempty"`
	ModelPool string  `json:"model_pool,omitempty" yaml:"model_pool,omitempty"`
}

// DefaultConfig returns a Config with hardcoded defaults.
func DefaultConfig() *Config {
	return &Config{
		Context: ContextConfig{
			Reduction: ContextReductionConfig{
				ConfirmAgeTurns:         2,
				ErrorAgeTurns:           3,
				HighRiskProtectAgeTurns: 4,
				ShellSuccessAgeTurns:    2,
				ShellSuccessBytes:       4000,
				ReadLikeAgeTurns:        2,
				ReadLikeOutputBytes:     4000,
				StaleAgeTurns:           4,
				StaleOutputBytes:        1500,
				WrapUpGraceRequests:     1,
				MinToolResultsPrune:     8,
				MinIncrementalTokens:    4096,
				HighPressureUsage:       0.80,
				ForcePruneUsage:         0.90,
			},
			Compaction: CompactionConfig{
				Threshold: 0.8,
				Profile:   CompactionProfileAuto,
			},
		},
		Diagnostics: DefaultDiagnosticsConfig(),
		Maintenance: MaintenanceConfig{SizeCheckOnStartup: false, WarnStateBytes: 10 * 1024 * 1024 * 1024, WarnCacheBytes: 5 * 1024 * 1024 * 1024},
	}
}

// LoadConfig loads configuration from the user-level config.yaml.
func LoadConfig() (*Config, error) {
	configPath, err := ConfigPath()
	if err != nil {
		return nil, err
	}
	return LoadConfigFromPath(configPath)
}

// ProjectConfigPath returns the project-local config path under .chord/.
func ProjectConfigPath(projectRoot string) string {
	return filepath.Join(projectRoot, ".chord", "config.yaml")
}

// LoadConfigFromPath loads configuration from an arbitrary YAML file path.
// The config is initialised with DefaultConfig() values before unmarshalling,
// so omitted YAML fields retain their defaults (e.g. compaction.threshold: 0.8).
func LoadConfigFromPath(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	return loadConfigData(path, data, true)
}

// LoadConfigOverrideFromPath loads a config file without applying built-in
// defaults. This is used for project-level overrides so omitted fields stay
// unset and do not accidentally shadow global defaults.
func LoadConfigOverrideFromPath(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	return loadConfigData(path, data, false)
}

// MergeProjectConfig overlays a project-level .chord/config.yaml onto an
// already-loaded global config. Missing project configs are ignored. Only keys
// documented as project-scoped participate in the merge; global-only keys such
// as paths.* and maintenance.* are intentionally ignored here.
func MergeProjectConfig(base *Config, path string) (projectCfg *Config, merged *Config, err error) {
	if strings.TrimSpace(path) == "" {
		return nil, base, nil
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		if os.IsNotExist(readErr) {
			return nil, base, nil
		}
		return nil, nil, fmt.Errorf("read config %s: %w", path, readErr)
	}
	projectCfg, err = loadConfigData(path, data, false)
	if err != nil {
		return nil, nil, err
	}
	merged, err = mergeConfigOverrideData(base, data, path)
	if err != nil {
		return nil, nil, err
	}
	return projectCfg, merged, nil
}

func loadConfigData(path string, data []byte, withDefaults bool) (*Config, error) {
	data, err := normalizeConfigShorthands(path, data)
	if err != nil {
		return nil, err
	}
	cfg := &Config{}
	if withDefaults {
		cfg = DefaultConfig()
	}
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	if err := ValidateDiagnosticsConfig(cfg); err != nil {
		return nil, fmt.Errorf("validate config %s: %w", path, err)
	}
	return cfg, nil
}

func normalizeConfigShorthands(path string, data []byte) ([]byte, error) {
	var raw map[string]any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	changed := normalizeContextReductionShorthand(raw)
	if changed {
		out, err := yaml.Marshal(raw)
		if err != nil {
			return nil, fmt.Errorf("marshal normalized config %s: %w", path, err)
		}
		return out, nil
	}
	return data, nil
}

func normalizeContextReductionShorthand(raw map[string]any) bool {
	contextRaw, ok := raw["context"].(map[string]any)
	if !ok {
		return false
	}
	reduction, ok := contextRaw["reduction"]
	if !ok {
		return false
	}
	enabled, ok := reduction.(bool)
	if !ok {
		return false
	}
	if enabled {
		contextRaw["reduction"] = map[string]any{}
	}
	raw["context"] = contextRaw
	return true
}

var projectScopedTopLevelKeys = map[string]bool{
	"providers":            true,
	"model_pools":          true,
	"context":              true,
	"thinking_translation": true,
	"skills":               true,
	"confirm_timeout":      true,
	"diff":                 true,
	"desktop_notification": true,
	"prevent_sleep":        true,
	"keymap":               true,
	"commands":             true,
	"ime_switch_target":    true,
	"log_level":            true,
	"lsp":                  true,
	"mcp":                  true,
	"hooks":                true,
	"max_output_tokens":    true,
	"stream_retry_rounds":  true,
	"proxy":                true,
	"web_fetch":            true,
	"worktree":             true,
}

func mergeConfigOverrideData(base *Config, overrideData []byte, overridePath string) (*Config, error) {
	baseMap, err := configToYAMLMap(base)
	if err != nil {
		return nil, err
	}
	overrideData, err = normalizeConfigShorthands(overridePath, overrideData)
	if err != nil {
		return nil, err
	}
	var overrideMap map[string]any
	if err := yaml.Unmarshal(overrideData, &overrideMap); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", overridePath, err)
	}
	mergeProjectConfigMap(baseMap, overrideMap, nil)
	mergedData, err := yaml.Marshal(baseMap)
	if err != nil {
		return nil, fmt.Errorf("marshal merged config %s: %w", overridePath, err)
	}
	return loadConfigData(overridePath, mergedData, false)
}

func configToYAMLMap(cfg *Config) (map[string]any, error) {
	if cfg == nil {
		return map[string]any{}, nil
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("marshal config: %w", err)
	}
	var out map[string]any
	if err := yaml.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("parse config yaml: %w", err)
	}
	if out == nil {
		return map[string]any{}, nil
	}
	return out, nil
}

func mergeProjectConfigMap(dst, src map[string]any, path []string) {
	if dst == nil || src == nil {
		return
	}
	for key, raw := range src {
		if len(path) == 0 && !projectScopedTopLevelKeys[key] {
			continue
		}
		if len(path) == 1 && path[0] == "skills" && key == "paths" {
			dst[key] = appendYAMLSequences(dst[key], raw)
			continue
		}
		if len(path) == 1 && path[0] == "hooks" {
			dst[key] = appendYAMLSequences(dst[key], raw)
			continue
		}
		if childDst, ok := dst[key].(map[string]any); ok {
			if childSrc, ok := raw.(map[string]any); ok {
				mergeProjectConfigMap(childDst, childSrc, append(path, key))
				dst[key] = childDst
				continue
			}
		}
		dst[key] = cloneYAMLValue(raw)
	}
}

func appendYAMLSequences(base, extra any) any {
	baseSeq, _ := base.([]any)
	extraSeq, ok := extra.([]any)
	if !ok {
		return cloneYAMLValue(extra)
	}
	out := make([]any, 0, len(baseSeq)+len(extraSeq))
	for _, item := range baseSeq {
		out = append(out, cloneYAMLValue(item))
	}
	for _, item := range extraSeq {
		out = append(out, cloneYAMLValue(item))
	}
	return out
}

func cloneYAMLValue(v any) any {
	switch vv := v.(type) {
	case map[string]any:
		cloned := make(map[string]any, len(vv))
		for k, child := range vv {
			cloned[k] = cloneYAMLValue(child)
		}
		return cloned
	case []any:
		cloned := make([]any, len(vv))
		for i, child := range vv {
			cloned[i] = cloneYAMLValue(child)
		}
		return cloned
	default:
		return vv
	}
}
