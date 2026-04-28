package config

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// Provider type constants
const (
	ProviderTypeChatCompletions = "chat-completions"
	ProviderTypeMessages        = "messages"
	ProviderTypeResponses       = "responses"
)

// Config is the top-level configuration for the chord agent.
type Config struct {
	Providers      map[string]ProviderConfig `json:"providers" yaml:"providers"`             // LLM providers
	Context        ContextConfig             `json:"context" yaml:"context"`                 // context compression settings
	Skills         SkillsConfig              `json:"skills" yaml:"skills"`                   // additional skill paths
	ConfirmTimeout int                       `json:"confirm_timeout" yaml:"confirm_timeout"` // confirmation timeout in seconds (0 = infinite, default)
	Diff           DiffConfig                `json:"diff" yaml:"diff"`                       // TUI diff rendering options
	// DesktopNotification, when true, enables OSC 9 idle notifications in local TUI (terminal unfocused). YAML: desktop_notification: true
	DesktopNotification *bool `json:"desktop_notification,omitempty" yaml:"desktop_notification,omitempty"`
	// PreventSleep, when true, prevents macOS idle sleep while any agent is active (non-idle). YAML: prevent_sleep: true
	// Only effective in the local TUI.
	PreventSleep    *bool               `json:"prevent_sleep,omitempty" yaml:"prevent_sleep,omitempty"`
	KeyMap          map[string][]string `json:"keymap,omitempty" yaml:"keymap,omitempty"`                       // custom key bindings (snake_case action → key list)
	Commands        map[string]string   `json:"commands,omitempty" yaml:"commands,omitempty"`                   // custom slash commands: "/cmd" → text to send as message
	IMESwitchTarget string              `json:"ime_switch_target,omitempty" yaml:"ime_switch_target,omitempty"` // English IM key (e.g. com.apple.keylayout.ABC); switch/restore use im-select or im-select.exe by platform
	LogLevel        string              `json:"log_level" yaml:"log_level"`                                     // log verbosity: "debug", "info" (default), "warn", "error"
	Paths           PathsConfig         `json:"paths,omitempty" yaml:"paths,omitempty"`                         // user-level state/cache/logs path overrides
	Maintenance     MaintenanceConfig   `json:"maintenance,omitempty" yaml:"maintenance,omitempty"`             // optional cleanup/status checks
	LSP             LSPConfig           `json:"lsp" yaml:"lsp"`                                                 // LSP server config
	MCP             MCPConfig           `json:"mcp,omitempty" yaml:"mcp,omitempty"`                             // MCP server configs
	Hooks           HookConfig          `json:"hooks,omitempty" yaml:"hooks,omitempty"`                         // lifecycle hook configs
	MaxOutputTokens int                 `json:"max_output_tokens" yaml:"max_output_tokens"`                     // global output token cap (0 = use DefaultOutputTokenMax)
	Proxy           string              `json:"proxy,omitempty" yaml:"proxy,omitempty"`                         // global proxy URL (http/https/socks5), empty = no proxy
	WebFetch        WebFetchConfig      `json:"web_fetch,omitempty" yaml:"web_fetch,omitempty"`                 // WebFetch-specific options
}

// WebFetchConfig controls WebFetch runtime behavior.
type WebFetchConfig struct {
	// UserAgent overrides the default browser-like User-Agent. Nil = inherit builtin default;
	// non-nil empty string explicitly resets to builtin default when merging project config.
	UserAgent *string `json:"user_agent,omitempty" yaml:"user_agent,omitempty"`
}

type MaintenanceConfig struct {
	SizeCheckOnStartup     bool  `json:"size_check_on_startup" yaml:"size_check_on_startup"`
	SizeCheckIntervalHours int   `json:"size_check_interval_hours" yaml:"size_check_interval_hours"`
	WarnStateBytes         int64 `json:"warn_state_bytes" yaml:"warn_state_bytes"`
	WarnCacheBytes         int64 `json:"warn_cache_bytes" yaml:"warn_cache_bytes"`
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
	KeyOrderSequential = "sequential" // pick the next key in list order (default)
	KeyOrderRandom     = "random"     // pick uniformly at random among available keys
)

const (
	OAuthProfileOpenAICodex       = "openai_codex"
	ProviderPresetCodex           = "codex"
	CompactionPresetGeneric       = "generic"
	CompactionPresetCodex         = "codex"
	CompactionProfileAuto         = "auto"
	CompactionProfileContinuation = "continuation"
	CompactionProfileArchival     = "archival"
)

// ProviderConfig specifies an LLM provider and its available models.
type ProviderConfig struct {
	Type               string                 `json:"type" yaml:"type"`                                                   // "chat-completions" | "messages" | "responses"
	APIURL             string                 `json:"api_url" yaml:"api_url"`                                             // complete API URL (e.g., "https://api.openai.com/v1/chat/completions")
	TokenURL           string                 `json:"token_url,omitempty" yaml:"token_url,omitempty"`                     // OAuth2 token endpoint for refresh_token grant
	ClientID           string                 `json:"client_id,omitempty" yaml:"client_id,omitempty"`                     // OAuth2 client_id (required by some providers, e.g. openai: app_EMoamEEZ73f0CkXaXp7hrann)
	Preset             string                 `json:"preset,omitempty" yaml:"preset,omitempty"`                           // e.g. "codex" for the official ChatGPT/Codex OAuth transport
	Store              *bool                  `json:"store,omitempty" yaml:"store,omitempty"`                             // whether to enable server-side storage for Responses API (enables previous_response_id reuse)
	ResponsesWebsocket *bool                  `json:"responses_websocket,omitempty" yaml:"responses_websocket,omitempty"` // whether to prefer Responses WebSocket transport; nil = preset default (codex:true, others:false)
	RateLimit          int                    `json:"rate_limit" yaml:"rate_limit"`                                       // requests per minute (0 = no limit)
	Proxy              *string                `json:"proxy,omitempty" yaml:"proxy,omitempty"`                             // per-provider proxy URL; nil = inherit global, non-nil (incl. "") = override
	Compat             *ProviderCompatConfig  `json:"compat,omitempty" yaml:"compat,omitempty"`                           // provider-level compat defaults (model-level can override model compat only)
	Models             map[string]ModelConfig `json:"models" yaml:"models"`
	KeyRotation        string                 `json:"key_rotation" yaml:"key_rotation"`             // "on_failure" (default) | "per_request"
	KeyOrder           string                 `json:"key_order" yaml:"key_order"`                   // "sequential" (default) | "random"
	Compress           bool                   `json:"compress,omitempty" yaml:"compress,omitempty"` // enable gzip request compression for this provider
}

// ModelModalities declares which input modalities a model supports.
type ModelModalities struct {
	Input []string `json:"input,omitempty" yaml:"input,omitempty"` // "text", "image", "pdf"
}

// ModelConfig specifies a model and its parameters.
type ModelConfig struct {
	Name              string                  `json:"name" yaml:"name"`
	Limit             ModelLimit              `json:"limit" yaml:"limit"`
	Modalities        *ModelModalities        `json:"modalities,omitempty" yaml:"modalities,omitempty"`
	Thinking          *ThinkingConfig         `json:"thinking,omitempty" yaml:"thinking,omitempty"`
	Reasoning         *ReasoningConfig        `json:"reasoning,omitempty" yaml:"reasoning,omitempty"`
	Text              *TextConfig             `json:"text,omitempty" yaml:"text,omitempty"`
	ParallelToolCalls *bool                   `json:"parallel_tool_calls,omitempty" yaml:"parallel_tool_calls,omitempty"` // nil = omit from request; non-nil = send explicit Responses API hint
	PromptCache       *PromptCacheConfig      `json:"prompt_cache,omitempty" yaml:"prompt_cache,omitempty"`
	Compat            *ModelCompatConfig      `json:"compat,omitempty" yaml:"compat,omitempty"`
	Cost              *ModelCost              `json:"cost,omitempty" yaml:"cost,omitempty"`
	Store             *bool                   `json:"store,omitempty" yaml:"store,omitempty"` // model-level override for server-side storage; takes priority over provider-level Store
	Variants          map[string]ModelVariant `json:"variants,omitempty" yaml:"variants,omitempty"`
}

// EffectiveResponsesWebsocket returns whether Responses WebSocket should be used.
// Provider override has highest priority; otherwise preset:codex defaults to enabled.
func EffectiveResponsesWebsocket(preset string, providerValue *bool) bool {
	if providerValue != nil {
		return *providerValue
	}
	return strings.EqualFold(strings.TrimSpace(preset), ProviderPresetCodex)
}

// EffectiveStore returns whether server-side storage should be enabled.
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

// SupportsInput reports whether the model accepts the given input modality.
// When Modalities is nil, defaults to ["text", "image"] for backward compatibility.
func (m *ModelConfig) SupportsInput(modality string) bool {
	if m.Modalities == nil {
		return modality == "text" || modality == "image"
	}
	for _, v := range m.Modalities.Input {
		if v == modality {
			return true
		}
	}
	return false
}

// ModelCompatConfig contains provider/model-specific compatibility toggles.
// All options are opt-in and default to disabled unless explicitly enabled.
type ModelCompatConfig struct {
	ThinkingToolcall *ThinkingToolcallCompatConfig `json:"thinking_toolcall,omitempty" yaml:"thinking_toolcall,omitempty"`
}

// ProviderCompatConfig contains provider-level compatibility toggles. Model
// behavior compat belongs under ModelCompatConfig; transport-layer compat lives
// here so the schema reflects the runtime semantics.
type ProviderCompatConfig struct {
	ThinkingToolcall   *ThinkingToolcallCompatConfig   `json:"thinking_toolcall,omitempty" yaml:"thinking_toolcall,omitempty"`
	AnthropicTransport *AnthropicTransportCompatConfig `json:"anthropic_transport,omitempty" yaml:"anthropic_transport,omitempty"`
}

// AnthropicTransportCompatConfig holds Anthropic transport/request tweaks for
// third-party proxies that expect Claude Code-like request metadata.
type AnthropicTransportCompatConfig struct {
	SystemPrefix   string   `json:"system_prefix,omitempty" yaml:"system_prefix,omitempty"`
	ExtraBeta      []string `json:"extra_beta,omitempty" yaml:"extra_beta,omitempty"`
	UserAgent      string   `json:"user_agent,omitempty" yaml:"user_agent,omitempty"`
	MetadataUserID bool     `json:"metadata_user_id,omitempty" yaml:"metadata_user_id,omitempty"`
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

// ModelLimit defines context and output token limits.
type ModelLimit struct {
	Context int `json:"context" yaml:"context"`
	Output  int `json:"output" yaml:"output"`
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
	Type    string `json:"type,omitempty" yaml:"type,omitempty"`
	Budget  int    `json:"budget,omitempty" yaml:"budget,omitempty"`
	Effort  string `json:"effort,omitempty" yaml:"effort,omitempty"`
	Display string `json:"display,omitempty" yaml:"display,omitempty"`
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
	ParallelToolCalls *bool              `json:"parallel_tool_calls,omitempty" yaml:"parallel_tool_calls,omitempty"` // nil = inherit model default/omit; non-nil = override request hint
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

// ModelCost records per-token pricing for cost tracking (USD per 1M tokens).
type ModelCost struct {
	Input      float64 `json:"input" yaml:"input"`
	Output     float64 `json:"output" yaml:"output"`
	CacheRead  float64 `json:"cache_read" yaml:"cache_read"`
	CacheWrite float64 `json:"cache_write" yaml:"cache_write"`
}

// ContextConfig controls automatic context compression behavior.
type ContextConfig struct {
	AutoCompact      bool             `json:"auto_compact" yaml:"auto_compact"`
	CompactThreshold float64          `json:"compact_threshold" yaml:"compact_threshold"`
	CompactModel     string           `json:"compact_model,omitempty" yaml:"compact_model,omitempty"`
	Compaction       CompactionConfig `json:"compaction,omitempty" yaml:"compaction,omitempty"`
}

// CompactionConfig controls durable compaction backend and output profile.
type CompactionConfig struct {
	Preset  string `json:"preset,omitempty" yaml:"preset,omitempty"`
	Profile string `json:"profile,omitempty" yaml:"profile,omitempty"`
}

// DefaultConfig returns a Config with hardcoded defaults.
func DefaultConfig() *Config {
	return &Config{
		Context: ContextConfig{
			AutoCompact:      true,
			CompactThreshold: 0.8,
			Compaction: CompactionConfig{
				Profile: CompactionProfileAuto,
			},
		},
		Maintenance: MaintenanceConfig{SizeCheckOnStartup: false, SizeCheckIntervalHours: 24, WarnStateBytes: 10 * 1024 * 1024 * 1024, WarnCacheBytes: 5 * 1024 * 1024 * 1024},
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

// LoadConfigFromPath loads configuration from an arbitrary YAML file path.
// This supports both global (<config-home>/config.yaml) and project-level
// (.chord/config.yaml) configs, which share the same format.
// The config is initialised with DefaultConfig() values before unmarshalling,
// so omitted YAML fields retain their defaults (e.g. compact_threshold: 0.8).
func LoadConfigFromPath(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}

	cfg := DefaultConfig()
	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}

	return cfg, nil
}
