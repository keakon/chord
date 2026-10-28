package config

import (
	"fmt"
	"strings"
)

// Provider type constants
const (
	ProviderTypeChatCompletions = "chat-completions"
	ProviderTypeMessages        = "messages"
	ProviderTypeResponses       = "responses"
	ProviderTypeGenerateContent = "generate-content"
)

const (
	AuthSchemeAnthropicAPIKey = "anthropic-api-key"
	AuthSchemeBearer          = "bearer"
	AuthSchemeAPIKey          = "api-key"
)

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
	// RetryBackoff modes for ProviderConfig.
	RetryBackoffExponential = "exponential" // default: exponential doubling, base = retry_delay_ms (default 1s), cap 60s
	RetryBackoffFixed       = "fixed"       // fixed delay per round = retry_delay_ms (default 1s), max 60s
	RetryBackoffNone        = "none"        // no generated round or ordinary-429 delay; hard key state still applies

	DefaultProviderRetryDelayMS = 1000
	MaxProviderRetryDelayMS     = 60_000

	// retry_after_max_s bounds. The header is always honored as the key
	// cooldown; this only caps how long a single hint may block a key.
	DefaultRetryAfterMaxS  = 60
	OfficialRetryAfterMaxS = 86_400 // preset codex default
	MaxRetryAfterMaxS      = 86_400

	OAuthProfileOpenAICodex       = "openai_codex"
	ProviderPresetCodex           = "codex"
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
	Preset                    string                 `json:"preset,omitempty" yaml:"preset,omitempty"`                                           // e.g. "codex" for official ChatGPT/Codex OAuth
	Store                     *bool                  `json:"store,omitempty" yaml:"store,omitempty"`                                             // provider-level Responses storage preference; nil defaults to false
	ResponsesWebsocket        *bool                  `json:"responses_websocket,omitempty" yaml:"responses_websocket,omitempty"`                 // whether to prefer Responses WebSocket transport; nil = preset default (codex:true, others:false)
	ResponseHeaderTimeout     int                    `json:"response_header_timeout,omitempty" yaml:"response_header_timeout,omitempty"`         // timeout from starting an HTTP request until response headers arrive, including request-body upload (seconds; 0 = built-in default)
	StreamIdleTimeout         int                    `json:"stream_idle_timeout,omitempty" yaml:"stream_idle_timeout,omitempty"`                 // per-stream idle timeout in seconds (0 = built-in defaults)
	StreamTotalTimeout        int                    `json:"stream_total_timeout,omitempty" yaml:"stream_total_timeout,omitempty"`               // wall-clock cap for one stream in seconds, counted from when the response body starts (0 = no cap; a stream that keeps producing data is never cut off by time alone)
	WebSocketHandshakeTimeout int                    `json:"websocket_handshake_timeout,omitempty" yaml:"websocket_handshake_timeout,omitempty"` // Responses WebSocket handshake timeout in seconds (0 = built-in default)
	RateLimit                 int                    `json:"rate_limit" yaml:"rate_limit"`                                                       // requests per minute (0 = no limit)
	UserAgent                 string                 `json:"user_agent,omitempty" yaml:"user_agent,omitempty"`                                   // optional User-Agent override for provider/model HTTP requests
	AuthScheme                string                 `json:"auth_scheme,omitempty" yaml:"auth_scheme,omitempty"`                                 // optional auth scheme override for request header selection
	Proxy                     *string                `json:"proxy,omitempty" yaml:"proxy,omitempty"`                                             // per-provider proxy URL; nil = inherit global, non-nil (incl. "") = override
	Compat                    *ProviderCompatConfig  `json:"compat,omitempty" yaml:"compat,omitempty"`                                           // provider-level compat defaults (model-level can override model compat only)
	TrustHTTP400              *bool                  `json:"trust_http_400,omitempty" yaml:"trust_http_400,omitempty"`                           // whether to treat HTTP 400 as a terminal request error; nil = official preset (codex) true, others false
	RetryAfterMaxS            *int                   `json:"retry_after_max_s,omitempty" yaml:"retry_after_max_s,omitempty"`                     // longest Retry-After wait honored, in seconds (1-86400); nil = official preset (codex) 86400, others 60
	SupportedServiceTiers     []ServiceTier          `json:"supported_service_tiers,omitempty" yaml:"supported_service_tiers,omitempty"`         // provider-level default non-standard tiers; model-level can override
	ParallelToolCalls         *bool                  `json:"parallel_tool_calls,omitempty" yaml:"parallel_tool_calls,omitempty"`                 // provider-level default; nil = send parallel_tool_calls: true; model/variant can override
	Models                    map[string]ModelConfig `json:"models" yaml:"models"`
	KeyRotation               string                 `json:"key_rotation" yaml:"key_rotation"`                         // "on_failure" (default) | "per_request"
	KeyOrder                  string                 `json:"key_order" yaml:"key_order"`                               // "sequential" (default, non-Codex) | "random" | "smart" (default for preset: codex)
	RetryBackoff              string                 `json:"retry_backoff,omitempty" yaml:"retry_backoff,omitempty"`   // "exponential" (default) | "fixed" | "none"
	RetryDelayMS              *int                   `json:"retry_delay_ms,omitempty" yaml:"retry_delay_ms,omitempty"` // millisecond delay for retry backoff; nil = omitted, 0 = explicit default
	Compress                  string                 `json:"compress,omitempty" yaml:"compress,omitempty"`             // upstream request body compression encoding: "" (endpoint default) | "off" | "gzip" | "zstd"
	// Emulate selects the client this provider presents as on the wire, for
	// example "codex" or "claude"; empty keeps Chord's own request identity.
	// The value references a payload defined under the top-level emulation
	// block, and only a provider whose wire protocol the client speaks acts on
	// it; a model-level emulate overrides it per model.
	Emulate string `json:"emulate,omitempty" yaml:"emulate,omitempty"`
	// Emulation carries the top-level emulation payload definitions so a
	// request can resolve its emulate selector without reaching back into the
	// global config. Attached by the loader; not a provider YAML key.
	Emulation *EmulationConfig `json:"-" yaml:"-"`
}

// Request compression encodings for ProviderConfig.Compress. An empty string
// leaves the choice to the provider preset (codex sends zstd, every other
// endpoint sends the body uncompressed); "off" disables request compression
// explicitly so a preset default cannot re-enable it.
const (
	RequestCompressionOff  = "off"
	RequestCompressionGzip = "gzip"
	RequestCompressionZstd = "zstd"
)

// EffectiveResponsesWebsocket returns whether Responses WebSocket should be used.
// Provider override has highest priority; otherwise preset:codex defaults to enabled.
func EffectiveResponsesWebsocket(preset string, providerValue *bool) bool {
	if providerValue != nil {
		return *providerValue
	}
	return strings.EqualFold(strings.TrimSpace(preset), ProviderPresetCodex)
}

// EffectiveRequestCompression resolves the request body compression encoding to
// send: the returned value is an encoding ("gzip" | "zstd") or "" for
// uncompressed. An explicit setting wins, "off" disables compression even where
// a preset would otherwise enable it, and an unset value follows the preset —
// the codex backend is the one endpoint verified to accept zstd bodies, which
// is what the Codex client sends there.
func EffectiveRequestCompression(preset, configured string) string {
	configured = strings.TrimSpace(configured)
	if configured == RequestCompressionOff {
		return ""
	}
	if configured == "" && strings.EqualFold(strings.TrimSpace(preset), ProviderPresetCodex) {
		return RequestCompressionZstd
	}
	return configured
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

// collectProviderIssues returns the retry, compression, key-selection,
// native-thinking, and reasoning-continuity problems for one provider config.
func collectProviderIssues(providerName string, cfg *ProviderConfig) []string {
	var issues []string
	if err := ValidateProviderGenerateContentCompat(providerName, *cfg); err != nil {
		issues = append(issues, err.Error())
	}
	if err := ValidateProviderRetry(providerName, *cfg); err != nil {
		issues = append(issues, err.Error())
	}
	if err := ValidateProviderCompression(providerName, *cfg); err != nil {
		issues = append(issues, err.Error())
	}
	if err := ValidateProviderKeySelection(providerName, *cfg); err != nil {
		issues = append(issues, err.Error())
	}
	if err := ValidateProviderNativeThinking(providerName, *cfg); err != nil {
		issues = append(issues, err.Error())
	}
	if err := ValidateProviderReasoningContinuity(providerName, *cfg); err != nil {
		issues = append(issues, err.Error())
	}
	return issues
}

// resetInvalidProviderFields clears provider fields that failed semantic
// validation, restoring their unset (default) state so the rest of the config
// still applies.
func resetInvalidProviderFields(cfg ProviderConfig) ProviderConfig {
	if !validRetryBackoff(cfg.RetryBackoff) {
		cfg.RetryBackoff = ""
	}
	if cfg.RetryDelayMS != nil && !validRetryDelayMS(*cfg.RetryDelayMS) {
		cfg.RetryDelayMS = nil
	}
	if cfg.RetryAfterMaxS != nil && !validRetryAfterMaxS(*cfg.RetryAfterMaxS) {
		cfg.RetryAfterMaxS = nil
	}
	if !validKeyRotation(cfg.KeyRotation) {
		cfg.KeyRotation = ""
	}
	if !validKeyOrder(cfg.KeyOrder, cfg.Preset) {
		cfg.KeyOrder = ""
	}
	if !validRequestCompression(cfg.Compress) {
		cfg.Compress = ""
	}
	return resetInvalidGenerateContentCompat(resetInvalidReasoningContinuity(resetInvalidNativeThinking(cfg)))
}
