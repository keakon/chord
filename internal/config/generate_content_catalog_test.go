package config

import (
	"fmt"
	"testing"

	"github.com/keakon/chord/internal/modelcatalog"
)

func TestGenerateContentCatalogLeafPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name            string
		provider, model *bool
		want            bool
	}{
		{"catalog", nil, nil, true}, {"provider_false", new(false), nil, false}, {"model_false", new(true), new(false), false}, {"model_true", new(false), new(true), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			profile := &modelcatalog.ConfigProfile{Compat: map[string]any{"generate_content": map[string]any{"automatic_continuation": true, "single_request_output_limit": 65536}}}
			defaults, err := DecodeCatalogProfile(profile)
			if err != nil {
				t.Fatal(err)
			}
			provider := &ProviderCompatConfig{GenerateContent: &GenerateContentCompatConfig{AutomaticContinuation: tc.provider}}
			model := &ModelCompatConfig{GenerateContent: &GenerateContentCompatConfig{AutomaticContinuation: tc.model}}
			if err := prepareCatalogCompatDefaults(&defaults, provider, model); err != nil {
				t.Fatal(err)
			}
			resolved := ResolveGenerateContentCompat(provider, defaults)
			if resolved == nil || resolved.AutomaticContinuation == nil || *resolved.AutomaticContinuation != tc.want || *resolved.SingleRequestOutputLimit != 65536 {
				t.Fatalf("resolved=%+v", resolved)
			}
		})
	}
}

func TestGenerateContentConfigNullAndEmptyInheritance(t *testing.T) {
	for _, tc := range []struct {
		name, override string
		want           bool
	}{
		{"block_null", "compat: null", true},
		{"nested_null", "compat: {generate_content: null}", true},
		{"leaf_null", "compat: {generate_content: {automatic_continuation: null}}", true},
		{"empty", "compat: {generate_content: {}}", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			global := writeCatalogTestConfig(t, "global.yaml", `providers:
  sample:
    type: generate-content
    api_url: https://example.invalid/v1beta
    compat:
      generate_content: {automatic_continuation: true, single_request_output_limit: 65536}
    models:
      model-alias:
        catalog: false
        compat:
          generate_content: {automatic_continuation: false}
model_pools:
  default: [sample/model-alias]
`)
			project := writeCatalogTestConfig(t, "project.yaml", fmt.Sprintf("providers:\n  sample:\n    models:\n      model-alias:\n        %s\n", tc.override))
			rc, err := LoadResolvedConfig(global, project)
			if err != nil {
				t.Fatal(err)
			}
			p := rc.Config.Providers["sample"]
			got := ResolveGenerateContentCompat(p.Compat, p.Models["model-alias"])
			if got == nil || got.AutomaticContinuation == nil || *got.AutomaticContinuation != tc.want || *got.SingleRequestOutputLimit != 65536 {
				t.Fatalf("resolved=%+v diagnostics=%+v", got, rc.Diagnostics)
			}
		})
	}
}

func TestGenerateContentCatalogProtocolIsolation(t *testing.T) {
	facts := modelcatalog.ModelFacts{ID: "sample/model-1", Connection: &modelcatalog.Connection{RequestURL: "https://example.invalid/v1beta"}, Profile: &modelcatalog.ConfigProfile{Compat: map[string]any{"generate_content": map[string]any{"automatic_continuation": true, "single_request_output_limit": 65536}}}}
	for _, protocol := range []string{ProviderTypeGenerateContent, ProviderTypeChatCompletions} {
		_, profile, err := customCatalogProfile(ProviderConfig{Type: protocol}, facts)
		if err != nil {
			t.Fatal(err)
		}
		if protocol == ProviderTypeGenerateContent {
			if profile == nil || profile.Compat["generate_content"] == nil {
				t.Fatal("native recipe lost")
			}
		} else if profile != nil && profile.Compat != nil {
			t.Fatal("wire recipe crossed protocol")
		}
	}
}
