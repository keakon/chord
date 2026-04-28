package config

import (
	"testing"

	"gopkg.in/yaml.v3"
)

func TestConfigYAML_DiffInlineMaxColumns(t *testing.T) {
	const raw = `
diff:
  inline_max_columns: 144
`

	cfg := DefaultConfig()
	if err := yaml.Unmarshal([]byte(raw), cfg); err != nil {
		t.Fatalf("yaml unmarshal failed: %v", err)
	}
	if cfg.Diff.InlineMaxColumns != 144 {
		t.Fatalf("Diff.InlineMaxColumns = %d, want 144", cfg.Diff.InlineMaxColumns)
	}
}
