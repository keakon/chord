package config

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keakon/golog"

	"github.com/keakon/chord/internal/logtest"
)

// loadConfigFromPathAndCaptureLog loads content through LoadConfigFromPath with
// a capturing default logger so tests can assert both that loading succeeds
// and that the offending value was reported as a warning.
func loadConfigFromPathAndCaptureLog(t *testing.T, content string) (*Config, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	var buf bytes.Buffer
	var cfg *Config
	logtest.WithDefault(logtest.NewLogger(&buf, golog.InfoLevel), func() {
		var err error
		cfg, err = LoadConfigFromPath(path)
		if err != nil {
			t.Fatalf("LoadConfigFromPath: %v", err)
		}
	})
	return cfg, buf.String()
}

// TestLoadConfigFromPathIgnoresUnknownTopLevelField confirms an unrecognized
// top-level key is logged and treated as not configured instead of blocking
// startup.
func TestLoadConfigFromPathIgnoresUnknownTopLevelField(t *testing.T) {
	cfg, logs := loadConfigFromPathAndCaptureLog(t, "bogus_top_level: true\n")
	if cfg == nil {
		t.Fatal("LoadConfigFromPath returned nil config")
	}
	if !strings.Contains(logs, "bogus_top_level") {
		t.Fatalf("logs = %q, want warning mentioning bogus_top_level", logs)
	}
}

// TestLoadConfigFromPathIgnoresUnknownProviderField confirms a typo at the
// provider level (e.g. api_key in config.yaml instead of auth.yaml) is logged
// and dropped while the provider itself still loads.
func TestLoadConfigFromPathIgnoresUnknownProviderField(t *testing.T) {
	cfg, logs := loadConfigFromPathAndCaptureLog(t, "providers:\n  gateway:\n    api_key: $X\n")
	if cfg == nil {
		t.Fatal("LoadConfigFromPath returned nil config")
	}
	if _, ok := cfg.Providers["gateway"]; !ok {
		t.Fatal("LoadConfigFromPath: gateway provider missing")
	}
	if !strings.Contains(logs, "api_key") {
		t.Fatalf("logs = %q, want warning mentioning api_key", logs)
	}
}

func TestLoadConfigFromPathIgnoresRemovedOfficialAPIField(t *testing.T) {
	cfg, logs := loadConfigFromPathAndCaptureLog(t, "providers:\n  sample:\n    type: responses\n    official_api: true\n")
	if cfg == nil || cfg.Providers["sample"].Type == "" {
		t.Fatal("LoadConfigFromPath: sample provider missing")
	}
	if got := cfg.Providers["sample"].Type; got != ProviderTypeResponses {
		t.Fatalf("provider type = %q, want responses", got)
	}
	if !strings.Contains(logs, "official_api") {
		t.Fatalf("logs = %q, want warning mentioning official_api", logs)
	}
}

// TestLoadConfigFromPathIgnoresUnknownModelField confirms a field placed at
// the model root that only takes effect under a nested object (such as
// include_thoughts under thinking) is logged and dropped while the model still
// loads.
func TestLoadConfigFromPathIgnoresUnknownModelField(t *testing.T) {
	cfg, logs := loadConfigFromPathAndCaptureLog(t, "providers:\n  gemini:\n    models:\n      gemini-3.5-flash:\n        include_thoughts: true\n")
	if cfg == nil {
		t.Fatal("LoadConfigFromPath returned nil config")
	}
	if _, ok := cfg.Providers["gemini"].Models["gemini-3.5-flash"]; !ok {
		t.Fatal("LoadConfigFromPath: gemini model missing")
	}
	if !strings.Contains(logs, "include_thoughts") {
		t.Fatalf("logs = %q, want warning mentioning include_thoughts", logs)
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
