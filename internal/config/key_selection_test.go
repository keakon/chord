package config

import "testing"

func TestValidateProviderKeySelectionAcceptsCommonModes(t *testing.T) {
	cases := []ProviderConfig{
		{},
		{KeyRotation: KeyRotationOnFailure, KeyOrder: KeyOrderSequential},
		{KeyRotation: KeyRotationPerRequest, KeyOrder: KeyOrderRandom},
		{Preset: ProviderPresetCodex, KeyOrder: KeyOrderSmart},
	}
	for _, cfg := range cases {
		if err := ValidateProviderKeySelection("p", cfg); err != nil {
			t.Fatalf("ValidateProviderKeySelection(%#v) unexpected error: %v", cfg, err)
		}
	}
}

func TestValidateProviderKeySelectionRejectsInvalidRotation(t *testing.T) {
	err := ValidateProviderKeySelection("p", ProviderConfig{KeyRotation: "per-call"})
	if err == nil {
		t.Fatal("expected invalid key_rotation error")
	}
}

func TestValidateProviderKeySelectionRejectsInvalidOrder(t *testing.T) {
	err := ValidateProviderKeySelection("p", ProviderConfig{KeyOrder: "round_robin"})
	if err == nil {
		t.Fatal("expected invalid key_order error")
	}
}

func TestValidateProviderKeySelectionRejectsSmartForNonCodex(t *testing.T) {
	err := ValidateProviderKeySelection("p", ProviderConfig{KeyOrder: KeyOrderSmart})
	if err == nil {
		t.Fatal("expected key_order smart non-codex error")
	}
}
