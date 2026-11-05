package sessionimport

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
)

// codexPageMetaLine renders a session_meta record with the given id and an
// optional history_base JSON object (empty for none).
func codexPageMetaLine(ordinal int, id string, historyBase string) string {
	payload := `{"id":"` + id + `"`
	if historyBase != "" {
		payload += `,"history_base":` + historyBase
	}
	payload += `}`
	return fmt.Sprintf(`{"timestamp":"2026-01-01T00:00:00Z","ordinal":%d,"type":"session_meta","payload":%s}`, ordinal, payload)
}

func codexPageUserLine(ordinal int, text string) string {
	return fmt.Sprintf(`{"timestamp":"2026-01-01T00:00:00Z","ordinal":%d,"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":%q}]}}`, ordinal, text)
}

func codexHistoryBaseJSON(rolloutID string, endOrdinalExclusive uint64, endByteOffset int) string {
	return fmt.Sprintf(`{"thread_id":"%s","end_ordinal_exclusive":%d,"end_byte_offset":%d}`, rolloutID, endOrdinalExclusive, endByteOffset)
}

func codexPrefixSize(t *testing.T, path string, records int) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := bytes.SplitAfter(data, []byte{'\n'})
	if records >= len(lines) {
		t.Fatalf("prefix of %d records exceeds file %s", records, path)
	}
	size := 0
	for _, line := range lines[:records] {
		size += len(line)
	}
	return size
}

func writeCodexRolloutFile(t *testing.T, dir string, name string, lines []string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

func codexTestJoinedContent(msgs []message.Message) string {
	parts := make([]string, 0, len(msgs))
	for _, m := range msgs {
		parts = append(parts, string(m.Role)+":"+m.Content)
	}
	return strings.Join(parts, "\n")
}

// TestConvertCodexRolloutFile_TwoPageChain verifies a referenced fork imports
// the base page's inherited prefix plus the continuation page: the parent
// page's post-fork records are truncated at the child's history_base bound.
func TestConvertCodexRolloutFile_TwoPageChain(t *testing.T) {
	dir := t.TempDir()
	threadID := codexTestUUID(1)
	base := writeCodexRolloutFile(t, dir, "rollout-2026-01-01T00-00-01-"+threadID+".jsonl", []string{
		codexPageMetaLine(0, threadID, ""),
		codexPageUserLine(1, "base zero"),
		codexPageUserLine(2, "base one"),
		codexPageUserLine(3, "base two"),
		// The parent keeps writing after the fork; these records belong to
		// the parent page only and must be cut by the child's bound.
		codexPageUserLine(4, "parent late three"),
		codexPageUserLine(5, "parent late four"),
	})
	continuation := writeCodexRolloutFile(t, dir, "rollout-2026-01-01T00-00-02-"+threadID+"_"+codexTestUUID(2)+".jsonl", []string{
		codexPageMetaLine(4, threadID, codexHistoryBaseJSON(threadID, 4, codexPrefixSize(t, base, 4))),
		codexPageUserLine(5, "child three"),
		codexPageUserLine(6, "child four"),
		codexPageUserLine(7, "child five"),
	})

	var report ImportReport
	msgs, err := convertCodexRolloutFile(continuation, []string{dir}, ReasoningStrict, &report)
	if err != nil {
		t.Fatalf("convertCodexRolloutFile: %v", err)
	}
	content := codexTestJoinedContent(msgs)
	for _, want := range []string{"base zero", "base one", "base two", "child three", "child four", "child five"} {
		if !strings.Contains(content, want) {
			t.Fatalf("stitched content missing %q:\n%s", want, content)
		}
	}
	for _, unwanted := range []string{"parent late three", "parent late four"} {
		if strings.Contains(content, unwanted) {
			t.Fatalf("stitched content kept post-fork parent record %q:\n%s", unwanted, content)
		}
	}
	if report.SourceSessionID != threadID {
		t.Fatalf("SourceSessionID=%q, want the thread id %q", report.SourceSessionID, threadID)
	}
	if report.Codex == nil {
		t.Fatal("report.Codex missing")
	}
	if report.Codex.HistoryPagesStitched != 2 {
		t.Fatalf("HistoryPagesStitched=%d, want 2", report.Codex.HistoryPagesStitched)
	}
	// Base page contributes its meta line plus the three inherited records;
	// the continuation page contributes its own meta plus three records.
	if report.Codex.HistoryItemsFromBase != 4 {
		t.Fatalf("HistoryItemsFromBase=%d, want 4", report.Codex.HistoryItemsFromBase)
	}
	if len(report.Codex.Pages) != 2 || report.Codex.Pages[0] != base || report.Codex.Pages[1] != continuation {
		t.Fatalf("Pages=%v, want [%s %s]", report.Codex.Pages, base, continuation)
	}
}

// TestConvertCodexRolloutFile_ThreePageChain verifies a chain of referenced
// forks stitches in ordinal order with every page truncated at the bound its
// child declared, producing a contiguous ordinal sequence.
func TestConvertCodexRolloutFile_ThreePageChain(t *testing.T) {
	dir := t.TempDir()
	threadID := codexTestUUID(1)
	midRollout := codexTestUUID(2)
	selectedRollout := codexTestUUID(3)
	oldest := writeCodexRolloutFile(t, dir, "rollout-2026-01-01T00-00-01-"+threadID+".jsonl", []string{
		codexPageMetaLine(0, threadID, ""),
		codexPageUserLine(1, "oldest zero"),
		codexPageUserLine(2, "oldest one"),
		codexPageUserLine(3, "oldest two"),
		codexPageUserLine(4, "oldest late"),
	})
	mid := writeCodexRolloutFile(t, dir, "rollout-2026-01-01T00-00-02-"+threadID+"_"+midRollout+".jsonl", []string{
		codexPageMetaLine(4, threadID, codexHistoryBaseJSON(threadID, 4, codexPrefixSize(t, oldest, 4))),
		codexPageUserLine(5, "mid three"),
		codexPageUserLine(6, "mid four"),
		codexPageUserLine(7, "mid late"),
	})
	selected := writeCodexRolloutFile(t, dir, "rollout-2026-01-01T00-00-03-"+threadID+"_"+selectedRollout+".jsonl", []string{
		codexPageMetaLine(7, threadID, codexHistoryBaseJSON(midRollout, 7, codexPrefixSize(t, mid, 3))),
		codexPageUserLine(8, "selected five"),
		codexPageUserLine(9, "selected six"),
	})

	var report ImportReport
	msgs, err := convertCodexRolloutFile(selected, []string{dir}, ReasoningStrict, &report)
	if err != nil {
		t.Fatalf("convertCodexRolloutFile: %v", err)
	}
	content := codexTestJoinedContent(msgs)
	for _, want := range []string{"oldest zero", "oldest one", "oldest two", "mid three", "mid four", "selected five", "selected six"} {
		if !strings.Contains(content, want) {
			t.Fatalf("stitched content missing %q:\n%s", want, content)
		}
	}
	for _, unwanted := range []string{"oldest late", "mid late"} {
		if strings.Contains(content, unwanted) {
			t.Fatalf("stitched content kept post-fork record %q:\n%s", unwanted, content)
		}
	}
	if report.Codex == nil || report.Codex.HistoryPagesStitched != 3 {
		t.Fatalf("Codex report=%+v, want 3 stitched pages", report.Codex)
	}
	// Oldest page: meta + 3 records; mid page: meta + 2 records.
	if report.Codex.HistoryItemsFromBase != 7 {
		t.Fatalf("HistoryItemsFromBase=%d, want 7", report.Codex.HistoryItemsFromBase)
	}
	if len(report.Codex.Pages) != 3 || report.Codex.Pages[0] != oldest || report.Codex.Pages[1] != mid || report.Codex.Pages[2] != selected {
		t.Fatalf("Pages=%v, want [%s %s %s]", report.Codex.Pages, oldest, mid, selected)
	}
}

// TestConvertCodexRolloutFile_LongChainImportsCompletely verifies a chain
// deeper than any fixed bound stitches whole: real chains cross thread ids
// through referenced sub-agent spawns and reach dozens of pages.
func TestConvertCodexRolloutFile_LongChainImportsCompletely(t *testing.T) {
	dir := t.TempDir()
	const pageCount = 25
	var selected string
	for i := range pageCount {
		threadID := codexTestUUID(i + 2)
		historyBase := ""
		if i > 0 {
			// This page inherited the prefix ending at its own first ordinal
			// from the previous page's rollout id.
			historyBase = codexHistoryBaseJSON(codexTestUUID(i+1), uint64(2*i), codexPrefixSize(t, selected, 2))
		}
		name := fmt.Sprintf("rollout-2026-01-01T00-00-%02d-"+threadID+".jsonl", i)
		selected = writeCodexRolloutFile(t, dir, name, []string{
			codexPageMetaLine(2*i, threadID, historyBase),
			codexPageUserLine(2*i+1, fmt.Sprintf("page %d record", i)),
		})
	}

	var report ImportReport
	msgs, err := convertCodexRolloutFile(selected, []string{dir}, ReasoningStrict, &report)
	if err != nil {
		t.Fatalf("convertCodexRolloutFile: %v", err)
	}
	if report.Codex == nil || report.Codex.HistoryPagesStitched != pageCount {
		t.Fatalf("Codex report=%+v, want %d stitched pages", report.Codex, pageCount)
	}
	if report.Codex.HistoryItemsFromBase != 2*(pageCount-1) {
		t.Fatalf("HistoryItemsFromBase=%d, want %d", report.Codex.HistoryItemsFromBase, 2*(pageCount-1))
	}
	if report.SourceSessionID != codexTestUUID(pageCount+1) {
		t.Fatalf("SourceSessionID=%q, want the final child thread", report.SourceSessionID)
	}
	for i := range pageCount {
		if want := fmt.Sprintf("page %d record", i); !strings.Contains(codexTestJoinedContent(msgs), want) {
			t.Fatalf("stitched content missing %q", want)
		}
	}
}

// TestConvertCodexRolloutFile_SelectedPageMetaGoverns pins the meta
// precedence: the selected (newest) page's session meta supplies the source
// session id even when a base page — here a parent thread's page, the
// sub-agent shape — carries its own different thread id.
func TestConvertCodexRolloutFile_SelectedPageMetaGoverns(t *testing.T) {
	dir := t.TempDir()
	baseThreadID := codexTestUUID(1)
	threadID := codexTestUUID(2)
	base := writeCodexRolloutFile(t, dir, "rollout-2026-01-01T00-00-01-"+baseThreadID+".jsonl", []string{
		codexPageMetaLine(0, baseThreadID, ""),
		codexPageUserLine(1, "base zero"),
	})
	continuation := writeCodexRolloutFile(t, dir, "rollout-2026-01-01T00-00-02-"+threadID+"_"+codexTestUUID(3)+".jsonl", []string{
		codexPageMetaLine(2, threadID, codexHistoryBaseJSON(baseThreadID, 2, codexPrefixSize(t, base, 2))),
		codexPageUserLine(3, "child only"),
	})

	var report ImportReport
	if _, err := convertCodexRolloutFile(continuation, []string{dir}, ReasoningStrict, &report); err != nil {
		t.Fatalf("convertCodexRolloutFile: %v", err)
	}
	if report.SourceSessionID != threadID {
		t.Fatalf("SourceSessionID=%q, want the selected page's id %q", report.SourceSessionID, threadID)
	}
}

// TestConvertCodexRolloutFile_Rejections covers every follow-the-chain
// refusal: a missing base page, a compressed base page, a base page in legacy
// format (records without ordinals), a base page truncated at a record
// boundary, and a cyclic chain. Each must fail outright instead of importing
// the selected page alone.
func TestConvertCodexRolloutFile_Rejections(t *testing.T) {
	t.Run("missing base page", func(t *testing.T) {
		dir := t.TempDir()
		threadID := codexTestUUID(1)
		missing := codexTestUUID(9)
		selected := writeCodexRolloutFile(t, dir, "rollout-2026-01-01T00-00-02-"+threadID+"_"+codexTestUUID(2)+".jsonl", []string{
			codexPageMetaLine(0, threadID, codexHistoryBaseJSON(missing, 3, 100)),
			codexPageUserLine(0, "child only"),
		})
		var report ImportReport
		if _, err := convertCodexRolloutFile(selected, []string{dir}, ReasoningStrict, &report); err == nil {
			t.Fatal("expected the missing base page to reject the import")
		} else if !strings.Contains(err.Error(), missing) {
			t.Fatalf("error %q does not name the missing rollout %q", err.Error(), missing)
		}
	})

	t.Run("compressed base page", func(t *testing.T) {
		dir := t.TempDir()
		threadID := codexTestUUID(1)
		baseRollout := codexTestUUID(2)
		selected := writeCodexRolloutFile(t, dir, "rollout-2026-01-01T00-00-02-"+threadID+"_"+codexTestUUID(3)+".jsonl", []string{
			codexPageMetaLine(2, threadID, codexHistoryBaseJSON(baseRollout, 2, 100)),
			codexPageUserLine(2, "child only"),
		})
		if err := os.WriteFile(filepath.Join(dir, "rollout-2026-01-01T00-00-01-"+baseRollout+".jsonl.zst"), []byte("compressed"), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
		var report ImportReport
		if _, err := convertCodexRolloutFile(selected, []string{dir}, ReasoningStrict, &report); err == nil {
			t.Fatal("expected the compressed base page to reject the import")
		} else if !strings.Contains(err.Error(), "compressed") {
			t.Fatalf("error %q does not mention compression", err.Error())
		}
	})

	t.Run("legacy base page without ordinals", func(t *testing.T) {
		dir := t.TempDir()
		threadID := codexTestUUID(1)
		baseRollout := codexTestUUID(2)
		base := writeCodexRolloutFile(t, dir, "rollout-2026-01-01T00-00-01-"+baseRollout+".jsonl", []string{
			`{"timestamp":"2026-01-01T00:00:00Z","type":"session_meta","payload":{"id":"` + baseRollout + `"}}`,
			`{"timestamp":"2026-01-01T00:00:01Z","type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"base zero"}]}}`,
		})
		selected := writeCodexRolloutFile(t, dir, "rollout-2026-01-01T00-00-02-"+threadID+"_"+codexTestUUID(3)+".jsonl", []string{
			codexPageMetaLine(2, threadID, codexHistoryBaseJSON(baseRollout, 2, codexPrefixSize(t, base, 2))),
			codexPageUserLine(3, "child only"),
		})
		var report ImportReport
		if _, err := convertCodexRolloutFile(selected, []string{dir}, ReasoningStrict, &report); err == nil {
			t.Fatal("expected the legacy base page to reject the import")
		} else if !strings.Contains(err.Error(), "ordinal") {
			t.Fatalf("error %q does not mention the missing ordinal", err.Error())
		}
	})

	t.Run("base page truncated at a record boundary", func(t *testing.T) {
		dir := t.TempDir()
		threadID := codexTestUUID(1)
		baseRollout := codexTestUUID(2)
		// The base declares a boundary of 4 but stops after ordinal 2: whole
		// records are missing, every surviving line is still valid JSONL
		// below the bound, and only the completeness check can catch it.
		base := writeCodexRolloutFile(t, dir, "rollout-2026-01-01T00-00-01-"+baseRollout+".jsonl", []string{
			codexPageMetaLine(0, baseRollout, ""),
			codexPageUserLine(1, "base zero"),
			codexPageUserLine(2, "base one"),
		})
		selected := writeCodexRolloutFile(t, dir, "rollout-2026-01-01T00-00-02-"+threadID+"_"+codexTestUUID(3)+".jsonl", []string{
			codexPageMetaLine(4, threadID, codexHistoryBaseJSON(baseRollout, 4, codexPrefixSize(t, base, 3))),
			codexPageUserLine(5, "child only"),
		})
		var report ImportReport
		if _, err := convertCodexRolloutFile(selected, []string{dir}, ReasoningStrict, &report); err == nil {
			t.Fatal("expected the truncated base page to reject the import")
		} else if !strings.Contains(err.Error(), "truncated") {
			t.Fatalf("error %q does not describe the truncation", err.Error())
		}
	})

	t.Run("cyclic chain", func(t *testing.T) {
		dir := t.TempDir()
		first := codexTestUUID(1)
		second := codexTestUUID(2)
		writeCodexRolloutFile(t, dir, "rollout-2026-01-01T00-00-01-"+first+".jsonl", []string{
			codexPageMetaLine(0, first, codexHistoryBaseJSON(second, 100, 100)),
			codexPageUserLine(0, "first page"),
		})
		selected := writeCodexRolloutFile(t, dir, "rollout-2026-01-01T00-00-02-"+second+".jsonl", []string{
			codexPageMetaLine(1, second, codexHistoryBaseJSON(first, 101, 100)),
			codexPageUserLine(1, "second page"),
		})
		var report ImportReport
		if _, err := convertCodexRolloutFile(selected, []string{dir}, ReasoningStrict, &report); err == nil {
			t.Fatal("expected the cyclic chain to reject the import")
		} else if !strings.Contains(err.Error(), "revisit") {
			t.Fatalf("error %q does not describe the cycle", err.Error())
		}
	})

	t.Run("compressed selected page", func(t *testing.T) {
		dir := t.TempDir()
		var report ImportReport
		if _, err := convertCodexRolloutFile(filepath.Join(dir, "rollout-2026-01-01T00-00-01-x.jsonl.zst"), []string{dir}, ReasoningStrict, &report); err == nil {
			t.Fatal("expected the compressed selected page to reject the import")
		}
	})
}

// TestImport_Codex_ExplicitPathStitchesHistory verifies the full import path
// stitches a history_base chain when the selected page arrives as an explicit
// file path, and that the report and provenance keep the selected page.
func TestImport_Codex_ExplicitPathStitchesHistory(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("CHORD_STATE_DIR", stateDir)
	t.Setenv("CHORD_SESSIONS_DIR", "")

	dir := t.TempDir()
	threadID := codexTestUUID(1)
	base := writeCodexRolloutFile(t, dir, "rollout-2026-01-01T00-00-01-"+threadID+".jsonl", []string{
		codexPageMetaLine(0, threadID, ""),
		codexPageUserLine(1, "base zero"),
	})
	continuation := writeCodexRolloutFile(t, dir, "rollout-2026-01-01T00-00-02-"+threadID+"_"+codexTestUUID(2)+".jsonl", []string{
		codexPageMetaLine(2, threadID, codexHistoryBaseJSON(threadID, 2, codexPrefixSize(t, base, 2))),
		codexPageUserLine(3, "child one"),
	})

	projectRoot := t.TempDir()
	res, err := Import(context.Background(), ImportOptions{Source: "codex", InputPath: continuation, SourceRoot: dir, ProjectRoot: projectRoot})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if res.Messages != 2 {
		t.Fatalf("Messages=%d, want 2", res.Messages)
	}
	if res.Report.SourceSessionID != threadID {
		t.Fatalf("SourceSessionID=%q, want %q", res.Report.SourceSessionID, threadID)
	}
	if res.Report.Codex == nil || res.Report.Codex.HistoryPagesStitched != 2 {
		t.Fatalf("Codex report=%+v, want 2 stitched pages", res.Report.Codex)
	}
	if res.Report.Codex.Pages[1] != continuation {
		t.Fatalf("Pages=%v, want the selected page last", res.Report.Codex.Pages)
	}
}
