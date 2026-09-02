package config

import (
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
