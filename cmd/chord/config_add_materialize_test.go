package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/config"
)

func TestConfigAddSavesIndependentModelSettings(t *testing.T) {
	cases := []struct {
		name, ref, protocol, source, variant string
	}{
		{"preset", "openai/gpt-6.1-sol", "", "", "high"},
		{"codex", "codex/gpt-6.1-sol", "", "", "high"},
		{"messages", "sample/model-1", config.ProviderTypeMessages, "anthropic/claude-fable-5-2", ""},
		{"responses", "sample/model-1", config.ProviderTypeResponses, "openai/gpt-6.1-sol", "high"},
		{"different protocol", "sample/model-1", config.ProviderTypeChatCompletions, "anthropic/claude-fable-5-2", ""},
		{"documented connection", "moonshotai/kimi-k3", "", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("CHORD_CONFIG_HOME", dir)
			opts := configAddOptions{catalogID: tc.source, variant: tc.variant}
			if tc.protocol != "" {
				path := map[string]string{
					config.ProviderTypeMessages:        "messages",
					config.ProviderTypeResponses:       "responses",
					config.ProviderTypeChatCompletions: "chat/completions",
				}[tc.protocol]
				opts.url = "https://example.invalid/v1/" + path
			}
			if err := runConfigAdd(context.Background(), io.Discard, tc.ref, opts); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "config.yaml")
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			resolved, err := config.LoadResolvedConfig(path, "")
			if err != nil {
				t.Fatal(err)
			}
			providerName, modelName := config.SplitProviderModelRef(tc.ref)
			provider := resolved.Config.Providers[providerName]
			model := provider.Models[modelName]
			if model.Catalog == nil || !model.Catalog.Disabled || model.Limit.Context == 0 || model.Limit.Output == 0 || model.Modalities == nil {
				t.Fatalf("incomplete explicit model: %+v", model)
			}
			if tc.protocol == config.ProviderTypeMessages {
				if model.Thinking == nil || model.Thinking.Type != config.ThinkingTypeAdaptive || model.Thinking.Effort != "" ||
					model.PromptCache == nil || !model.PromptCache.CacheToolsEnabled() || model.Compaction == nil || *model.Compaction.Threshold != 0.7 {
					t.Fatalf("missing Messages profile: %+v", model)
				}
			}
			if tc.protocol == config.ProviderTypeChatCompletions && (model.Thinking != nil || model.PromptCache != nil) {
				t.Fatalf("cross-protocol settings were copied: %+v", model)
			}
			// A new wire name with no catalog binding must still resolve all
			// settings and variants. Explicit URLs make the provider usable too.
			renamed := strings.ReplaceAll(string(raw), modelName, "independent-model")
			independent, err := loadConfigAddCandidate([]byte(renamed))
			if err != nil {
				t.Fatal(err)
			}
			for _, d := range independent.Diagnostics {
				if d.Severity == config.DiagnosticSeverityError {
					t.Fatal(d.String())
				}
			}
			got := independent.Config.Providers[providerName].Models["independent-model"]
			model.Name = got.Name
			if !reflect.DeepEqual(model, got) {
				t.Fatalf("saved settings depend on the catalog binding:\nbefore=%+v\nafter=%+v", model, got)
			}
			if err := runConfigAdd(context.Background(), io.Discard, tc.ref, opts); err != nil {
				t.Fatal(err)
			}
			repeated, err := os.ReadFile(path)
			if err != nil || string(repeated) != string(raw) {
				t.Fatalf("repeated add changed saved settings: %v\n%s", err, repeated)
			}
		})
	}
}

func TestConfigAddReasoningChoicesFromEmptyConfig(t *testing.T) {
	cases := []struct {
		ref, field string
		levels     []string
	}{
		{"anthropic/claude-fable-5-2", "thinking", []string{"low", "medium", "high", "xhigh", "max"}},
		{"anthropic/claude-opus-5-5", "thinking", []string{"low", "medium", "high", "xhigh", "max"}},
		{"google/gemini-4-argon", "level", []string{"low", "medium", "high"}},
		{"inception/mercury-2.5", "reasoning", []string{"instant", "low", "medium", "high"}},
		{"stepfun/step-5-preview", "reasoning", []string{"low", "medium", "high"}},
		{"moonshotai/kimi-k3", "reasoning", []string{"low", "high", "max"}},
		{"zhipuai/glm-5.3", "reasoning", []string{"low", "high", "max"}},
		{"xai/grok-4.6", "reasoning", []string{"low", "medium", "high", "xhigh"}},
		{"meta/muse-spark-1.3", "reasoning", []string{"minimal", "low", "medium", "high", "xhigh", "max"}},
		{"openai/gpt-6.1-sol", "reasoning", []string{"low", "medium", "high", "xhigh", "max"}},
		{"codex/gpt-5.6-sol", "reasoning", []string{"low", "high", "xhigh"}},
	}
	for _, tc := range cases {
		t.Run(tc.ref, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("CHORD_CONFIG_HOME", dir)
			if err := runConfigAdd(context.Background(), io.Discard, tc.ref, configAddOptions{variant: "high"}); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "config.yaml")
			rc, err := config.LoadResolvedConfig(path, "")
			if err != nil {
				t.Fatal(err)
			}
			provider, name := config.SplitProviderModelRef(tc.ref)
			model := rc.Config.Providers[provider].Models[name]
			if model.Thinking != nil && (model.Thinking.Effort != "" || model.Thinking.Level != "") ||
				model.Reasoning != nil && model.Reasoning.Effort != "" {
				t.Fatalf("base effort must remain unset: thinking=%+v reasoning=%+v", model.Thinking, model.Reasoning)
			}
			if len(model.Variants) != len(tc.levels) {
				t.Fatalf("variants=%v, want %v", model.Variants, tc.levels)
			}
			for _, level := range tc.levels {
				variant := model.Variants[level]
				var got string
				switch tc.field {
				case "thinking":
					if variant.Thinking != nil {
						got = variant.Thinking.Effort
					}
				case "level":
					if variant.Thinking != nil {
						got = variant.Thinking.Level
					}
				case "reasoning":
					if variant.Reasoning != nil {
						got = variant.Reasoning.Effort
					}
				}
				if got != level {
					t.Fatalf("%s: variant=%+v", level, variant)
				}
			}
			if rc.Config.ModelPools["default"][0] != tc.ref+"@high" {
				t.Fatal("selected variant was not saved")
			}
			first, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := runConfigAdd(context.Background(), io.Discard, tc.ref, configAddOptions{variant: "high"}); err != nil {
				t.Fatal(err)
			}
			second, err := os.ReadFile(path)
			if err != nil || string(first) != string(second) {
				t.Fatalf("repeated add changed config: %v", err)
			}
		})
	}
}

func TestConfigAddPreservesExplicitReasoningChoices(t *testing.T) {
	for _, block := range []string{
		"thinking: {type: adaptive, effort: low}",
		"thinking: null",
		"thinking: {type: enabled, budget: 4096}",
		"thinking: {type: adaptive}\n        variants: null",
		"thinking: {type: adaptive}\n        variants: {high: {thinking: {effort: max}}}",
	} {
		t.Run(block, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("CHORD_CONFIG_HOME", dir)
			path := filepath.Join(dir, "config.yaml")
			raw := []byte("providers:\n  sample:\n    type: messages\n    api_url: https://example.invalid/v1/messages\n    models:\n      model-1:\n        " + block + "\n")
			if err := os.WriteFile(path, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := runConfigAdd(context.Background(), io.Discard, "sample/model-1", configAddOptions{catalogID: "anthropic/claude-opus-5-5"}); err != nil {
				t.Fatal(err)
			}
			saved, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			for _, field := range []string{"thinking", "variants"} {
				keys := []string{"providers", "sample", "models", "model-1", field}
				before := configAddYAMLValue(t, raw, keys...)
				if strings.Contains(block, field+":") && !reflect.DeepEqual(before, configAddYAMLValue(t, saved, keys...)) {
					// Generated sibling variants may be added, but a user override
					// of a named choice must retain its exact effective value.
					if field == "variants" && strings.Contains(block, "variants: {high:") {
						keys = append(keys, "high")
						if reflect.DeepEqual(configAddYAMLValue(t, raw, keys...), configAddYAMLValue(t, saved, keys...)) {
							continue
						}
					}
					t.Fatalf("%s override changed:\n%s", field, saved)
				}
			}
		})
	}
}

func TestConfigAddPreservesNullVariants(t *testing.T) {
	for _, tc := range []struct {
		name, declaration string
	}{
		{"literal", "variants: null"},
		{"alias", "variants: *clear"},
		{"merged literal", "<<: {variants: null}"},
		{"merged alias", "<<: *shared"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("CHORD_CONFIG_HOME", dir)
			path := filepath.Join(dir, "config.yaml")
			raw := []byte(`model_templates:
  clear: &clear null
  shared: &shared
    variants: *clear
providers:
  sample:
    type: messages
    api_url: https://example.invalid/v1/messages
    models:
      model-1:
        ` + tc.declaration + `
      model-2: *shared
`)
			if err := os.WriteFile(path, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			before, err := config.LoadResolvedConfig(path, "")
			if err != nil {
				t.Fatal(err)
			}
			if variants := before.Config.Providers["sample"].Models["model-1"].Variants; variants != nil {
				t.Fatalf("initial variants = %v, want nil", variants)
			}
			if err = runConfigAdd(context.Background(), io.Discard, "sample/model-1", configAddOptions{catalogID: "anthropic/claude-opus-5-5"}); err != nil {
				t.Fatal(err)
			}
			after, err := config.LoadResolvedConfig(path, "")
			if err != nil {
				t.Fatal(err)
			}
			if variants := after.Config.Providers["sample"].Models["model-1"].Variants; variants != nil {
				t.Errorf("saved variants = %v, want nil", variants)
			}
			saved, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			model := configAddYAMLValue(t, saved, "providers", "sample", "models", "model-1").(map[string]any)
			if variants, declared := model["variants"]; !declared || variants != nil {
				t.Errorf("explicit null declaration changed: declared=%v variants=%v", declared, variants)
			}
			for _, keys := range [][]string{
				{"model_templates"},
				{"providers", "sample", "models", "model-2"},
			} {
				if !reflect.DeepEqual(configAddYAMLValue(t, raw, keys...), configAddYAMLValue(t, saved, keys...)) {
					t.Errorf("shared settings changed at %v", keys)
				}
			}
		})
	}
}

func TestConfigAddMaterializesSharedOverridesAndResponsesFields(t *testing.T) {
	original := []byte(`model_templates:
  shared: &shared
    limit: {context: 200000, output: 32000}
    reasoning: null
    compaction: null
    cost: {input: 2, output: 4}
    store: false
    compat:
      responses:
        send_tool_choice: false
providers:
  sample:
    preset: openai
    compat:
      responses:
        send_store: false
    models:
      model-1: *shared
      model-2: *shared
`)
	dir := t.TempDir()
	t.Setenv("CHORD_CONFIG_HOME", dir)
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	opts := configAddOptions{catalogID: "openai/gpt-6.1-sol"}
	edit := configAddEdit{providerName: "sample", wireModel: "model-1", borrowID: opts.catalogID, poolName: "default", poolRef: "sample/model-1"}
	draft, err := editConfigYAMLForAdd(original, edit)
	if err != nil {
		t.Fatal(err)
	}
	before, err := loadConfigAddCandidate(draft)
	if err != nil {
		t.Fatal(err)
	}
	provider := before.Config.Providers["sample"]
	wantFields, _ := config.ResolveResponsesCompat(provider.Preset, "model-1", provider.Models["model-1"], provider.Compat)
	if err := runConfigAdd(context.Background(), io.Discard, "sample/model-1", opts); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, preserved := range [][]string{
		{"model_templates", "shared"},
		{"providers", "sample", "models", "model-2"},
		{"providers", "sample", "compat"},
	} {
		if !reflect.DeepEqual(configAddYAMLValue(t, original, preserved...), configAddYAMLValue(t, raw, preserved...)) {
			t.Fatalf("shared settings changed at %v:\n%s", preserved, raw)
		}
	}
	after, err := config.LoadResolvedConfig(path, "")
	if err != nil {
		t.Fatal(err)
	}
	provider = after.Config.Providers["sample"]
	model := provider.Models["model-1"]
	gotFields, sources := config.ResolveResponsesCompat(provider.Preset, "model-1", model, provider.Compat)
	if !reflect.DeepEqual(wantFields, gotFields) || sources["send_store"] != "provider" || sources["send_tool_choice"] != "model" {
		t.Fatalf("Responses contract changed: want=%+v got=%+v sources=%v", wantFields, gotFields, sources)
	}
	if model.Reasoning != nil || model.Compaction != nil || model.Limit.Context != 200000 || model.Limit.Output != 32000 || model.Cost.Input != 2 || model.Store == nil || *model.Store {
		t.Fatalf("explicit overrides changed: %+v", model)
	}
}

func TestConfigAddMaterializesResponsesFieldsThroughNull(t *testing.T) {
	for _, block := range []string{"compat: null", "compat: {responses: null}"} {
		t.Run(block, func(t *testing.T) {
			raw := []byte("providers:\n  sample:\n    preset: openai\n    models:\n      model-1:\n        " + block + "\n")
			edit := configAddEdit{providerName: "sample", wireModel: "model-1", borrowID: "openai/gpt-6.1-sol", poolName: "default", poolRef: "sample/model-1"}
			draft, err := editConfigYAMLForAdd(raw, edit)
			if err != nil {
				t.Fatal(err)
			}
			before, err := loadConfigAddCandidate(draft)
			if err != nil {
				t.Fatal(err)
			}
			provider := before.Config.Providers["sample"]
			want, _ := config.ResolveResponsesCompat(provider.Preset, edit.wireModel, provider.Models[edit.wireModel], provider.Compat)
			saved, err := materializeConfigAdd(draft, before, edit)
			if err != nil {
				t.Fatal(err)
			}
			after, err := loadConfigAddCandidate(saved)
			if err != nil {
				t.Fatal(err)
			}
			provider = after.Config.Providers["sample"]
			got, _ := config.ResolveResponsesCompat(provider.Preset, edit.wireModel, provider.Models[edit.wireModel], provider.Compat)
			if !reflect.DeepEqual(want, got) {
				t.Fatalf("null block erased preset Responses contract: before=%+v after=%+v", want, got)
			}
		})
	}
}
