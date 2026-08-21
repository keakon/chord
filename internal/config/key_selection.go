package config

import (
	"fmt"
	"strings"
)

// validKeyRotation reports whether v is a supported key rotation mode ("" is
// the default). Shared by validation and by the tolerant loader's reset so
// both sides agree on what is invalid.
func validKeyRotation(v string) bool {
	switch strings.TrimSpace(v) {
	case "", KeyRotationOnFailure, KeyRotationPerRequest:
		return true
	}
	return false
}

// validKeyOrder reports whether v is a usable key order for this provider.
// key_order=smart is only supported for preset=codex providers; the other
// values ("" means sequential) work everywhere.
func validKeyOrder(order, preset string) bool {
	switch strings.TrimSpace(order) {
	case "", KeyOrderSequential, KeyOrderRandom:
		return true
	case KeyOrderSmart:
		return strings.TrimSpace(strings.ToLower(preset)) == ProviderPresetCodex
	}
	return false
}

// ValidateProviderKeySelection validates provider key selection settings.
func ValidateProviderKeySelection(providerName string, cfg ProviderConfig) error {
	if !validKeyRotation(cfg.KeyRotation) {
		return fmt.Errorf("invalid key_rotation %q for provider %q (allowed: %s, %s)", cfg.KeyRotation, providerName, KeyRotationOnFailure, KeyRotationPerRequest)
	}
	order := strings.TrimSpace(cfg.KeyOrder)
	if order == KeyOrderSmart && strings.TrimSpace(strings.ToLower(cfg.Preset)) != ProviderPresetCodex {
		return fmt.Errorf("key_order %q is only supported for preset=%s providers (provider %q)", KeyOrderSmart, ProviderPresetCodex, providerName)
	}
	if order != "" && order != KeyOrderSequential && order != KeyOrderRandom && order != KeyOrderSmart {
		return fmt.Errorf("invalid key_order %q for provider %q (allowed: %s, %s, %s)", cfg.KeyOrder, providerName, KeyOrderSequential, KeyOrderRandom, KeyOrderSmart)
	}
	return nil
}
