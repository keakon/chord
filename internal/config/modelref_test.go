package config

import (
	"strings"
	"testing"
)

func TestLookupConfiguredModelVariantValidatesVariant(t *testing.T) {
	providers := map[string]ProviderConfig{
		"openai": {
			Models: map[string]ModelConfig{
				"gpt-4.1": {
					Variants: map[string]ModelVariant{"high": {}},
				},
			},
		},
	}

	providerCfg, modelCfg, err := LookupConfiguredModelVariant(providers, "openai", "gpt-4.1", "high")
	if err != nil {
		t.Fatalf("LookupConfiguredModelVariant: %v", err)
	}
	if _, ok := providerCfg.Models["gpt-4.1"]; !ok {
		t.Fatal("expected returned provider config to include gpt-4.1")
	}
	if _, ok := modelCfg.Variants["high"]; !ok {
		t.Fatal("expected returned model config to include high variant")
	}
}

func TestValidateConfiguredVariantRejectsUnknownVariant(t *testing.T) {
	modelCfg := ModelConfig{}
	if err := ValidateConfiguredVariant(modelCfg, "openai", "gpt-4.1", "high"); err == nil || !strings.Contains(err.Error(), `variant "high" not found for model "gpt-4.1" in provider "openai"`) {
		t.Fatalf("ValidateConfiguredVariant err = %v, want unknown variant error", err)
	}
}

func TestResolveConfiguredModelRefRequiresProvider(t *testing.T) {
	providers := map[string]ProviderConfig{
		"openai": {
			Models: map[string]ModelConfig{
				"shared": {},
			},
		},
	}

	_, _, variantName, _, _, err := ResolveConfiguredModelRef(providers, "shared@high")
	if err == nil || !strings.Contains(err.Error(), `model reference "shared@high" must include a provider; use provider/model[@variant]`) {
		t.Fatalf("ResolveConfiguredModelRef err = %v, want missing provider error", err)
	}
	if variantName != "high" {
		t.Fatalf("variantName = %q, want high", variantName)
	}
}
