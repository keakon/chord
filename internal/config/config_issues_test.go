package config

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeIssueTestConfig(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path
}

func TestCollectConfigFileIssuesValid(t *testing.T) {
	path := writeIssueTestConfig(t, t.TempDir(), "config.yaml", "providers:\n  sample:\n    type: responses\n    models:\n      test-model:\n        limit:\n          context: 100000\n          output: 64000\n")
	issues, err := CollectConfigFileIssues(path, true)
	if err != nil {
		t.Fatalf("CollectConfigFileIssues: %v", err)
	}
	if len(issues) != 0 {
		t.Fatalf("issues = %#v, want none", issues)
	}
}

func TestCollectConfigFileIssuesReportsAllUnknownKeys(t *testing.T) {
	path := writeIssueTestConfig(t, t.TempDir(), "config.yaml", "bogus_top_level: true\nproviders:\n  sample:\n    type: responses\n    api_key: $X\n    models:\n      test-model:\n        include_thoughts: true\n")
	issues, err := CollectConfigFileIssues(path, true)
	if err != nil {
		t.Fatalf("CollectConfigFileIssues: %v", err)
	}
	joined := strings.Join(issues, "\n")
	for _, want := range []string{"bogus_top_level", "api_key", "include_thoughts"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("issues = %q, want one mentioning %q", joined, want)
		}
	}
}

func TestCollectConfigFileIssuesReportsWrongType(t *testing.T) {
	path := writeIssueTestConfig(t, t.TempDir(), "config.yaml", "max_output_tokens: abc\n")
	issues, err := CollectConfigFileIssues(path, true)
	if err != nil {
		t.Fatalf("CollectConfigFileIssues: %v", err)
	}
	if len(issues) == 0 || !strings.Contains(strings.Join(issues, "\n"), "cannot unmarshal") {
		t.Fatalf("issues = %#v, want wrong-type report for max_output_tokens", issues)
	}
}

func TestCollectConfigFileIssuesReportsSemanticProblems(t *testing.T) {
	path := writeIssueTestConfig(t, t.TempDir(), "config.yaml", "providers:\n  sample:\n    type: responses\n    retry_backoff: linear\ndiagnostics:\n  python:\n    large_file:\n      line_threshold: -1\n")
	issues, err := CollectConfigFileIssues(path, true)
	if err != nil {
		t.Fatalf("CollectConfigFileIssues: %v", err)
	}
	joined := strings.Join(issues, "\n")
	for _, want := range []string{"invalid retry_backoff", "line_threshold"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("issues = %q, want one mentioning %q", joined, want)
		}
	}
}

func TestCollectConfigFileIssuesReportsMalformedYAML(t *testing.T) {
	path := writeIssueTestConfig(t, t.TempDir(), "config.yaml", "providers: [\n")
	issues, err := CollectConfigFileIssues(path, true)
	if err != nil {
		t.Fatalf("CollectConfigFileIssues: %v", err)
	}
	if len(issues) != 1 {
		t.Fatalf("issues = %#v, want a single parse error", issues)
	}
}

func TestCollectConfigFileIssuesAllowsReminderAtOrAboveThreshold(t *testing.T) {
	// A reminder >= threshold is legal and must not report an issue: the
	// reminder fires on the threshold crossing itself (usage reaching
	// min(reminder, threshold)) while the grace period defers the actual
	// compaction; the reminder never raises the compaction line.
	path := writeIssueTestConfig(t, t.TempDir(), "config.yaml", "context:\n  compaction:\n    threshold: 0.65\n    reminder: 0.7\n")
	issues, err := CollectConfigFileIssues(path, true)
	if err != nil {
		t.Fatalf("CollectConfigFileIssues: %v", err)
	}
	for _, issue := range issues {
		if strings.Contains(issue, "compaction") {
			t.Fatalf("reminder at or above the threshold must not report issues, got %q in %v", issue, issues)
		}
	}
}

func TestCollectConfigFileIssuesReportsDeadReminderOnDisabledCompaction(t *testing.T) {
	path := writeIssueTestConfig(t, t.TempDir(), "config.yaml", "context:\n  compaction:\n    threshold: 0\n    reminder: 0.7\n")
	issues, err := CollectConfigFileIssues(path, true)
	if err != nil {
		t.Fatalf("CollectConfigFileIssues: %v", err)
	}
	found := false
	for _, issue := range issues {
		if strings.Contains(issue, "reminder") && strings.Contains(issue, "threshold is 0") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected a dead-reminder issue for threshold 0, got %v", issues)
	}
}

func TestCollectConfigFileIssuesAllowsValidPerModelReminder(t *testing.T) {
	// The per-model compaction block lives on the model definition
	// (ModelConfig.compaction); the old context.compaction.models table is
	// gone and must not be referenced. Valid global + per-model lines report
	// no issue.
	path := writeIssueTestConfig(t, t.TempDir(), "config.yaml", "context:\n  compaction:\n    threshold: 0.65\n    reminder: 0.5\nproviders:\n  openai:\n    type: responses\n    models:\n      gpt-5.6-luna:\n        compaction:\n          threshold: 0.3\n          reminder: 0.2\n")
	issues, err := CollectConfigFileIssues(path, true)
	if err != nil {
		t.Fatalf("CollectConfigFileIssues: %v", err)
	}
	for _, issue := range issues {
		if strings.Contains(issue, "compaction") {
			t.Fatalf("valid compaction config must not report issues, got %q in %v", issue, issues)
		}
	}
}

func TestCollectConfigFileIssuesReportsDeadModelReminder(t *testing.T) {
	// A reminder configured against a model-level threshold of 0 never fires
	// (threshold 0 disables auto-compaction and reminders for that model); the
	// issue names the model definition so the user fixes it on the model side.
	path := writeIssueTestConfig(t, t.TempDir(), "config.yaml", "providers:\n  openai:\n    type: responses\n    models:\n      gpt-5.6-luna:\n        compaction:\n          threshold: 0\n          reminder: 0.2\n")
	issues, err := CollectConfigFileIssues(path, true)
	if err != nil {
		t.Fatalf("CollectConfigFileIssues: %v", err)
	}
	found := false
	for _, issue := range issues {
		if strings.Contains(issue, "openai/gpt-5.6-luna") && strings.Contains(issue, "threshold is 0") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected a dead-reminder issue for the model threshold 0, got %v", issues)
	}
}

func TestCollectProjectConfigIssuesReportsUnsupportedFields(t *testing.T) {
	path := writeIssueTestConfig(t, t.TempDir(), "config.yaml", "provder:\n  x: 1\nproviders:\n  sample:\n    type: responses\n")
	issues, err := CollectProjectConfigIssues(path)
	if err != nil {
		t.Fatalf("CollectProjectConfigIssues: %v", err)
	}
	joined := strings.Join(issues, "\n")
	if !strings.Contains(joined, `"provder"`) || !strings.Contains(joined, "not supported in project config") {
		t.Fatalf("issues = %q, want unsupported project field report for provder", joined)
	}
}

func TestCollectProjectConfigIssuesMissingFile(t *testing.T) {
	issues, err := CollectProjectConfigIssues(filepath.Join(t.TempDir(), "nope.yaml"))
	if err != nil {
		t.Fatalf("CollectProjectConfigIssues: %v", err)
	}
	if issues != nil {
		t.Fatalf("issues = %#v, want nil for missing file", issues)
	}
}

func TestCompactionReminderMinusOneDisablesWithoutIssue(t *testing.T) {
	// reminder: -1 is the explicit "no context-pressure reminder" switch; it
	// keeps automatic compaction on and must neither be reported nor reset,
	// globally or per model.
	path := writeIssueTestConfig(t, t.TempDir(), "config.yaml", "context:\n  compaction:\n    threshold: 0.8\n    reminder: -1\nproviders:\n  openai:\n    type: responses\n    models:\n      gpt-5.6-luna:\n        compaction:\n          reminder: -1\n")
	issues, err := CollectConfigFileIssues(path, true)
	if err != nil {
		t.Fatalf("CollectConfigFileIssues: %v", err)
	}
	for _, issue := range issues {
		if strings.Contains(issue, "reminder") {
			t.Fatalf("reminder -1 must not report an issue, got %q in %v", issue, issues)
		}
	}
	cfg, err := LoadConfigFromPath(path)
	if err != nil {
		t.Fatalf("LoadConfigFromPath: %v", err)
	}
	if cfg.Context.Compaction.Reminder != CompactionReminderDisabled {
		t.Fatalf("global reminder = %v, want %v (kept)", cfg.Context.Compaction.Reminder, CompactionReminderDisabled)
	}
	mc := cfg.Providers["openai"].Models["gpt-5.6-luna"]
	if mc.Compaction == nil || mc.Compaction.Reminder == nil || *mc.Compaction.Reminder != CompactionReminderDisabled {
		t.Fatalf("per-model reminder -1 must be kept, got %+v", mc.Compaction)
	}
}

func TestCollectConfigFileIssuesReportsOutOfRangeCompactionValues(t *testing.T) {
	path := writeIssueTestConfig(t, t.TempDir(), "config.yaml", "context:\n  compaction:\n    threshold: 1.5\n    reminder: -0.2\n")
	issues, err := CollectConfigFileIssues(path, true)
	if err != nil {
		t.Fatalf("CollectConfigFileIssues: %v", err)
	}
	joined := strings.Join(issues, "\n")
	for _, want := range []string{"context.compaction.threshold", "context.compaction.reminder"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("issues = %q, want one mentioning %q", joined, want)
		}
	}
}

func TestCollectConfigFileIssuesReportsOutOfRangeModelCompactionValues(t *testing.T) {
	path := writeIssueTestConfig(t, t.TempDir(), "config.yaml", "providers:\n  openai:\n    type: responses\n    models:\n      gpt-5.6-luna:\n        compaction:\n          threshold: 2\n          reminder: 2\n")
	issues, err := CollectConfigFileIssues(path, true)
	if err != nil {
		t.Fatalf("CollectConfigFileIssues: %v", err)
	}
	joined := strings.Join(issues, "\n")
	for _, want := range []string{"openai/gpt-5.6-luna", "compaction.threshold", "compaction.reminder"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("issues = %q, want one mentioning %q", joined, want)
		}
	}
}

func TestCollectConfigFileIssuesReportsInheritedReminderAtOrAboveModelThreshold(t *testing.T) {
	// A model that overrides only its threshold inherits the global
	// reminder; when the inherited line sits at or above the model's own
	// threshold the reminder never injects for that model, which is silent
	// without this report.
	path := writeIssueTestConfig(t, t.TempDir(), "config.yaml", "context:\n  compaction:\n    threshold: 0.8\n    reminder: 0.9\nproviders:\n  openai:\n    type: responses\n    models:\n      gpt-5.6-luna:\n        compaction:\n          threshold: 0.85\n      gpt-5.6-sol:\n        compaction:\n          threshold: 0.95\n")
	issues, err := CollectConfigFileIssues(path, true)
	if err != nil {
		t.Fatalf("CollectConfigFileIssues: %v", err)
	}
	joined := strings.Join(issues, "\n")
	if !strings.Contains(joined, "openai/gpt-5.6-luna: inherits context.compaction.reminder 0.9, which is at or above this model's compaction.threshold 0.85") {
		t.Fatalf("issues = %q, want the inherited-reminder trap report for gpt-5.6-luna", joined)
	}
	if strings.Contains(joined, "gpt-5.6-sol") {
		t.Fatalf("issues = %q, a model whose threshold clears the inherited reminder must not be reported", joined)
	}
}

func TestCollectConfigFileIssuesDoesNotReportDerivedReminderAgainstModelThreshold(t *testing.T) {
	// A derived global reminder (reminder unset/0) is always below the
	// threshold, so a threshold-only model override must not be flagged.
	path := writeIssueTestConfig(t, t.TempDir(), "config.yaml", "context:\n  compaction:\n    threshold: 0.8\nproviders:\n  openai:\n    type: responses\n    models:\n      gpt-5.6-luna:\n        compaction:\n          threshold: 0.5\n")
	issues, err := CollectConfigFileIssues(path, true)
	if err != nil {
		t.Fatalf("CollectConfigFileIssues: %v", err)
	}
	joined := strings.Join(issues, "\n")
	if strings.Contains(joined, "inherits context.compaction.reminder") {
		t.Fatalf("issues = %q, derived reminder must not be reported against a model threshold", joined)
	}
}

func TestLoadConfigFromPathFallsBackForOutOfRangeCompactionValues(t *testing.T) {
	path := writeIssueTestConfig(t, t.TempDir(), "config.yaml", "context:\n  compaction:\n    threshold: 1.5\n    reminder: 2\n")
	cfg, err := LoadConfigFromPath(path)
	if err != nil {
		t.Fatalf("LoadConfigFromPath: %v", err)
	}
	if cfg.Context.Compaction.Threshold != DefaultContextCompactUsage {
		t.Fatalf("out-of-range threshold = %v, want the built-in default %v", cfg.Context.Compaction.Threshold, DefaultContextCompactUsage)
	}
	if cfg.Context.Compaction.Reminder != 0 {
		t.Fatalf("out-of-range reminder = %v, want 0 (re-derives from the threshold)", cfg.Context.Compaction.Reminder)
	}
}

func TestLoadConfigFromPathRejectsNaNInfCompactionValues(t *testing.T) {
	// yaml.v3 decodes .nan/.inf/-.inf plain scalars into float64 NaN/±Inf
	// without a type error, so they must be caught by the semantic checks and
	// fall back to the defaults instead of riding through to the runtime.
	path := writeIssueTestConfig(t, t.TempDir(), "config.yaml", "context:\n  compaction:\n    threshold: .nan\n    reminder: .inf\n")
	cfg, err := LoadConfigFromPath(path)
	if err != nil {
		t.Fatalf("LoadConfigFromPath: %v", err)
	}
	if cfg.Context.Compaction.Threshold != DefaultContextCompactUsage {
		t.Fatalf("NaN threshold = %v, want the built-in default %v", cfg.Context.Compaction.Threshold, DefaultContextCompactUsage)
	}
	if cfg.Context.Compaction.Reminder != 0 {
		t.Fatalf("+Inf reminder = %v, want 0 (re-derives from the threshold)", cfg.Context.Compaction.Reminder)
	}
	if math.IsNaN(cfg.Context.Compaction.Threshold) || math.IsInf(cfg.Context.Compaction.Reminder, 0) {
		t.Fatal("NaN/Inf compaction values rode through config loading")
	}
}

func TestLoadConfigFromPathFallsBackForOutOfRangeModelCompactionValues(t *testing.T) {
	path := writeIssueTestConfig(t, t.TempDir(), "config.yaml", "providers:\n  openai:\n    type: responses\n    models:\n      gpt-5.6-luna:\n        compaction:\n          threshold: 2\n          reminder: -.inf\n")
	cfg, err := LoadConfigFromPath(path)
	if err != nil {
		t.Fatalf("LoadConfigFromPath: %v", err)
	}
	mc := cfg.Providers["openai"].Models["gpt-5.6-luna"].Compaction
	if mc == nil {
		t.Fatal("model compaction block missing after load")
	}
	if mc.Threshold != nil {
		t.Fatalf("out-of-range model threshold = %v, want nil (inherit the global value)", *mc.Threshold)
	}
	if mc.Reminder != nil {
		t.Fatalf("out-of-range model reminder = %v, want nil (inherit the global value)", *mc.Reminder)
	}
}
