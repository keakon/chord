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
	data := `{"timestamp":"2026-01-01T00:00:00Z","type":"session_meta","payload":{"id":"sess-1"}}
{"timestamp":"2026-01-01T00:00:01Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}}
`
	if err := os.WriteFile(rollout, []byte(data), 0o600); err != nil {
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

func TestNewImportCmd_PrintsClaudeImportSummary(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("CHORD_STATE_DIR", stateDir)
	t.Setenv("CHORD_SESSIONS_DIR", "")
	input := filepath.Join(t.TempDir(), "claude.jsonl")
	data := []byte(`{"uuid":"u1","message":{"role":"user","content":[{"type":"text","text":"hi"}]}}
{"uuid":"a1","parentUuid":"u1","message":{"role":"assistant","content":[{"type":"thinking","thinking":"think","signature":"sig"},{"type":"tool_use","id":"tool1","name":"Read","input":{"file_path":"README.md"}}]}}
{"uuid":"u2","parentUuid":"a1","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tool1","content":"ok"}]}}
{"uuid":"s1","isSidechain":true,"agentId":"agent-1","message":{"role":"assistant","content":"side"}}
`)
	if err := os.WriteFile(input, data, 0o600); err != nil {
		t.Fatalf("write input: %v", err)
	}
	cmd := newImportCmd()
	_ = cmd.Flags().Set("project", t.TempDir())
	_ = cmd.Flags().Set("dry-run", "true")

	out, err := captureStdout(t, func() error {
		return cmd.RunE(cmd, []string{"claude", input})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	for _, want := range []string{
		"Tools: structured 1 calls / 1 results, kept as source calls 0",
		"Claude: selected 3/3 main messages, skipped 1 sidechain messages",
		"Claude selection:",
		"Detected 1 sub-agent messages",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
}

func TestNewImportCmd_PrintsOpenCodeCompactionSummary(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("CHORD_STATE_DIR", stateDir)
	t.Setenv("CHORD_SESSIONS_DIR", "")
	input := filepath.Join(t.TempDir(), "opencode.json")
	// A v1 export: the compaction is a part inside a user message and its
	// summary lives in a paired assistant message.
	data := []byte(`{
  "messages": [
    {"info":{"id":"msg-head-1","role":"user"},"parts":[{"id":"p1","type":"text","text":"first request"}]},
    {"info":{"id":"msg-head-2","role":"assistant"},"parts":[{"id":"p2","type":"text","text":"head reply"}]},
    {"info":{"id":"msg-tail-1","role":"user"},"parts":[{"id":"p3","type":"text","text":"recent question"}]},
    {"info":{"id":"msg-tail-2","role":"assistant"},"parts":[{"id":"p4","type":"text","text":"recent reply"}]},
    {"info":{"id":"msg-marker","role":"user"},"parts":[{"id":"p5","type":"compaction","auto":false,"tail_start_id":"msg-tail-1"}]},
    {"info":{"id":"msg-summary","role":"assistant","parentID":"msg-marker","summary":true,"time":{"created":1,"completed":2}},"parts":[{"id":"p6","type":"text","text":"## Objective\n- keep going"}]},
    {"info":{"id":"msg-after-1","role":"user"},"parts":[{"id":"p7","type":"text","text":"next question"}]}
  ]
}`)
	if err := os.WriteFile(input, data, 0o600); err != nil {
		t.Fatalf("write input: %v", err)
	}
	cmd := newImportCmd()
	_ = cmd.Flags().Set("project", t.TempDir())
	_ = cmd.Flags().Set("dry-run", "true")

	out, err := captureStdout(t, func() error {
		return cmd.RunE(cmd, []string{"opencode", input})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	for _, want := range []string{
		// The checkpoint plus the two tail messages and the one after it: the
		// head, the marker, and the paired summary are all spoken for.
		"Messages: 4",
		"OpenCode: 1 compaction checkpoint(s), 2 earlier message(s) replaced, 2 recent message(s) kept, 0 marker(s) skipped",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
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
		"Tools: structured 1 calls / 1 results, kept as source calls 0",
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
