package config

import (
	"encoding/json"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/keakon/chord/internal/modelcatalog"
)

func TestGenerateContentCompatResolution(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		provider := &ProviderCompatConfig{GenerateContent: &GenerateContentCompatConfig{AutomaticContinuation: new(!enabled), SingleRequestOutputLimit: new(65536)}}
		model := ModelConfig{Compat: &ModelCompatConfig{GenerateContent: &GenerateContentCompatConfig{AutomaticContinuation: new(enabled)}}}
		got := ResolveGenerateContentCompat(provider, model)
		if *got.AutomaticContinuation != enabled || *got.SingleRequestOutputLimit != 65536 {
			t.Fatalf("resolved=%+v", got)
		}
		*got.AutomaticContinuation = !enabled
		*got.SingleRequestOutputLimit = 1
		if *model.Compat.GenerateContent.AutomaticContinuation != enabled || *provider.GenerateContent.SingleRequestOutputLimit != 65536 {
			t.Fatal("snapshot aliases source")
		}
	}
}
func TestGenerateContentCompatRoundTripAndStrictValidation(t *testing.T) {
	value := ModelConfig{Compat: &ModelCompatConfig{GenerateContent: &GenerateContentCompatConfig{AutomaticContinuation: new(false), SingleRequestOutputLimit: new(65536)}}}
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var got ModelConfig
	if err = json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.Compat.GenerateContent.AutomaticContinuation == nil || *got.Compat.GenerateContent.AutomaticContinuation {
		t.Fatal("false lost")
	}
	y, err := yaml.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err = yaml.Unmarshal(y, &got); err != nil {
		t.Fatal(err)
	}
	for _, leaf := range []map[string]any{{"automatic_continuation": "true"}, {"single_request_output_limit": 0}, {"single_request_output_limit": -1}, {"automatic_continution": true}} {
		if _, err := DecodeCatalogProfile(&modelcatalog.ConfigProfile{Compat: map[string]any{"generate_content": leaf}}); err == nil {
			t.Fatalf("accepted invalid leaf %v", leaf)
		}
	}
	if _, err := DecodeCatalogProfile(&modelcatalog.ConfigProfile{Compat: map[string]any{"generate_content": map[string]any{"automatic_continuation": false, "single_request_output_limit": 65536}}}); err != nil {
		t.Fatal(err)
	}
	decoder := yaml.NewDecoder(strings.NewReader("compat:\n  generate_content:\n    automatic_continuation: 'true'\n"))
	decoder.KnownFields(true)
	if err := decoder.Decode(&got); err == nil {
		t.Fatal("accepted string as bool")
	}
}
func TestGenerateContentCompatReset(t *testing.T) {
	cfg := ProviderConfig{Compat: &ProviderCompatConfig{GenerateContent: &GenerateContentCompatConfig{AutomaticContinuation: new(true), SingleRequestOutputLimit: new(-1)}}}
	if ValidateProviderGenerateContentCompat("sample", cfg) == nil {
		t.Fatal("accepted invalid limit")
	}
	result := resetInvalidProviderFields(cfg)
	if result.Compat.GenerateContent.SingleRequestOutputLimit != nil || !*result.Compat.GenerateContent.AutomaticContinuation {
		t.Fatal("incorrect reset")
	}
	if cfg.Compat.GenerateContent.SingleRequestOutputLimit == nil {
		t.Fatal("mutated source")
	}
}
