package sessionimport

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
)

func TestConvertCodexRolloutFile_HistoryIntegrity(t *testing.T) {
	baseID, childID := codexTestUUID(1), codexTestUUID(2)
	prefix := codexPageMetaLine(0, baseID, "") + "\n" + codexPageUserLine(1, "inherited") + "\n"
	reference := fmt.Sprintf(`{"thread_id":%q,"end_ordinal_exclusive":2,"end_byte_offset":%d}`, baseID, len(prefix))
	noMeta := codexPageUserLine(0, "first") + "\n" + codexPageUserLine(1, "second") + "\n"
	for _, tc := range []struct {
		name       string
		base       string
		reference  string
		childID    string
		childStart int
		childItems string
		wantError  bool
	}{
		{name: "frozen prefix ignores incomplete append", base: prefix + `{"ordinal":2,`, reference: reference, childID: childID, childStart: 2, childItems: codexPageUserLine(3, "child")},
		{name: "empty base", reference: reference, childID: childID, childStart: 2, childItems: codexPageUserLine(3, "child"), wantError: true},
		{name: "base missing metadata", base: noMeta, reference: codexHistoryBaseJSON(baseID, 2, len(noMeta)), childID: childID, childStart: 2, childItems: codexPageUserLine(3, "child"), wantError: true},
		{name: "base ordinal gap", base: strings.Replace(prefix, `"ordinal":1`, `"ordinal":2`, 1), reference: reference, childID: childID, childStart: 2, childItems: codexPageUserLine(3, "child"), wantError: true},
		{name: "base duplicate ordinal", base: strings.Replace(prefix, `"ordinal":1`, `"ordinal":0`, 1), reference: reference, childID: childID, childStart: 2, childItems: codexPageUserLine(3, "child"), wantError: true},
		{name: "byte boundary excludes required record", base: prefix, reference: codexHistoryBaseJSON(baseID, 2, len(codexPageMetaLine(0, baseID, ""))+1), childID: childID, childStart: 2, childItems: codexPageUserLine(3, "child"), wantError: true},
		{name: "byte boundary includes excluded record", base: prefix, reference: codexHistoryBaseJSON(baseID, 1, len(prefix)), childID: childID, childStart: 1, childItems: codexPageUserLine(2, "child"), wantError: true},
		{name: "base starts after zero", base: codexPageUserLine(1, "inherited") + "\n", reference: reference, childID: childID, childStart: 2, childItems: codexPageUserLine(3, "child"), wantError: true},
		{name: "child starts too late", base: prefix, reference: reference, childID: childID, childStart: 4, childItems: codexPageUserLine(5, "child"), wantError: true},
		{name: "child overlaps base", base: prefix, reference: reference, childID: childID, childStart: 1, childItems: codexPageUserLine(2, "child"), wantError: true},
		{name: "child duplicate ordinal", base: prefix, reference: reference, childID: childID, childStart: 2, childItems: codexPageUserLine(2, "child"), wantError: true},
		{name: "child missing ordinal", base: prefix, reference: reference, childID: childID, childStart: 2, childItems: `{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"child"}]}}`, wantError: true},
		{name: "child null ordinal", base: prefix, reference: reference, childID: childID, childStart: 2, childItems: `{"ordinal":null,"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"child"}]}}`, wantError: true},
		{name: "child missing identity", base: prefix, reference: reference, childStart: 2, childItems: codexPageUserLine(3, "child"), wantError: true},
		{name: "invalid reference type", base: prefix, reference: `"invalid"`, childID: childID, childStart: 2, childItems: codexPageUserLine(3, "child"), wantError: true},
		{name: "missing reference id", base: prefix, reference: `{"end_ordinal_exclusive":2,"end_byte_offset":1}`, childID: childID, childStart: 2, childItems: codexPageUserLine(3, "child"), wantError: true},
		{name: "missing ordinal boundary", base: prefix, reference: fmt.Sprintf(`{"thread_id":%q,"end_byte_offset":%d}`, baseID, len(prefix)), childID: childID, childStart: 2, childItems: codexPageUserLine(3, "child"), wantError: true},
		{name: "missing byte boundary", base: prefix, reference: fmt.Sprintf(`{"thread_id":%q,"end_ordinal_exclusive":2}`, baseID), childID: childID, childStart: 2, childItems: codexPageUserLine(3, "child"), wantError: true},
		{name: "byte boundary beyond file", base: prefix, reference: fmt.Sprintf(`{"thread_id":%q,"end_ordinal_exclusive":2,"end_byte_offset":%d}`, baseID, len(prefix)+1), childID: childID, childStart: 2, childItems: codexPageUserLine(3, "child"), wantError: true},
		{name: "byte boundary before newline", base: prefix, reference: fmt.Sprintf(`{"thread_id":%q,"end_ordinal_exclusive":2,"end_byte_offset":%d}`, baseID, len(prefix)-1), childID: childID, childStart: 2, childItems: codexPageUserLine(3, "child"), wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			basePath := filepath.Join(dir, "rollout-2026-01-01T00-00-00-"+baseID+".jsonl")
			if err := os.WriteFile(basePath, []byte(tc.base), 0o600); err != nil {
				t.Fatal(err)
			}
			selected := writeCodexRolloutFile(t, dir, "rollout-2026-01-01T00-00-01-"+childID+".jsonl", []string{
				codexPageMetaLine(tc.childStart, tc.childID, tc.reference),
				tc.childItems,
			})
			var report ImportReport
			msgs, err := convertCodexRolloutFile(selected, []string{dir}, ReasoningStrict, &report)
			if tc.wantError {
				if err == nil || len(msgs) != 0 {
					t.Fatalf("incomplete history accepted: messages=%d, error=%v", len(msgs), err)
				}
				if !strings.Contains(err.Error(), basePath) && !strings.Contains(err.Error(), selected) {
					t.Fatalf("error does not identify the failing page: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(msgs) != 2 || report.SourceSessionID != childID {
				t.Fatalf("messages=%+v, source=%q", msgs, report.SourceSessionID)
			}
		})
	}
}

func TestConvertCodexRolloutFile_ToolPairAcrossArchivedBase(t *testing.T) {
	active, archived := t.TempDir(), t.TempDir()
	baseID, childID := codexTestUUID(1), codexTestUUID(2)
	base := writeCodexRolloutFile(t, archived, "rollout-2026-01-01T00-00-00-"+baseID+".jsonl", []string{
		codexPageMetaLine(0, baseID, ""),
		codexPageUserLine(1, "inspect the files"),
		`{"ordinal":2,"type":"response_item","payload":{"type":"function_call","name":"sample_tool","arguments":"{}","call_id":"call_1"}}`,
	})
	selected := writeCodexRolloutFile(t, active, "rollout-2026-01-01T00-00-01-"+childID+".jsonl", []string{
		codexPageMetaLine(3, childID, codexHistoryBaseJSON(baseID, 3, codexPrefixSize(t, base, 3))),
		`{"ordinal":4,"type":"response_item","payload":{"type":"function_call_output","output":"complete","call_id":"call_1"}}`,
	})
	var report ImportReport
	msgs, err := convertCodexRolloutFile(selected, []string{active, archived}, ReasoningStrict, &report)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 3 || len(msgs[1].ToolCalls) != 1 || msgs[2].Role != message.RoleTool {
		t.Fatalf("messages=%+v, want user, call and result", msgs)
	}
	if msgs[1].ToolCalls[0].ID != msgs[2].ToolCallID || msgs[2].Content != "complete" {
		t.Fatalf("call and result were not paired: %+v", msgs)
	}
	if report.SourceSessionID != childID || report.Codex.Pages[0] != base {
		t.Fatalf("report=%+v, pages=%+v", report, report.Codex.Pages)
	}
}

func TestImport_Codex_InvalidHistoryDoesNotWriteSession(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("CHORD_STATE_DIR", stateDir)
	t.Setenv("CHORD_SESSIONS_DIR", "")
	dir := t.TempDir()
	baseID, childID := codexTestUUID(1), codexTestUUID(2)
	base := writeCodexRolloutFile(t, dir, "rollout-2026-01-01T00-00-00-"+baseID+".jsonl", []string{
		codexPageMetaLine(0, baseID, ""),
		codexPageUserLine(1, "inherited"),
	})
	selected := writeCodexRolloutFile(t, dir, "rollout-2026-01-01T00-00-01-"+childID+".jsonl", []string{
		codexPageMetaLine(4, childID, codexHistoryBaseJSON(baseID, 2, codexPrefixSize(t, base, 2))),
		codexPageUserLine(5, "child"),
	})
	res, err := Import(context.Background(), ImportOptions{
		Source: "codex", InputPath: selected, SourceRoot: dir, ProjectRoot: t.TempDir(),
	})
	if err == nil || res != nil {
		t.Fatalf("Import=%+v, %v; want no session for invalid history", res, err)
	}
	if err := filepath.WalkDir(stateDir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && entry.Name() == "main.jsonl" {
			t.Errorf("invalid import wrote a transcript at %s", path)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
