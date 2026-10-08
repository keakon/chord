package main

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/keakon/chord/internal/config"
)

func TestCatalogAdvisoryPinPlansOrder(t *testing.T) {
	plans := catalogAdvisoryPinPlans()
	want := []catalogAdvisoryPinLevel{
		catalogAdvisoryPinDeclaration,
		catalogAdvisoryPinModelTemplate,
		catalogAdvisoryPinBinding,
	}
	if len(plans) != len(want) {
		t.Fatalf("plans = %+v", plans)
	}
	for i, plan := range plans {
		if plan.Level != want[i] {
			t.Fatalf("plan %d level = %q, want %q", i, plan.Level, want[i])
		}
	}
}

func TestCatalogAdvisoryDeclarationPinReplacesDeclaredScalar(t *testing.T) {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(`providers:
  openai:
    models:
      gpt-6.1-sol:
        <<: &shared
          compaction:
            threshold: 0.8
`), &doc); err != nil {
		t.Fatal(err)
	}
	advisory := config.CatalogConfigAdvisory{
		Provider: "openai", Model: "gpt-6.1-sol", Field: "compaction.threshold",
		Current: 0.8, Recommended: 0.25,
		CurrentOrigin: config.CatalogAdvisoryOrigin{Layer: config.OriginLayerGlobal, File: "config.yaml", Line: 7, Col: 13},
	}
	detail, err := applyCatalogAdvisoryDeclarationPin(&doc, advisory, "config.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(detail, "config.yaml:7") {
		t.Fatalf("detail = %q", detail)
	}
	edited, err := encodeConfigYAMLDocument(&doc)
	if err != nil {
		t.Fatal(err)
	}
	if text := string(edited); !strings.Contains(text, "threshold: 0.25") || strings.Contains(text, "threshold: 0.8") {
		t.Fatalf("config = %s", text)
	}
}

func TestCatalogAdvisoryModelTemplatePinKeepsSiblingLeaves(t *testing.T) {
	raw := []byte(`model_templates:
  "base": &base
    compaction: {threshold: 0.8, reminder: 0.24}
  "models": &models
    gpt-6.1-sol: &sol
      <<: *base
providers:
  openai:
    preset: openai
    models: *models
`)
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	advisory := config.CatalogConfigAdvisory{
		Provider: "openai", Model: "gpt-6.1-sol", Field: "compaction.threshold",
		Current: 0.8, Recommended: 0.25,
	}
	plans := catalogAdvisoryPinPlans()
	detail, err := plans[1].Apply(&doc, advisory, "config.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(detail, "config.yaml:") {
		t.Fatalf("detail = %q", detail)
	}
	edited, err := encodeConfigYAMLDocument(&doc)
	if err != nil {
		t.Fatal(err)
	}
	text := string(edited)
	if strings.Contains(text, "!!merge") {
		t.Fatalf("merge keys must keep their canonical form: %s", text)
	}
	if !strings.Contains(text, "threshold: 0.25") || !strings.Contains(text, "reminder: 0.24") {
		t.Fatalf("override must keep the sibling leaf: %s", text)
	}
	if strings.Count(text, "threshold: 0.8") != 1 {
		t.Fatalf("the shared base block must stay unchanged: %s", text)
	}
}
