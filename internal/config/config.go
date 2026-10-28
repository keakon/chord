package config

import (
	"gopkg.in/yaml.v3"
)

// Config is the top-level configuration for the chord agent.
type Config struct {
	InputHistory    InputHistoryConfig        `json:"input_history" yaml:"input_history,omitempty"`
	ImageGeneration ImageGenerationConfig     `json:"image_generation" yaml:"image_generation,omitempty"`
	Providers       map[string]ProviderConfig `json:"providers" yaml:"providers"`                         // LLM providers
	ModelPools      map[string][]string       `json:"model_pools,omitempty" yaml:"model_pools,omitempty"` // reusable model pool definitions
	// ModelTemplates is a pure YAML anchor namespace: entries are only
	// reachable through anchors/aliases elsewhere in the document and are never
	// interpreted directly. Declared so strict decoding accepts the key.
	ModelTemplates            map[string]yaml.Node `json:"-" yaml:"model_templates,omitempty"`
	Orchestration             OrchestrationConfig  `json:"orchestration" yaml:"orchestration,omitempty"` // multi-agent runtime resource limits
	Context                   ContextConfig        `json:"context" yaml:"context"`                       // context compression settings
	Skills                    SkillsConfig         `json:"skills" yaml:"skills"`                         // additional skill paths
	ConfirmTimeout            int                  `json:"confirm_timeout" yaml:"confirm_timeout"`       // confirmation timeout in seconds (0 = infinite, default)
	QuestionAutoSelectTimeout int                  `json:"question_auto_select_timeout" yaml:"question_auto_select_timeout"`
	QuestionTimeout           int                  `json:"question_timeout" yaml:"question_timeout"` // optional-question timeout in seconds (0 = wait indefinitely)
	Diff                      DiffConfig           `json:"diff" yaml:"diff"`                         // TUI diff rendering options
	// DesktopNotification, when true, enables terminal notifications in local TUI.
	// Chord auto-selects the terminal OSC protocol by environment (for example, OSC 777 vs OSC 9).
	// YAML: desktop_notification: true
	DesktopNotification *bool `json:"desktop_notification,omitempty" yaml:"desktop_notification,omitempty"`
	// DesktopNotificationForeground controls notifications while the TUI is focused.
	// Nil defaults to true. YAML: desktop_notification_foreground: true
	DesktopNotificationForeground *bool `json:"desktop_notification_foreground,omitempty" yaml:"desktop_notification_foreground,omitempty"`
	// PreventSleep requests host sleep prevention during agent work on macOS, Linux, and Windows.
	// Defaults to false; applies to TUI, headless, and ACP. Platform services and policies limit coverage. YAML: prevent_sleep: true
	PreventSleep        *bool                       `json:"prevent_sleep,omitempty" yaml:"prevent_sleep,omitempty"`
	KeyMap              KeyMapConfig                `json:"keymap,omitempty" yaml:"keymap,omitempty"`                             // custom key bindings (snake_case action → key list)
	Commands            map[string]string           `json:"commands,omitempty" yaml:"commands,omitempty"`                         // custom slash commands: "/cmd" → text to send as message
	IMESwitchTarget     string                      `json:"ime_switch_target,omitempty" yaml:"ime_switch_target,omitempty"`       // English IM key (e.g. com.apple.keylayout.ABC); switch/restore use im-select or im-select.exe by platform
	LogLevel            string                      `json:"log_level" yaml:"log_level"`                                           // log verbosity: "debug", "info" (default), "warn", "error"
	Paths               PathsConfig                 `json:"paths" yaml:"paths,omitempty"`                                         // user-level state/cache/logs path overrides
	Maintenance         MaintenanceConfig           `json:"maintenance" yaml:"maintenance,omitempty"`                             // optional cleanup/status checks
	LSP                 LSPConfig                   `json:"lsp" yaml:"lsp"`                                                       // LSP server config
	Diagnostics         DiagnosticsConfig           `json:"diagnostics" yaml:"diagnostics,omitempty"`                             // post-tool diagnostics config
	MCP                 MCPConfig                   `json:"mcp,omitempty" yaml:"mcp,omitempty"`                                   // MCP server configs
	Hooks               HookConfig                  `json:"hooks" yaml:"hooks,omitempty"`                                         // lifecycle hook configs
	MaxOutputTokens     int                         `json:"max_output_tokens" yaml:"max_output_tokens"`                           // global output token cap (0 = use DefaultOutputTokenMax)
	StreamRetryRounds   int                         `json:"stream_retry_rounds" yaml:"stream_retry_rounds"`                       // hard cap on public LLM retry rounds (0 = keep retrying until success/cancel)
	Proxy               string                      `json:"proxy,omitempty" yaml:"proxy,omitempty"`                               // global proxy URL (http/https/socks5), empty = no proxy
	Memory              MemoryConfig                `json:"memory" yaml:"memory,omitempty"`                                       // project memory auto-extraction
	WebFetch            WebFetchConfig              `json:"web_fetch" yaml:"web_fetch,omitempty"`                                 // WebFetch-specific options
	HostedTools         map[string]HostedToolConfig `json:"hosted_tools,omitempty" yaml:"hosted_tools,omitempty"`                 // provider-side (hosted) tool catalog: local tool surface + per-family wire declarations
	Worktree            WorktreeConfig              `json:"worktree" yaml:"worktree,omitempty"`                                   // git worktree integration options
	ThinkingTranslation *ThinkingTranslationConfig  `json:"thinking_translation,omitempty" yaml:"thinking_translation,omitempty"` // optional thinking translation enhancement
	// Emulation defines payloads selected by provider/model emulate keys.
	Emulation EmulationConfig `json:"emulation,omitzero" yaml:"emulation,omitempty"`
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

// MemoryConfig controls automatic project memory extraction. Reading an
// existing MEMORY.md is always automatic and does not require this setting;
// the key only gates whether Chord sends frozen history sessions to the model
// to grow memory records. Project .chord/config.yaml can override the
// user-level value like other config.
type MemoryConfig struct {
	// Enabled enables automatic extraction for this machine + project.
	// Nil means unset (defaults to false).
	Enabled *bool `json:"enabled,omitempty" yaml:"enabled,omitempty"`
	// ModelPool is the name of the model pool to use for extraction requests.
	// When set, extraction uses this dedicated pool instead of the main model
	// pool, which avoids inheriting aggressive reasoning settings (e.g. effort:max)
	// that can cause output truncation. The pool must be defined in model_pools.
	// When unset, extraction uses the main model pool, preserving each model's
	// configured reasoning settings.
	ModelPool string `json:"model_pool,omitempty" yaml:"model_pool,omitempty"`
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

// DefaultConfig returns a Config with hardcoded defaults.
func DefaultConfig() *Config {
	return &Config{
		Context: ContextConfig{
			Reduction: ContextReductionConfig{
				ConfirmAgeTurns:         2,
				ErrorAgeTurns:           3,
				HighRiskProtectAgeTurns: 4,
				DiffProtectAgeTurns:     12,
				ShellSuccessAgeTurns:    2,
				ShellSuccessBytes:       3000,
				ShellReadOnlyAgeTurns:   3,
				ReadLikeAgeTurns:        2,
				ReadLikeOutputBytes:     3000,
				StaleAgeTurns:           3,
				StaleOutputBytes:        1500,
				WrapUpGraceRequests:     1,
				MinToolResultsPrune:     6,
				MinIncrementalTokens:    2048,
				mode:                    contextReductionEnabled,
			},
			Compaction: CompactionConfig{
				Threshold:   DefaultContextCompactUsage,
				Profile:     CompactionProfileAuto,
				ModelDriven: new(true),
			},
		},
		Diagnostics: DefaultDiagnosticsConfig(),
		Maintenance: MaintenanceConfig{SizeCheckOnStartup: false, WarnStateBytes: 10 * 1024 * 1024 * 1024, WarnCacheBytes: 5 * 1024 * 1024 * 1024},
	}
}
