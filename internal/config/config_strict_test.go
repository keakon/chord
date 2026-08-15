package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLoadConfigFromPathRejectsUnknownTopLevelField confirms strict decoding
// surfaces misplaced top-level keys instead of silently dropping them.
func TestLoadConfigFromPathRejectsUnknownTopLevelField(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("bogus_top_level: true\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if _, err := LoadConfigFromPath(path); err == nil {
		t.Fatalf("LoadConfigFromPath: expected error for unknown top-level key, got nil")
	}
}

// TestLoadConfigFromPathRejectsUnknownProviderField guards against typos at the
// provider level (e.g. api_key in config.yaml instead of auth.yaml).
func TestLoadConfigFromPathRejectsUnknownProviderField(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	content := []byte("providers:\n  gateway:\n    api_key: $X\n")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if _, err := LoadConfigFromPath(path); err == nil {
		t.Fatalf("LoadConfigFromPath: expected error for unknown provider key, got nil")
	}
}

func TestLoadConfigFromPathRejectsRemovedOfficialAPIField(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	content := []byte("providers:\n  sample:\n    type: responses\n    official_api: true\n")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if _, err := LoadConfigFromPath(path); err == nil {
		t.Fatal("LoadConfigFromPath: expected error for removed official_api key, got nil")
	}
}

// TestLoadConfigFromPathRejectsUnknownModelField guards against fields placed
// at the model root that only take effect under a nested object, such as
// include_thoughts (valid only under thinking).
func TestLoadConfigFromPathRejectsUnknownModelField(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	content := []byte("providers:\n  gemini:\n    models:\n      gemini-3.5-flash:\n        include_thoughts: true\n")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if _, err := LoadConfigFromPath(path); err == nil {
		t.Fatalf("LoadConfigFromPath: expected error for model-root include_thoughts, got nil")
	}
}

// TestLoadConfigFromPathAllowsModelTemplatesAnchors confirms the pure
// anchor-namespace key is accepted and anchors/aliases resolve into real fields.
func TestLoadConfigFromPathAllowsModelTemplatesAnchors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	content := []byte(`
model_templates:
  "base": &base
    limit:
      context: 1048576
      output: 65536

providers:
  gemini:
    type: generate-content
    models:
      gemini-3.5-flash: *base
`)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	cfg, err := LoadConfigFromPath(path)
	if err != nil {
		t.Fatalf("LoadConfigFromPath: %v", err)
	}
	m := cfg.Providers["gemini"].Models["gemini-3.5-flash"]
	if m.Limit.Context != 1048576 || m.Limit.Output != 65536 {
		t.Fatalf("anchor merge did not resolve: %#v", m.Limit)
	}
}

// TestLoadConfigFromPathAllowsModelTemplatesMergeKeys exercises merge keys
// (<<:) against the model_templates namespace through the real load path.
func TestLoadConfigFromPathAllowsModelTemplatesMergeKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	content := []byte(`
model_templates:
  "limits": &limits
    limit:
      context: 400000
      output: 128000
  "reasoning": &reasoning
    reasoning:
      effort: medium

providers:
  openai:
    type: responses
    models:
      gpt-5.5:
        <<: [*limits, *reasoning]
`)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	cfg, err := LoadConfigFromPath(path)
	if err != nil {
		t.Fatalf("LoadConfigFromPath: %v", err)
	}
	m := cfg.Providers["openai"].Models["gpt-5.5"]
	if m.Limit.Context != 400000 || m.Limit.Output != 128000 {
		t.Fatalf("limits merge did not resolve: %#v", m.Limit)
	}
	if m.Reasoning == nil || m.Reasoning.Effort != "medium" {
		t.Fatalf("reasoning merge did not resolve: %#v", m.Reasoning)
	}
}

// TestLoadConfigFromPathAcceptsEmptyAndCommentOnlyFiles pins the pre-strict
// behavior: a file with no YAML document (empty, or fully commented out to
// "disable" it) loads as an empty config instead of failing with io.EOF.
func TestLoadConfigFromPathAcceptsEmptyAndCommentOnlyFiles(t *testing.T) {
	for name, content := range map[string]string{
		"empty":        "",
		"comment-only": "# providers:\n#   gateway:\n#     type: responses\n",
		"whitespace":   "\n\n",
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
				t.Fatalf("WriteFile: %v", err)
			}
			cfg, err := LoadConfigFromPath(path)
			if err != nil {
				t.Fatalf("LoadConfigFromPath: %v", err)
			}
			if cfg == nil {
				t.Fatal("LoadConfigFromPath returned nil config")
			}
		})
	}
}
