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
