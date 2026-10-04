package modelcatalog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// resetEffective restores the embedded baseline so a test that installs a
// cache cannot leak its fixture into other tests of this binary.
func resetEffective(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		effectiveStatePtr.Store(&effectiveState{catalog: mustLoadCatalog()})
	})
}

func fixtureCatalog(t *testing.T, version string, mutate func(*Catalog)) *Catalog {
	t.Helper()
	c, err := loadCatalog(catalogData)
	if err != nil {
		t.Fatalf("load embedded catalog fixture: %v", err)
	}
	c.Version = version
	if mutate != nil {
		mutate(c)
	}
	return c
}

func writeCache(t *testing.T, f *CacheFile) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "modelcatalog-cache.json")
	if err := WriteCacheFile(path, f); err != nil {
		t.Fatalf("write cache: %v", err)
	}
	return path
}

func TestInstallCachedCatalogInstallsNewerSnapshotWhole(t *testing.T) {
	resetEffective(t)
	candidate := Candidate{
		WireModelID: "gpt-6.2-sol-mirror",
		Scope:       "gateway-a",
		ModelID:     "openai/gpt-6.1-sol",
		Context:     400000,
		Output:      128000,
		Sources:     []Source{{URL: "https://example.invalid/gpt-6.2-sol-mirror", Checked: "2026-10-02"}},
	}
	path := writeCache(t, &CacheFile{
		SchemaVersion: CacheSchemaVersion,
		Repository:    "https://example.invalid/chord-models",
		Revision:      "v2099-01-01.1",
		FetchedAt:     "2026-10-03T00:00:00Z",
		Catalog:       fixtureCatalog(t, "2099-01-01.1", nil),
		Candidates:    []Candidate{candidate},
	})
	if err := InstallCachedCatalog(path, nil); err != nil {
		t.Fatalf("install cached catalog: %v", err)
	}
	if got := Version(); got != "2099-01-01.1" {
		t.Fatalf("effective version = %q, want the cached snapshot version", got)
	}
	origin := OriginInfo()
	if !origin.Cached || origin.Source == nil || origin.Source.Revision != "v2099-01-01.1" {
		t.Fatalf("origin = %+v, want the cached snapshot source", origin)
	}
	if status := CurrentCacheStatus(); !status.Present || !status.Installed {
		t.Fatalf("cache status = %+v, want installed", status)
	}
	candidates := EffectiveCandidates()
	if len(candidates) != 1 || candidates[0].WireModelID != "gpt-6.2-sol-mirror" {
		t.Fatalf("candidates = %+v, want the cache's candidate entries", candidates)
	}
	// Whole-catalog replacement: the cached snapshot keeps carrying the
	// embedded endpoints even though it is a different version.
	if _, ok := Model("openai/gpt-6.1-sol"); !ok {
		t.Fatal("cached snapshot must serve the same model identities as its sources")
	}
}

func TestInstallCachedCatalogKeepsEmbeddedForOlderOrMissingCache(t *testing.T) {
	resetEffective(t)
	embeddedVersion := Version()

	path := writeCache(t, &CacheFile{
		SchemaVersion: CacheSchemaVersion,
		Repository:    "https://example.invalid/chord-models",
		Revision:      "v2020-01-01.1",
		FetchedAt:     "2026-10-03T00:00:00Z",
		Catalog:       fixtureCatalog(t, "2020-01-01.1", nil),
	})
	if err := InstallCachedCatalog(path, nil); err != nil {
		t.Fatalf("install older cache: %v", err)
	}
	if Version() != embeddedVersion {
		t.Fatalf("an older cache must never replace the embedded snapshot")
	}
	status := CurrentCacheStatus()
	if !status.Present || status.Installed || status.Detail == "" {
		t.Fatalf("status = %+v, want present-but-not-installed with a reason", status)
	}

	if err := InstallCachedCatalog(filepath.Join(t.TempDir(), "absent.json"), nil); err != nil {
		t.Fatalf("missing cache must be the normal no-cache state, got %v", err)
	}
	if Version() != embeddedVersion {
		t.Fatal("a missing cache must keep the embedded snapshot")
	}
}

func TestInstallCachedCatalogRejectsIncompatibleCacheAndFallsBack(t *testing.T) {
	resetEffective(t)
	embeddedVersion := Version()

	path := writeCache(t, &CacheFile{
		SchemaVersion: CacheSchemaVersion,
		Repository:    "https://example.invalid/chord-models",
		Revision:      "v2099-01-01.1",
		FetchedAt:     "2026-10-03T00:00:00Z",
		Catalog:       fixtureCatalog(t, "2099-01-01.1", nil),
	})
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// A cache written by a newer chord: an unknown field anywhere — including
	// inside the catalog — must reject the whole file.
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	doc["future_field"] = true
	poisoned, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	poisonedPath := filepath.Join(t.TempDir(), "modelcatalog-cache.json")
	if err := os.WriteFile(poisonedPath, poisoned, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := InstallCachedCatalog(poisonedPath, nil); err == nil {
		t.Fatal("an incompatible cache must be rejected")
	}
	if Version() != embeddedVersion {
		t.Fatal("a rejected cache must fall back to the embedded snapshot")
	}
	if status := CurrentCacheStatus(); !status.Present || status.Installed || status.Detail == "" {
		t.Fatalf("status = %+v, want a recorded rejection reason", status)
	}

	// A corrupt file behaves the same.
	corruptPath := filepath.Join(t.TempDir(), "modelcatalog-cache.json")
	if err := os.WriteFile(corruptPath, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := InstallCachedCatalog(corruptPath, nil); err == nil {
		t.Fatal("a corrupt cache must be rejected")
	}
	if Version() != embeddedVersion {
		t.Fatal("a corrupt cache must fall back to the embedded snapshot")
	}
}

func TestCacheFileValidateRejectsBadEntries(t *testing.T) {
	valid := func(f *CacheFile) *CacheFile {
		f.Catalog = fixtureCatalog(t, "2099-01-01.1", nil)
		return f
	}
	cases := []struct {
		name   string
		mutate func(*CacheFile)
		want   string
	}{
		{"schema version", func(f *CacheFile) { f.SchemaVersion = 99 }, "schema version"},
		{"repository", func(f *CacheFile) { f.Repository = " " }, "repository"},
		{"revision", func(f *CacheFile) { f.Revision = " " }, "revision"},
		{"fetched at", func(f *CacheFile) { f.FetchedAt = "yesterday" }, "RFC 3339"},
		{"missing catalog", func(f *CacheFile) { f.Catalog = nil }, "no catalog"},
		{
			"candidate references unknown model",
			func(f *CacheFile) {
				f.Candidates = []Candidate{{
					WireModelID: "m", Scope: "gw",
					Sources: []Source{{URL: "https://example.invalid/m", Checked: "2026-10-02"}},
				}}
			},
			"candidate",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := valid(&CacheFile{
				SchemaVersion: CacheSchemaVersion,
				Repository:    "https://example.invalid/chord-models",
				Revision:      "v2099-01-01.1",
				FetchedAt:     "2026-10-03T00:00:00Z",
			})
			tc.mutate(f)
			err := f.validate()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("validate() = %v, want an error containing %q", err, tc.want)
			}
		})
	}
}

func TestCandidateValidateAgainst(t *testing.T) {
	catalog := fixtureCatalog(t, "2099-01-01.1", nil)
	base := Candidate{
		WireModelID: "gpt-6.1-sol-messages",
		Scope:       "gateway-a",
		ModelID:     "openai/gpt-6.1-sol",
		Sources:     []Source{{URL: "https://example.invalid/gpt-6.1-sol-messages", Checked: "2026-10-02"}},
	}
	if err := base.ValidateAgainst(catalog); err != nil {
		t.Fatalf("valid candidate rejected: %v", err)
	}
	cases := []struct {
		name   string
		mutate func(*Candidate)
		want   string
	}{
		{"wire name", func(c *Candidate) { c.WireModelID = " " }, "wire_model_id"},
		{"at sign", func(c *Candidate) { c.WireModelID = "gpt-6.1-sol@x" }, "@"},
		{"scope", func(c *Candidate) { c.Scope = "" }, "scope"},
		{"unknown model", func(c *Candidate) { c.ModelID = "openai/absent" }, "not in the catalog"},
		{"negative fact", func(c *Candidate) { c.Output = -1 }, "negative"},
		{"input beyond context", func(c *Candidate) { c.Context = 10; c.Input = 20 }, "exceeds"},
		{"no facts and no reference", func(c *Candidate) { c.ModelID = "" }, "needs observed facts"},
		{"unknown modality", func(c *Candidate) { c.InputModalities = []string{"smell"} }, "modality"},
		{"no sources", func(c *Candidate) { c.Sources = nil }, "source is required"},
		{"non-https source", func(c *Candidate) { c.Sources = []Source{{URL: "http://example.invalid", Checked: "2026-10-02"}} }, "https"},
		{"source without host", func(c *Candidate) { c.Sources = []Source{{URL: "https:///catalog", Checked: "2026-10-02"}} }, "host"},
		{"bad date", func(c *Candidate) { c.Sources = []Source{{URL: "https://example.invalid", Checked: "2026-13-02"}} }, "calendar day"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := base
			tc.mutate(&c)
			err := c.ValidateAgainst(catalog)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("ValidateAgainst() = %v, want an error containing %q", err, tc.want)
			}
		})
	}
}

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int // sign of CompareVersions(a, b)
	}{
		{"2026-10-01.1", "2026-10-01.1", 0},
		{"2026-10-01.1", "2026-10-01.2", -1},
		{"2099-01-01.1", "2026-10-01.9", 1},
		{"2026-10-01", "2026-10-01.1", -1},
		{"2026-11-01.1", "2026-10-30.9", 1},
		{"2027-01-01.1", "2026-12-31.9", 1},
		{"v2026-10-01.1", "2026-10-01.1", 1}, // letters sort after digits, callers strip the prefix
	}
	for _, tc := range cases {
		if got := sign(CompareVersions(tc.a, tc.b)); got != tc.want {
			t.Errorf("CompareVersions(%q, %q) sign = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

func sign(n int) int {
	switch {
	case n < 0:
		return -1
	case n > 0:
		return 1
	}
	return 0
}

func TestSuggestCandidatesRanksAndFloors(t *testing.T) {
	resetEffective(t)
	path := writeCache(t, &CacheFile{
		SchemaVersion: CacheSchemaVersion,
		Repository:    "https://example.invalid/chord-models",
		Revision:      "v2099-01-01.1",
		FetchedAt:     "2026-10-03T00:00:00Z",
		Catalog:       fixtureCatalog(t, "2099-01-01.1", nil),
		Candidates: []Candidate{
			{
				WireModelID: "gpt-6.1-sol-messages", Scope: "gateway-b",
				ModelID: "openai/gpt-6.1-sol",
				Sources: []Source{{URL: "https://example.invalid/b", Checked: "2026-10-02"}},
			},
			{
				WireModelID: "gpt-6.1-sol-messages", Scope: "gateway-a",
				Context: 400000, Output: 128000,
				Sources: []Source{{URL: "https://example.invalid/a", Checked: "2026-10-02"}},
			},
			{
				WireModelID: "completely-unrelated", Scope: "gateway-a",
				ModelID: "openai/gpt-6.1-sol",
				Sources: []Source{{URL: "https://example.invalid/c", Checked: "2026-10-02"}},
			},
		},
	})
	if err := InstallCachedCatalog(path, nil); err != nil {
		t.Fatalf("install cached catalog: %v", err)
	}
	out := SuggestCandidates("gpt-6.1-sol-messages", 5)
	if len(out) != 2 {
		t.Fatalf("expected the two sightings of the typed name, got %+v", out)
	}
	if out[0].Candidate.Scope != "gateway-a" || out[1].Candidate.Scope != "gateway-b" {
		t.Fatalf("equal scores must order by scope, got %q then %q", out[0].Candidate.Scope, out[1].Candidate.Scope)
	}
	if SuggestCandidates("no-such-model-here", 5) != nil {
		t.Error("below-threshold queries must suggest nothing")
	}
	if SuggestCandidates("gpt-6.1-sol-messages", 0) != nil {
		t.Error("a non-positive limit must suggest nothing")
	}
}

func TestEqualSnapshotDeliversCandidatesAndRejectsConflictingCatalog(t *testing.T) {
	resetEffective(t)
	c := fixtureCatalog(t, Version(), nil)
	candidate := Candidate{WireModelID: "sample-alias", Scope: "sample", ModelID: c.Models[0].ID, Sources: []Source{{URL: "https://example.invalid/catalog", Checked: "2026-10-05"}}}
	path := writeCache(t, &CacheFile{SchemaVersion: CacheSchemaVersion, Repository: c.Source.Repository, Revision: c.Source.Revision, FetchedAt: "2026-10-05T00:00:00Z", Catalog: c, Candidates: []Candidate{candidate}})
	if err := InstallCachedCatalog(path, nil); err != nil {
		t.Fatal(err)
	}
	if len(EffectiveCandidates()) != 1 {
		t.Fatal("identical release did not deliver candidates")
	}
	c.Models[0].Context++
	path = writeCache(t, &CacheFile{SchemaVersion: CacheSchemaVersion, Repository: c.Source.Repository, Revision: c.Source.Revision, FetchedAt: "2026-10-05T00:00:00Z", Catalog: c})
	if err := InstallCachedCatalog(path, nil); err == nil {
		t.Fatal("conflicting equal-version snapshot accepted")
	}
	if len(EffectiveCandidates()) != 1 {
		t.Fatal("rejected cache changed candidates")
	}
}
