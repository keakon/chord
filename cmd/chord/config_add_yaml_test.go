package main

import (
	"bytes"
	"reflect"
	"testing"

	"gopkg.in/yaml.v3"
)

func configAddYAMLValue(t *testing.T, raw []byte, path ...string) any {
	t.Helper()
	var root map[string]any
	if err := yaml.Unmarshal(raw, &root); err != nil {
		t.Fatalf("edited YAML does not decode: %v\n%s", err, raw)
	}
	var value any = root
	for _, key := range path {
		mapping, ok := value.(map[string]any)
		if !ok {
			t.Fatalf("path %v contains a non-mapping value: %#v", path, value)
		}
		value = mapping[key]
	}
	return value
}

func TestConfigAddYAMLSharedSettingsRemainIndependent(t *testing.T) {
	cases := []struct {
		name     string
		original string
		preserve []string
	}{
		{"template alias", `model_templates:
  base: &base
    limit: {context: 200000, output: 32000}
providers:
  sample:
    models:
      model-1: *base
      model-2: *base
`, []string{"providers", "sample", "models", "model-2"}},
		{"edited model anchor", `providers:
  sample:
    models:
      model-1: &base
        limit: {context: 200000, output: 32000}
      model-2: *base
`, []string{"providers", "sample", "models", "model-2"}},
		{"edited provider anchor", `providers:
  sample: &provider
    type: responses
    api_url: https://example.invalid/v1/responses
    models:
      model-1: {limit: {context: 200000, output: 32000}}
  peer: *provider
`, []string{"providers", "peer"}},
		{"inherited models", `model_templates:
  provider: &provider
    type: responses
    models:
      model-1: {limit: {context: 200000, output: 32000}}
providers:
  sample: {<<: *provider}
  peer: {<<: *provider}
`, []string{"providers", "peer"}},
		{"inherited providers", `model_templates:
  providers: &providers
    sample:
      models:
        model-1: {limit: {context: 200000, output: 32000}}
    peer: {type: responses}
providers: {<<: *providers}
`, []string{"model_templates", "providers"}},
		{"merge precedence", `model_templates:
  first: &first
    limit: {context: 200000, output: 32000}
    reasoning: {effort: high}
  second: &second
    limit: {context: 100000, output: 16000}
    text: {verbosity: low}
providers:
  sample:
    models:
      model-1: {<<: [*first, *second]}
      model-2: {<<: [*first, *second]}
`, []string{"providers", "sample", "models", "model-2"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			original := []byte(tc.original)
			edit := configAddEdit{providerName: "sample", wireModel: "model-1", borrowID: "openai/gpt-6-sol", poolName: "coding", poolRef: "sample/model-1"}
			edited, err := editConfigYAMLForAdd(original, edit)
			if err != nil {
				t.Fatal(err)
			}
			if before, after := configAddYAMLValue(t, original, tc.preserve...), configAddYAMLValue(t, edited, tc.preserve...); !reflect.DeepEqual(before, after) {
				t.Fatalf("shared settings changed: before=%#v after=%#v\n%s", before, after, edited)
			}
			limitPath := []string{"providers", "sample", "models", "model-1", "limit"}
			if !reflect.DeepEqual(configAddYAMLValue(t, original, limitPath...), configAddYAMLValue(t, edited, limitPath...)) {
				t.Fatalf("existing model limits changed:\n%s", edited)
			}
			if got := configAddYAMLValue(t, edited, "providers", "sample", "models", "model-1", "catalog"); got != edit.borrowID {
				t.Fatalf("catalog not added: %v", got)
			}
			repeated, err := editConfigYAMLForAdd(edited, edit)
			if err != nil || !bytes.Equal(edited, repeated) {
				t.Fatalf("repeat edit must be idempotent: %v\n%s", err, repeated)
			}
		})
	}
}

func TestConfigAddYAMLSharedPools(t *testing.T) {
	for _, selected := range []string{"default", "review"} {
		original := []byte("model_pools:\n  default: &pool [sample/model-1]\n  review: *pool\n")
		edited, err := editConfigYAMLForAdd(original, configAddEdit{providerName: "sample", wireModel: "model-2", poolName: selected, poolRef: "sample/model-2"})
		if err != nil {
			t.Fatal(err)
		}
		other := "default"
		if selected == other {
			other = "review"
		}
		if got := configAddYAMLValue(t, edited, "model_pools", selected); !reflect.DeepEqual(got, []any{"sample/model-1", "sample/model-2"}) {
			t.Fatalf("selected pool: %#v", got)
		}
		if got := configAddYAMLValue(t, edited, "model_pools", other); !reflect.DeepEqual(got, []any{"sample/model-1"}) {
			t.Fatalf("other pool changed: %#v", got)
		}
	}
}

func TestConfigAddYAMLSharedCompression(t *testing.T) {
	original := []byte("providers:\n  sample:\n    compress: &compression gzip\n  peer:\n    compress: *compression\n")
	edited, err := editConfigYAMLForAdd(original, configAddEdit{providerName: "sample", wireModel: "model-1", compress: "off", poolName: "default", poolRef: "sample/model-1"})
	if err != nil {
		t.Fatal(err)
	}
	if got := configAddYAMLValue(t, edited, "providers", "peer", "compress"); got != "gzip" {
		t.Fatalf("peer compression changed: %v", got)
	}
	if got := configAddYAMLValue(t, edited, "providers", "sample", "compress"); got != "" {
		t.Fatalf("selected compression was not disabled: %v", got)
	}
}

func TestConfigAddYAMLRecursiveAliasesRejected(t *testing.T) {
	original := []byte("providers:\n  sample: &self\n    models: *self\n")
	if _, err := editConfigYAMLForAdd(original, configAddEdit{providerName: "sample", wireModel: "model-1", borrowID: "openai/gpt-6-sol", poolName: "default", poolRef: "sample/model-1"}); err == nil {
		t.Fatal("recursive aliases must fail without writing")
	}
}
