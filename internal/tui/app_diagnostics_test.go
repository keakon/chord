package tui

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/keakon/chord/internal/ctxmgr"
)

func TestTUIDiagnosticRingBufferKeepsNewestEvents(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	for i := range maxTUIDiagnosticEvents + 5 {
		m.recordTUIDiagnostic("event", "n=%d", i)
	}
	events := m.snapshotTUIDiagnosticEvents()
	if len(events) != maxTUIDiagnosticEvents {
		t.Fatalf("len(events) = %d, want %d", len(events), maxTUIDiagnosticEvents)
	}
	if !strings.Contains(events[0].Detail, "n=5") {
		t.Fatalf("first event = %q, want n=5", events[0].Detail)
	}
	if !strings.Contains(events[len(events)-1].Detail, "n=132") {
		t.Fatalf("last event = %q, want n=132", events[len(events)-1].Detail)
	}
}

func TestTUIDiagnosticCoalescesConsecutiveIdenticalEvents(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.recordTUIDiagnostic("focus", "gained=true")
	for range 200 {
		m.recordTUIDiagnostic("stream-flush", "deferred=true")
	}
	m.recordTUIDiagnostic("focus-settle", "generation=1")

	events := m.snapshotTUIDiagnosticEvents()
	if len(events) != 3 {
		t.Fatalf("len(events) = %d, want 3 (coalesced)", len(events))
	}
	if events[0].Kind != "focus" {
		t.Fatalf("events[0].Kind = %q, want focus", events[0].Kind)
	}
	skip := events[1]
	if skip.Kind != "stream-flush" {
		t.Fatalf("events[1].Kind = %q, want stream-flush", skip.Kind)
	}
	if skip.RepeatCount != 199 {
		t.Fatalf("events[1].RepeatCount = %d, want 199", skip.RepeatCount)
	}
	if skip.FirstAt.IsZero() {
		t.Fatal("events[1].FirstAt should not be zero")
	}
	if !skip.At.After(skip.FirstAt) {
		t.Fatalf("events[1].At (%v) should be after FirstAt (%v)", skip.At, skip.FirstAt)
	}
	if events[2].Kind != "focus-settle" {
		t.Fatalf("events[2].Kind = %q, want focus-settle", events[2].Kind)
	}
}

func TestTUIDiagnosticCoalesceDoesNotMergeDifferentDetail(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.recordTUIDiagnostic("stream-flush", "reason=stream")
	m.recordTUIDiagnostic("stream-flush", "reason=scroll")
	m.recordTUIDiagnostic("stream-flush", "reason=stream")

	events := m.snapshotTUIDiagnosticEvents()
	if len(events) != 3 {
		t.Fatalf("len(events) = %d, want 3 (different details not coalesced)", len(events))
	}
}

func TestTUIDiagnosticCoalescesToolCallUpdateLengthChanges(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.recordTUIDiagnostic("focus", "focused=true")
	for i := range maxTUIDiagnosticEvents + 10 {
		m.recordTUIDiagnostic("tool-call-update", "tool=TodoWrite id=call-1 block=79 len=%d->%d", i, i+1)
	}
	m.recordTUIDiagnostic("scroll-flush", "reason=wheel")

	events := m.snapshotTUIDiagnosticEvents()
	if len(events) != 3 {
		t.Fatalf("len(events) = %d, want 3 (tool-call-update coalesced)", len(events))
	}
	if events[0].Kind != "focus" {
		t.Fatalf("events[0].Kind = %q, want focus", events[0].Kind)
	}
	update := events[1]
	if update.Kind != "tool-call-update" {
		t.Fatalf("events[1].Kind = %q, want tool-call-update", update.Kind)
	}
	if update.RepeatCount != maxTUIDiagnosticEvents+9 {
		t.Fatalf("events[1].RepeatCount = %d, want %d", update.RepeatCount, maxTUIDiagnosticEvents+9)
	}
	if !strings.Contains(update.Detail, "len=137->138") {
		t.Fatalf("events[1].Detail = %q, want latest length", update.Detail)
	}
	if events[2].Kind != "scroll-flush" {
		t.Fatalf("events[2].Kind = %q, want scroll-flush", events[2].Kind)
	}
}

func TestBuildTUIDiagnosticDumpIncludesKeySections(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockUser, Content: "hello"})
	m.recordTUIDiagnostic("test", "something happened")
	m.lastImageProtocolReason = "focus-settle:inline-replay"
	m.lastImageProtocolSummary = "backend=kitty visible_inline=false kitty_visible_parts=0 kitty_seq_bytes=0 cmds=0"

	path, dump, err := m.buildDiagnosticDump(time.Date(2026, 3, 30, 12, 34, 56, 0, time.UTC), "/debug tui-dump")
	if err != nil {
		t.Fatalf("buildTUIDiagnosticDump error: %v", err)
	}
	if !strings.Contains(path, filepath.Join("logs", "tui-dumps")) {
		t.Fatalf("dump path = %q, want runtime tui-dumps dir", path)
	}
	for _, want := range []string{
		"[model]",
		"[layout]",
		"[viewport]",
		"[recent_events]",
		"[blocks.visible]",
		"[blocks.rendered]",
		"[viewport_render]",
		"[frame_bottom]",
		"[screen_buffer_bottom]",
		"[screen_buffer]",
		"something happened",
		"hello",
		"last_image_protocol_reason",
		"kitty_placement_cache_len",
		"display_state",
		"background_idle_since",
		"last_cache_sweep_at",
		"last_free_os_memory_at",
		"free_os_memory_pending",
		"free_os_memory_last_reclaimable_bytes",
		"free_os_memory_last_ran",
		"idle_sweep_generation",
		"hot_budget_dirty",
		"hot_bytes_dirty",
		"max_hot_bytes",
		"chord_version:",
		"chord_commit:",
		"chord_build_time:",
		"chord_vcs_time:",
		"chord_dirty:",
		"go_version:",
		"executable_path:",
		"executable_mtime:",
		// The [memory] section is what a user is told to read when checking
		// whether attachments left the heap or a request body stayed pinned.
		// Pin its keys here so a rename cannot silently remove them.
		"[memory]",
		"tui.hot_bytes:",
		"tui.resident_bytes:",
		"llm.body_cache_bytes:",
		"llm.image_wire_cache_bytes:",
		"llm.image_encoding_cache_bytes:",
		"llm.image_data_url_cache_bytes:",
	} {
		if !strings.Contains(dump, want) {
			t.Fatalf("dump missing %q\n%s", want, dump)
		}
	}
}

func TestBuildTUIDiagnosticDumpTruncatesHugeRenderedSectionsInMiddle(t *testing.T) {
	rows := make([]string, 0, 400)
	for i := range 400 {
		rows = append(rows, fmt.Sprintf("%03d line-%03d", i+1, i))
	}
	content := strings.Join(rows, "\n")
	if len(strings.Split(content, "\n")) <= tuiDiagnosticDumpSectionMaxLines {
		t.Fatal("test fixture must exceed dump section limit")
	}

	var sb strings.Builder
	writeDiagnosticDumpSection(&sb, content)
	dump := sb.String()
	if !strings.Contains(dump, "... truncated ") {
		t.Fatalf("section missing truncation marker\n%s", dump)
	}
	if !strings.Contains(dump, "line-000") {
		t.Fatalf("section missing head content\n%s", dump)
	}
	if !strings.Contains(dump, "line-399") {
		t.Fatalf("section missing tail content\n%s", dump)
	}
}

func TestSlashCompletionNoLongerOffersDiagnosticsCommand(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.mode = ModeInsert
	m.input.SetValue("/diag")

	if got := m.getSlashCompletions(m.input.Value()); len(got) != 0 {
		t.Fatalf("slash completions = %#v, want none for /diag", got)
	}
	if got := m.renderSlashCompletionDropdown(m.input.Value()); got != "" {
		t.Fatalf("renderSlashCompletionDropdown(/diag) = %q, want empty", got)
	}
}

func TestDiagnosticsBundleSuccessTriggersStatusCardAndToast(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	updated, cmd := m.Update(diagnosticsBundleMsg{path: "/tmp/chord-diagnostics.zip"})
	model, ok := updated.(*Model)
	if !ok {
		t.Fatalf("Update returned %T, want *Model", updated)
	}
	if cmd == nil {
		t.Fatal("successful diagnostics export should schedule toast command")
	}
	blocks := model.viewport.visibleBlocks()
	if len(blocks) == 0 {
		t.Fatal("expected diagnostics status card")
	}
	last := blocks[len(blocks)-1]
	if last.StatusTitle != "DIAGNOSTICS" {
		t.Fatalf("StatusTitle = %q, want DIAGNOSTICS", last.StatusTitle)
	}
	if !strings.Contains(last.Content, "Before sharing it") {
		t.Fatalf("status content = %q, want sensitive-content reminder", last.Content)
	}
}

func TestDiagnosticsBundleShowsImmediatelyWhileAssistantStreamIsActive(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	block := &Block{ID: m.nextBlockID, Type: BlockAssistant, Content: "streaming", Streaming: true}
	m.nextBlockID++
	m.currentAssistantBlock = block
	m.assistantBlockAppended = true
	m.appendViewportBlock(block)

	updated, cmd := m.Update(diagnosticsBundleMsg{path: "/tmp/chord-diagnostics.zip"})
	model, ok := updated.(*Model)
	if !ok {
		t.Fatalf("Update returned %T, want *Model", updated)
	}
	if cmd == nil {
		t.Fatal("successful diagnostics export should schedule toast command")
	}
	blocks := model.viewport.visibleBlocks()
	if len(blocks) != 2 {
		t.Fatalf("visible block count = %d, want 2", len(blocks))
	}
	last := blocks[len(blocks)-1]
	if last.StatusTitle != "DIAGNOSTICS" {
		t.Fatalf("StatusTitle = %q, want DIAGNOSTICS", last.StatusTitle)
	}
	if got := len(model.pendingLocalStatusCards); got != 0 {
		t.Fatalf("pendingLocalStatusCards = %d, want 0", got)
	}
	if model.currentAssistantBlock != block {
		t.Fatal("currentAssistantBlock should remain active")
	}

	model.currentAssistantBlock.Content += " more"
	model.currentAssistantBlock.InvalidateCache()
	model.viewport.InvalidateBlock(model.currentAssistantBlock.ID)
	streamBlock := model.viewport.GetFocusedBlock(block.ID)
	if streamBlock == nil {
		t.Fatal("expected streaming assistant block to remain visible")
	}
	if streamBlock.Content != "streaming more" {
		t.Fatalf("stream block content = %q, want updated assistant content", streamBlock.Content)
	}
}

type memoryBreakdownAgent struct{ sessionControlAgent }

func (*memoryBreakdownAgent) GetResidentMemoryBreakdown() ctxmgr.MessageBytes {
	return ctxmgr.MessageBytes{Text: 7, Image: 11, PDF: 13}
}

func (*memoryBreakdownAgent) AttachmentCacheBytes() int64 { return 0 }

func TestDiagnosticDumpIncludesPDFResidentBucket(t *testing.T) {
	m := NewModelWithSize(&memoryBreakdownAgent{}, 80, 24)
	_, dump, err := m.buildDiagnosticDump(time.Now(), "diagnostics")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ctxmgr.text_bytes: 7", "ctxmgr.image_bytes: 11", "ctxmgr.pdf_bytes: 13", "ctxmgr.total_bytes: 31"} {
		if !strings.Contains(dump, want) {
			t.Fatalf("dump missing %q", want)
		}
	}
}

// Diagnostic dumps run on the Update path, so they must not hydrate spilled
// blocks: reading and re-rendering every cold block froze the UI on long
// sessions. Cold blocks contribute their stored metadata only.
func TestDiagnosticDumpDoesNotHydrateSpilledBlocks(t *testing.T) {
	m := NewModelWithSize(nil, 80, 24)
	m.viewport.AppendBlock(&Block{ID: 1, Type: BlockAssistant, Content: "off-screen spilled body"})
	m.viewport.AppendBlock(&Block{ID: 2, Type: BlockUser, Content: strings.Repeat("tail line\n", 60)})
	block := m.viewport.blocks[0]
	if !m.viewport.spillBlock(block) {
		t.Fatal("fixture: expected spillBlock to succeed")
	}
	if !block.spillCold {
		t.Fatal("fixture: expected a cold block")
	}

	_, dump, err := m.buildDiagnosticDump(time.Now(), "test")
	if err != nil {
		t.Fatal(err)
	}
	if !block.spillCold {
		t.Fatal("diagnostics dump hydrated a spilled block")
	}
	rendered := dump[strings.Index(dump, "[blocks.rendered]"):]
	if end := strings.Index(rendered, "[viewport_render]"); end >= 0 {
		rendered = rendered[:end]
	}
	if strings.Contains(rendered, "off-screen spilled body") {
		t.Fatalf("dump rendered an off-screen spilled block:\n%s", rendered)
	}
	if !strings.Contains(rendered, "off-screen blocks omitted") {
		t.Fatalf("dump missing omitted-block marker:\n%s", rendered)
	}
}
