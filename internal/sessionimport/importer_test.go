package sessionimport

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/recovery"
)

func TestImport_CodexStructured_WritesRecoverableSession(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("CHORD_STATE_DIR", stateDir)
	t.Setenv("CHORD_SESSIONS_DIR", "")

	projectRoot := t.TempDir()
	input := filepath.Join(t.TempDir(), "codex-rollout.jsonl")
	if err := os.WriteFile(input, []byte(`{"timestamp":"2026-05-09T04:43:46Z","type":"session_meta","payload":{"id":"codex-1"}}
{"timestamp":"2026-05-09T04:43:47Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"run pwd"}]}}
{"timestamp":"2026-05-09T04:43:48Z","type":"response_item","payload":{"type":"function_call","name":"shell","arguments":"{\"command\":\"pwd\"}","call_id":"call_1"}}
{"timestamp":"2026-05-09T04:43:49Z","type":"response_item","payload":{"type":"function_call_output","output":"/tmp","call_id":"call_1"}}
`), 0o600); err != nil {
		t.Fatalf("write input: %v", err)
	}

	res, err := Import(context.Background(), ImportOptions{Source: "codex", InputPath: input, ProjectRoot: projectRoot})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}

	rm := recovery.NewRecoveryManager(res.SessionDir)
	msgs, err := rm.LoadMessages("main")
	if err != nil {
		t.Fatalf("LoadMessages: %v", err)
	}
	if len(msgs) != 3 {
		t.Fatalf("msgs len=%d, want 3", len(msgs))
	}
	if msgs[1].Role != "assistant" || len(msgs[1].ToolCalls) != 1 || msgs[1].ToolCalls[0].Name != "shell" {
		t.Fatalf("msgs[1]=%+v", msgs[1])
	}
	if msgs[2].Role != "tool" || msgs[2].ToolCallID != "call_1" {
		t.Fatalf("msgs[2]=%+v", msgs[2])
	}
}

func TestImport_ClaudeStructured_WritesRecoverableSessionAndReport(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("CHORD_STATE_DIR", stateDir)
	t.Setenv("CHORD_SESSIONS_DIR", "")

	projectRoot := t.TempDir()
	input := filepath.Join(t.TempDir(), "claude-transcript.jsonl")
	if err := os.WriteFile(input, []byte(`{"uuid":"u1","message":{"role":"user","content":[{"type":"text","text":"hi"}]}}
{"uuid":"a1","parentUuid":"u1","message":{"role":"assistant","content":[{"type":"thinking","thinking":"reason","signature":"sig"},{"type":"tool_use","id":"toolu_1","name":"Shell","input":{"command":"pwd"}}]}}
{"uuid":"u2","parentUuid":"a1","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"/tmp"}]}}
{"uuid":"s1","isSidechain":true,"message":{"role":"assistant","content":[{"type":"text","text":"sub-agent"}]}}
`), 0o600); err != nil {
		t.Fatalf("write input: %v", err)
	}

	res, err := Import(context.Background(), ImportOptions{Source: "claude", InputPath: input, ProjectRoot: projectRoot, ReasoningMode: ReasoningStrict})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}

	rm := recovery.NewRecoveryManager(res.SessionDir)
	msgs, err := rm.LoadMessages("main")
	if err != nil {
		t.Fatalf("LoadMessages: %v", err)
	}
	if len(msgs) != 3 {
		t.Fatalf("msgs len=%d, want 3", len(msgs))
	}
	if msgs[1].Role != "assistant" || len(msgs[1].ThinkingBlocks) != 1 || len(msgs[1].ToolCalls) != 1 {
		t.Fatalf("msgs[1]=%+v", msgs[1])
	}
	if msgs[2].Role != "tool" || msgs[2].ToolCallID != "toolu_1" || msgs[2].Content != "/tmp" {
		t.Fatalf("msgs[2]=%+v", msgs[2])
	}

	meta, err := recovery.LoadSessionMeta(res.SessionDir)
	if err != nil {
		t.Fatalf("LoadSessionMeta: %v", err)
	}
	if meta == nil || meta.ImportedFrom == nil || meta.ImportedFrom.Source != "claude" {
		t.Fatalf("ImportedFrom missing: %+v", meta)
	}

	data, err := os.ReadFile(filepath.Join(res.SessionDir, "import-report.json"))
	if err != nil {
		t.Fatalf("read import-report: %v", err)
	}
	var rep ImportReport
	if err := json.Unmarshal(data, &rep); err != nil {
		t.Fatalf("parse import-report: %v", err)
	}
	if rep.Claude == nil {
		t.Fatalf("missing claude report: %+v", rep)
	}
	if rep.Claude.StructuredToolCalls != 1 || rep.Claude.StructuredToolResults != 1 {
		t.Fatalf("unexpected structured tool stats: %+v", rep.Claude)
	}
	if rep.Claude.SidechainMessagesSkipped != 1 {
		t.Fatalf("SidechainMessagesSkipped=%d, want 1", rep.Claude.SidechainMessagesSkipped)
	}
	if len(rep.Claude.Diagnostics) == 0 {
		t.Fatalf("expected claude diagnostics, got none")
	}
}
func TestImport_OpenCode_WritesRecoverableSession(t *testing.T) {
	stateDir := t.TempDir()
	// Import path policy uses env overrides.
	t.Setenv("CHORD_STATE_DIR", stateDir)
	// Ensure sessions dir follows state dir.
	t.Setenv("CHORD_SESSIONS_DIR", "")

	projectRoot := t.TempDir()
	input := filepath.Join(t.TempDir(), "export.json")
	if err := os.WriteFile(input, []byte(`{"info":{"id":"sess-1"},"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"hello"}]}`), 0o600); err != nil {
		t.Fatalf("write input: %v", err)
	}

	res, err := Import(context.Background(), ImportOptions{Source: "opencode", InputPath: input, ProjectRoot: projectRoot})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if res.SessionID == "" || res.SessionDir == "" {
		t.Fatalf("missing session fields: %+v", res)
	}

	// Ensure recovery can read it.
	rm := recovery.NewRecoveryManager(res.SessionDir)
	msgs, err := rm.LoadMessages("main")
	if err != nil {
		t.Fatalf("LoadMessages: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("msgs len=%d, want 2", len(msgs))
	}

	// Ensure session-meta has import provenance.
	meta, err := recovery.LoadSessionMeta(res.SessionDir)
	if err != nil {
		t.Fatalf("LoadSessionMeta: %v", err)
	}
	if meta == nil || meta.ImportedFrom == nil || meta.ImportedFrom.Source != "opencode" {
		t.Fatalf("ImportedFrom missing: %+v", meta)
	}

	// Ensure import-report exists and is valid JSON.
	data, err := os.ReadFile(filepath.Join(res.SessionDir, "import-report.json"))
	if err != nil {
		t.Fatalf("read import-report: %v", err)
	}
	var rep ImportReport
	if err := json.Unmarshal(data, &rep); err != nil {
		t.Fatalf("parse import-report: %v", err)
	}
	if rep.Source != "opencode" {
		t.Fatalf("rep.Source=%q", rep.Source)
	}

	// Ensure it shows up in session list (main.jsonl non-empty).
	locator, err := config.DefaultPathLocator()
	if err != nil {
		t.Fatalf("DefaultPathLocator: %v", err)
	}
	pl, err := locator.LocateProject(projectRoot)
	if err != nil {
		t.Fatalf("LocateProject: %v", err)
	}
	list, err := recovery.ListSessions(pl.ProjectSessionsDir, "")
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	found := false
	for _, s := range list {
		if s.ID == res.SessionID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("imported session not found in ListSessions")
	}
}
