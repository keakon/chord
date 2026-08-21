package config

import (
	"fmt"
	"strings"
)

// validRetryBackoff reports whether v is a supported retry backoff mode ("" is
// the default). Shared by validation and by the tolerant loader's reset so
// both sides agree on what is invalid.
func validRetryBackoff(v string) bool {
	switch strings.TrimSpace(v) {
	case "", RetryBackoffExponential, RetryBackoffFixed, RetryBackoffNone:
		return true
	}
	return false
}

func validRetryDelayMS(v int) bool   { return v >= 0 && v <= MaxProviderRetryDelayMS }
func validRetryAfterMaxS(v int) bool { return v >= 1 && v <= MaxRetryAfterMaxS }

// ValidateProviderRetry validates provider round retry settings.
func ValidateProviderRetry(providerName string, cfg ProviderConfig) error {
	if !validRetryBackoff(cfg.RetryBackoff) {
		return fmt.Errorf("invalid retry_backoff %q for provider %q (allowed: %s, %s, %s)", cfg.RetryBackoff, providerName, RetryBackoffExponential, RetryBackoffFixed, RetryBackoffNone)
	}
	if cfg.RetryDelayMS != nil && !validRetryDelayMS(*cfg.RetryDelayMS) {
		return fmt.Errorf("retry_delay_ms must be between 0 and %d for provider %q", MaxProviderRetryDelayMS, providerName)
	}
	if cfg.RetryAfterMaxS != nil && !validRetryAfterMaxS(*cfg.RetryAfterMaxS) {
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
