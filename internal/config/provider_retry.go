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

// validRequestCompression reports whether v is a supported upstream request
// body compression encoding ("" disables compression). Shared by validation
// and by the tolerant loader's reset so both sides agree on what is invalid.
func validRequestCompression(v string) bool {
	switch strings.TrimSpace(v) {
	case "", RequestCompressionGzip, RequestCompressionZstd:
		return true
	}
	return false
}

// ValidateProviderCompression validates the provider request body compression
// setting. The pre-1.0 boolean form (`compress: true` / `compress: false`) is
// rejected with a migration hint instead of a bare allowed-values error, since
// it silently enabled gzip before the setting became an encoding choice.
func ValidateProviderCompression(providerName string, cfg ProviderConfig) error {
	v := strings.TrimSpace(cfg.Compress)
	if validRequestCompression(v) {
		return nil
	}
	if v == "true" || v == "false" {
		return fmt.Errorf("compress value %q for provider %q is the removed boolean form; set compress to %q or %q, or remove the field to disable request compression", cfg.Compress, providerName, RequestCompressionGzip, RequestCompressionZstd)
	}
	return fmt.Errorf("invalid compress value %q for provider %q (allowed: %q, %q)", cfg.Compress, providerName, RequestCompressionGzip, RequestCompressionZstd)
}

// ValidateProviderRuntime validates provider settings used during runtime setup.
func ValidateProviderRuntime(providerName string, cfg ProviderConfig) error {
	if err := ValidateProviderKeySelection(providerName, cfg); err != nil {
		return err
	}
	return ValidateProviderRetry(providerName, cfg)
}
