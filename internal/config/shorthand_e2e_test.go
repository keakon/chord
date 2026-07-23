package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLoadConfigContextReductionShorthandEndToEnd exercises every accepted
// spelling of context.reduction through the real load path.
func TestLoadConfigContextReductionShorthandEndToEnd(t *testing.T) {
	cases := []struct {
		name         string
		yaml         string
		wantDisabled bool
		wantErr      bool
	}{
		{"true keeps defaults", "context:\n  reduction: true\n", false, false},
		{"false disables", "context:\n  reduction: false\n", true, false},
		{"null keeps defaults", "context:\n  reduction: null\n", false, false},
		{"unknown fields rejected", "context:\n  reduction:\n    valid_read_age_turns: 12\n", false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(tc.yaml), 0o644); err != nil {
				t.Fatalf("WriteFile: %v", err)
			}
			cfg, err := LoadConfigFromPath(path)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("LoadConfigFromPath: expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("LoadConfigFromPath: %v", err)
			}
			r := cfg.Context.Reduction
			if r.DisabledValue() != tc.wantDisabled {
				t.Fatalf("disabled = %v, want %v", r.DisabledValue(), tc.wantDisabled)
			}
			if !tc.wantDisabled {
				defaults := DefaultConfig().Context.Reduction
				if r.ReadLikeAgeTurns != defaults.ReadLikeAgeTurns || r.StaleAgeTurns != defaults.StaleAgeTurns {
					t.Fatalf("tuning = %+v, want default thresholds retained", r)
				}
			}
		})
	}
}
