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

func TestResolvedConfigIssuesValid(t *testing.T) {
	path := writeIssueTestConfig(t, t.TempDir(), "config.yaml", "providers:\n  sample:\n    type: responses\n    models:\n      test-model:\n        limit:\n          context: 100000\n          output: 64000\n")
	issues, err := resolvedFileIssues(path, "")
	if err != nil {
		t.Fatalf("ResolvedConfigIssues: %v", err)
	}
	if len(issues) != 0 {
		t.Fatalf("issues = %#v, want none", issues)
	}
}

func TestResolvedConfigIssuesReportsAllUnknownKeys(t *testing.T) {
	path := writeIssueTestConfig(t, t.TempDir(), "config.yaml", "bogus_top_level: true\nproviders:\n  sample:\n    type: responses\n    api_key: $X\n    models:\n      test-model:\n        include_thoughts: true\n")
	issues, err := resolvedFileIssues(path, "")
	if err != nil {
		t.Fatalf("ResolvedConfigIssues: %v", err)
	}
	joined := strings.Join(issues, "\n")
	for _, want := range []string{"bogus_top_level", "api_key", "include_thoughts"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("issues = %q, want one mentioning %q", joined, want)
		}
	}
}

func TestResolvedConfigIssuesReportsWrongType(t *testing.T) {
	path := writeIssueTestConfig(t, t.TempDir(), "config.yaml", "max_output_tokens: abc\n")
	issues, err := resolvedFileIssues(path, "")
	if err != nil {
		t.Fatalf("ResolvedConfigIssues: %v", err)
	}
	if len(issues) == 0 || !strings.Contains(strings.Join(issues, "\n"), "cannot unmarshal") {
		t.Fatalf("issues = %#v, want wrong-type report for max_output_tokens", issues)
	}
}

func TestResolvedConfigIssuesReportsSemanticProblems(t *testing.T) {
	path := writeIssueTestConfig(t, t.TempDir(), "config.yaml", "providers:\n  sample:\n    type: responses\n    retry_backoff: linear\ndiagnostics:\n  python:\n    large_file:\n      line_threshold: -1\n")
	issues, err := resolvedFileIssues(path, "")
	if err != nil {
		t.Fatalf("ResolvedConfigIssues: %v", err)
	}
	joined := strings.Join(issues, "\n")
	for _, want := range []string{"invalid retry_backoff", "line_threshold"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("issues = %q, want one mentioning %q", joined, want)
		}
	}
}

func TestResolvedConfigIssuesReportsMalformedYAML(t *testing.T) {
	path := writeIssueTestConfig(t, t.TempDir(), "config.yaml", "providers: [\n")
	if _, err := LoadResolvedConfig(path, ""); err == nil {
		t.Fatal("malformed YAML must prevent loading")
	}
}

func TestResolvedConfigIssuesAllowsReminderAtOrAboveThreshold(t *testing.T) {
	// A reminder >= threshold is legal and must not report an issue: the
	// reminder fires on the threshold crossing itself (usage reaching
	// min(reminder, threshold)) while the grace period defers the actual
	// compaction; the reminder never raises the compaction line.
	path := writeIssueTestConfig(t, t.TempDir(), "config.yaml", "context:\n  compaction:\n    threshold: 0.65\n    reminder: 0.7\n")
	issues, err := resolvedFileIssues(path, "")
	if err != nil {
		t.Fatalf("ResolvedConfigIssues: %v", err)
	}
	for _, issue := range issues {
		if strings.Contains(issue, "compaction") {
			t.Fatalf("reminder at or above the threshold must not report issues, got %q in %v", issue, issues)
		}
	}
}

func TestResolvedConfigIssuesReportsNegativeQuestionTimeout(t *testing.T) {
	path := writeIssueTestConfig(t, t.TempDir(), "config.yaml", "question_timeout: -5\n")
	issues, err := resolvedFileIssues(path, "")
	if err != nil {
		t.Fatalf("ResolvedConfigIssues: %v", err)
	}
	if joined := strings.Join(issues, "\n"); !strings.Contains(joined, "question_timeout") {
		t.Fatalf("issues = %q, want one mentioning question_timeout", joined)
	}
}

func TestLoadConfigFromPathResetsNegativeQuestionTimeout(t *testing.T) {
	path := writeIssueTestConfig(t, t.TempDir(), "config.yaml", "question_timeout: -5\n")
	cfg, err := LoadConfigFromPath(path)
	if err != nil {
		t.Fatalf("LoadConfigFromPath: %v", err)
	}
	if cfg.QuestionTimeout != 0 {
		t.Fatalf("question_timeout = %d, want 0 after rejecting the negative value", cfg.QuestionTimeout)
	}
}

func TestResolvedConfigIssuesReportsDeadReminderOnDisabledCompaction(t *testing.T) {
	path := writeIssueTestConfig(t, t.TempDir(), "config.yaml", "context:\n  compaction:\n    threshold: 0\n    reminder: 0.7\n")
	issues, err := resolvedFileIssues(path, "")
	if err != nil {
		t.Fatalf("ResolvedConfigIssues: %v", err)
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

func TestResolvedConfigIssuesAllowsValidPerModelReminder(t *testing.T) {
	// The per-model compaction block lives on the model definition
	// (ModelConfig.compaction); the old context.compaction.models table is
	// gone and must not be referenced. Valid global + per-model lines report
	// no issue.
	path := writeIssueTestConfig(t, t.TempDir(), "config.yaml", "context:\n  compaction:\n    threshold: 0.65\n    reminder: 0.5\nproviders:\n  openai:\n    type: responses\n    models:\n      gpt-5.6-luna:\n        compaction:\n          threshold: 0.3\n          reminder: 0.2\n")
	issues, err := resolvedFileIssues(path, "")
	if err != nil {
		t.Fatalf("ResolvedConfigIssues: %v", err)
	}
	for _, issue := range issues {
		if strings.Contains(issue, "compaction") {
			t.Fatalf("valid compaction config must not report issues, got %q in %v", issue, issues)
		}
	}
}

func TestResolvedConfigIssuesReportsDeadModelReminder(t *testing.T) {
	// A reminder configured against a model-level threshold of 0 never fires
	// (threshold 0 disables auto-compaction and reminders for that model); the
	// issue names the model definition so the user fixes it on the model side.
	path := writeIssueTestConfig(t, t.TempDir(), "config.yaml", "providers:\n  openai:\n    type: responses\n    models:\n      gpt-5.6-luna:\n        compaction:\n          threshold: 0\n          reminder: 0.2\n")
	issues, err := resolvedFileIssues(path, "")
	if err != nil {
		t.Fatalf("ResolvedConfigIssues: %v", err)
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

func TestResolvedProjectConfigIssuesReportsUnsupportedFields(t *testing.T) {
	path := writeIssueTestConfig(t, t.TempDir(), "config.yaml", "provder:\n  x: 1\nproviders:\n  sample:\n    type: responses\n")
	issues, err := resolvedFileIssues("", path)
	if err != nil {
		t.Fatalf("ResolvedProjectConfigIssues: %v", err)
	}
	joined := strings.Join(issues, "\n")
	if !strings.Contains(joined, `"provder"`) || !strings.Contains(joined, "not supported in project config") {
		t.Fatalf("issues = %q, want unsupported project field report for provder", joined)
	}
}

func TestResolvedProjectConfigIssuesReportsInvalidNativeThinking(t *testing.T) {
	path := writeIssueTestConfig(t, t.TempDir(), "config.yaml", `providers:
  sample:
    type: chat-completions
    models:
      model-1:
        compat:
          chat_completions:
            native_thinking: auto
`)
	issues, err := resolvedFileIssues("", path)
	if err != nil {
		t.Fatalf("ResolvedProjectConfigIssues: %v", err)
	}
	joined := strings.Join(issues, "\n")
	if !strings.Contains(joined, `invalid native_thinking "auto"`) || !strings.Contains(joined, `for model "model-1" in provider "sample"`) {
		t.Fatalf("issues = %q, want the invalid native_thinking selector reported against the model", joined)
	}
}

func TestResolvedProjectConfigIssuesMissingFile(t *testing.T) {
	issues, err := resolvedFileIssues("", filepath.Join(t.TempDir(), "nope.yaml"))
	if err != nil {
		t.Fatalf("ResolvedProjectConfigIssues: %v", err)
	}
	if issues != nil {
		t.Fatalf("issues = %#v, want nil for missing file", issues)
	}
}

func TestResolvedConfigIssuesReportsLegacyBooleanCompress(t *testing.T) {
	// The pre-1.0 boolean compress form (`compress: true` / `compress:
	// false`) silently enabled gzip; it is now rejected so users migrate to
	// the explicit encoding, with a hint naming the replacement.
	for _, value := range []string{"true", "false"} {
		path := writeIssueTestConfig(t, t.TempDir(), "config.yaml", "providers:\n  sample:\n    type: responses\n    compress: "+value+"\n")
		issues, err := resolvedFileIssues(path, "")
		if err != nil {
			t.Fatalf("ResolvedConfigIssues(%v): %v", value, err)
		}
		joined := strings.Join(issues, "\n")
		if !strings.Contains(joined, "removed boolean form") || !strings.Contains(joined, "gzip") {
			t.Fatalf("issues for compress: %v = %q, want a removed-boolean-form hint naming gzip", value, joined)
		}
	}
}

func TestLoadConfigFromPathIgnoresLegacyBooleanCompress(t *testing.T) {
	// The tolerant loader treats compress: true as invalid and falls back to
	// no compression instead of silently enabling gzip.
	path := writeIssueTestConfig(t, t.TempDir(), "config.yaml", "providers:\n  sample:\n    type: responses\n    compress: true\n")
	cfg, err := LoadConfigFromPath(path)
	if err != nil {
		t.Fatalf("LoadConfigFromPath: %v", err)
	}
	if got := cfg.Providers["sample"].Compress; got != "" {
		t.Fatalf("compress after legacy true = %q, want empty (compression off)", got)
	}
}

func TestResolvedConfigIssuesReportsUnknownCompressEncoding(t *testing.T) {
	path := writeIssueTestConfig(t, t.TempDir(), "config.yaml", "providers:\n  sample:\n    type: responses\n    compress: brotli\n")
	issues, err := resolvedFileIssues(path, "")
	if err != nil {
		t.Fatalf("ResolvedConfigIssues: %v", err)
	}
	joined := strings.Join(issues, "\n")
	if !strings.Contains(joined, "invalid compress value") || !strings.Contains(joined, "gzip") || !strings.Contains(joined, "zstd") {
		t.Fatalf("issues = %q, want an invalid-compress report naming gzip and zstd", joined)
	}
}

func TestResolvedConfigIssuesAllowsRequestCompressionEncodings(t *testing.T) {
	for _, value := range []string{"gzip", "zstd"} {
		path := writeIssueTestConfig(t, t.TempDir(), "config.yaml", "providers:\n  sample:\n    type: responses\n    compress: "+value+"\n")
		issues, err := resolvedFileIssues(path, "")
		if err != nil {
			t.Fatalf("ResolvedConfigIssues(%v): %v", value, err)
		}
		if len(issues) != 0 {
			t.Fatalf("issues for compress: %v = %#v, want none", value, issues)
		}
		cfg, err := LoadConfigFromPath(path)
		if err != nil {
			t.Fatalf("LoadConfigFromPath(%v): %v", value, err)
		}
		if got := cfg.Providers["sample"].Compress; got != value {
			t.Fatalf("compress after load = %q, want %q", got, value)
		}
	}
}

func TestCompactionReminderMinusOneDisablesWithoutIssue(t *testing.T) {
	// reminder: -1 is the explicit "no context-pressure reminder" switch; it
	// keeps automatic compaction on and must neither be reported nor reset,
	// globally or per model.
	path := writeIssueTestConfig(t, t.TempDir(), "config.yaml", "context:\n  compaction:\n    threshold: 0.8\n    reminder: -1\nproviders:\n  openai:\n    type: responses\n    models:\n      gpt-5.6-luna:\n        compaction:\n          reminder: -1\n")
	issues, err := resolvedFileIssues(path, "")
	if err != nil {
		t.Fatalf("ResolvedConfigIssues: %v", err)
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

func TestResolvedConfigIssuesReportsOutOfRangeCompactionValues(t *testing.T) {
	path := writeIssueTestConfig(t, t.TempDir(), "config.yaml", "context:\n  compaction:\n    threshold: 1.5\n    reminder: -0.2\n")
	issues, err := resolvedFileIssues(path, "")
	if err != nil {
		t.Fatalf("ResolvedConfigIssues: %v", err)
	}
	joined := strings.Join(issues, "\n")
	for _, want := range []string{"context.compaction.threshold", "context.compaction.reminder"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("issues = %q, want one mentioning %q", joined, want)
		}
	}
}

func TestResolvedConfigIssuesReportsOutOfRangeModelCompactionValues(t *testing.T) {
	path := writeIssueTestConfig(t, t.TempDir(), "config.yaml", "providers:\n  openai:\n    type: responses\n    models:\n      gpt-5.6-luna:\n        compaction:\n          threshold: 2\n          reminder: 2\n")
	issues, err := resolvedFileIssues(path, "")
	if err != nil {
		t.Fatalf("ResolvedConfigIssues: %v", err)
	}
	joined := strings.Join(issues, "\n")
	for _, want := range []string{"openai/gpt-5.6-luna", "compaction.threshold", "compaction.reminder"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("issues = %q, want one mentioning %q", joined, want)
		}
	}
}

func TestResolvedConfigIssuesReportsInheritedReminderAtOrAboveModelThreshold(t *testing.T) {
	// A model that overrides only its threshold inherits the global
	// reminder; when the inherited line sits at or above the model's own
	// threshold the reminder never injects for that model, which is silent
	// without this report.
	path := writeIssueTestConfig(t, t.TempDir(), "config.yaml", "context:\n  compaction:\n    threshold: 0.8\n    reminder: 0.9\nproviders:\n  openai:\n    type: responses\n    models:\n      gpt-5.6-luna:\n        compaction:\n          threshold: 0.85\n      gpt-5.6-sol:\n        compaction:\n          threshold: 0.95\n")
	issues, err := resolvedFileIssues(path, "")
	if err != nil {
		t.Fatalf("ResolvedConfigIssues: %v", err)
	}
	joined := strings.Join(issues, "\n")
	if !strings.Contains(joined, "openai/gpt-5.6-luna: inherits context.compaction.reminder 0.9, which is at or above this model's compaction.threshold 0.85") {
		t.Fatalf("issues = %q, want the inherited-reminder trap report for gpt-5.6-luna", joined)
	}
	if strings.Contains(joined, "gpt-5.6-sol") {
		t.Fatalf("issues = %q, a model whose threshold clears the inherited reminder must not be reported", joined)
	}
}

func TestResolvedConfigIssuesDoesNotReportDerivedReminderAgainstModelThreshold(t *testing.T) {
	// A derived global reminder (reminder unset/0) is always below the
	// threshold, so a threshold-only model override must not be flagged.
	path := writeIssueTestConfig(t, t.TempDir(), "config.yaml", "context:\n  compaction:\n    threshold: 0.8\nproviders:\n  openai:\n    type: responses\n    models:\n      gpt-5.6-luna:\n        compaction:\n          threshold: 0.5\n")
	issues, err := resolvedFileIssues(path, "")
	if err != nil {
		t.Fatalf("ResolvedConfigIssues: %v", err)
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

func resolvedFileIssues(globalPath, projectPath string) ([]string, error) {
	if globalPath == "" {
		globalPath = filepath.Join(filepath.Dir(projectPath), "missing-global.yaml")
	}
	rc, err := LoadResolvedConfig(globalPath, projectPath)
	if err != nil {
		return nil, err
	}
	var issues []string
	for _, diagnostic := range rc.Diagnostics {
		if diagnostic.Fallback != "" && diagnostic.Fallback != "built-in defaults" {
			issues = append(issues, diagnostic.String())
		}
	}
	return issues, nil
}

func TestQuestionAutoSelectionConfigValidation(t *testing.T) {
	path := writeIssueTestConfig(t, t.TempDir(), "config.yaml", "question_auto_select_timeout: -1\n")
	cfg, err := LoadConfigFromPath(path)
	issues, issueErr := resolvedFileIssues(path, "")
	if issueErr != nil {
		t.Fatal(issueErr)
	}
	if err != nil {
		t.Fatal(err)
	}
	if cfg.QuestionAutoSelectTimeout != 0 || !strings.Contains(strings.Join(issues, "\n"), "question_auto_select_timeout") {
		t.Fatalf("negative auto selection: %+v %v", cfg, issues)
	}
}
