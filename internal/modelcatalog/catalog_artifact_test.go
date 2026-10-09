package modelcatalog_test

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/modelcatalog"
	"github.com/keakon/chord/internal/modelcatalog/gen"
)

// TestCatalogArtifactUpToDate locks the committed catalog.json to the YAML
// sources under data/: CI fails when the embedded artifact drifts from what
// the generator produces.
func TestCatalogArtifactUpToDate(t *testing.T) {
	want, err := gen.Generate("data")
	if err != nil {
		t.Fatalf("generate from sources: %v", err)
	}
	got, err := os.ReadFile("catalog.json")
	if err != nil {
		t.Fatalf("read committed artifact: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("catalog.json is stale relative to data/*.yaml; run `go run ./cmd/modelcatalog-gen` and commit the result")
	}
}

// TestRuntimeEmbedMatchesSources pins the runtime-embedded catalog to the
// same identity the generator derives from the sources, so a regeneration
// that forgets to rebuild can never go unnoticed. The committed snapshot must
// also record the chord-models revision it was synced from, so diagnostics
// and `chord doctor config` can always say where the catalog came from.
func TestRuntimeEmbedMatchesSources(t *testing.T) {
	sources, err := gen.Load("data")
	if err != nil {
		t.Fatalf("load sources: %v", err)
	}
	if got, want := sources.Version, modelcatalog.Version(); got != want {
		t.Fatalf("embedded catalog version %q does not match sources %q", want, got)
	}
	if len(sources.Endpoints) == 0 || len(sources.Models) == 0 || len(sources.Bindings) == 0 {
		t.Fatal("catalog sources must not be empty")
	}
	if sources.Source == nil {
		t.Fatal("data/snapshot.yaml is missing: the embedded snapshot must record the chord-models revision it was synced from")
	}
	if !strings.HasPrefix(sources.Source.Repository, "https://") || strings.TrimSpace(sources.Source.Revision) == "" {
		t.Fatalf("snapshot source record is incomplete: %+v", sources.Source)
	}
}
