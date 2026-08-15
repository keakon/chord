package config

import (
	"fmt"
	"strings"
)

// ValidateProviderRetry validates provider round retry settings.
func ValidateProviderRetry(providerName string, cfg ProviderConfig) error {
	backoff := strings.TrimSpace(cfg.RetryBackoff)
	switch backoff {
	case "", RetryBackoffExponential, RetryBackoffFixed, RetryBackoffNone:
	default:
		return fmt.Errorf("invalid retry_backoff %q for provider %q (allowed: %s, %s, %s)", cfg.RetryBackoff, providerName, RetryBackoffExponential, RetryBackoffFixed, RetryBackoffNone)
	}
	if cfg.RetryDelayMS != nil && (*cfg.RetryDelayMS < 0 || *cfg.RetryDelayMS > MaxProviderRetryDelayMS) {
		return fmt.Errorf("retry_delay_ms must be between 0 and %d for provider %q", MaxProviderRetryDelayMS, providerName)
	}
	if cfg.RetryAfterMaxS != nil && (*cfg.RetryAfterMaxS < 1 || *cfg.RetryAfterMaxS > MaxRetryAfterMaxS) {
		return fmt.Errorf("retry_after_max_s must be between 1 and %d for provider %q", MaxRetryAfterMaxS, providerName)
	}
	return nil
}

// ValidateProviderRuntime validates provider settings used during runtime setup.
func ValidateProviderRuntime(providerName string, cfg ProviderConfig) error {
	if err := ValidateProviderKeySelection(providerName, cfg); err != nil {
		return err
	}
	return ValidateProviderRetry(providerName, cfg)
}
