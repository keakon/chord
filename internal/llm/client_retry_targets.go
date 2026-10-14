package llm

import (
	"errors"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/message"
)

type streamRetryTarget struct {
	provider     *ProviderConfig
	impl         Provider
	modelID      string
	maxTokens    int
	contextLimit int
	inputLimit   int
	tuning       RequestTuning
	serviceTier  config.ServiceTier
	variant      string
	isFallback   bool
}

type replayEchoTarget struct {
	provider *ProviderConfig
	modelID  string
	variant  string
}

func (c *Client) buildStreamRetryTargets(
	startProvider *ProviderConfig,
	startImpl Provider,
	startModelID string,
	startMaxTokens int,
	startTuning RequestTuning,
	variantForStart string,
	outputCapSetting int,
	fallbackEnabled bool,
	fallbackModels []FallbackModel,
) []streamRetryTarget {
	tier := c.ServiceTier()
	startServiceTier := effectiveServiceTierForTuning(startTuning, tier)
	if tier != config.ServiceTierStandard {
		startTuning = serviceTierTuning(startTuning, tier)
	}
	targets := []streamRetryTarget{
		{
			provider:     startProvider,
			impl:         startImpl,
			modelID:      startModelID,
			maxTokens:    startMaxTokens,
			contextLimit: c.ContextLimitForModelRef(providerModelRef(startProvider, startModelID)),
			inputLimit:   c.InputLimitForModelRef(providerModelRef(startProvider, startModelID)),
			tuning:       startTuning,
			serviceTier:  startServiceTier,
			variant:      variantForStart,
			isFallback:   false,
		},
	}
	if !fallbackEnabled {
		return targets
	}
	for _, fb := range fallbackModels {
		var fbTuning RequestTuning
		fbVariantUsed := ""
		fbContextLimit := fb.ContextLimit
		fbInputLimit := resolveFallbackInputLimit(fb, outputCapSetting)
		if m, ok := fb.ProviderConfig.GetModel(fb.ModelID); ok {
			fbTuning = tuningFromModel(m, fb.ProviderConfig.Preset(), fb.ProviderConfig.SupportedServiceTiers(), fb.ProviderConfig.ParallelToolCallsConfig())
			if fbContextLimit <= 0 {
				fbContextLimit = m.Limit.Context
			}
			if fb.Variant != "" {
				if v, ok := m.Variants[fb.Variant]; ok {
					fbTuning = mergeVariantTuning(fbTuning, v)
					fbVariantUsed = fb.Variant
				}
			}
		}
		if fbInputLimit <= 0 {
			fbInputLimit = fbContextLimit
		}
		// The one-shot post-compaction replay floor rides on the start
		// target's tuning; fallback tunings are rebuilt from model config and
		// would otherwise replay a foreign native payload across the
		// rewritten boundary at Native on the first post-compaction request.
		if startTuning.ReplayCompat != nil && (fbTuning.ReplayCompat == nil || *fbTuning.ReplayCompat < *startTuning.ReplayCompat) {
			level := *startTuning.ReplayCompat
			fbTuning.ReplayCompat = &level
		}
		// Session identity likewise rides on the start target: fallback
		// tunings are rebuilt from model config and must still carry the
		// Client's prompt-cache key so every pool entry stays in the same
		// cache namespace.
		fbTuning.SessionKey = startTuning.SessionKey
		fbServiceTier := effectiveServiceTierForTuning(fbTuning, tier)
		if tier != config.ServiceTierStandard {
			fbTuning = serviceTierTuning(fbTuning, tier)
		}
		targets = append(targets, streamRetryTarget{
			provider:     fb.ProviderConfig,
			impl:         fb.ProviderImpl,
			modelID:      fb.ModelID,
			maxTokens:    fb.MaxTokens,
			contextLimit: fbContextLimit,
			inputLimit:   fbInputLimit,
			tuning:       fbTuning,
			serviceTier:  fbServiceTier,
			variant:      fbVariantUsed,
			isFallback:   true,
		})
	}
	return targets
}

func (t streamRetryTarget) displayRef() string {
	displayRef := providerModelRef(t.provider, t.modelID)
	if t.variant != "" {
		displayRef += "@" + t.variant
	}
	return displayRef
}

func (t streamRetryTarget) fallbackModel() FallbackModel {
	return FallbackModel{
		ProviderConfig: t.provider,
		ProviderImpl:   t.impl,
		ModelID:        t.modelID,
		MaxTokens:      t.maxTokens,
		ContextLimit:   t.contextLimit,
		InputLimit:     t.inputLimit,
		Variant:        t.variant,
	}
}

func emitStreamStatus(cb StreamCallback, typ, detail string) {
	if cb == nil {
		return
	}
	cb(message.StreamDelta{
		Type: "status",
		Status: &message.StatusDelta{
			Type:   typ,
			Detail: detail,
		},
	})
}

func emitStreamStatusDelta(cb StreamCallback, status message.StatusDelta) {
	if cb == nil {
		return
	}
	cb(message.StreamDelta{Type: message.StreamDeltaStatus, Status: &status})
}

func emitRetryError(cb StreamCallback, err error, provider, model, maskedKey, accountID, email string) {
	if cb == nil || err == nil {
		return
	}
	cb(message.StreamDelta{
		Type:      message.StreamDeltaRetryError,
		Err:       err,
		Provider:  provider,
		Model:     model,
		MaskedKey: maskedKey,
		AccountID: accountID,
		Email:     email,
	})
}

// retryErrorFields resolves the display identity a retry error about key
// carries: the provider name plus the OAuth account metadata when available.
func retryErrorFields(provider *ProviderConfig, key string) (providerName, accountID, email string) {
	if provider == nil {
		return "", "", ""
	}
	providerName = provider.Name()
	if info := provider.oauthInfoForKey(key); info != nil {
		accountID = info.AccountID
		email = info.Email
	}
	return providerName, accountID, email
}

func emitRetryErrorForKey(cb StreamCallback, err error, provider *ProviderConfig, model, key string) {
	providerName, accountID, email := retryErrorFields(provider, key)
	emitRetryError(cb, err, providerName, model, maskedKey(key), accountID, email)
	if cb != nil && provider != nil && err != nil {
		// The failure reached the user as a retry error, so a later cooling
		// wait must not report this same cause a second time.
		provider.markKeyCooldownCauseReported(err)
	}
}

// markTerminalCooldownCauseReported covers a request that gives up right after
// recording a cooldown cause: the failure reaches the user as the request's
// own error, so a later request must not replay it from the pending queue.
// Without a callback the caller is a background request whose error nobody
// sees, and the queued cause stays for the next request that can show it.
func markTerminalCooldownCauseReported(cb StreamCallback, provider *ProviderConfig, err error) {
	if cb != nil && provider != nil {
		provider.markKeyCooldownCauseReported(err)
	}
}

// reportCoolingCause surfaces the API failure behind a cooling wait the caller
// is about to show. The failure may have come from a caller without a stream
// callback (background extraction, compaction or thinking translation), so the
// cause is kept on the shared key state and claimed here;
// claimKeyCooldownCause makes sure concurrent waiters report each recorded
// failure only once.
func reportCoolingCause(cb StreamCallback, cause *keyCooldownCause) {
	if cb == nil || cause == nil || cause.provider == nil {
		return
	}
	if !cause.provider.claimKeyCooldownCause(cause) {
		return
	}
	emitRetryErrorForKey(cb, cause.Err, cause.provider, cause.Model, cause.Key)
}

func emitKeyCooldownDeltas(cb StreamCallback, result markKeyCooldownResult) {
	if cb == nil {
		return
	}
	if result.expired || result.expiredAccountID != "" || result.expiredEmail != "" {
		cb(message.StreamDelta{Type: message.StreamDeltaKeyExpired, AccountID: result.expiredAccountID, Email: result.expiredEmail})
	}
	if result.deactivated || result.deactivatedAccountID != "" || result.deactivatedEmail != "" {
		cb(message.StreamDelta{Type: message.StreamDeltaKeyDeactivated, AccountID: result.deactivatedAccountID, Email: result.deactivatedEmail})
	}
	if result.invalidated || result.invalidatedAccountID != "" || result.invalidatedEmail != "" {
		cb(message.StreamDelta{Type: message.StreamDeltaKeyInvalidated, AccountID: result.invalidatedAccountID, Email: result.invalidatedEmail})
	}
}

func isAuthAPIStatusError(err error) bool {
	apiErr, ok := errors.AsType[*APIError](err)
	return ok && apiErr != nil && (apiErr.StatusCode == 401 || apiErr.StatusCode == 403)
}

func isRateLimitAPIStatusError(err error) bool {
	apiErr, ok := errors.AsType[*APIError](err)
	return ok && apiErr != nil && apiErr.StatusCode == 429
}

func updateSuccessfulCallStatus(status *CallStatus, target streamRetryTarget) {
	if status == nil {
		return
	}
	status.RunningModelRef = target.displayRef()
	status.RunningContextLimit = target.contextLimit
	status.RunningInputLimit = target.inputLimit
	status.ServiceTier = target.serviceTier
}
