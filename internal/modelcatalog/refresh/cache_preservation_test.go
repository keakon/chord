package refresh

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/modelcatalog"
	"github.com/keakon/chord/internal/modelcatalog/gen"
)

func TestRunInvalidRepositoryPreservesPreviousCache(t *testing.T) {
	upstream := initUpstreamRepo(t, "v2199-01-01.1", false)
	cachePath := filepath.Join(t.TempDir(), "cache.json")
	catalog, err := gen.Load(upstream)
	if err != nil {
		t.Fatal(err)
	}
	cache := &modelcatalog.CacheFile{SchemaVersion: modelcatalog.CacheSchemaVersion, Repository: "https://example.invalid/catalog", Revision: "v2199-01-01.1", FetchedAt: "2026-10-09T00:00:00Z", Catalog: catalog}
	if err := modelcatalog.WriteCacheFile(cachePath, cache); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(cachePath)
	if err != nil {
		t.Fatal(err)
	}
	next := initUpstreamRepo(t, "v2199-01-02.1", false)
	if _, err := Run(context.Background(), next, cachePath); err == nil || !strings.Contains(err.Error(), "repository") {
		t.Fatalf("invalid repository error = %v", err)
	}
	after, err := os.ReadFile(cachePath)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("failed refresh changed the previous cache: %v", err)
	}
}
