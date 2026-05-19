package config

import (
	"fmt"
	"strings"
)

// ValidateProviderKeySelection validates provider key selection settings.
func ValidateProviderKeySelection(providerName string, cfg ProviderConfig) error {
	rotation := strings.TrimSpace(cfg.KeyRotation)
	switch rotation {
	case "", KeyRotationOnFailure, KeyRotationPerRequest:
	default:
		return fmt.Errorf("invalid key_rotation %q for provider %q (allowed: %s, %s)", cfg.KeyRotation, providerName, KeyRotationOnFailure, KeyRotationPerRequest)
	}

	order := strings.TrimSpace(cfg.KeyOrder)
	switch order {
	case "", KeyOrderSequential, KeyOrderRandom, KeyOrderSmart:
	default:
		return fmt.Errorf("invalid key_order %q for provider %q (allowed: %s, %s, %s)", cfg.KeyOrder, providerName, KeyOrderSequential, KeyOrderRandom, KeyOrderSmart)
	}
	if order == KeyOrderSmart && strings.TrimSpace(strings.ToLower(cfg.Preset)) != ProviderPresetCodex {
		return fmt.Errorf("key_order %q is only supported for preset=%s providers (provider %q)", KeyOrderSmart, ProviderPresetCodex, providerName)
	}
	return nil
}
