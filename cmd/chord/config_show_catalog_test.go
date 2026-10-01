package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/modelcatalog"
)

func writeCatalogShowConfig(t *testing.T, global, project string) *config.ResolvedConfig {
	t.Helper()
	dir := t.TempDir()
	globalPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(globalPath, []byte(global), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	projectPath := ""
	if project != "" {
		projectPath = filepath.Join(dir, "project.yaml")
		if err := os.WriteFile(projectPath, []byte(project), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
	}
	rc, err := config.LoadResolvedConfig(globalPath, projectPath)
	if err != nil {
		t.Fatalf("LoadResolvedConfig: %v", err)
	}
	return rc
}

func TestRenderConfigShowCatalogMarksConfigured(t *testing.T) {
	rc := writeCatalogShowConfig(t, `providers:
  lab:
    preset: openai
    type: responses
    api_url: https://example.invalid/v1/responses
    models:
      gpt-6.1-sol: {}
model_pools:
  default:
    - lab/gpt-6-sol
`, "")

	var buf bytes.Buffer
	if err := renderConfigShowCatalog(&buf, configShowOptions{}, rc); err != nil {
		t.Fatalf("renderConfigShowCatalog: %v", err)
	}
	text := buf.String()
	for _, want := range []string{
		"Built-in model catalog, version " + modelcatalog.Version(),
		"Endpoints:",
		"openai",
		"Models (configured = defined or referenced by a pool in the effective config):",
		"openai / gpt-6.1-sol  [configured]",
		"openai / gpt-6-sol  [configured]",
		"codex / gpt-5.6-sol  [not configured]",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("catalog text missing %q:\n%s", want, text)
		}
	}
}

func TestRenderConfigShowCatalogWithoutConfig(t *testing.T) {
	rc := writeCatalogShowConfig(t, "", "")

	var buf bytes.Buffer
	if err := renderConfigShowCatalog(&buf, configShowOptions{}, rc); err != nil {
		t.Fatalf("renderConfigShowCatalog: %v", err)
	}
	text := buf.String()
	if !strings.Contains(text, "openai / gpt-6.1-sol  [not configured]") {
		t.Fatalf("catalog view without config should list everything unconfigured:\n%s", text)
	}
}

func TestRenderConfigShowCatalogJSON(t *testing.T) {
	rc := writeCatalogShowConfig(t, `providers:
  lab:
    preset: anthropic
    type: messages
    api_url: https://example.invalid/v1/messages
    models:
      claude-sonnet-5-5: {}
`, "")

	var buf bytes.Buffer
	if err := renderConfigShowCatalog(&buf, configShowOptions{JSON: true}, rc); err != nil {
		t.Fatalf("renderConfigShowCatalog(json): %v", err)
	}
	var report configShowCatalogReport
	if err := json.Unmarshal(buf.Bytes(), &report); err != nil {
		t.Fatalf("decode JSON report: %v", err)
	}
	if report.Version != modelcatalog.Version() {
		t.Fatalf("version = %q, want %q", report.Version, modelcatalog.Version())
	}
	if len(report.Endpoints) == 0 || len(report.Models) == 0 {
		t.Fatalf("report = %+v, want endpoints and models", report)
	}
	configured := 0
	var sonnet *configShowCatalogModel
	for i := range report.Models {
		m := &report.Models[i]
		if m.Configured {
			configured++
			sonnet = m
		}
	}
	if configured != 1 || sonnet == nil || sonnet.Endpoint != "anthropic" || sonnet.WireModel != "claude-sonnet-5-5" {
		t.Fatalf("configured models = %+v, want exactly the anthropic binding", report.Models)
	}
	if sonnet.Context == 0 || sonnet.Output == 0 || len(sonnet.Sources) == 0 {
		t.Fatalf("anthropic binding facts = %+v, want limits and sources", sonnet)
	}
	for _, m := range report.Models {
		if m.Endpoint != "anthropic" {
			continue
		}
		if m.WireModel != "claude-sonnet-5-5" && m.Configured {
			t.Fatalf("binding %+v marked configured, want only claude-sonnet-5-5", m)
		}
	}
	// The openai endpoint carries verified reasoning variants; the view must
	// surface them so users can see the tiers before configuring a model.
	var variantModel *configShowCatalogModel
	for i := range report.Models {
		if report.Models[i].Endpoint == "openai" && len(report.Models[i].Variants) > 0 {
			variantModel = &report.Models[i]
			break
		}
	}
	if variantModel == nil {
		t.Fatalf("no openai binding lists variants: %+v", report.Models)
	}
}

func TestConfigShowCatalogPathExclusive(t *testing.T) {
	err := configShowFlagsError(configShowOptions{Catalog: true, Path: "providers.sample"})
	if err == nil || !strings.Contains(err.Error(), "--catalog") {
		t.Fatalf("configShowFlagsError = %v, want a --catalog/--path conflict error", err)
	}
	if err := configShowFlagsError(configShowOptions{Catalog: true}); err != nil {
		t.Fatalf("configShowFlagsError(catalog only) = %v, want nil", err)
	}
	if err := configShowFlagsError(configShowOptions{Path: "providers.sample"}); err != nil {
		t.Fatalf("configShowFlagsError(path only) = %v, want nil", err)
	}
}

func TestCatalogShowRecognizesExplicitAlias(t *testing.T) {
	rc := writeCatalogShowConfig(t, `providers:
  sample:
    preset: openai
    models:
      alias:
        catalog: openai/gpt-6.1-sol
`, "")
	var buf bytes.Buffer
	if err := renderConfigShowCatalog(&buf, configShowOptions{}, rc); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"openai / gpt-6.1-sol  [configured]", "reasoning options:", "source:", "Responses fields:"} {
		if !strings.Contains(buf.String(), want) {
			t.Fatalf("missing %q in %s", want, buf.String())
		}
	}
}
