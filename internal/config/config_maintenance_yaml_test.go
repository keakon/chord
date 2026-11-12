package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMaintenanceCleanupRejectsInvalidEffectiveMapping(t *testing.T) {
	cases := map[string]string{
		"explicit zero": `
maintenance:
  cleanup:
    sessions: {enabled: true, retain_days: 0}
    llm_dumps: {enabled: true, retain_days: 3}
`,
		"merged invalid field": `
model_templates:
  defaults: &defaults
    cleanup:
      sessions: {enabled: true, retain_day: 90}
      llm_dumps: {enabled: true, retain_days: 3}
maintenance:
  <<: *defaults
`,
		"aliased invalid policy": `
model_templates:
  policy: &policy {enabled: true, retain_days: "90"}
maintenance:
  cleanup:
    sessions: *policy
    llm_dumps: {enabled: true, retain_days: 3}
`,
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := loadTestConfig(t, input)
			if cfg.Maintenance.Cleanup.Sessions.Enabled {
				t.Fatalf("invalid policy enabled: %+v", cfg.Maintenance.Cleanup.Sessions)
			}
			if !cfg.Maintenance.Cleanup.LLMDumps.Enabled || cfg.Maintenance.Cleanup.LLMDumps.RetainDays != 3 {
				t.Fatalf("valid sibling changed: %+v", cfg.Maintenance.Cleanup.LLMDumps)
			}
		})
	}
}

func TestMaintenanceCleanupUnknownSectionKeyDisablesBoth(t *testing.T) {
	cfg := loadTestConfig(t, `
maintenance:
  cleanup:
    dry_run: true
    sessions: {enabled: true, retain_days: 90}
    llm_dumps: {enabled: true, retain_days: 3}
`)
	if cfg.Maintenance.Cleanup.Sessions.Enabled || cfg.Maintenance.Cleanup.LLMDumps.Enabled {
		t.Fatalf("ambiguous cleanup mapping enabled: %+v", cfg.Maintenance.Cleanup)
	}
}

func TestMaintenanceCleanupResolvesAliasesAndMergePrecedence(t *testing.T) {
	cfg := loadTestConfig(t, `
model_templates:
  policy: &policy {enabled: true, retain_days: 90}
  other: &other {retain_days: 60}
  cleanup: &cleanup
    sessions: *policy
    llm_dumps:
      <<: [*policy, *other]
      retain_days: 3
  maintenance: &maintenance {cleanup: *cleanup}
maintenance: *maintenance
`)
	if !cfg.Maintenance.Cleanup.Sessions.Enabled || cfg.Maintenance.Cleanup.Sessions.RetainDays != 90 {
		t.Fatalf("aliased policy = %+v", cfg.Maintenance.Cleanup.Sessions)
	}
	if !cfg.Maintenance.Cleanup.LLMDumps.Enabled || cfg.Maintenance.Cleanup.LLMDumps.RetainDays != 3 {
		t.Fatalf("explicit override lost: %+v", cfg.Maintenance.Cleanup.LLMDumps)
	}
}

func TestMaintenanceCleanupProjectCannotOverrideGlobalPolicies(t *testing.T) {
	cfg := loadTestConfig(t, `
maintenance:
  cleanup:
    sessions: {enabled: true, retain_days: 90}
`)
	projectPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(projectPath, []byte(`
maintenance:
  cleanup:
    sessions: {enabled: false, retain_days: 1}
    llm_dumps: {enabled: true, retain_days: 1}
`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, merged, err := MergeProjectConfig(cfg, projectPath)
	if err != nil {
		t.Fatal(err)
	}
	if merged.Maintenance.Cleanup != cfg.Maintenance.Cleanup {
		t.Fatalf("project changed global cleanup: %+v", merged.Maintenance.Cleanup)
	}
}
