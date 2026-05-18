package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewImportCmd_AllowsIDWithoutFile(t *testing.T) {
	cmd := newImportCmd()
	if err := cmd.Args(cmd, []string{"codex"}); err != nil {
		t.Fatalf("Args rejected single source arg: %v", err)
	}
}

func TestNewImportCmd_RejectsNoInputAndNoID(t *testing.T) {
	cmd := newImportCmd()
	_ = cmd.Flags().Set("project", t.TempDir())
	if err := cmd.RunE(cmd, []string{"codex"}); err == nil {
		t.Fatal("expected error when neither file nor --id is provided")
	}
}

func TestNewImportCmd_SucceedsWithIDAndRoot(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("CHORD_STATE_DIR", stateDir)
	t.Setenv("CHORD_SESSIONS_DIR", "")
	root := t.TempDir()
	rollout := filepath.Join(root, "2026", "05", "07", "rollout-1.jsonl")
	if err := os.MkdirAll(filepath.Dir(rollout), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(rollout, []byte(`{"timestamp":"2026-01-01T00:00:00Z","item":{"session_id":"sess-1","role":"user","content":"hi"}}`+"\n"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	cmd := newImportCmd()
	_ = cmd.Flags().Set("project", t.TempDir())
	_ = cmd.Flags().Set("id", "sess-1")
	_ = cmd.Flags().Set("root", root)

	out, err := captureStdout(t, func() error {
		return cmd.RunE(cmd, []string{"codex"})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "Imported codex session") {
		t.Fatalf("unexpected output: %q", out)
	}
}

func TestNewImportCmd_PrintsImportSummaryAndLimitsWarnings(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("CHORD_STATE_DIR", stateDir)
	t.Setenv("CHORD_SESSIONS_DIR", "")
	input := filepath.Join(t.TempDir(), "rollout.jsonl")
	data := []byte(`{"timestamp":"2026-01-01T00:00:00Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"run"}]}}
{"timestamp":"2026-01-01T00:00:01Z","type":"response_item","payload":{"type":"function_call","name":"shell","arguments":"{\"command\":\"pwd\"}","call_id":"call_1"}}
{"timestamp":"2026-01-01T00:00:02Z","type":"response_item","payload":{"type":"function_call_output","output":"/tmp","call_id":"call_1"}}
`)
	if err := os.WriteFile(input, data, 0o600); err != nil {
		t.Fatalf("write input: %v", err)
	}
	cmd := newImportCmd()
	_ = cmd.Flags().Set("project", t.TempDir())

	out, err := captureStdout(t, func() error {
		return cmd.RunE(cmd, []string{"codex", input})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	for _, want := range []string{
		"Tools: structured 1 calls / 1 results, downgraded 0 calls / 0 results",
		"Skipped: 0 entries, 0 status events, 0 duplicates",
		"Report:",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}

	var b strings.Builder
	printImportWarnings(&b, []string{"w1", "w2", "w3", "w4", "w5", "w6"}, "/tmp/import-report.json")
	warnings := b.String()
	if strings.Count(warnings, "- w") != maxImportWarningsShown || !strings.Contains(warnings, "1 more warnings omitted; see /tmp/import-report.json") {
		t.Fatalf("warning output not limited as expected: %q", warnings)
	}

	b.Reset()
	printImportWarnings(&b, []string{"w1", "w2", "w3", "w4", "w5", "w6"}, "")
	dryRunWarnings := b.String()
	if strings.Contains(dryRunWarnings, "see import-report.json") || !strings.Contains(dryRunWarnings, "run without --dry-run") {
		t.Fatalf("dry-run warning output should not point to a missing report file: %q", dryRunWarnings)
	}
}
