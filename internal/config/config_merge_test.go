package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestMergeProjectConfigCrossReferencedTemplates pins the merge round-trip
// against a model_templates namespace whose entries reference each other.
// Re-emitting the namespace orders its keys alphabetically, which can place an
// alias before the anchor it references and fail the merged re-parse with an
// unknown-anchor error. The namespace is therefore dropped from the round-trip:
// provider values are materialized at decode time and anchors cannot be
// referenced across documents.
func TestMergeProjectConfigCrossReferencedTemplates(t *testing.T) {
	dir := t.TempDir()
	global := filepath.Join(dir, "config.yaml")
	// The raw file must define anchors before their aliases. The names are
	// chosen so alphabetical re-emission inverts that order ("a-model" sorts
	// before "z-cost"), which is how a previously valid config fails after the
	// round-trip.
	data := `model_templates:
  "z-cost": &z-cost
    cost:
      input: 1
      output: 5
  "a-model": &a-model
    <<: *z-cost
    limit:
      context: 200000
providers:
  sample:
    type: responses
    api_url: https://example.invalid/v1/responses
    models:
      m1:
        <<: *a-model
        name: M1
`
	if err := os.WriteFile(global, []byte(data), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	project := filepath.Join(dir, "project.yaml")
	if err := os.WriteFile(project, []byte("providers:\n  sample:\n    models:\n      m1:\n        limit:\n          output: 20000\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	base, err := LoadConfigFromPath(global)
	if err != nil {
		t.Fatalf("LoadConfigFromPath: %v", err)
	}
	_, merged, err := MergeProjectConfig(base, project)
	if err != nil {
		t.Fatalf("MergeProjectConfig: %v", err)
	}
	m := merged.Providers["sample"].Models["m1"]
	if m.Limit.Output != 20000 || m.Limit.Context != 200000 {
		t.Fatalf("merged limit = %+v, want the project output over the template context", m.Limit)
	}
	if m.Cost == nil || m.Cost.Input != 1 || m.Cost.Output != 5 {
		t.Fatalf("merged cost = %+v, want the template values materialized", m.Cost)
	}
	if len(merged.ModelTemplates) != 0 {
		t.Fatalf("ModelTemplates = %d entries, want the namespace dropped from the merged config", len(merged.ModelTemplates))
	}
}
