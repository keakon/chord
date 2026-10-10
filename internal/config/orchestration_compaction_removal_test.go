package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestOrchestrationCompactionFieldIsUnknown(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	writeTestFile(t, path, "context:\n  compaction:\n    threshold: 0.4\norchestration:\n  subagent_compact_usage: 0.2\n  subagent_queue_messages: 7\n")
	cfg, err := LoadConfigFromPath(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Context.Compaction.Threshold != 0.4 || cfg.Orchestration.SubAgentQueueMessages != 7 {
		t.Fatalf("valid settings were lost: context=%+v orchestration=%+v", cfg.Context, cfg.Orchestration)
	}
	issues, err := resolvedFileIssues(path, "")
	if err != nil {
		t.Fatal(err)
	}
	if joined := strings.Join(issues, "\n"); !strings.Contains(joined, "subagent_compact_usage") || !strings.Contains(joined, "not found") {
		t.Fatalf("issues = %q, want unknown field diagnostic", joined)
	}
}
