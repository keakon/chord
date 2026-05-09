package llm

import (
	"context"
	"fmt"
	"math/rand"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/keakon/golog/log"

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
}

// OpenAITuning holds OpenAI-specific request tuning parameters.
type OpenAITuning struct {
	ReasoningEffort   string // "low"|"medium"|"high"|"xhigh" ("" = disabled)
	ReasoningSummary  string // "auto"|"concise"|"detailed" ("" = disabled)
	TextVerbosity     string // "low"|"medium"|"high" ("" = disabled)
	ParallelToolCalls *bool  // nil = omit from request; non-nil = send explicit Responses API hint
}

// RequestTuning bundles all provider-specific tuning parameters for a single
// LLM request. Each provider reads only its own sub-struct.
type RequestTuning struct {
	Anthropic AnthropicTuning
	OpenAI    OpenAITuning
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
	AccountID             string
	Email                 string
	Expires               int64
	Status                config.OAuthCredentialStatus
	CodexPrimaryResetAt   int64
	CodexSecondaryResetAt int64
}

// OAuthKeyInfo holds OAuth-specific metadata for a key.
type OAuthKeyInfo struct {
	Expires               int64 // millisecond-precision Unix timestamp
	CredentialIndex       int   // index in auth[providerName]
	AccountID             string
	Email                 string
	CodexPrimaryResetAt   int64
	CodexSecondaryResetAt int64
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
	keyStates                  []*KeyState
	limiter                    *rate.Limiter // optional rate limiter (nil = no rate limiting)
	models                     map[string]config.ModelConfig
	compat                     *config.ProviderCompatConfig // provider-level compat defaults
	store                      *bool                        // provider-level store setting for Responses API
	preset                     string                       // trimmed config preset (e.g. "codex")
	responsesWebsocket         *bool                        // provider-level Responses WebSocket preference; nil = preset default
	keyRotation                string                       // "on_failure" (default) | "per_request"
	keyOrder                   string                       // "sequential" (default) | "random"
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
	compress                   bool // whether gzip request compression is enabled
}

// OAuthRefresher handles on-demand OAuth token refresh.
type OAuthRefresher struct {
	tokenURL       string
	clientID       string
	authConfigPath string
	authConfigMu   *sync.Mutex
	authConfig     *config.AuthConfig
	httpClient     *http.Client // used for token refresh requests; may use proxy
	providerName   string
}

func (r *OAuthRefresher) persistCredentialStatus(match config.OAuthCredentialMatch, status config.OAuthCredentialStatus) error {
	if r == nil || r.authConfig == nil || r.authConfigMu == nil {
		return nil
	}
	_, _, err := r.mutateCredential(match, func(cred *config.OAuthCredential) (bool, error) {
		if cred.Status == status {
			return false, nil
		}
		cred.Status = status
		return true, nil
	})
	if err != nil {
		return fmt.Errorf("persist oauth credential status: %w", err)
	}
	return nil
}

func (r *OAuthRefresher) persistCodexResetHints(match config.OAuthCredentialMatch, primaryResetAt, secondaryResetAt int64) (*config.OAuthCredential, bool, error) {
	if r == nil || r.authConfig == nil || r.authConfigMu == nil {
		return nil, false, nil
	}
	return r.mutateCredential(match, func(cred *config.OAuthCredential) (bool, error) {
		changed := false
		if cred.CodexPrimaryResetAt != primaryResetAt {
			cred.CodexPrimaryResetAt = primaryResetAt
			changed = true
		}
		if cred.CodexSecondaryResetAt != secondaryResetAt {
			cred.CodexSecondaryResetAt = secondaryResetAt
			changed = true
		}
		return changed, nil
	})
}

func (r *OAuthRefresher) mutateCredential(
	match config.OAuthCredentialMatch,
	mutate func(*config.OAuthCredential) (bool, error),
) (*config.OAuthCredential, bool, error) {
	if r == nil || r.authConfig == nil || r.authConfigMu == nil {
		return nil, false, nil
	}
	if mutate == nil {
		return nil, false, fmt.Errorf("oauth credential mutate func is nil")
	}
	if r.authConfigPath == "" {
		return r.mutateCredentialInMemory(match, mutate)
	}
	auth, updated, changed, err := config.UpdateOAuthCredentialInFile(r.authConfigPath, r.providerName, match, mutate)
	if err != nil {
		return nil, false, err
	}
	r.authConfigMu.Lock()
	*r.authConfig = auth
	r.authConfigMu.Unlock()
	return updated, changed, nil
}

func (r *OAuthRefresher) mutateCredentialInMemory(
	match config.OAuthCredentialMatch,
	mutate func(*config.OAuthCredential) (bool, error),
) (*config.OAuthCredential, bool, error) {
	r.authConfigMu.Lock()
	defer r.authConfigMu.Unlock()

	if match.AccountID == "" {
		return nil, false, fmt.Errorf("oauth credential account_id is required for provider %q", r.providerName)
	}
	creds := (*r.authConfig)[r.providerName]
	for i := range creds {
		if creds[i].OAuth == nil || creds[i].OAuth.AccountID != match.AccountID {
			continue
		}
		updated := *creds[i].OAuth
		changed, err := mutate(&updated)
		if err != nil {
			return nil, false, err
		}
		if !changed {
			return &updated, false, nil
		}
		creds[i].OAuth = &updated
		return &updated, true, nil
	}
	return nil, false, fmt.Errorf("oauth credential not found for provider %q", r.providerName)
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
	if keyRotation == "" {
		keyRotation = config.KeyRotationOnFailure
	}
	keyOrder := cfg.KeyOrder
	if keyOrder == "" {
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
		oauthProfile:               oauthProfile,
		keyStates:                  keyStates,
		models:                     models,
		compat:                     cfg.Compat,
		store:                      cfg.Store,
		preset:                     strings.TrimSpace(cfg.Preset),
		responsesWebsocket:         cfg.ResponsesWebsocket,
		keyRotation:                keyRotation,
		keyOrder:                   keyOrder,
		stickyIdx:                  stickyIdx,
		lastSelectedSlot:           -1,
		effectiveProxyURL:          "",
		compress:                   cfg.Compress,
		polledRateLimitByCredIdx:   polledRateLimitByCredIdx,
		polledRateLimitAttemptedAt: polledRateLimitAttemptedAt,
		polledRateLimitSucceededAt: polledRateLimitSucceededAt,
		polledRateLimitInFlight:    polledRateLimitInFlight,
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

func (p *ProviderConfig) keyStateBySlotLocked(slot int) *KeyState {
	if slot < 0 || slot >= len(p.keyStates) {
		return nil
	}
	return p.keyStates[slot]
}

func (p *ProviderConfig) keyStateByKeyLocked(key string) *KeyState {
	for _, ks := range p.keyStates {
		if ks.Key == key {
			return ks
		}
	}
	return nil
}

func (p *ProviderConfig) keyStateSelectableLocked(now time.Time, ks *KeyState) bool {
	if ks == nil {
		return false
	}
	return keyStateSelectable(now, ks)
}

func (p *ProviderConfig) keyStateHealthyLocked(now time.Time, ks *KeyState) bool {
	if !p.keyStateSelectableLocked(now, ks) {
		return false
	}
	return !ks.Recovering
}

func (p *ProviderConfig) markHealthyLocked(ks *KeyState) {
	if ks == nil {
		return
	}
	ks.Recovering = false
}

func (p *ProviderConfig) markRecoveringLocked(ks *KeyState) {
	if ks == nil {
		return
	}
	ks.Recovering = true
}

func (p *ProviderConfig) markCooldownLocked(ks *KeyState, d time.Duration) {
	if ks == nil {
		return
	}
	if d <= 0 {
		ks.CooldownCount = 0
		ks.CooldownEnd = time.Time{}
		return
	}
	ks.CooldownCount++
	ks.Recovering = true
	// Exponential backoff: d * 2^(count-1), capped at 1 minute.
	const maxCooldown = 1 * time.Minute
	effective := saturatingDoublingDuration(d, maxCooldown, ks.CooldownCount-1)
	ks.CooldownEnd = time.Now().Add(effective)
}

func (p *ProviderConfig) markQuotaExhaustedLocked(ks *KeyState, until time.Time) {
	if ks == nil {
		return
	}
	if until.After(ks.ExhaustedUntil) {
		ks.ExhaustedUntil = until
	}
	if until.After(ks.SoftCooldownUntil) {
		ks.SoftCooldownUntil = until
	}
	ks.CooldownEnd = time.Time{}
	ks.Recovering = true
}

func (p *ProviderConfig) markTemporaryUnavailableLocked(ks *KeyState, now time.Time, d time.Duration) {
	if ks == nil || d <= 0 {
		return
	}
	if ks.CooldownEnd.After(now) || ks.ExhaustedUntil.After(now) {
		return
	}
	ks.CooldownEnd = now.Add(d)
	ks.Recovering = true
}

func codexSoftCooldownUntilFromMillis(primaryResetAt, secondaryResetAt int64, now time.Time) time.Time {
	var until time.Time
	consider := func(ms int64) {
		if ms <= 0 {
			return
		}
		reset := time.UnixMilli(ms)
		if !reset.After(now) {
			return
		}
		if until.IsZero() || reset.After(until) {
			until = reset
		}
	}
	consider(primaryResetAt)
	consider(secondaryResetAt)
	return until
}

func codexWindowResetMillis(w *ratelimit.RateLimitWindow, now time.Time) int64 {
	if w == nil || w.ResetsAt.IsZero() || !w.ResetsAt.After(now) || w.UsedPercent() < 100 {
		return 0
	}
	return w.ResetsAt.UnixMilli()
}

func (p *ProviderConfig) codexSoftCooldownUntilLocked(now time.Time, ks *KeyState) time.Time {
	if ks == nil {
		return time.Time{}
	}
	if ks.SoftCooldownUntil.After(now) {
		return ks.SoftCooldownUntil
	}
	if ks.OAuthInfo != nil {
		until := codexSoftCooldownUntilFromMillis(ks.OAuthInfo.CodexPrimaryResetAt, ks.OAuthInfo.CodexSecondaryResetAt, now)
		ks.SoftCooldownUntil = until
		return until
	}
	ks.SoftCooldownUntil = time.Time{}
	return time.Time{}
}

func (p *ProviderConfig) codexSnapshotForKeyStateLocked(ks *KeyState) *ratelimit.KeyRateLimitSnapshot {
	if ks == nil {
		return nil
	}
	if ks.OAuthInfo != nil && p.polledRateLimitByCredIdx != nil {
		if snap := p.polledRateLimitByCredIdx[ks.OAuthInfo.CredentialIndex]; snap != nil {
			return snap
		}
	}
	return ks.RateLimit
}

func codexWindowRemainingPct(w *ratelimit.RateLimitWindow) (float64, bool) {
	if w == nil {
		return 0, false
	}
	used := w.UsedPercent()
	if used < 0 {
		return 0, false
	}
	remaining := 100 - used
	if remaining < 0 {
		remaining = 0
	}
	if remaining > 100 {
		remaining = 100
	}
	return remaining, true
}

func codexHeadroomScore(snap *ratelimit.KeyRateLimitSnapshot) (float64, bool) {
	if snap == nil {
		return 0, false
	}
	pRem, pOK := codexWindowRemainingPct(snap.Primary)
	sRem, sOK := codexWindowRemainingPct(snap.Secondary)
	switch {
	case pOK && sOK:
		if pRem < sRem {
			return pRem, true
		}
		return sRem, true
	case pOK:
		return pRem, true
	case sOK:
		return sRem, true
	default:
		return 0, false
	}
}

func codexCreditsPenalty(snap *ratelimit.KeyRateLimitSnapshot) bool {
	if snap == nil || snap.Credits == nil {
		return false
	}
	return !snap.Credits.Unlimited && !snap.Credits.HasCredits
}

func (p *ProviderConfig) codexSoftCooledLocked(now time.Time, ks *KeyState) bool {
	return p.codexSoftCooldownUntilLocked(now, ks).After(now)
}

func (p *ProviderConfig) codexSmartLessLocked(now time.Time, a, b *KeyState) bool {
	if a == nil {
		return false
	}
	if b == nil {
		return true
	}
	aSoft := p.codexSoftCooledLocked(now, a)
	bSoft := p.codexSoftCooledLocked(now, b)
	if aSoft != bSoft {
		return !aSoft
	}
	aNever := !a.EverSelected
	bNever := !b.EverSelected
	if aNever != bNever {
		return aNever
	}
	aSnap := p.codexSnapshotForKeyStateLocked(a)
	bSnap := p.codexSnapshotForKeyStateLocked(b)
	aPenalty := codexCreditsPenalty(aSnap)
	bPenalty := codexCreditsPenalty(bSnap)
	if aPenalty != bPenalty {
		return !aPenalty
	}
	aHeadroom, aKnown := codexHeadroomScore(aSnap)
	bHeadroom, bKnown := codexHeadroomScore(bSnap)
	if aKnown != bKnown {
		return aKnown
	}
	if aKnown && bKnown && aHeadroom != bHeadroom {
		return aHeadroom > bHeadroom
	}
	if aSoft && bSoft {
		aUntil := p.codexSoftCooldownUntilLocked(now, a)
		bUntil := p.codexSoftCooldownUntilLocked(now, b)
		if !aUntil.Equal(bUntil) {
			return aUntil.Before(bUntil)
		}
	}
	if !a.LastUsed.Equal(b.LastUsed) {
		return a.LastUsed.Before(b.LastUsed)
	}
	return false
}

func (p *ProviderConfig) bestCandidateIndexLocked(now time.Time, candidates []int) int {
	if len(candidates) == 0 {
		return -1
	}
	best := candidates[0]
	for _, idx := range candidates[1:] {
		if idx < 0 || idx >= len(p.keyStates) {
			continue
		}
		if p.keyOrder == config.KeyOrderSmart {
			if p.codexSmartLessLocked(now, p.keyStates[idx], p.keyStates[best]) {
				best = idx
			}
			continue
		}
		if p.keyStates[idx].LastUsed.Before(p.keyStates[best].LastUsed) {
			best = idx
		}
	}
	return best
}

func (p *ProviderConfig) postSelectLocked(selectedKS *KeyState, selectedIdx int, now time.Time) (string, bool) {
	if !selectedKS.ExhaustedUntil.IsZero() && !now.Before(selectedKS.ExhaustedUntil) {
		selectedKS.ExhaustedUntil = time.Time{}
		selectedKS.Recovering = true
	}
	if selectedKS.RateLimit != nil {
		p.inlineDisplaySnap = selectedKS.RateLimit
	}
	selectedKey := selectedKS.Key
	// Suppress the switched flag when only one key is selectable to avoid
	// spurious key_switched notifications. When other keys are cooling or
	// exhausted, the same key is repeatedly returned — that is a retry, not
	// a switch. Also suppress when a key was deactivated between selections
	// (e.g. compact ↔ main call interleaving that might leave lastSelectedSlot
	// out of sync).
	selectableSlots := 0
	for _, ks := range p.keyStates {
		if p.keyStateSelectableLocked(now, ks) {
			selectableSlots++
		}
	}
	switched := selectableSlots > 1 && p.lastSelectedSlot >= 0 && p.lastSelectedSlot != selectedIdx
	p.lastSelectedSlot = selectedIdx
	p.lastSelectedKey = selectedKey
	selectedKS.EverSelected = true
	return selectedKey, switched
}

func (p *ProviderConfig) pickRandomHealthyCandidateLocked(now time.Time, excludeIdx int) int {
	var healthy []int
	var fallback []int
	for i, ks := range p.keyStates {
		if i == excludeIdx {
			continue
		}
		if !p.keyStateSelectableLocked(now, ks) {
			continue
		}
		fallback = append(fallback, i)
		if p.keyStateHealthyLocked(now, ks) {
			healthy = append(healthy, i)
		}
	}
	candidates := healthy
	if len(candidates) == 0 {
		candidates = fallback
	}
	if len(candidates) == 0 {
		return -1
	}
	if p.keyOrder == config.KeyOrderSmart {
		return p.bestCandidateIndexLocked(now, candidates)
	}
	return candidates[rand.Intn(len(candidates))]
}

func (p *ProviderConfig) selectOnFailureKeyLocked(now time.Time) (*KeyState, int) {
	pinnedIdx := p.stickyIdx
	pinned := p.keyStateBySlotLocked(pinnedIdx)
	if p.keyOrder == config.KeyOrderSmart && pinned != nil && pinned.EverSelected {
		if p.keyStateSelectableLocked(now, pinned) {
			if p.keyStateHealthyLocked(now, pinned) {
				pinned.LastUsed = now
				return pinned, pinnedIdx
			}
			if altIdx := p.pickRandomHealthyCandidateLocked(now, pinnedIdx); altIdx >= 0 {
				p.stickyIdx = altIdx
				selected := p.keyStates[altIdx]
				selected.LastUsed = now
				return selected, altIdx
			}
			pinned.LastUsed = now
			return pinned, pinnedIdx
		}
	} else if p.keyOrder != config.KeyOrderSmart && pinned != nil && p.keyStateSelectableLocked(now, pinned) {
		if p.keyStateHealthyLocked(now, pinned) {
			pinned.LastUsed = now
			return pinned, pinnedIdx
		}
		if altIdx := p.pickRandomHealthyCandidateLocked(now, pinnedIdx); altIdx >= 0 {
			p.stickyIdx = altIdx
			selected := p.keyStates[altIdx]
			selected.LastUsed = now
			return selected, altIdx
		}
		pinned.LastUsed = now
		return pinned, pinnedIdx
	}

	var healthy []int
	var fallback []int
	for i, ks := range p.keyStates {
		if !p.keyStateSelectableLocked(now, ks) {
			continue
		}
		fallback = append(fallback, i)
		if p.keyStateHealthyLocked(now, ks) {
			healthy = append(healthy, i)
		}
	}
	candidates := healthy
	if len(candidates) == 0 {
		candidates = fallback
	}
	if len(candidates) == 0 {
		return nil, -1
	}
	var idx int
	if p.keyOrder == config.KeyOrderRandom {
		idx = candidates[rand.Intn(len(candidates))]
	} else {
		idx = p.bestCandidateIndexLocked(now, candidates)
	}
	p.stickyIdx = idx
	selected := p.keyStates[idx]
	selected.LastUsed = now
	return selected, idx
}

// SetOAuthRefresher configures OAuth credential refresh support.
// oauthKeys must map the current access token string to the auth.yaml slot metadata
// for each OAuth credential that should participate in selection.
func (p *ProviderConfig) SetOAuthRefresher(
	tokenURL string,
	clientID string,
	authConfigPath string,
	authConfig *config.AuthConfig,
	authConfigMu *sync.Mutex,
	oauthKeys map[string]OAuthKeySetup,
	proxyURL string,
) {
	if tokenURL == "" {
		return
	}
	var httpClient *http.Client
	if proxyURL != "" {
		var clientErr error
		httpClient, clientErr = NewHTTPClientWithProxy(proxyURL, 30*time.Second)
		if clientErr != nil {
			log.Warnf("failed to create OAuth refresh HTTP client with proxy, using default proxy=%v error=%v", proxyURL, clientErr)
		}
	}
	p.oauthRefresher = &OAuthRefresher{
		tokenURL:       tokenURL,
		clientID:       clientID,
		authConfigPath: authConfigPath,
		authConfig:     authConfig,
		authConfigMu:   authConfigMu,
		httpClient:     httpClient,
		providerName:   p.name,
	}
	p.effectiveProxyURL = proxyURL
	for _, ks := range p.keyStates {
		setup, ok := oauthKeys[ks.Key]
		if !ok {
			continue
		}
		ks.OAuthInfo = &OAuthKeyInfo{
			Expires:               setup.Expires,
			CredentialIndex:       setup.CredentialIndex,
			AccountID:             setup.AccountID,
			Email:                 setup.Email,
			CodexPrimaryResetAt:   setup.CodexPrimaryResetAt,
			CodexSecondaryResetAt: setup.CodexSecondaryResetAt,
		}
		if until := codexSoftCooldownUntilFromMillis(setup.CodexPrimaryResetAt, setup.CodexSecondaryResetAt, time.Now()); !until.IsZero() {
			ks.SoftCooldownUntil = until
		}
		ks.Invalid = !setup.Status.IsValid()
		if ks.Invalid {
			ks.Recovering = false
			ks.CooldownEnd = time.Time{}
			ks.ExhaustedUntil = time.Time{}
		}
	}
}

func (p *ProviderConfig) persistCodexResetHintsForKey(key string, primaryResetAt, secondaryResetAt int64) bool {
	if key == "" || p.oauthRefresher == nil {
		return false
	}
	p.mu.Lock()
	ks := p.keyStateByKeyLocked(key)
	if ks == nil || ks.OAuthInfo == nil || ks.OAuthInfo.AccountID == "" {
		p.mu.Unlock()
		return false
	}
	match := config.OAuthCredentialMatch{AccountID: ks.OAuthInfo.AccountID}
	if primaryResetAt == 0 && secondaryResetAt == 0 {
		ks.SoftCooldownUntil = time.Time{}
	} else if until := codexSoftCooldownUntilFromMillis(primaryResetAt, secondaryResetAt, time.Now()); !until.IsZero() {
		ks.SoftCooldownUntil = until
	}
	if ks.OAuthInfo != nil {
		ks.OAuthInfo.CodexPrimaryResetAt = primaryResetAt
		ks.OAuthInfo.CodexSecondaryResetAt = secondaryResetAt
	}
	p.mu.Unlock()
	updated, changed, err := p.oauthRefresher.persistCodexResetHints(match, primaryResetAt, secondaryResetAt)
	if err != nil {
		log.Warnf("failed to persist codex reset hints provider=%v error=%v", p.name, err)
		return false
	}
	if updated != nil {
		p.mu.Lock()
		if ks := p.keyStateByKeyLocked(key); ks != nil && ks.OAuthInfo != nil {
			ks.OAuthInfo.CodexPrimaryResetAt = updated.CodexPrimaryResetAt
			ks.OAuthInfo.CodexSecondaryResetAt = updated.CodexSecondaryResetAt
			ks.SoftCooldownUntil = codexSoftCooldownUntilFromMillis(updated.CodexPrimaryResetAt, updated.CodexSecondaryResetAt, time.Now())
		}
		p.mu.Unlock()
	}
	return changed
}

func (p *ProviderConfig) clearCodexResetHintsForKey(key string) bool {
	return p.persistCodexResetHintsForKey(key, 0, 0)
}

// Warmup initialises LastUsed for all keys to time.Now(), simulating a first
// call so that cooldown timers start immediately.
func (p *ProviderConfig) Warmup() {
	p.mu.Lock()
	defer p.mu.Unlock()

	now := time.Now()
	for _, ks := range p.keyStates {
		ks.LastUsed = now
	}
}

// SetRateLimiter configures an optional rate limiter. rpm is the maximum
// requests per minute. If rpm <= 0, rate limiting is disabled.
func (p *ProviderConfig) SetRateLimiter(rpm int) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if rpm <= 0 {
		p.limiter = nil
		return
	}

	burst := rpm / 5
	if burst < 5 {
		burst = 5
	}
	p.limiter = rate.NewLimiter(rate.Every(time.Minute/time.Duration(rpm)), burst)
}

// SelectKeyWithContext returns an API key that is selectable.
// Codex x-codex-* snapshots do not affect selection (only real errors e.g. 429
// apply cooldown via MarkCooldown). Selection follows key_rotation + key_order:
// on_failure pins to stickyIdx until that key is unavailable; while pinned, a
// recovering key is deprioritized in favor of any other selectable healthy key,
// but may still be retried when no healthy alternative exists. per_request picks
// a key on every call per key_order.
// key_order=sequential picks the earliest-LastUsed selectable key; key_order=random
// picks uniformly at random among selectable candidates, preferring healthy keys
// over recovering ones when possible.
// If a rate limiter is configured, it waits for a token before selecting.
// If no key is selectable, it returns AllKeysCoolingError with a retry duration.
// If the selected key is an OAuth token that is about to expire (<60s), it
// refreshes the token before returning.
// The second return value is true when the selected credential slot differs from
// the previously selected slot (i.e., a real key-slot switch occurred).
func (p *ProviderConfig) SelectKeyWithContext(ctx context.Context) (string, bool, error) {
	// Rate limiting: wait for a token before proceeding (outside the mutex).
	// This allows multiple agents/goroutines to queue up without holding the lock.
	if p.limiter != nil {
		if err := p.limiter.Wait(ctx); err != nil {
			return "", false, err
		}
	}

	p.mu.Lock()

	if len(p.keyStates) == 0 {
		// No keys configured — return empty string for providers that don't require auth
		// (e.g., local services, public APIs). The provider implementation should handle
		// empty keys gracefully (e.g., omit Authorization header).
		p.mu.Unlock()
		return "", false, nil
	}

	now := time.Now()
	selectableTotal := 0
	for _, ks := range p.keyStates {
		if ks.Invalid {
			continue
		}
		selectableTotal++
	}
	if selectableTotal == 0 {
		p.mu.Unlock()
		return "", false, &NoUsableKeysError{Provider: p.name}
	}

	var selectedKS *KeyState
	selectedIdx := -1
	if p.keyRotation == config.KeyRotationOnFailure {
		selectedKS, selectedIdx = p.selectOnFailureKeyLocked(now)
	} else {
		if p.keyOrder == config.KeyOrderRandom {
			selectedIdx = p.pickRandomHealthyCandidateLocked(now, -1)
			if selectedIdx >= 0 {
				selectedKS = p.keyStates[selectedIdx]
				selectedKS.LastUsed = now
			}
		} else {
			var healthyCandidates []int
			var fallbackCandidates []int
			for i, ks := range p.keyStates {
				if !p.keyStateSelectableLocked(now, ks) {
					continue
				}
				fallbackCandidates = append(fallbackCandidates, i)
				if p.keyStateHealthyLocked(now, ks) {
					healthyCandidates = append(healthyCandidates, i)
				}
			}
			candidates := healthyCandidates
			if len(candidates) == 0 {
				candidates = fallbackCandidates
			}
			selectedIdx = p.bestCandidateIndexLocked(now, candidates)
			if selectedIdx >= 0 {
				selectedKS = p.keyStates[selectedIdx]
				selectedKS.LastUsed = now
			}
		}
	}

	if selectedKS == nil || selectedIdx < 0 {
		retryAfter := p.earliestKeyRecoveryLocked(now)
		if retryAfter <= 0 {
			retryAfter = 10 * time.Second
		}
		p.mu.Unlock()
		return "", false, &AllKeysCoolingError{RetryAfter: retryAfter}
	}

	// Refresh OAuth token if it's about to expire (<60s).
	if selectedKS.OAuthInfo != nil && p.oauthRefresher != nil {
		if selectedKS.Key == "" || isExpiringSoon(selectedKS.OAuthInfo.Expires) {
			if err := p.refreshOAuthKey(ctx, selectedKS); err != nil {
				// Log warning but continue with the old token (might still work).
				log.Warnf("failed to refresh OAuth token on-demand provider=%v error=%v", p.name, err)
			}
		}
	}

	selectedKey, switched := p.postSelectLocked(selectedKS, selectedIdx, now)
	shouldRefreshCodexUsage := selectedKS.OAuthInfo != nil && p.oauthProfile == config.OAuthProfileOpenAICodex && p.codexPollFetchFn != nil
	p.mu.Unlock()
	if shouldRefreshCodexUsage {
		p.WakeCodexRateLimitPolling()
	}
	return selectedKey, switched, nil
}

// MarkTemporaryUnavailable blocks the key until now+d if it is not already in a
// future cooldown window (e.g. from MarkCooldown after 429). Used when rotating
// to another key after retriable failures so the UI key pool reflects reality.
// Does not touch CooldownCount (no exponential stacking with API backoff).
func (p *ProviderConfig) MarkTemporaryUnavailable(key string, d time.Duration) {
	if d <= 0 || key == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	ks := p.keyStateByKeyLocked(key)
	if ks == nil {
		return
	}
	p.markTemporaryUnavailableLocked(ks, time.Now(), d)
}

// MarkRecovering marks the key as selectable-but-not-preferred. Under
// key_rotation=on_failure, selection prefers other healthy keys before retrying
// a recovering key, without applying an explicit cooldown window.
func (p *ProviderConfig) MarkRecovering(key string) {
	if key == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.markRecoveringLocked(p.keyStateByKeyLocked(key))
}

// MarkCooldown puts the specified key into cooldown for the given duration.
// The key will not be selected by SelectKey until the cooldown expires.
// If d > 0, the count is incremented and exponential backoff applied (capped at 1min).
// If d == 0, the count is reset.
func (p *ProviderConfig) MarkCooldown(key string, d time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.markCooldownLocked(p.keyStateByKeyLocked(key), d)
}

// MarkQuotaExhaustedUntil marks a key unavailable until the real provider reset time.
// Unlike MarkCooldown, this does not use exponential backoff or the 5-minute cap.
func (p *ProviderConfig) MarkQuotaExhaustedUntil(key string, until time.Time) {
	if key == "" || until.IsZero() || !until.After(time.Now()) {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.markQuotaExhaustedLocked(p.keyStateByKeyLocked(key), until)
}

// MarkKeySuccess clears soft failure state after a successful request.
func (p *ProviderConfig) MarkKeySuccess(key string) {
	if key == "" {
		return
	}
	p.mu.Lock()
	ks := p.keyStateByKeyLocked(key)
	if ks == nil {
		p.mu.Unlock()
		return
	}
	ks.CooldownCount = 0
	if !ks.ExhaustedUntil.After(time.Now()) {
		ks.ExhaustedUntil = time.Time{}
	}
	clearSoftHints := ks.OAuthInfo != nil && (ks.OAuthInfo.CodexPrimaryResetAt != 0 || ks.OAuthInfo.CodexSecondaryResetAt != 0)
	p.markHealthyLocked(ks)
	p.mu.Unlock()
	if clearSoftHints {
		p.clearCodexResetHintsForKey(key)
	}
}

// UpdateKeySnapshot stores the latest rate-limit snapshot for the given key.
func (p *ProviderConfig) UpdateKeySnapshot(key string, snap *ratelimit.KeyRateLimitSnapshot) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, ks := range p.keyStates {
		if ks.Key == key {
			ks.RateLimit = snap
			if ks.Key == p.lastSelectedKey {
				p.inlineDisplaySnap = snap
			}
			return
		}
	}
}

// KeySnapshot returns the latest rate-limit snapshot for the given key, or nil.
func (p *ProviderConfig) KeySnapshot(key string) *ratelimit.KeyRateLimitSnapshot {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, ks := range p.keyStates {
		if ks.Key == key {
			return ks.RateLimit
		}
	}
	return nil
}

func (p *ProviderConfig) CurrentInlineRateLimitSnapshot() *ratelimit.KeyRateLimitSnapshot {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.inlineDisplaySnap
}

func (p *ProviderConfig) CurrentPolledRateLimitSnapshot() *ratelimit.KeyRateLimitSnapshot {
	p.mu.Lock()
	defer p.mu.Unlock()
	key, _, credIdx, ok := p.codexUsagePollAuthLocked()
	_ = key
	if !ok {
		return nil
	}
	if p.polledRateLimitByCredIdx == nil {
		return nil
	}
	return p.polledRateLimitByCredIdx[credIdx]
}

func (p *ProviderConfig) UpdatePolledRateLimitSnapshotForCredentialIndex(credIdx int, snap *ratelimit.KeyRateLimitSnapshot) {
	if snap == nil {
		return
	}
	p.mu.Lock()
	if p.polledRateLimitByCredIdx == nil {
		p.polledRateLimitByCredIdx = make(map[int]*ratelimit.KeyRateLimitSnapshot)
	}
	p.polledRateLimitByCredIdx[credIdx] = snap
	if p.polledRateLimitSucceededAt == nil {
		p.polledRateLimitSucceededAt = make(map[int]time.Time)
	}
	p.polledRateLimitSucceededAt[credIdx] = time.Now()
	cb := p.onPolledUpdate
	p.mu.Unlock()
	if cb != nil {
		cb()
	}
}

// SetOnPolledRateLimitUpdated registers a callback invoked after UpdatePolledRateLimitSnapshotForCredentialIndex
// writes a new polled snapshot. Used by the agent layer to push a RateLimitUpdatedEvent to the TUI.
func (p *ProviderConfig) SetOnPolledRateLimitUpdated(fn func()) {
	p.mu.Lock()
	p.onPolledUpdate = fn
	p.mu.Unlock()
}

func (p *ProviderConfig) ClearInlineDisplayRateLimitSnapshot() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.inlineDisplaySnap = nil
}

func (p *ProviderConfig) CurrentKeySnapshot() *ratelimit.KeyRateLimitSnapshot {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.lastSelectedKey == "" {
		if p.inlineDisplaySnap != nil {
			return p.inlineDisplaySnap
		}
		return nil
	}
	for _, ks := range p.keyStates {
		if ks.Key == p.lastSelectedKey {
			if ks.RateLimit != nil {
				return ks.RateLimit
			}
			return p.inlineDisplaySnap
		}
	}
	return nil
}

// TryRefreshOAuthKey attempts to refresh the OAuth token for the key with the
// given access token value. Returns the refreshed access token, whether a refresh
// succeeded, and the refresh error when it failed for an OAuth key. Returns
// "", false, nil if the key is not an OAuth token or no refresher is configured.
func (p *ProviderConfig) TryRefreshOAuthKey(ctx context.Context, key string) (string, bool, error) {
	if p.oauthRefresher == nil {
		return "", false, nil
	}
	p.mu.Lock()
	var oauthKS *KeyState
	for _, ks := range p.keyStates {
		if ks.Key == key && ks.OAuthInfo != nil {
			oauthKS = ks
			break
		}
	}
	if oauthKS == nil {
		p.mu.Unlock()
		return "", false, nil
	}
	// refreshOAuthKey expects p.mu to be held and temporarily releases it.
	err := p.refreshOAuthKey(ctx, oauthKS)
	refreshedKey := oauthKS.Key
	p.mu.Unlock()
	if err != nil {
		log.Warnf("OAuth token refresh on auth error failed provider=%v error=%v", p.name, err)
		return "", false, err
	}
	return refreshedKey, true, nil
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

// AnthropicTransportCompat returns a copy of the provider-level Anthropic
// transport compatibility config. Model-level overrides are intentionally not
// supported for transport semantics.
func (p *ProviderConfig) AnthropicTransportCompat() *config.AnthropicTransportCompatConfig {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.compat == nil || p.compat.AnthropicTransport == nil {
		return nil
	}
	cfg := *p.compat.AnthropicTransport
	if len(cfg.ExtraBeta) > 0 {
		cfg.ExtraBeta = append([]string(nil), cfg.ExtraBeta...)
	}
	return &cfg
}

// GetRetryDelay returns the delay before the next retry round.
// Backoff is deterministic: 1s, 2s, 4s, 8s, 16s, 32s, then 60s for later rounds.
func (p *ProviderConfig) GetRetryDelay(attempt int) time.Duration {
	if attempt <= 0 {
		return 0
	}
	const maxRetryDelayAttempt = 6
	const maxRetryDelay = 60 * time.Second
	if attempt > maxRetryDelayAttempt {
		return maxRetryDelay
	}
	return saturatingDoublingDuration(time.Second, maxRetryDelay, attempt-1)
}

// KeyCount returns the number of API keys configured for this provider,
// excluding permanently deactivated keys.
func (p *ProviderConfig) KeyCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	count := 0
	for _, ks := range p.keyStates {
		if !ks.Invalid {
			count++
		}
	}
	return count
}

// AvailableKeyCount returns the number of keys that are selectable and the total
// non-deactivated key count.
// Safe for concurrent use (holds p.mu).
func (p *ProviderConfig) AvailableKeyCount() (available, total int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	for _, ks := range p.keyStates {
		if ks.Invalid {
			continue
		}
		total++
		if p.keyStateSelectableLocked(now, ks) {
			available++
		}
	}
	return available, total
}

// HealthyKeyCount returns the number of keys that are selectable and have been
// re-confirmed healthy (i.e. not in recovering state), along with the total
// non-deactivated key count.
func (p *ProviderConfig) HealthyKeyCount() (healthy, total int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	for _, ks := range p.keyStates {
		if ks.Invalid {
			continue
		}
		total++
		if p.keyStateHealthyLocked(now, ks) {
			healthy++
		}
	}
	return healthy, total
}

// MarkDeactivated permanently marks an OAuth key as unusable for this session
// and persists that state back to auth.yaml when possible. Unlike MarkCooldown,
// this key will never be selected again and is excluded from the total key count
// shown in the sidebar.
func (p *ProviderConfig) MarkDeactivated(key string) {
	p.markInvalid(key, config.OAuthStatusDeactivated)
}

// MarkExpired permanently marks an OAuth key as expired (refresh token unusable)
// and persists that state back to auth.yaml when possible. This key will never
// be selected again and is excluded from the total key count shown in the sidebar.
func (p *ProviderConfig) MarkExpired(key string) {
	p.markInvalid(key, config.OAuthStatusExpired)
}

// markInvalid is the shared implementation for marking a key as permanently invalid.
func (p *ProviderConfig) markInvalid(key string, status config.OAuthCredentialStatus) {
	if key == "" {
		return
	}
	p.mu.Lock()
	ks := p.keyStateByKeyLocked(key)
	if ks == nil {
		p.mu.Unlock()
		return
	}
	ks.Invalid = true
	ks.Recovering = false
	ks.CooldownEnd = time.Time{}
	ks.ExhaustedUntil = time.Time{}
	refresher := p.oauthRefresher
	match := config.OAuthCredentialMatch{}
	if ks.OAuthInfo != nil {
		match.AccountID = ks.OAuthInfo.AccountID
	}
	p.mu.Unlock()
	if refresher == nil || match.AccountID == "" {
		return
	}
	if err := refresher.persistCredentialStatus(match, status); err != nil {
		log.Warnf("failed to persist invalid OAuth credential status provider=%v status=%v error=%v", p.name, status, err)
	}
}

// ConfirmedKeyCount is retained as a semantic alias for HealthyKeyCount.
func (p *ProviderConfig) ConfirmedKeyCount() (confirmed, total int) {
	return p.HealthyKeyCount()
}

func keyStateSelectable(now time.Time, ks *KeyState) bool {
	if ks.Invalid {
		return false
	}
	if now.Before(ks.ExhaustedUntil) {
		return false
	}
	if now.Before(ks.CooldownEnd) {
		return false
	}
	return !ratelimit.SnapshotBlocksKeyAt(ks.RateLimit, now)
}

// KeyPoolNextTransition returns the shortest time until some key may transition
// between blocked and unblocked (cooldown expiry).
// Used by the TUI to refresh the key pool line without polling every frame.
// Returns 0 when there is no known upcoming transition or when total keys <= 1.
func (p *ProviderConfig) KeyPoolNextTransition() time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.keyStates) <= 1 {
		return 0
	}
	return p.keyPoolNextTransitionLocked(time.Now())
}

func (p *ProviderConfig) keyPoolNextTransitionLocked(now time.Time) time.Duration {
	d := p.earliestKeyRecoveryLocked(now)
	if d <= 0 {
		return 0
	}
	return d
}

// earliestKeyRecoveryLocked returns the minimum time until any key becomes
// selectable again (cooldown ends). Must hold p.mu.
func (p *ProviderConfig) earliestKeyRecoveryLocked(now time.Time) time.Duration {
	var minD time.Duration
	for _, ks := range p.keyStates {
		if now.Before(ks.CooldownEnd) {
			d := time.Until(ks.CooldownEnd)
			if d > 0 && (minD == 0 || d < minD) {
				minD = d
			}
		}
		if now.Before(ks.ExhaustedUntil) {
			d := time.Until(ks.ExhaustedUntil)
			if d > 0 && (minD == 0 || d < minD) {
				minD = d
			}
		}
	}
	return minD
}

func (p *ProviderConfig) oauthInfoForKey(key string) *OAuthKeyInfo {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, ks := range p.keyStates {
		if ks.Key == key && ks.OAuthInfo != nil {
			copyInfo := *ks.OAuthInfo
			return &copyInfo
		}
	}
	return nil
}

func (p *ProviderConfig) isOpenAIOAuthKey(key string) bool {
	info := p.oauthInfoForKey(key)
	if info == nil {
		return false
	}
	return p.oauthProfile == config.OAuthProfileOpenAICodex
}

// usesPresetCodexRateLimitCooldown reports whether this provider is configured
// with preset: codex (official ChatGPT/Codex OAuth). Only these providers may
// use x-codex-* rate-limit snapshots when choosing 429 cooldown after Retry-After
// is absent or zero; all other providers fall back to the default duration.
func (p *ProviderConfig) usesPresetCodexRateLimitCooldown() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.oauthProfile == config.OAuthProfileOpenAICodex
}

func (p *ProviderConfig) StartCodexRateLimitPolling(fetchFn func(string, string) ([]*ratelimit.KeyRateLimitSnapshot, error)) {
	if fetchFn == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.oauthProfile != config.OAuthProfileOpenAICodex {
		return
	}
	p.codexPollFetchFn = fetchFn
}

func (p *ProviderConfig) StopCodexRateLimitPolling() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.codexPollFetchFn = nil
	p.polledRateLimitInFlight = make(map[int]bool)
}

func (p *ProviderConfig) StartCodexWarmup(ctx context.Context) bool {
	if p == nil {
		return false
	}
	if ctx == nil {
		ctx = context.Background()
	}

	p.mu.Lock()
	if p.oauthProfile != config.OAuthProfileOpenAICodex || p.codexPollFetchFn == nil {
		p.mu.Unlock()
		return false
	}
	now := time.Now()
	candidates := make([]int, 0, len(p.keyStates))
	for i, ks := range p.keyStates {
		if ks == nil || ks.Invalid || ks.OAuthInfo == nil || ks.OAuthInfo.AccountID == "" {
			continue
		}
		if !p.keyStateSelectableLocked(now, ks) {
			continue
		}
		candidates = append(candidates, i)
	}
	if len(candidates) < 2 {
		p.mu.Unlock()
		return false
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		return p.codexSmartLessLocked(now, p.keyStates[candidates[i]], p.keyStates[candidates[j]])
	})
	if p.polledRateLimitInFlight == nil {
		p.polledRateLimitInFlight = make(map[int]bool)
	}
	if p.polledRateLimitAttemptedAt == nil {
		p.polledRateLimitAttemptedAt = make(map[int]time.Time)
	}
	if p.polledRateLimitSucceededAt == nil {
		p.polledRateLimitSucceededAt = make(map[int]time.Time)
	}
	p.mu.Unlock()

	go func(providerName string, candidateIdxs []int, ctx context.Context) {
		const successTTL = time.Minute
		const failureBackoff = 30 * time.Second
		for _, credIdx := range candidateIdxs {
			if ctx.Err() != nil {
				return
			}
			p.mu.Lock()
			ks := p.keyStateBySlotLocked(credIdx)
			if ks == nil || ks.Invalid || ks.OAuthInfo == nil || ks.OAuthInfo.AccountID == "" {
				p.mu.Unlock()
				continue
			}
			now := time.Now()
			if p.polledRateLimitInFlight[credIdx] {
				p.mu.Unlock()
				continue
			}
			if lastOK := p.polledRateLimitSucceededAt[credIdx]; !lastOK.IsZero() && now.Sub(lastOK) < successTTL {
				p.mu.Unlock()
				continue
			}
			if lastAttempt := p.polledRateLimitAttemptedAt[credIdx]; !lastAttempt.IsZero() && now.Sub(lastAttempt) < failureBackoff {
				p.mu.Unlock()
				continue
			}
			key := ks.Key
			accountID := ks.OAuthInfo.AccountID
			p.polledRateLimitInFlight[credIdx] = true
			p.polledRateLimitAttemptedAt[credIdx] = now
			p.mu.Unlock()

			snaps, err := FetchCodexUsageSnapshot(ctx, p, key, accountID)
			p.mu.Lock()
			if p.polledRateLimitInFlight != nil {
				delete(p.polledRateLimitInFlight, credIdx)
			}
			p.mu.Unlock()
			if err != nil {
				log.Debugf("codex warmup usage probe failed provider=%v account_id=%v error=%v", providerName, accountID, err)
				continue
			}
			for _, snap := range snaps {
				if snap == nil {
					continue
				}
				if snap.LimitID == "" || snap.LimitID == "codex" {
					p.UpdatePolledRateLimitSnapshotForCredentialIndex(credIdx, snap)
					break
				}
			}
		}
	}(p.name, candidates, ctx)

	return true
}

func (p *ProviderConfig) WakeCodexRateLimitPolling() {
	p.mu.Lock()
	if p.oauthProfile != config.OAuthProfileOpenAICodex || p.codexPollFetchFn == nil {
		p.mu.Unlock()
		return
	}
	key, accountID, credIdx, ok := p.codexUsagePollAuthLocked()
	if !ok {
		p.mu.Unlock()
		return
	}
	if p.polledRateLimitInFlight == nil {
		p.polledRateLimitInFlight = make(map[int]bool)
	}
	if p.polledRateLimitAttemptedAt == nil {
		p.polledRateLimitAttemptedAt = make(map[int]time.Time)
	}
	now := time.Now()
	const successTTL = time.Minute
	const failureBackoff = 30 * time.Second
	if p.polledRateLimitInFlight[credIdx] {
		p.mu.Unlock()
		return
	}
	// Bypass the success TTL once a known reset timestamp has passed; otherwise the
	// UI may keep showing an expired window (e.g. "100%" with no timer) for up to
	// successTTL before the next natural poll trigger.
	force := false
	if p.polledRateLimitByCredIdx != nil {
		if snap := p.polledRateLimitByCredIdx[credIdx]; snap != nil {
			for _, w := range []*ratelimit.RateLimitWindow{snap.Primary, snap.Secondary} {
				if w != nil && !w.ResetsAt.IsZero() && !w.ResetsAt.After(now) {
					force = true
					break
				}
			}
		}
	}
	if !force {
		if lastOK := p.polledRateLimitSucceededAt[credIdx]; !lastOK.IsZero() && now.Sub(lastOK) < successTTL {
			p.mu.Unlock()
			return
		}
	}
	if lastAttempt := p.polledRateLimitAttemptedAt[credIdx]; !lastAttempt.IsZero() && now.Sub(lastAttempt) < failureBackoff {
		p.mu.Unlock()
		return
	}
	fetchFn := p.codexPollFetchFn
	p.polledRateLimitInFlight[credIdx] = true
	p.polledRateLimitAttemptedAt[credIdx] = now
	p.mu.Unlock()

	go func(providerName string, key string, accountID string, credIdx int, fetchFn func(string, string) ([]*ratelimit.KeyRateLimitSnapshot, error)) {
		defer func() {
			p.mu.Lock()
			if p.polledRateLimitInFlight != nil {
				delete(p.polledRateLimitInFlight, credIdx)
			}
			p.mu.Unlock()
		}()
		snaps, err := fetchFn(key, accountID)
		if err != nil {
			log.Debugf("codex usage poll failed provider=%v error=%v", providerName, err)
			return
		}
		for _, snap := range snaps {
			if snap == nil {
				continue
			}
			snap.Provider = providerName
			if snap.LimitID == "" || snap.LimitID == "codex" {
				p.UpdatePolledRateLimitSnapshotForCredentialIndex(credIdx, snap)
				break
			}
		}
	}(p.name, key, accountID, credIdx, fetchFn)
}

// codexUsagePollAuthLocked returns the currently selected Codex OAuth key and account id.
// It only ever returns the *current* key (no scanning other keys) so refreshes stay aligned
// with codex-rs semantics.
func (p *ProviderConfig) codexUsagePollAuthLocked() (key string, accountID string, credIdx int, ok bool) {
	if p.lastSelectedSlot < 0 {
		return "", "", 0, false
	}
	ks := p.keyStateBySlotLocked(p.lastSelectedSlot)
	if ks == nil || ks.OAuthInfo == nil || ks.Invalid {
		return "", "", 0, false
	}
	credIdx = ks.OAuthInfo.CredentialIndex
	if credIdx < 0 {
		credIdx = p.lastSelectedSlot
	}
	return ks.Key, ks.OAuthInfo.AccountID, credIdx, true
}

func (p *ProviderConfig) EffectiveProxyURL() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.effectiveProxyURL
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

// APIURL returns the provider's complete API URL.
func (p *ProviderConfig) APIURL() string {
	return p.apiURL
}

// isExpiringSoon reports whether the OAuth token with the given expiry timestamp
// (millisecond-precision Unix time) expires within 60 seconds.
func isExpiringSoon(expires int64) bool {
	if expires == 0 {
		return false
	}
	return time.Until(time.UnixMilli(expires)) < 60*time.Second
}

// refreshOAuthKey refreshes the OAuth token for ks.
// Must be called with p.mu held; it temporarily releases p.mu during the HTTP call.
func (p *ProviderConfig) refreshOAuthKey(ctx context.Context, ks *KeyState) error {
	if p.oauthRefresher == nil {
		return nil
	}
	r := p.oauthRefresher
	credIdx := ks.OAuthInfo.CredentialIndex

	// Read current credential under authConfigMu.
	r.authConfigMu.Lock()
	creds := (*r.authConfig)[p.name]
	if credIdx >= len(creds) || creds[credIdx].OAuth == nil {
		r.authConfigMu.Unlock()
		return fmt.Errorf("invalid OAuth credential index %d for provider %q", credIdx, p.name)
	}
	credCopy := *creds[credIdx].OAuth
	r.authConfigMu.Unlock()

	// Release p.mu during the network call to avoid blocking other key selections.
	p.mu.Unlock()
	newCred, err := config.RefreshOAuthToken(ctx, r.httpClient, r.tokenURL, r.clientID, &credCopy)
	p.mu.Lock()

	if err != nil {
		log.Warnf("OAuth token refresh failed provider=%v error=%v", p.name, err)
		return err
	}

	match := config.OAuthCredentialMatch{AccountID: credCopy.AccountID}
	preservedPrimaryResetAt := credCopy.CodexPrimaryResetAt
	preservedSecondaryResetAt := credCopy.CodexSecondaryResetAt
	persistedCred, _, persistErr := r.mutateCredential(match, func(cred *config.OAuthCredential) (bool, error) {
		newCopy := *newCred
		newCopy.CodexPrimaryResetAt = cred.CodexPrimaryResetAt
		if newCopy.CodexPrimaryResetAt == 0 {
			newCopy.CodexPrimaryResetAt = preservedPrimaryResetAt
		}
		newCopy.CodexSecondaryResetAt = cred.CodexSecondaryResetAt
		if newCopy.CodexSecondaryResetAt == 0 {
			newCopy.CodexSecondaryResetAt = preservedSecondaryResetAt
		}
		*cred = newCopy
		return true, nil
	})
	if persistErr != nil {
		log.Warnf("failed to persist refreshed OAuth token provider=%v error=%v", p.name, persistErr)
		persistedCred = newCred
	}
	if persistedCred == nil {
		persistedCred = newCred
	}

	// Update in-memory key state.
	ks.Key = persistedCred.Access
	ks.OAuthInfo.Expires = persistedCred.Expires
	ks.OAuthInfo.CodexPrimaryResetAt = persistedCred.CodexPrimaryResetAt
	ks.OAuthInfo.CodexSecondaryResetAt = persistedCred.CodexSecondaryResetAt
	ks.SoftCooldownUntil = codexSoftCooldownUntilFromMillis(persistedCred.CodexPrimaryResetAt, persistedCred.CodexSecondaryResetAt, time.Now())
	if persistedCred.AccountID != "" {
		ks.OAuthInfo.AccountID = persistedCred.AccountID
	}
	if persistedCred.Email != "" {
		ks.OAuthInfo.Email = persistedCred.Email
	}
	log.Infof("OAuth token refreshed provider=%v", p.name)

	return nil
}
