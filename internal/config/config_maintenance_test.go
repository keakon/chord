package config

import (
	"strings"
	"testing"
)

func loadTestConfig(t *testing.T, data string) *Config {
	t.Helper()
	cfg, err := loadConfigData("test-config.yaml", []byte(data), true, nil)
	if err != nil {
		t.Fatalf("loadConfigData: %v", err)
	}
	return cfg
}

func TestMaintenanceCleanupDefaultsAreDisabled(t *testing.T) {
	cfg := loadTestConfig(t, "providers: {}\n")
	c := cfg.Maintenance.Cleanup
	if c.Sessions.Enabled || c.LLMDumps.Enabled {
		t.Fatalf("cleanup policies must default to disabled: %#v", c)
	}
	if c.Sessions.RetainDays != DefaultSessionsRetainDays || c.LLMDumps.RetainDays != DefaultLLMDumpsRetainDays {
		t.Fatalf("default retain days = %#v", c)
	}
}

func TestMaintenanceCleanupEnabledUsesDefaultDays(t *testing.T) {
	cfg := loadTestConfig(t, `
maintenance:
  cleanup:
    sessions:
      enabled: true
    llm_dumps:
      enabled: true
`)
	c := cfg.Maintenance.Cleanup
	if !c.Sessions.Enabled || c.Sessions.RetainDays != DefaultSessionsRetainDays {
		t.Fatalf("sessions policy = %#v", c.Sessions)
	}
	if !c.LLMDumps.Enabled || c.LLMDumps.RetainDays != DefaultLLMDumpsRetainDays {
		t.Fatalf("llm_dumps policy = %#v", c.LLMDumps)
	}
}

func TestMaintenanceCleanupExplicitDaysRoundTrip(t *testing.T) {
	cfg := loadTestConfig(t, `
maintenance:
  cleanup:
    sessions:
      enabled: true
      retain_days: 7
    llm_dumps:
      enabled: true
      retain_days: 36500
`)
	c := cfg.Maintenance.Cleanup
	if c.Sessions.RetainDays != 7 || c.LLMDumps.RetainDays != MaxMaintenanceRetainDays {
		t.Fatalf("policies = %#v", c)
	}
}

func TestMaintenanceCleanupOutOfBoundsDaysDisablePolicy(t *testing.T) {
	cfg := loadTestConfig(t, `
maintenance:
  cleanup:
    sessions:
      enabled: true
      retain_days: -3
    llm_dumps:
      enabled: true
      retain_days: 36501
`)
	c := cfg.Maintenance.Cleanup
	if c.Sessions.Enabled || c.LLMDumps.Enabled {
		t.Fatalf("out-of-bounds policies must be disabled: %#v", c)
	}
	issues := collectMaintenanceCleanupIssues(cfg)
	if len(issues) != 0 {
		t.Fatalf("issues after reset = %q, want none", issues)
	}
}

func TestCollectMaintenanceCleanupIssuesReportsAndResets(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Maintenance.Cleanup.Sessions = MaintenanceRetainConfig{Enabled: true, RetainDays: -1}
	cfg.Maintenance.Cleanup.LLMDumps = MaintenanceRetainConfig{Enabled: true, RetainDays: MaxMaintenanceRetainDays + 1}
	issues := collectMaintenanceCleanupIssues(cfg)
	if len(issues) != 2 {
		t.Fatalf("issues = %q, want two", issues)
	}
	if !strings.Contains(issues[0], "maintenance.cleanup.sessions.retain_days") {
		t.Fatalf("issue = %q, want sessions path", issues[0])
	}
	if cfg.Maintenance.Cleanup.Sessions.Enabled || cfg.Maintenance.Cleanup.LLMDumps.Enabled {
		t.Fatalf("policies must be reset: %#v", cfg.Maintenance.Cleanup)
	}
}

func TestMaintenanceCleanupDecodeErrorDisablesAffectedPolicy(t *testing.T) {
	cfg := loadTestConfig(t, `
maintenance:
  cleanup:
    sessions:
      enabled: true
      retain_day: 7
    llm_dumps:
      enabled: true
      retain_days: 3
`)
	if cfg.Maintenance.Cleanup.Sessions.Enabled {
		t.Fatalf("sessions policy must be disabled on unknown key: %#v", cfg.Maintenance.Cleanup.Sessions)
	}
	if !cfg.Maintenance.Cleanup.LLMDumps.Enabled || cfg.Maintenance.Cleanup.LLMDumps.RetainDays != 3 {
		t.Fatalf("llm_dumps policy must survive: %#v", cfg.Maintenance.Cleanup.LLMDumps)
	}
}

func TestMaintenanceCleanupWrongTypeDisablesAffectedPolicy(t *testing.T) {
	cfg := loadTestConfig(t, `
maintenance:
  cleanup:
    sessions:
      enabled: true
      retain_days: "7"
    llm_dumps:
      enabled: true
`)
	if cfg.Maintenance.Cleanup.Sessions.Enabled {
		t.Fatalf("sessions policy must be disabled on wrong type: %#v", cfg.Maintenance.Cleanup.Sessions)
	}
	if !cfg.Maintenance.Cleanup.LLMDumps.Enabled {
		t.Fatalf("llm_dumps policy must survive: %#v", cfg.Maintenance.Cleanup.LLMDumps)
	}
}

func TestMaintenanceCleanupScalarSectionDisablesBoth(t *testing.T) {
	cfg := loadTestConfig(t, `
maintenance:
  cleanup: true
`)
	if cfg.Maintenance.Cleanup.Sessions.Enabled || cfg.Maintenance.Cleanup.LLMDumps.Enabled {
		t.Fatalf("scalar cleanup must disable both policies: %#v", cfg.Maintenance.Cleanup)
	}
}

func TestMaintenanceCleanupValidSectionLoads(t *testing.T) {
	cfg := loadTestConfig(t, `
maintenance:
  size_check_on_startup: true
  cleanup:
    sessions:
      enabled: true
      retain_days: 14
`)
	if !cfg.Maintenance.Cleanup.Sessions.Enabled || cfg.Maintenance.Cleanup.Sessions.RetainDays != 14 {
		t.Fatalf("valid cleanup config = %#v", cfg.Maintenance.Cleanup)
	}
	if !cfg.Maintenance.SizeCheckOnStartup {
		t.Fatal("size_check_on_startup must survive")
	}
}
