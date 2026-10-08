package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/config"
)

func TestEditConfigYAMLForCatalogAdvisoryPinsValue(t *testing.T) {
	raw := []byte(`providers:
  openai:
    models:
      model-1:
        reasoning:
          summary: none
`)
	advisory := config.CatalogConfigAdvisory{
		Provider: "openai", Model: "model-1", Field: "reasoning.summary", Recommended: "auto",
	}
	edited, err := editConfigYAMLForCatalogAdvisory(raw, advisory, "pin")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(edited), "summary: auto") {
		t.Fatalf("edited YAML = %s", edited)
	}
	if !strings.Contains(string(edited), "providers:") {
		t.Fatalf("edited YAML lost config structure: %s", edited)
	}
}

func TestEditConfigYAMLForCatalogAdvisaryFollowsCatalog(t *testing.T) {
	raw := []byte(`providers:
  openai:
    models:
      model-1:
        reasoning:
          summary: none
`)
	advisory := config.CatalogConfigAdvisory{Provider: "openai", Model: "model-1", Field: "reasoning.summary"}
	edited, err := editConfigYAMLForCatalogAdvisory(raw, advisory, "follow-catalog")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(edited), "summary:") {
		t.Fatalf("edited YAML = %s", edited)
	}
}

func TestRenderConfigAdvisoriesJSON(t *testing.T) {
	var out bytes.Buffer
	err := renderConfigAdvisories(&out, []config.CatalogConfigAdvisory{{
		Provider: "openai", Model: "model-1", Field: "reasoning.summary",
		Path:    "providers.openai.models.model-1.reasoning.summary",
		Current: "none", Recommended: "auto", CatalogID: "openai/model-1",
	}}, true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"recommended": "auto"`) {
		t.Fatalf("JSON = %s", out.String())
	}
}

func TestRunConfigAdviseKeepsCurrentValue(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("CHORD_CONFIG_HOME", configHome)
	configPath := filepath.Join(configHome, "config.yaml")
	if err := os.WriteFile(configPath, []byte(`providers:
  openai:
    preset: openai
    models:
      gpt-6.1-sol:
        reasoning: {summary: none}
model_pools:
  default: [openai/gpt-6.1-sol]
`), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := runConfigAdvise(t.Context(), &out, []string{"openai/gpt-6.1-sol", "reasoning.summary"}, configAdviseOptions{Keep: true}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Kept current value") {
		t.Fatalf("output = %s", out.String())
	}
	if got, err := os.ReadFile(configPath); err != nil || !strings.Contains(string(got), "summary: none") {
		t.Fatalf("config = %s, err=%v", got, err)
	}
}

func TestRunConfigAdvisePinsRecommendedValue(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("CHORD_CONFIG_HOME", configHome)
	configPath := filepath.Join(configHome, "config.yaml")
	if err := os.WriteFile(configPath, []byte(`providers:
  openai:
    preset: openai
    models:
      gpt-6.1-sol:
        reasoning: {summary: none}
model_pools:
  default: [openai/gpt-6.1-sol]
`), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := runConfigAdvise(t.Context(), &out, []string{"openai/gpt-6.1-sol", "reasoning.summary"}, configAdviseOptions{Accept: "pin"}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), "summary: auto") {
		t.Fatalf("config = %s", got)
	}
}

func TestRenderConfigAdvisoriesNamesInheritedSource(t *testing.T) {
	var out bytes.Buffer
	err := renderConfigAdvisories(&out, []config.CatalogConfigAdvisory{{
		Provider: "openai", Model: "model-1", Field: "compaction.threshold",
		Path:          "providers.openai.models.model-1.compaction.threshold",
		Current:       0.8,
		Recommended:   0.25,
		CatalogID:     "openai/model-1",
		CurrentOrigin: config.CatalogAdvisoryOrigin{File: "/tmp/config.yaml", Line: 7},
	}}, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := out.String(); !strings.Contains(got, "accept: chord config advise openai/model-1 compaction.threshold --accept pin") ||
		!strings.Contains(got, "fix:    set 0.25 by expanding the inherited entry to add an explicit override under providers.openai.models.model-1.compaction.threshold") ||
		!strings.Contains(got, "/tmp/config.yaml:7") ||
		strings.Contains(got, "edit the direct YAML value") {
		t.Fatalf("output = %s", got)
	}
}

func TestRenderConfigAdvisoriesGroupsSharedDeclaration(t *testing.T) {
	advisories := []config.CatalogConfigAdvisory{
		{
			Provider: "openai", Model: "model-1", Field: "compaction.threshold",
			Path:          "providers.openai.models.model-1.compaction.threshold",
			Current:       0.8,
			Recommended:   0.25,
			CatalogID:     "openai/model-1",
			CurrentOrigin: config.CatalogAdvisoryOrigin{File: "/tmp/config.yaml", Line: 3},
		},
		{
			Provider: "openai2", Model: "model-1", Field: "compaction.threshold",
			Path:          "providers.openai2.models.model-1.compaction.threshold",
			Current:       0.8,
			Recommended:   0.25,
			CatalogID:     "openai/model-1",
			CurrentOrigin: config.CatalogAdvisoryOrigin{File: "/tmp/config.yaml", Line: 3},
		},
	}
	var out bytes.Buffer
	if err := renderConfigAdvisories(&out, advisories, false); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if strings.Count(got, advisories[0].Path+":") != 1 {
		t.Fatalf("output must list the shared declaration once: %s", got)
	}
	if !strings.Contains(got, "shared: /tmp/config.yaml:3 (openai/model-1, openai2/model-1)") ||
		!strings.Contains(got, "(keeps all 2)") {
		t.Fatalf("output = %s", got)
	}
	for _, want := range []string{
		"Apply every recommendation: chord config advise --accept pin",
		"Keep every current value:   chord config advise --keep-current",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("listing must suggest the batch command %q: %s", want, got)
		}
	}
}

func TestRunConfigAdviseKeepCurrentFansOutSharedDeclaration(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("CHORD_CONFIG_HOME", configHome)
	configPath := filepath.Join(configHome, "config.yaml")
	data := []byte(`model_templates:
  "gpt-base": &gpt-base
    compaction:
      threshold: 0.8
  "gpt-models": &gpt-models
    gpt-6.1-sol:
      <<: *gpt-base
providers:
  openai:
    preset: openai
    models: *gpt-models
  openai2:
    preset: openai
    models: *gpt-models
model_pools:
  default: [openai/gpt-6.1-sol, openai2/gpt-6.1-sol]
`)
	if err := os.WriteFile(configPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := runConfigAdvise(t.Context(), &out, []string{"openai/gpt-6.1-sol", "compaction.threshold"}, configAdviseOptions{Keep: true}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Kept current value for 2 bindings") {
		t.Fatalf("output = %s", out.String())
	}
	if got, err := os.ReadFile(configPath); err != nil || !bytes.Equal(got, data) {
		t.Fatalf("config changed: %s, err=%v", got, err)
	}
	ackData, err := os.ReadFile(filepath.Join(configHome, "model-config-advisories.json"))
	if err != nil {
		t.Fatal(err)
	}
	var state struct {
		Acks []struct {
			Provider string `json:"provider"`
		} `json:"acks"`
	}
	if err := json.Unmarshal(ackData, &state); err != nil {
		t.Fatalf("decode ack file: %v", err)
	}
	if len(state.Acks) != 2 || state.Acks[0].Provider != "openai" || state.Acks[1].Provider != "openai2" {
		t.Fatalf("ack state = %+v, want both bindings", state)
	}
	out.Reset()
	if err := runConfigAdvise(t.Context(), &out, nil, configAdviseOptions{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "No active catalog configuration recommendations.") {
		t.Fatalf("output after acknowledgment = %s", out.String())
	}
}

func TestRunConfigAdvisePinsInheritedValueAtItsDeclaration(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("CHORD_CONFIG_HOME", configHome)
	configPath := filepath.Join(configHome, "config.yaml")
	data := []byte(`providers:
  openai:
    preset: openai
    models:
      gpt-6.1-sol:
        <<: &shared
          compaction: {threshold: 0.8}
model_pools:
  default: [openai/gpt-6.1-sol]
`)
	if err := os.WriteFile(configPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := runConfigAdvise(t.Context(), &out, []string{"openai/gpt-6.1-sol", "compaction.threshold"}, configAdviseOptions{Accept: "pin"}); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); !strings.Contains(got, "Applied pin") || !strings.Contains(got, "updated the declaring value") {
		t.Fatalf("output = %s", got)
	}
	got, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if text := string(got); !strings.Contains(text, "threshold: 0.25") || strings.Contains(text, "threshold: 0.8") {
		t.Fatalf("config = %s", got)
	}
}

func TestRunConfigAdviseFollowCatalogOnInheritedValueSuggestsPin(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("CHORD_CONFIG_HOME", configHome)
	if err := os.WriteFile(filepath.Join(configHome, "config.yaml"), []byte(`providers:
  openai:
    preset: openai
    models:
      gpt-6.1-sol:
        <<: &shared
          compaction: {threshold: 0.8}
model_pools:
  default: [openai/gpt-6.1-sol]
`), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err := runConfigAdvise(t.Context(), &out, []string{"openai/gpt-6.1-sol", "compaction.threshold"}, configAdviseOptions{Accept: "follow-catalog"})
	if err == nil {
		t.Fatal("expected follow-catalog on an inherited value to fail")
	}
	if !strings.Contains(err.Error(), "use --accept pin") || !strings.Contains(err.Error(), "add an explicit override") {
		t.Fatalf("error = %v", err)
	}
}

func TestRunConfigAdvisePinsInheritedValueOnModelTemplate(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("CHORD_CONFIG_HOME", configHome)
	configPath := filepath.Join(configHome, "config.yaml")
	if err := os.WriteFile(configPath, []byte(`model_templates:
  "gpt-base": &gpt-base
    compaction:
      threshold: 0.8
      reminder: 0.24
  "gpt-models": &gpt-models
    gpt-6.1-sol: &gpt-sol
      <<: *gpt-base
    gpt-6-sol:
      <<: *gpt-base
providers:
  openai:
    preset: openai
    models: *gpt-models
  openai2:
    preset: openai
    models: *gpt-models
model_pools:
  default: [openai/gpt-6.1-sol, openai2/gpt-6.1-sol]
`), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := runConfigAdvise(t.Context(), &out, []string{"openai/gpt-6.1-sol", "compaction.threshold"}, configAdviseOptions{Accept: "pin"}); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); !strings.Contains(got, "model template") {
		t.Fatalf("output = %s", got)
	}
	got, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(got)
	if strings.Contains(text, "!!merge") {
		t.Fatalf("merge keys must keep their canonical form: %s", text)
	}
	if !strings.Contains(text, "threshold: 0.25") || !strings.Contains(text, "reminder: 0.24") {
		t.Fatalf("override must keep the sibling leaf: %s", text)
	}
	if !strings.Contains(text, "threshold: 0.8") {
		t.Fatalf("the shared declaration must stay unchanged: %s", text)
	}
	out.Reset()
	if err := runConfigAdvise(t.Context(), &out, nil, configAdviseOptions{}); err != nil {
		t.Fatal(err)
	}
	listing := out.String()
	if strings.Contains(listing, "providers.openai.models.gpt-6.1-sol.compaction.threshold") {
		t.Fatalf("pinned threshold must be resolved: %s", listing)
	}
	if !strings.Contains(listing, "providers.openai.models.gpt-6.1-sol.compaction.reminder") ||
		!strings.Contains(listing, "gpt-6-sol.compaction.threshold") {
		t.Fatalf("unrelated recommendations must remain: %s", listing)
	}
}

func TestRunConfigAdvisePinsInheritedValueByExpandingBinding(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("CHORD_CONFIG_HOME", configHome)
	configPath := filepath.Join(configHome, "config.yaml")
	if err := os.WriteFile(configPath, []byte(`model_templates:
  "gpt-base": &gpt-base
    compaction:
      threshold: 0.8
  "gpt-models": &gpt-models
    gpt-6.1-sol: *gpt-base
    gpt-6-sol: *gpt-base
providers:
  openai:
    preset: openai
    models: *gpt-models
  openai2:
    preset: openai
    models: *gpt-models
model_pools:
  default: [openai/gpt-6.1-sol, openai2/gpt-6.1-sol]
`), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := runConfigAdvise(t.Context(), &out, []string{"openai/gpt-6.1-sol", "compaction.threshold"}, configAdviseOptions{Accept: "pin"}); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); !strings.Contains(got, "expanded providers.openai.models") {
		t.Fatalf("output = %s", got)
	}
	got, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(got)
	for _, want := range []string{"<<: *gpt-models", "<<: *gpt-base", "threshold: 0.25", "threshold: 0.8"} {
		if !strings.Contains(text, want) {
			t.Fatalf("config = %s, want %q", text, want)
		}
	}
	if strings.Contains(text, "!!merge") {
		t.Fatalf("merge keys must keep their canonical form: %s", text)
	}
	if strings.Count(text, "models: *gpt-models") != 1 {
		t.Fatalf("only the edited provider may lose its plain models alias: %s", text)
	}
	out.Reset()
	if err := runConfigAdvise(t.Context(), &out, nil, configAdviseOptions{}); err != nil {
		t.Fatal(err)
	}
	if listing := out.String(); !strings.Contains(listing, "providers.openai2.models.gpt-6.1-sol.compaction.threshold") {
		t.Fatalf("the other binding must keep its recommendation: %s", listing)
	}
}

func TestRunConfigAdvisePinFallsBackToManualHint(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("CHORD_CONFIG_HOME", configHome)
	configPath := filepath.Join(configHome, "config.yaml")
	data := []byte(`model_templates:
  "gpt-base": &gpt-base
    compaction:
      threshold: 0.8
  "gpt-models": &gpt-models
    gpt-6.1-sol: *gpt-base
    gpt-6-sol: *gpt-base
  "provider-base": &provider-base
    preset: openai
    models: *gpt-models
providers:
  openai: *provider-base
model_pools:
  default: [openai/gpt-6.1-sol]
`)
	if err := os.WriteFile(configPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err := runConfigAdvise(t.Context(), &out, []string{"openai/gpt-6.1-sol", "compaction.threshold"}, configAdviseOptions{Accept: "pin"})
	if err == nil {
		t.Fatal("expected no safe automatic write location")
	}
	for _, want := range []string{"cannot pin", "automatically", "set 0.25 by expanding the inherited entry to add an explicit override", "config.yaml:"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %v, want %q", err, want)
		}
	}
	if got, readErr := os.ReadFile(configPath); readErr != nil || !bytes.Equal(got, data) {
		t.Fatalf("config changed: %s, err=%v", got, readErr)
	}
}

// batchAdvisoryConfig is a shared model template whose compaction block is
// inherited by one model of two providers, so every recommendation belongs to
// a shared declaration group.
const batchAdvisoryConfig = `model_templates:
  "gpt-base": &gpt-base
    compaction:
      threshold: 0.8
      reminder: 0.24
  "gpt-models": &gpt-models
    gpt-6.1-sol:
      <<: *gpt-base
providers:
  openai:
    preset: openai
    models: *gpt-models
  openai2:
    preset: openai
    models: *gpt-models
model_pools:
  default: [openai/gpt-6.1-sol, openai2/gpt-6.1-sol]
`

func TestRunConfigAdvisePinsAllRecommendations(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("CHORD_CONFIG_HOME", configHome)
	configPath := filepath.Join(configHome, "config.yaml")
	if err := os.WriteFile(configPath, []byte(batchAdvisoryConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := runConfigAdvise(t.Context(), &out, nil, configAdviseOptions{Accept: "pin"}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(out.String(), "Applied pin"); got != 2 {
		t.Fatalf("one command must resolve both shared declarations, output = %s", out.String())
	}
	got, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(got)
	if !strings.Contains(text, "threshold: 0.25") || !strings.Contains(text, "reminder: 0.2") {
		t.Fatalf("config = %s", text)
	}
	if strings.Contains(text, "threshold: 0.8") || strings.Contains(text, "reminder: 0.24") {
		t.Fatalf("stale values remain: %s", text)
	}
	out.Reset()
	if err := runConfigAdvise(t.Context(), &out, nil, configAdviseOptions{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "No active catalog configuration recommendations.") {
		t.Fatalf("output after batch pin = %s", out.String())
	}
}

func TestRunConfigAdviseKeepsAllRecommendations(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("CHORD_CONFIG_HOME", configHome)
	configPath := filepath.Join(configHome, "config.yaml")
	data := []byte(batchAdvisoryConfig)
	if err := os.WriteFile(configPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := runConfigAdvise(t.Context(), &out, nil, configAdviseOptions{Keep: true}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(out.String(), "Kept current value for 2 bindings"); got != 2 {
		t.Fatalf("one command must acknowledge both shared declarations, output = %s", out.String())
	}
	if got, err := os.ReadFile(configPath); err != nil || !bytes.Equal(got, data) {
		t.Fatalf("config changed: %s, err=%v", got, err)
	}
	ackData, err := os.ReadFile(filepath.Join(configHome, "model-config-advisories.json"))
	if err != nil {
		t.Fatal(err)
	}
	var state struct {
		Acks []struct {
			Provider string `json:"provider"`
			Field    string `json:"field"`
		} `json:"acks"`
	}
	if err := json.Unmarshal(ackData, &state); err != nil {
		t.Fatalf("decode ack file: %v", err)
	}
	if len(state.Acks) != 4 || state.Acks[0].Provider != "openai" || state.Acks[0].Field != "compaction.threshold" {
		t.Fatalf("ack state = %+v, want both fields for both bindings", state)
	}
	out.Reset()
	if err := runConfigAdvise(t.Context(), &out, nil, configAdviseOptions{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "No active catalog configuration recommendations.") {
		t.Fatalf("output after batch keep = %s", out.String())
	}
}

func TestRunConfigAdviseBatchOnEmptyState(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("CHORD_CONFIG_HOME", configHome)
	configPath := filepath.Join(configHome, "config.yaml")
	data := []byte(`providers:
  openai:
    preset: openai
    models:
      gpt-6.1-sol: {}
model_pools:
  default: [openai/gpt-6.1-sol]
`)
	if err := os.WriteFile(configPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, opts := range []configAdviseOptions{{Accept: "pin"}, {Keep: true}} {
		var out bytes.Buffer
		if err := runConfigAdvise(t.Context(), &out, nil, opts); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "No active catalog configuration recommendations.") {
			t.Fatalf("output = %s", out.String())
		}
	}
	if got, err := os.ReadFile(configPath); err != nil || !bytes.Equal(got, data) {
		t.Fatalf("config changed: %s, err=%v", got, err)
	}
	if _, err := os.Stat(filepath.Join(configHome, "model-config-advisories.json")); !os.IsNotExist(err) {
		t.Fatalf("empty keep must not write an acknowledgment file, err=%v", err)
	}
}

func TestRunConfigAdviseFollowCatalogAllRejectsInheritedValue(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("CHORD_CONFIG_HOME", configHome)
	configPath := filepath.Join(configHome, "config.yaml")
	data := []byte(batchAdvisoryConfig)
	if err := os.WriteFile(configPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err := runConfigAdvise(t.Context(), &out, nil, configAdviseOptions{Accept: "follow-catalog"})
	if err == nil {
		t.Fatal("expected follow-catalog on inherited values to fail")
	}
	for _, want := range []string{"cannot follow the catalog for providers.openai.models.gpt-6.1-sol.compaction.threshold", "use --accept pin"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %v, want %q", err, want)
		}
	}
	if got, readErr := os.ReadFile(configPath); readErr != nil || !bytes.Equal(got, data) {
		t.Fatalf("config changed: %s, err=%v", got, readErr)
	}
}

// Pinning a value inherited from a template that another model consumes
// without a recommendation must not rewrite the shared declaration: the
// override has to land on the selected model, or the unrecommended consumer
// changes silently.
func TestRunConfigAdvisePinKeepsUnrecommendedConsumer(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("CHORD_CONFIG_HOME", configHome)
	configPath := filepath.Join(configHome, "config.yaml")
	if err := os.WriteFile(configPath, []byte(`model_templates:
  "gpt-base": &gpt-base
    compaction: {threshold: 0.8}
providers:
  openai:
    preset: openai
    models:
      gpt-6.1-sol:
        <<: *gpt-base
      custom-model:
        <<: *gpt-base
        limit: {context: 100000, output: 10000}
model_pools:
  default: [openai/gpt-6.1-sol, openai/custom-model]
`), 0o600); err != nil {
		t.Fatal(err)
	}
	threshold := func(t *testing.T, rc *config.ResolvedConfig, model string) float64 {
		t.Helper()
		value := rc.Config.Providers["openai"].Models[model].Compaction.Threshold
		if value == nil {
			t.Fatalf("model %q has no resolved compaction threshold", model)
		}
		return *value
	}
	before, err := config.LoadResolvedConfig(configPath, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, diagnostic := range before.Diagnostics {
		if diagnostic.Severity == config.DiagnosticSeverityError {
			t.Fatalf("invalid fixture: %+v", diagnostic)
		}
	}
	var out bytes.Buffer
	if err := runConfigAdvise(t.Context(), &out, []string{"openai/gpt-6.1-sol", "compaction.threshold"}, configAdviseOptions{Accept: "pin"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "model template") {
		t.Fatalf("expected a model-local override, output = %s", out.String())
	}
	after, err := config.LoadResolvedConfig(configPath, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := threshold(t, after, "gpt-6.1-sol"); got != 0.25 {
		t.Fatalf("selected model threshold = %v, want 0.25", got)
	}
	if got, want := threshold(t, after, "custom-model"), threshold(t, before, "custom-model"); got != want {
		t.Fatalf("consumer without a recommendation changed: %v -> %v", want, got)
	}
}

// A directly declared value that another model inherits through a merge key
// must not be edited in place: the consumer would silently take a value its
// own catalog entry does not recommend.
func TestRunConfigAdviseRejectsSharedDirectDeclaration(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("CHORD_CONFIG_HOME", configHome)
	configPath := filepath.Join(configHome, "config.yaml")
	data := []byte(`providers:
  openai:
    preset: openai
    models:
      gpt-6.1-sol: &shared
        compaction: {threshold: 0.8}
      gpt-6-sol:
        <<: *shared
model_pools:
  default: [openai/gpt-6.1-sol, openai/gpt-6-sol]
`)
	if err := os.WriteFile(configPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, accept := range []string{"pin", "follow-catalog"} {
		var out bytes.Buffer
		err := runConfigAdvise(t.Context(), &out, []string{"openai/gpt-6.1-sol", "compaction.threshold"}, configAdviseOptions{Accept: accept})
		if err == nil {
			t.Fatalf("accept %q must reject the shared declaration, output = %s", accept, out.String())
		}
		if !strings.Contains(err.Error(), "gpt-6-sol") {
			t.Fatalf("accept %q error = %v, want the affected consumer named", accept, err)
		}
	}
	if got, readErr := os.ReadFile(configPath); readErr != nil || !bytes.Equal(got, data) {
		t.Fatalf("config changed: %s, err=%v", got, readErr)
	}
}

// A model mapping aliased by a provider binding without a recommendation must
// not receive the model-template override: the edit would change a consumer
// the candidate validation cannot see. The pin has to fall to the binding
// level and leave the unrecommended consumer untouched.
func TestRunConfigAdvisePinKeepsUnrecommendedSharedModelBinding(t *testing.T) {
	configHome := t.TempDir()
	t.Setenv("CHORD_CONFIG_HOME", configHome)
	configPath := filepath.Join(configHome, "config.yaml")
	if err := os.WriteFile(configPath, []byte(`model_templates:
  "gpt-base": &gpt-base
    compaction: {threshold: 0.8}
  "gpt-models": &gpt-models
    gpt-6.1-sol:
      <<: *gpt-base
      limit: {context: 100000, output: 10000}
providers:
  openai:
    preset: openai
    models: *gpt-models
  sample:
    type: responses
    api_url: https://example.invalid/v1/responses
    models: *gpt-models
model_pools:
  default: [openai/gpt-6.1-sol, sample/gpt-6.1-sol]
`), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := config.LoadResolvedConfig(configPath, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, diagnostic := range before.Diagnostics {
		if diagnostic.Severity == config.DiagnosticSeverityError {
			t.Fatalf("invalid fixture: %+v", diagnostic)
		}
	}
	for _, advisory := range config.CatalogConfigAdvisories(before) {
		if advisory.Provider == "sample" {
			t.Fatalf("custom provider unexpectedly has a catalog recommendation: %+v", advisory)
		}
	}
	threshold := func(rc *config.ResolvedConfig, provider, model string) float64 {
		t.Helper()
		value := rc.Config.Providers[provider].Models[model].Compaction.Threshold
		if value == nil {
			t.Fatalf("%s/%s has no resolved compaction threshold", provider, model)
		}
		return *value
	}
	var out bytes.Buffer
	if err := runConfigAdvise(t.Context(), &out, []string{"openai/gpt-6.1-sol", "compaction.threshold"}, configAdviseOptions{Accept: "pin"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "expanded providers.openai.models") {
		t.Fatalf("expected a binding-local override, output = %s", out.String())
	}
	after, err := config.LoadResolvedConfig(configPath, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := threshold(after, "openai", "gpt-6.1-sol"); got != 0.25 {
		t.Fatalf("selected binding threshold = %v, want 0.25", got)
	}
	if got, want := threshold(after, "sample", "gpt-6.1-sol"), threshold(before, "sample", "gpt-6.1-sol"); got != want {
		t.Fatalf("unrecommended shared binding changed: %v -> %v", want, got)
	}
}
