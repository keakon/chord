package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/modelcatalog"
)

func TestCatalogAcknowledgmentsSerializeWriters(t *testing.T) {
	t.Setenv("CHORD_CONFIG_HOME", t.TempDir())
	const count = 16
	start := make(chan struct{})
	results := make(chan error, count)
	for i := range count {
		go func() {
			<-start
			results <- RecordCatalogAdvisoryAcknowledgment("sample", fmt.Sprintf("model-%d", i))
		}()
	}
	close(start)
	for range count {
		if err := <-results; err != nil {
			t.Error(err)
		}
	}
	acks, err := loadCatalogAdvisoryAcks()
	if err != nil {
		t.Fatal(err)
	}
	if len(acks) != count {
		t.Fatalf("acknowledgments = %d, want %d", len(acks), count)
	}
	for i := range count {
		if !acknowledged("sample", fmt.Sprintf("model-%d", i), modelcatalog.Version(), acks) {
			t.Fatalf("missing acknowledgment %d", i)
		}
	}
}

func TestCatalogAcknowledgmentPrunesInactiveVersions(t *testing.T) {
	t.Setenv("CHORD_CONFIG_HOME", t.TempDir())
	path, err := catalogAdvisoryAckPath()
	if err != nil {
		t.Fatal(err)
	}
	state := catalogAdvisoryAckFile{Acks: []catalogAdvisoryAck{
		{Provider: "sample", Model: "model-1", CatalogVersion: "2000-01-01.1"},
		{Provider: "sample", Model: "model-1", CatalogVersion: modelcatalog.Version()},
	}}
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RecordCatalogAdvisoryAcknowledgment("sample", "model-1"); err != nil {
		t.Fatal(err)
	}
	acks, err := loadCatalogAdvisoryAcks()
	if err != nil || len(acks) != 1 || acks[0].CatalogVersion != modelcatalog.Version() {
		t.Fatalf("acknowledgments = %+v, err=%v, want current version only", acks, err)
	}
}

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
