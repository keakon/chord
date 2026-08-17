package llm

import (
	"strings"
	"time"

	"github.com/keakon/chord/internal/config"
)

// providerTrustsHTTP400 reports whether HTTP 400 should be treated as a
// terminal request error for this provider. The explicit trust_http_400
// config wins when set; otherwise the official preset (codex) trusts 400
// and aggregating/proxy gateways do not, because they often collapse upstream
// overload/rate-limit failures into 400.
func providerTrustsHTTP400(p *ProviderConfig) bool {
	if p == nil {
		return false
	}
	if p.trustHTTP400 != nil {
		return *p.trustHTTP400
	}
	return providerIsOfficialPreset(p)
}

// resolveRetryAfterMax resolves the longest Retry-After wait honored for a
// provider. The explicit retry_after_max_s config wins when set (the loader
// validates its 1-86400s range); otherwise the official preset (codex) honors
// up to a day while third-party gateways, which can echo arbitrary values,
// are bounded to one minute.
func resolveRetryAfterMax(cfg config.ProviderConfig) time.Duration {
	if cfg.RetryAfterMaxS != nil {
		seconds := min(max(*cfg.RetryAfterMaxS, 1), config.MaxRetryAfterMaxS)
		return time.Duration(seconds) * time.Second
	}
	if presetIsOfficial(cfg.Preset) {
		return config.OfficialRetryAfterMaxS * time.Second
	}
	return config.DefaultRetryAfterMaxS * time.Second
}

// providerRetryAfterMax returns the resolved Retry-After cap; the untrusted
// default applies when the provider is unknown.
func providerRetryAfterMax(p *ProviderConfig) time.Duration {
	if p == nil || p.retryAfterMax <= 0 {
		return config.DefaultRetryAfterMaxS * time.Second
	}
	return p.retryAfterMax
}

func providerIsOfficialPreset(p *ProviderConfig) bool {
	if p == nil {
		return false
	}
	return presetIsOfficial(p.preset)
}

func presetIsOfficial(preset string) bool {
	preset = strings.TrimSpace(preset)
	return strings.EqualFold(preset, config.ProviderPresetCodex)
}
