package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/modelcatalog"
)

func TestCatalogFreshnessAdvisories(t *testing.T) {
	t.Setenv("CHORD_CONFIG_HOME", t.TempDir())
	cfg := &Config{
		Providers: map[string]ProviderConfig{
			"gw": {
				Models: map[string]ModelConfig{
					// Older generation borrowed by wire name: advisory fires.
					"gpt-6-sol-gw": {Catalog: &ModelCatalogRef{ID: "openai/gpt-6-sol"}},
					// Newest generation: nothing newer exists.
					"gpt-6.1-gw": {Catalog: &ModelCatalogRef{ID: "openai/gpt-6.1-sol"}},
					// Different family: never counted as a newer generation.
					"opus-gw": {Catalog: &ModelCatalogRef{ID: "anthropic/claude-opus-5-5"}},
					// No catalog reference: out of scope.
					"plain": {},
					// Borrow disabled: out of scope.
					"disabled-borrow": {Catalog: &ModelCatalogRef{Disabled: true}},
				},
			},
		},
	}
	advisories := CatalogFreshnessAdvisories(cfg)
	if len(advisories) != 1 {
		t.Fatalf("advisories = %d entries, want 1: %v", len(advisories), advisories)
	}
	if !strings.Contains(advisories[0], "providers.gw.models.gpt-6-sol-gw borrows catalog model \"openai/gpt-6-sol\"") ||
		!strings.Contains(advisories[0], "openai/gpt-6.1-sol") {
		t.Errorf("advisory text does not name the reference and the newer entry: %s", advisories[0])
	}
	if !strings.Contains(advisories[0], "--keep-current") {
		t.Errorf("advisory must offer the keep-current acknowledgment: %s", advisories[0])
	}

	// The generic advisory channel carries them for doctor and the startup log.
	if !slices.Contains(Advisories(cfg), advisories[0]) {
		t.Error("CatalogFreshnessAdvisories must flow through Advisories()")
	}
}

func TestCatalogAdvisoryAcknowledgment(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("CHORD_CONFIG_HOME", stateDir)
	cfg := &Config{
		Providers: map[string]ProviderConfig{
			"gw": {Models: map[string]ModelConfig{
				"gpt-6-sol-gw": {Catalog: &ModelCatalogRef{ID: "openai/gpt-6-sol"}},
			}},
		},
	}
	if advisories := CatalogFreshnessAdvisories(cfg); len(advisories) != 1 {
		t.Fatalf("expected one advisory before acknowledgment, got %v", advisories)
	}
	if err := RecordCatalogAdvisoryAcknowledgment("gw", "gpt-6-sol-gw"); err != nil {
		t.Fatalf("RecordCatalogAdvisoryAcknowledgment: %v", err)
	}
	if advisories := CatalogFreshnessAdvisories(cfg); len(advisories) != 0 {
		t.Fatalf("acknowledged reference must stay silent, got %v", advisories)
	}

	// The acknowledgment file pins the catalog version, so a later catalog
	// change re-arms the advisory.
	path := filepath.Join(stateDir, "catalog-advisories.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read ack file: %v", err)
	}
	if !strings.Contains(string(data), modelcatalog.Version()) {
		t.Error("ack file must record the catalog version it was acknowledged under")
	}
}

func TestCatalogAdvisoriesEmptyConfig(t *testing.T) {
	t.Setenv("CHORD_CONFIG_HOME", t.TempDir())
	if got := CatalogFreshnessAdvisories(nil); got != nil {
		t.Errorf("nil config must produce no advisories, got %v", got)
	}
	if got := CatalogFreshnessAdvisories(&Config{}); got != nil {
		t.Errorf("empty config must produce no advisories, got %v", got)
	}
}
