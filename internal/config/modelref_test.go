package config

import (
	"strings"
	"testing"
)

func TestLookupConfiguredModelVariantValidatesVariant(t *testing.T) {
	providers := map[string]ProviderConfig{
		"sample": {
			Models: map[string]ModelConfig{
				"model-beta": {
					Variants: map[string]ModelVariant{"high": {}},
				},
			},
		},
	}

	providerCfg, modelCfg, err := LookupConfiguredModelVariant(providers, "sample", "model-beta", "high")
	if err != nil {
		t.Fatalf("LookupConfiguredModelVariant: %v", err)
	}
	if _, ok := providerCfg.Models["model-beta"]; !ok {
		t.Fatal("expected returned provider config to include model-beta")
	}
	if _, ok := modelCfg.Variants["high"]; !ok {
		t.Fatal("expected returned model config to include high variant")
	}
}

func TestValidateConfiguredVariantRejectsUnknownVariant(t *testing.T) {
	modelCfg := ModelConfig{}
	if err := ValidateConfiguredVariant(modelCfg, "sample", "model-beta", "high"); err == nil || !strings.Contains(err.Error(), `variant "high" not found for model "model-beta" in provider "sample"`) {
		t.Fatalf("ValidateConfiguredVariant err = %v, want unknown variant error", err)
	}
}

func TestResolveConfiguredModelRefRequiresProvider(t *testing.T) {
	providers := map[string]ProviderConfig{
		"sample": {
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
