package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateProviderRetryAcceptsSupportedValues(t *testing.T) {
	tests := []ProviderConfig{
		{},
		{RetryBackoff: RetryBackoffExponential},
		{RetryBackoff: RetryBackoffFixed, RetryDelayMS: new(500)},
		{RetryBackoff: RetryBackoffNone, RetryDelayMS: new(MaxProviderRetryDelayMS)},
		{RetryDelayMS: new(0)},
		{RetryAfterMaxS: new(1)},
		{RetryAfterMaxS: new(MaxRetryAfterMaxS)},
	}
	for _, cfg := range tests {
		if err := ValidateProviderRetry("sample", cfg); err != nil {
			t.Fatalf("ValidateProviderRetry(%#v) unexpected error: %v", cfg, err)
		}
	}
}

func TestValidateProviderRetryRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name string
		cfg  ProviderConfig
		want string
	}{
		{name: "unknown mode", cfg: ProviderConfig{RetryBackoff: "linear"}, want: "invalid retry_backoff"},
		{name: "negative delay", cfg: ProviderConfig{RetryDelayMS: new(-1)}, want: "retry_delay_ms must be between"},
		{name: "delay above cap", cfg: ProviderConfig{RetryDelayMS: new(MaxProviderRetryDelayMS + 1)}, want: "retry_delay_ms must be between"},
		{name: "zero Retry-After cap", cfg: ProviderConfig{RetryAfterMaxS: new(0)}, want: "retry_after_max_s must be between"},
		{name: "Retry-After cap above maximum", cfg: ProviderConfig{RetryAfterMaxS: new(MaxRetryAfterMaxS + 1)}, want: "retry_after_max_s must be between"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateProviderRetry("sample", tt.cfg)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ValidateProviderRetry(%#v) error = %v, want containing %q", tt.cfg, err, tt.want)
			}
		})
	}
}

func TestLoadConfigFromPathToleratesInvalidProviderRetry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(`providers:
  sample:
    type: responses
    retry_backoff: linear
`), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	cfg, err := LoadConfigFromPath(path)
	if err != nil {
		t.Fatalf("LoadConfigFromPath: %v", err)
	}
	if got := cfg.Providers["sample"].RetryBackoff; got != "" {
		t.Fatalf("retry_backoff = %q, want it reset to the default (not configured) so the invalid value cannot fail startup", got)
	}
	if got := cfg.Providers["sample"].Type; got != "responses" {
		t.Fatalf("type = %q, want valid sibling fields preserved", got)
	}
}
