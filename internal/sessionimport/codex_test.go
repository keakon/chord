package sessionimport

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/keakon/chord/internal/recovery"
)

func TestConvertCodexRollout_ParsesBasicJSONL(t *testing.T) {
	data := []byte(`{"timestamp":"2026-01-01T00:00:00Z","item":{"type":"EventMsg","session_id":"sess-1","role":"user","content":"hi"}}
{"timestamp":"2026-01-01T00:00:01Z","item":{"type":"ResponseItem","role":"assistant","content":"hello"}}
{"timestamp":"2026-01-01T00:00:02Z","item":{"FunctionCall":{"name":"bash","arguments":{"command":"ls"}}}}
{"timestamp":"2026-01-01T00:00:03Z","item":{"FunctionCallOutput":{"output":"file1"}}}
{"timestamp":"2026-01-01T00:00:04Z","item":{"type":"Reasoning","text":"secret"}}
`)

	var report ImportReport
	msgs, err := convertCodexRollout(data, ReasoningStrict, &report)
	if err != nil {
		t.Fatalf("convertCodexRollout: %v", err)
	}
	if len(msgs) != 4 {
		t.Fatalf("msgs len=%d, want 4", len(msgs))
	}
	if msgs[0].Role != "user" || msgs[0].Content != "hi" {
		t.Fatalf("msg0=%+v", msgs[0])
	}
	if msgs[1].Role != "assistant" || msgs[1].Content != "hello" {
		t.Fatalf("msg1=%+v", msgs[1])
	}
	if msgs[2].Role != "assistant" || msgs[2].Content == "" {
		t.Fatalf("msg2=%+v", msgs[2])
	}
	if msgs[3].Role != "assistant" || msgs[3].Content == "" {
		t.Fatalf("msg3=%+v", msgs[3])
	}
	if report.SourceSessionID != "sess-1" {
		t.Fatalf("SourceSessionID=%q, want sess-1", report.SourceSessionID)
	}
	if report.ReasoningBlocksSkipped == 0 {
		t.Fatalf("ReasoningBlocksSkipped=%d, want >0", report.ReasoningBlocksSkipped)
	}
	if report.ToolEntriesRendered != 2 {
		t.Fatalf("ToolEntriesRendered=%d, want 2", report.ToolEntriesRendered)
	}
}

func TestConvertCodexRollout_ParsesNewPayloadSchema(t *testing.T) {
	data := []byte(`{"timestamp":"2026-05-09T04:43:46Z","type":"session_meta","payload":{"id":"019e0955-00ce-73a2-bc23-213802de80d6"}}
{"timestamp":"2026-05-09T04:43:47Z","type":"response_item","payload":{"type":"message","role":"developer","content":[{"type":"input_text","text":"internal guidance"}]}}
{"timestamp":"2026-05-09T04:43:48Z","type":"event_msg","payload":{"type":"user_message","message":"hello"}}
{"timestamp":"2026-05-09T04:43:49Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"hello"}]}}
{"timestamp":"2026-05-09T04:43:50Z","type":"event_msg","payload":{"type":"agent_message","message":"hi there"}}
{"timestamp":"2026-05-09T04:43:51Z","type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hi there"}],"phase":"commentary"}}
{"timestamp":"2026-05-09T04:43:52Z","type":"response_item","payload":{"type":"function_call","name":"shell","arguments":"{\"command\":\"pwd\"}"}}
{"timestamp":"2026-05-09T04:43:53Z","type":"response_item","payload":{"type":"function_call_output","output":"/tmp"}}
`)

	var report ImportReport
	msgs, err := convertCodexRollout(data, ReasoningStrict, &report)
	if err != nil {
		t.Fatalf("convertCodexRollout: %v", err)
	}
	if len(msgs) != 4 {
		t.Fatalf("msgs len=%d, want 4", len(msgs))
	}
	if msgs[0].Role != "user" || msgs[0].Content != "hello" {
		t.Fatalf("msg0=%+v", msgs[0])
	}
	if msgs[1].Role != "assistant" || msgs[1].Content != "hi there" {
		t.Fatalf("msg1=%+v", msgs[1])
	}
	if msgs[2].Role != "assistant" || msgs[2].Content == "" {
		t.Fatalf("msg2=%+v", msgs[2])
	}
	if msgs[3].Role != "assistant" || msgs[3].Content == "" {
		t.Fatalf("msg3=%+v", msgs[3])
	}
	if report.SourceSessionID != "019e0955-00ce-73a2-bc23-213802de80d6" {
		t.Fatalf("SourceSessionID=%q", report.SourceSessionID)
	}
	if report.ToolEntriesRendered != 2 {
		t.Fatalf("ToolEntriesRendered=%d, want 2", report.ToolEntriesRendered)
	}
	if report.SkippedEntries < 3 {
		t.Fatalf("SkippedEntries=%d, want at least 3", report.SkippedEntries)
	}
}

func TestImport_Codex_WritesRecoverableSession(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("CHORD_STATE_DIR", stateDir)
	t.Setenv("CHORD_SESSIONS_DIR", "")

	projectRoot := t.TempDir()
	input := filepath.Join(t.TempDir(), "rollout.jsonl")
	if err := os.WriteFile(input, []byte(`{"timestamp":"2026-01-01T00:00:00Z","item":{"session_id":"sess-1","role":"user","content":"hi"}}
{"timestamp":"2026-01-01T00:00:01Z","item":{"role":"assistant","content":"hello"}}
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
	if len(msgs) != 2 {
		t.Fatalf("msgs len=%d, want 2", len(msgs))
	}

	meta, err := recovery.LoadSessionMeta(res.SessionDir)
	if err != nil {
		t.Fatalf("LoadSessionMeta: %v", err)
	}
	if meta == nil || meta.ImportedFrom == nil || meta.ImportedFrom.Source != "codex" {
		t.Fatalf("ImportedFrom missing: %+v", meta)
	}
	if meta.ImportedFrom.SourceSessionID != "sess-1" {
		t.Fatalf("SourceSessionID=%q, want sess-1", meta.ImportedFrom.SourceSessionID)
	}
}
