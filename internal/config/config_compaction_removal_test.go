package config

import (
	"strings"
	"testing"
)

func TestRemovedCompactionPresetReportsIssueAndKeepsOtherSettings(t *testing.T) {
	content := "context:\n  compaction:\n    preset: codex\n    threshold: 0.75\n    reminder: 0.6\n    model_driven: true\n    model_pool: summary-pool\nmodel_pools:\n  summary-pool: [sample/test-model]\n"
	cfg, logs := loadConfigFromPathAndCaptureLog(t, content)
	if !strings.Contains(logs, "field preset not found in type config.CompactionConfig") {
		t.Fatalf("config warning = %q, want unknown compaction preset", logs)
	}
	if comp := cfg.Context.Compaction; comp.Threshold != 0.75 || comp.Reminder != 0.6 || !comp.ModelDriven || comp.ModelPool != "summary-pool" {
		t.Fatalf("other compaction settings were lost: %+v", comp)
	}
	path := writeIssueTestConfig(t, t.TempDir(), "config.yaml", content)
	issues, err := resolvedFileIssues(path, "")
	if err != nil {
		t.Fatalf("collect config issues: %v", err)
	}
	if len(issues) != 1 || !strings.Contains(issues[0], "field preset not found in type config.CompactionConfig") {
		t.Fatalf("config issues = %v, want only the removed preset", issues)
	}
}
