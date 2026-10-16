package sessionimport

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/message"
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

func TestImport_OpenCode_InlineImageSurvivesInstallAsFile(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("CHORD_STATE_DIR", stateDir)
	t.Setenv("CHORD_SESSIONS_DIR", "")

	projectRoot := t.TempDir()
	input := filepath.Join(t.TempDir(), "export.json")
	png := "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAIAAACQd1PeAAAADElEQVR4nGP4z8AAAAMBAQDJ/pLvAAAAAElFTkSuQmCC"
	body := `{"info":{"id":"sess-2"},"messages":[{"role":"user","parts":[{"type":"file","mime":"image/png","filename":"clipboard","url":"data:image/png;base64,` + png + `"}]}]}`
	if err := os.WriteFile(input, []byte(body), 0o600); err != nil {
		t.Fatalf("write input: %v", err)
	}

	res, err := Import(context.Background(), ImportOptions{Source: "opencode", InputPath: input, ProjectRoot: projectRoot})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}

	rm := recovery.NewRecoveryManager(res.SessionDir)
	msgs, err := rm.LoadMessages("main")
	if err != nil {
		t.Fatalf("LoadMessages: %v", err)
	}
	if len(msgs) != 1 || len(msgs[0].Parts) != 1 {
		t.Fatalf("image part not persisted: %+v", msgs)
	}
	part := msgs[0].Parts[0]
	if part.Type != message.ContentPartImage {
		t.Fatalf("part type=%q, want image", part.Type)
	}
	// The attachment is materialized under the installed session directory;
	// a path still naming the staging directory would point nowhere.
	if !strings.HasPrefix(part.ImagePath, res.SessionDir) {
		t.Fatalf("ImagePath=%q, want it under %q", part.ImagePath, res.SessionDir)
	}
	if _, err := os.Stat(part.ImagePath); err != nil {
		t.Fatalf("attachment file missing at %q: %v", part.ImagePath, err)
	}
	saved, err := os.ReadFile(part.ImagePath)
	if err != nil {
		t.Fatalf("read attachment: %v", err)
	}
	if !bytes.HasPrefix(saved, []byte("\x89PNG\r\n\x1a\n")) {
		t.Fatalf("attachment is not a PNG: %d bytes", len(saved))
	}
}

// Attachments are materialized under <sessionDir>/images/, so only that prefix
// is rewritten after the staging directory is installed. Transcript text that
// happens to name the staging directory keeps the path the source recorded.
func TestRewriteStagedAttachmentPathsRewritesOnlyTheImagesPrefix(t *testing.T) {
	stagedDir := filepath.Join(t.TempDir(), "importing-20261018")
	sessionDir := filepath.Join(t.TempDir(), "20261018000000")
	if err := os.MkdirAll(filepath.Join(sessionDir, "images"), 0o755); err != nil {
		t.Fatal(err)
	}
	lines := []message.Message{
		{Role: message.RoleTool, ToolCallID: "c1", Content: "attachment at " + filepath.Join(stagedDir, "images", "1-0.png")},
		{Role: message.RoleTool, ToolCallID: "c2", Content: "staged under " + stagedDir + ", which is not an attachment path"},
		{Role: message.RoleTool, ToolCallID: "c3", Content: "notes at " + filepath.Join(stagedDir, "docs", "notes.md")},
	}
	var buf bytes.Buffer
	for _, m := range lines {
		encoded, err := json.Marshal(m)
		if err != nil {
			t.Fatalf("marshal message: %v", err)
		}
		buf.Write(encoded)
		buf.WriteByte('\n')
	}
	logPath := filepath.Join(sessionDir, identity.MainSessionLogFilename)
	if err := os.WriteFile(logPath, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := rewriteStagedAttachmentPaths(sessionDir, stagedDir, sessionDir); err != nil {
		t.Fatalf("rewriteStagedAttachmentPaths: %v", err)
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, jsonPathBytes(filepath.Join(sessionDir, "images", "1-0.png"))) {
		t.Fatalf("attachment path was not moved to %q", sessionDir)
	}
	for _, kept := range []string{
		"staged under " + stagedDir + ", which is not an attachment path",
		filepath.Join(stagedDir, "docs", "notes.md"),
	} {
		if !bytes.Contains(data, jsonPathBytes(kept)) {
			t.Fatalf("unrelated path %q was rewritten", kept)
		}
	}
}

// The rewrite runs inside the staging directory, before the install renames it
// into place: a session that could not be rewritten never appears at its final
// path. The installed spelling is therefore only substituted, never written to
// directly.
func TestRewriteStagedAttachmentPathsWritesTheStagedLogBeforeInstall(t *testing.T) {
	stagedDir := filepath.Join(t.TempDir(), "importing-20261018")
	sessionDir := filepath.Join(t.TempDir(), "20261018000000")
	if err := os.MkdirAll(stagedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(stagedDir, identity.MainSessionLogFilename)
	stagedLine := []message.Message{
		{Role: message.RoleTool, ToolCallID: "c1", Content: "attachment at " + filepath.Join(stagedDir, "images", "1-0.png")},
	}
	encoded, err := json.Marshal(stagedLine[0])
	if err != nil {
		t.Fatalf("marshal message: %v", err)
	}
	if err := os.WriteFile(logPath, append(encoded, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := rewriteStagedAttachmentPaths(stagedDir, stagedDir, sessionDir); err != nil {
		t.Fatalf("rewriteStagedAttachmentPaths: %v", err)
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read staged log: %v", err)
	}
	if !bytes.Contains(data, jsonPathBytes(filepath.Join(sessionDir, "images", "1-0.png"))) {
		t.Fatalf("staged log does not name the installed attachment path: %s", data)
	}
	if _, err := os.Stat(sessionDir); !os.IsNotExist(err) {
		t.Fatalf("session dir exists before install (statErr=%v)", err)
	}
}
