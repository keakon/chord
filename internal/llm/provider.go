package llm

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"time"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/ratelimit"

	"golang.org/x/time/rate"
)

// AnthropicTuning holds Anthropic-specific request tuning parameters.
type AnthropicTuning struct {
	ThinkingType    string // ""|"enabled"|"adaptive"|"disabled"
	ThinkingBudget  int    // token budget for manual mode
	ThinkingEffort  string // ""|"low"|"medium"|"high"|"max"
	ThinkingDisplay string // ""|"summarized"|"omitted"
	PromptCacheMode string // ""|"off"|"auto"|"explicit"
	PromptCacheTTL  string // ""|"1h"
	CacheTools      bool
	CacheBoundary   AnthropicCacheBoundary
	ServiceTier     string // ""|"fast" (Anthropic first-party service tier)
	ToolChoice      string // ""|"auto"|"required" (required maps to Anthropic any)
	Temperature     *float64
}

// AnthropicCacheBoundary carries request-local prompt-cache placement hints.
// MessageIndex is zero-based in the original message list supplied to the
// provider and marks the stable reduced prefix's last source message. Providers
// that normalize or merge messages resolve it against their final wire shape.
// It is intentionally transient tuning, not persisted conversation state.
type AnthropicCacheBoundary struct {
	MessageIndex int
	BlockIndex   int
	Valid        bool
}

// OpenAITuning holds OpenAI-specific request tuning parameters.
type OpenAITuning struct {
	ServiceTier       string // ""|"fast"|"flex"|"priority" ("" = omit; OpenAI Responses only)
	ReasoningEffort   string // "low"|"medium"|"high"|"xhigh" ("" = disabled)
	ReasoningSummary  string // "auto"|"concise"|"detailed" ("" = disabled)
	TextVerbosity     string // "low"|"medium"|"high" ("" = disabled)
	ParallelToolCalls *bool  // nil = Responses request default false; non-nil = explicit override
	ToolChoice        string // ""|"auto"|"required"
}

// GeminiTuning holds Gemini-specific request tuning parameters.
type GeminiTuning struct {
	ThinkingBudget  *int   // nil = omit; -1 dynamic; 0 disable (model-dependent); >0 fixed budget
	ThinkingLevel   string // ""|"low"|"medium"|"high" (Gemini 3+)
	IncludeThoughts *bool  // nil = omit; true/false explicit includeThoughts
	ToolChoice      string // ""|"auto"|"required"|"none" (required maps to Gemini ANY)
}

// RequestTuning bundles all provider-specific tuning parameters for a single
// LLM request. Each provider reads only its own sub-struct.
type RequestTuning struct {
	Anthropic             AnthropicTuning
	OpenAI                OpenAITuning
	Gemini                GeminiTuning
	SupportedServiceTiers map[config.ServiceTier]bool
}

// Provider is the interface that all LLM provider implementations must satisfy.
type Provider interface {
	CompleteStream(
		ctx context.Context,
		apiKey string,
		model string,
		systemPrompt string,
		messages []message.Message,
		tools []message.ToolDefinition,
		maxTokens int,
		tuning RequestTuning,
		cb StreamCallback,
	) (*message.Response, error)
}

// KeyState tracks the state of a single API key for cooldown and load balancing.
type KeyState struct {
	Key               string
	LastUsed          time.Time
	CooldownEnd       time.Time
	CooldownCount     int                             // consecutive cooldowns for exponential backoff
	OAuthInfo         *OAuthKeyInfo                   // nil if not OAuth
	RateLimit         *ratelimit.KeyRateLimitSnapshot // latest rate-limit snapshot (nil = no data yet)
	Recovering        bool                            // true until the key proves healthy again via visible output after a failure/cooldown
	ExhaustedUntil    time.Time                       // confirmed quota exhaustion (e.g. Codex OAuth) until real reset
	Invalid           bool                            // permanently unusable (OAuth account deactivated or refresh token expired)
	EverSelected      bool                            // true once this slot has been selected in the current process
	SoftCooldownUntil time.Time                       // persisted Codex soft hint: latest known future reset across windows
}

// OAuthKeySetup mirrors auth.yaml OAuth credential state needed to initialize a key slot.
type OAuthKeySetup struct {
	CredentialIndex       int
	AccountUserID         string
	AccountID             string
	Email                 string
	Access                string
	RefreshSHA256         string
	Expires               int64
	Status                config.OAuthCredentialStatus
	CodexPrimaryResetAt   int64
	CodexSecondaryResetAt int64
	StateUpdatedAt        int64
	LastWarmupAt          int64
	RateLimit             *ratelimit.KeyRateLimitSnapshot
}

// OAuthKeyInfo holds OAuth-specific metadata for a key.
type OAuthKeyInfo struct {
	Expires               int64 // millisecond-precision Unix timestamp
	CredentialIndex       int   // index in auth[providerName]
	AccountUserID         string
	AccountID             string
	Email                 string
	Access                string
	RefreshSHA256         string
	Status                config.OAuthCredentialStatus
	CodexPrimaryResetAt   int64
	CodexSecondaryResetAt int64
	StateUpdatedAt        int64
	LastWarmupAt          int64
}

// ProviderConfig holds provider-level configuration.
//
// Codex OAuth rate-limit snapshots:
//   - KeyState.RateLimit stores key-scoped inline snapshots (x-codex-* headers / WS frames).
//   - polledRateLimitByCredIdx stores account-scoped /wham/usage snapshots keyed by OAuth credential index.
//     We only fetch usage for the currently selected OAuth credential (codex-rs style on-demand), and
//     we debounce refreshes to avoid noisy background polling.
type ProviderConfig struct {
	mu                         sync.Mutex
	name                       string
	typeName                   string
	apiURL                     string
	oauthProfile               string
	authScheme                 string // resolved request auth scheme; set once at construction, then read-only
	keyStates                  []*KeyState
	limiter                    *rate.Limiter // optional rate limiter (nil = no rate limiting)
	models                     map[string]config.ModelConfig
	compat                     *config.ProviderCompatConfig // provider-level compat defaults
	store                      *bool                        // provider-level Responses storage preference; nil defaults to false
	officialAPI                *bool                        // nil = infer from known official endpoints
	supportedServiceTiers      []config.ServiceTier         // provider-level default non-standard service tiers
	preset                     string                       // trimmed config preset (e.g. "codex")
	responsesWebsocket         *bool                        // provider-level Responses WebSocket preference; nil = preset default
	responseHeaderTimeout      time.Duration                // provider HTTP response-header timeout; 0 means built-in default
	streamIdleTimeout          time.Duration                // provider-level stream idle timeout; 0 means parser defaults
	websocketHandshakeTimeout  time.Duration                // provider-level Responses WebSocket handshake timeout; 0 means default
	keyRotation                string                       // "on_failure" (default) | "per_request"
	keyOrder                   string                       // "sequential" (default, non-Codex) | "random" | "smart" (Codex)
	retryDelayBase             time.Duration                // test hook; <0 disables retry backoff
	stickyIdx                  int                          // index of the currently pinned key (on_failure rotation)
	oauthRefresher             *OAuthRefresher              // nil if no OAuth support
	lastSelectedKey            string                       // last key returned by SelectKeyWithContext (for inline snapshot selection)
	lastSelectedSlot           int                          // last credential slot returned by SelectKeyWithContext (for switch detection)
	polledRateLimitByCredIdx   map[int]*ratelimit.KeyRateLimitSnapshot
	polledRateLimitAttemptedAt map[int]time.Time
	polledRateLimitSucceededAt map[int]time.Time
	polledRateLimitInFlight    map[int]bool
	inlineDisplaySnap          *ratelimit.KeyRateLimitSnapshot
	codexPollFetchFn           func(string, string) ([]*ratelimit.KeyRateLimitSnapshot, error)
	onPolledUpdate             func() // called after polled snapshot writes a new snapshot
	effectiveProxyURL          string
	userAgent                  string
	compress                   bool // whether gzip request compression is enabled
	authStatePath              string
	authState                  config.AuthStateFile
	authStateMTime             time.Time
	authStateSize              int64
	authStateDigest            string
	authStateMonitor           *authStateMonitor
}

// NewProviderConfig creates a new ProviderConfig.
func NewProviderConfig(name string, cfg config.ProviderConfig, keys []string) *ProviderConfig {
	apiURL := cfg.APIURL
	if apiURL == "" {
		// Default based on type/profile.
		switch {
		case cfg.Type == config.ProviderTypeResponses && cfg.Preset == config.ProviderPresetCodex:
			apiURL = config.OpenAICodexResponsesURL
		}
	}

	models := cfg.Models
	if models == nil {
		models = make(map[string]config.ModelConfig)
	}

	// Initialize key states from the provided key list.
	keyStates := make([]*KeyState, len(keys))
	for i, k := range keys {
		keyStates[i] = &KeyState{Key: k}
	}

	keyRotation := cfg.KeyRotation
	if keyRotation != config.KeyRotationPerRequest {
		keyRotation = config.KeyRotationOnFailure
	}
	keyOrder := cfg.KeyOrder
	if keyOrder != config.KeyOrderSequential && keyOrder != config.KeyOrderRandom && keyOrder != config.KeyOrderSmart {
		if cfg.Preset == config.ProviderPresetCodex {
			keyOrder = config.KeyOrderSmart
		} else {
			keyOrder = config.KeyOrderSequential
		}
	}

	// Initialize stickyIdx: for random order, pick a random starting key.
	stickyIdx := 0
	if keyOrder == config.KeyOrderRandom && len(keyStates) > 0 {
		stickyIdx = rand.Intn(len(keyStates))
	}

	var oauthProfile string
	if cfg.Preset == config.ProviderPresetCodex {
		oauthProfile = config.OAuthProfileOpenAICodex
	}

	polledRateLimitByCredIdx := make(map[int]*ratelimit.KeyRateLimitSnapshot)
	polledRateLimitAttemptedAt := make(map[int]time.Time)
	polledRateLimitSucceededAt := make(map[int]time.Time)
	polledRateLimitInFlight := make(map[int]bool)

	return &ProviderConfig{
		name:                       name,
		typeName:                   cfg.Type,
		apiURL:                     apiURL,
		authScheme:                 config.EffectiveAuthScheme(cfg.Preset, cfg.Type, apiURL, cfg.AuthScheme),
		oauthProfile:               oauthProfile,
		keyStates:                  keyStates,
		models:                     models,
		compat:                     cfg.Compat,
		store:                      cfg.Store,
		officialAPI:                cfg.OfficialAPI,
		supportedServiceTiers:      append([]config.ServiceTier(nil), cfg.SupportedServiceTiers...),
		preset:                     strings.TrimSpace(cfg.Preset),
		responsesWebsocket:         cfg.ResponsesWebsocket,
		responseHeaderTimeout:      durationFromPositiveSecondsClamped(int64(cfg.ResponseHeaderTimeout), 0),
		streamIdleTimeout:          durationFromPositiveSecondsClamped(int64(cfg.StreamIdleTimeout), 0),
		websocketHandshakeTimeout:  durationFromPositiveSecondsClamped(int64(cfg.WebSocketHandshakeTimeout), 0),
		keyRotation:                keyRotation,
		keyOrder:                   keyOrder,
		stickyIdx:                  stickyIdx,
		lastSelectedSlot:           -1,
		effectiveProxyURL:          "",
		userAgent:                  strings.TrimSpace(cfg.UserAgent),
		compress:                   cfg.Compress,
		polledRateLimitByCredIdx:   polledRateLimitByCredIdx,
		polledRateLimitAttemptedAt: polledRateLimitAttemptedAt,
		polledRateLimitSucceededAt: polledRateLimitSucceededAt,
		polledRateLimitInFlight:    polledRateLimitInFlight,
	}
}

// NewProviderImpl creates a provider implementation using the provider's
// configured transport settings and the supplied effective proxy URL.
func NewProviderImpl(providerCfg *ProviderConfig, proxyURL string) (Provider, error) {
	if providerCfg == nil {
		return nil, fmt.Errorf("provider config is nil")
	}
	client, err := NewStreamingHTTPClientWithProxy(proxyURL, providerCfg.ResponseHeaderTimeout())
	if err != nil {
		return nil, fmt.Errorf("create HTTP client for %s provider: %w", providerCfg.Type(), err)
	}
	switch providerCfg.Type() {
	case config.ProviderTypeChatCompletions:
		return NewOpenAIProviderWithClient(providerCfg, client, proxyURL)
	case config.ProviderTypeResponses:
		return NewResponsesProviderWithClient(providerCfg, client, proxyURL)
	case config.ProviderTypeMessages:
		return NewAnthropicProviderWithClient(providerCfg, client, proxyURL)
	case config.ProviderTypeGenerateContent:
		return NewGeminiProviderWithClient(providerCfg, client, proxyURL)
	default:
		return nil, fmt.Errorf("unsupported provider type %q", providerCfg.Type())
	}
}

// StoreConfig returns the provider-level store setting (nil means not configured).
func (p *ProviderConfig) StoreConfig() *bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.store
}

// EffectiveResponsesWebsocket returns whether the Responses WebSocket transport should be attempted
// for this provider (preset default + provider override).
func (p *ProviderConfig) EffectiveResponsesWebsocket() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return config.EffectiveResponsesWebsocket(p.preset, p.responsesWebsocket)
}

// IsCodexOAuthTransport reports whether this provider uses the official ChatGPT/Codex OAuth wire profile.
func (p *ProviderConfig) IsCodexOAuthTransport() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.oauthProfile == config.OAuthProfileOpenAICodex
}

// IsAzureOpenAITransport reports whether this provider uses the Azure OpenAI Responses wire profile.
func (p *ProviderConfig) IsAzureOpenAITransport() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return strings.EqualFold(strings.TrimSpace(p.preset), config.ProviderPresetAzure)
}

// AuthScheme returns the resolved request auth scheme. The field is set once in
// NewProviderConfig and never mutated, so no lock is taken (see Name/Type/APIURL).
func (p *ProviderConfig) AuthScheme() string {
	return p.authScheme
}

func (p *ProviderConfig) Preset() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.preset
}

func (p *ProviderConfig) SupportedServiceTiers() []config.ServiceTier {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]config.ServiceTier(nil), p.supportedServiceTiers...)
}

// GetModel returns the ModelConfig for the given model ID.
func (p *ProviderConfig) GetModel(modelID string) (config.ModelConfig, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	m, ok := p.models[modelID]
	return m, ok
}

// ThinkingToolcallCompat resolves thinking-toolcall compatibility config for
// the given model. Provider-level compat acts as defaults, model-level compat
// overrides provider-level fields when present.
func (p *ProviderConfig) ThinkingToolcallCompat(modelID string) *config.ThinkingToolcallCompatConfig {
	p.mu.Lock()
	defer p.mu.Unlock()

	var providerCfg *config.ThinkingToolcallCompatConfig
	if p.compat != nil {
		providerCfg = p.compat.ThinkingToolcall
	}

	var modelCfg *config.ThinkingToolcallCompatConfig
	if m, ok := p.models[modelID]; ok && m.Compat != nil {
		modelCfg = m.Compat.ThinkingToolcall
	}

	if providerCfg == nil && modelCfg == nil {
		return nil
	}

	merged := &config.ThinkingToolcallCompatConfig{}

	if providerCfg != nil {
		merged.Enabled = providerCfg.Enabled
	}

	if modelCfg != nil {
		if modelCfg.Enabled != nil {
			merged.Enabled = modelCfg.Enabled
		}
	}

	return merged
}

// ReasoningContinuityCompat resolves reasoning-continuity compatibility config
// for the given model. Provider-level compat acts as defaults, model-level
// compat overrides provider-level fields when present.
func (p *ProviderConfig) ReasoningContinuityCompat(modelID string) *config.ReasoningContinuityCompatConfig {
	p.mu.Lock()
	defer p.mu.Unlock()

	var providerCfg *config.ReasoningContinuityCompatConfig
	if p.compat != nil {
		providerCfg = p.compat.ReasoningContinuity
	}

	var modelCfg *config.ReasoningContinuityCompatConfig
	if m, ok := p.models[modelID]; ok && m.Compat != nil {
		modelCfg = m.Compat.ReasoningContinuity
	}

	if providerCfg == nil && modelCfg == nil {
		return nil
	}

	merged := &config.ReasoningContinuityCompatConfig{}

	if providerCfg != nil {
		merged.Mode = providerCfg.Mode
	}

	if modelCfg != nil && strings.TrimSpace(modelCfg.Mode) != "" {
		merged.Mode = modelCfg.Mode
	}

	return merged
}

// GetRetryDelay returns the delay before the next retry round.
// Backoff is deterministic: 1s, 2s, 4s, 8s, 16s, 32s, then 60s for later rounds.
func (p *ProviderConfig) GetRetryDelay(attempt int) time.Duration {
	if attempt <= 0 {
		return 0
	}
	if p.retryDelayBase < 0 {
		return 0
	}
	baseDelay := time.Second
	if p.retryDelayBase > 0 {
		baseDelay = p.retryDelayBase
	}
	const maxRetryDelayAttempt = 6
	const maxRetryDelay = 60 * time.Second
	if attempt > maxRetryDelayAttempt {
		return maxRetryDelay
	}
	return saturatingDoublingDuration(baseDelay, maxRetryDelay, attempt-1)
}

// EffectiveProxyURL returns the effective proxy URL configured for this provider.
func (p *ProviderConfig) EffectiveProxyURL() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.effectiveProxyURL
}

// UserAgent returns the provider-level User-Agent override, or empty for the default.
func (p *ProviderConfig) UserAgent() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.userAgent
}

// CompressEnabled reports whether upstream request body compression is enabled.
func (p *ProviderConfig) CompressEnabled() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.compress
}

// SetCompressEnabled sets whether upstream request body compression is enabled.
func (p *ProviderConfig) SetCompressEnabled(enabled bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.compress = enabled
}

func (p *ProviderConfig) Name() string {
	return p.name
}

// Type returns the provider type.
func (p *ProviderConfig) Type() string {
	return p.typeName
}

// ResponseHeaderTimeout returns the initial HTTP response-header timeout configured for this provider.
func (p *ProviderConfig) ResponseHeaderTimeout() time.Duration {
	return p.responseHeaderTimeout
}

// StreamIdleTimeout returns the provider-level stream idle timeout override.
func (p *ProviderConfig) StreamIdleTimeout() time.Duration {
	return p.streamIdleTimeout
}

// WebSocketHandshakeTimeout returns the provider-level Responses WebSocket handshake timeout override.
func (p *ProviderConfig) WebSocketHandshakeTimeout() time.Duration {
	return p.websocketHandshakeTimeout
}

// APIURL returns the provider's complete API URL.
func (p *ProviderConfig) APIURL() string {
	return p.apiURL
}
