package sessionimport

import (
	"strings"
	"testing"

	"github.com/keakon/chord/internal/message"
)

// v1 export shape: a compaction is a part inside a user message, and its
// summary body lives in a paired assistant message.
const openCodeV1CompactionExport = `{
  "messages": [
    {"info":{"id":"msg-head-1","role":"user"},"parts":[{"id":"p1","type":"text","text":"first request"}]},
    {"info":{"id":"msg-head-2","role":"assistant"},"parts":[{"id":"p2","type":"text","text":"head reply"}]},
    {"info":{"id":"msg-tail-1","role":"user"},"parts":[{"id":"p3","type":"text","text":"recent question"}]},
    {"info":{"id":"msg-tail-2","role":"assistant"},"parts":[{"id":"p4","type":"text","text":"recent reply"}]},
    {"info":{"id":"msg-marker","role":"user"},"parts":[{"id":"p5","type":"compaction","auto":false,"tail_start_id":"msg-tail-1"}]},
    {"info":{"id":"msg-summary","role":"assistant","parentID":"msg-marker","summary":true,"time":{"created":1,"completed":2}},"parts":[
      {"id":"p6","type":"reasoning","text":"summarize only"},
      {"id":"p7","type":"text","text":"## Objective\n- keep going"}
    ]},
    {"info":{"id":"msg-after-1","role":"user"},"parts":[{"id":"p8","type":"text","text":"next question"}]},
    {"info":{"id":"msg-after-2","role":"assistant"},"parts":[{"id":"p9","type":"text","text":"next reply"}]}
  ]
}`

func TestConvertOpenCodeExport_CompactionBecomesCheckpointReplacingHead(t *testing.T) {
	var report ImportReport
	msgs, err := convertOpenCodeExport([]byte(openCodeV1CompactionExport), ReasoningStrict, &report)
	if err != nil {
		t.Fatalf("convertOpenCodeExport: %v", err)
	}

	// checkpoint + tail (2) + subsequent (2); the head, marker and paired
	// summary message are all spoken for by the checkpoint.
	if len(msgs) != 5 {
		t.Fatalf("msgs len=%d, want 5: %+v", len(msgs), msgs)
	}
	checkpoint := msgs[0]
	if checkpoint.Role != message.RoleUser || !checkpoint.IsCompactionSummary {
		t.Fatalf("first message is not a checkpoint: %+v", checkpoint)
	}
	if checkpoint.CompactionSummaryMode != message.CompactionSummaryModeModelSummary {
		t.Fatalf("CompactionSummaryMode=%q, want model_summary", checkpoint.CompactionSummaryMode)
	}
	if checkpoint.Provenance == nil || checkpoint.Provenance.Source != "import:opencode" {
		t.Fatalf("checkpoint provenance = %+v, want the opencode import provenance", checkpoint.Provenance)
	}
	wantContent := "[Session Anchors]\nOriginal request:\n- first request\n\n[/Session Anchors]\n\n## Objective\n- keep going"
	if checkpoint.Content != wantContent {
		t.Fatalf("checkpoint content =\n%q\nwant\n%q", checkpoint.Content, wantContent)
	}
	if got := message.CompactionAnchorsSection(checkpoint.Content); got != "Original request:\n- first request" {
		t.Fatalf("anchors section = %q, want the original first request", got)
	}
	for _, msg := range msgs[1:] {
		if msg.IsCompactionSummary {
			t.Fatalf("only the first message may be a checkpoint: %+v", msg)
		}
	}
	if msgs[1].Role != message.RoleUser || msgs[1].Content != "recent question" {
		t.Fatalf("tail not kept after the checkpoint: %+v", msgs[1])
	}
	if msgs[3].Role != message.RoleUser || msgs[3].Content != "next question" {
		t.Fatalf("subsequent history not kept: %+v", msgs[3])
	}
	for _, warning := range report.Warnings {
		if strings.Contains(warning, "compaction") {
			t.Fatalf("unexpected compaction warning: %q", warning)
		}
	}
	if report.OpenCode == nil {
		t.Fatalf("OpenCode report section missing: %+v", report)
	}
	if report.OpenCode.CompactionCheckpoints != 1 {
		t.Fatalf("CompactionCheckpoints=%d, want 1", report.OpenCode.CompactionCheckpoints)
	}
	if report.OpenCode.CompactionHeadReplaced != 2 {
		t.Fatalf("CompactionHeadReplaced=%d, want 2", report.OpenCode.CompactionHeadReplaced)
	}
	if report.OpenCode.CompactionTailKept != 2 {
		t.Fatalf("CompactionTailKept=%d, want 2", report.OpenCode.CompactionTailKept)
	}
	if report.OpenCode.MarkersSkipped != 0 {
		t.Fatalf("MarkersSkipped=%d, want 0", report.OpenCode.MarkersSkipped)
	}
}

func TestConvertOpenCodeExport_CompactionTailStartUnresolvableKeepsHistory(t *testing.T) {
	data := strings.Replace(openCodeV1CompactionExport, `"tail_start_id":"msg-tail-1"`, `"tail_start_id":"msg-missing"`, 1)
	var report ImportReport
	msgs, err := convertOpenCodeExport([]byte(data), ReasoningStrict, &report)
	if err != nil {
		t.Fatalf("convertOpenCodeExport: %v", err)
	}
	// Fail-open: the full history stays, with the checkpoint standing where
	// the marker was — nothing is guessed about where the tail began.
	if len(msgs) != 7 {
		t.Fatalf("msgs len=%d, want 7: %+v", len(msgs), msgs)
	}
	checkpoint := msgs[4]
	if !checkpoint.IsCompactionSummary {
		t.Fatalf("checkpoint not at the marker position: %+v", msgs)
	}
	if !strings.Contains(checkpoint.Content, "## Objective") {
		t.Fatalf("checkpoint lost the summary body: %q", checkpoint.Content)
	}
	if strings.Contains(checkpoint.Content, "Session Anchors") {
		t.Fatalf("no head was replaced, so no original-request anchor belongs here: %q", checkpoint.Content)
	}
	if msgs[0].Content != "first request" || msgs[1].Content != "head reply" {
		t.Fatalf("history before the checkpoint was not kept: %+v", msgs[:2])
	}
	if msgs[5].Content != "next question" {
		t.Fatalf("history after the checkpoint was not kept: %+v", msgs[5])
	}
	if !hasWarning(report.Warnings, "tail_start_id") {
		t.Fatalf("warnings=%q, want a tail_start_id warning", report.Warnings)
	}
	if report.OpenCode == nil || report.OpenCode.CompactionHeadReplaced != 0 {
		t.Fatalf("OpenCode report = %+v, want no head replacement", report.OpenCode)
	}
}

func TestConvertOpenCodeExport_CompactionWithoutTailStartIDKeepsHistory(t *testing.T) {
	data := strings.Replace(openCodeV1CompactionExport, `,"tail_start_id":"msg-tail-1"`, "", 1)
	var report ImportReport
	msgs, err := convertOpenCodeExport([]byte(data), ReasoningStrict, &report)
	if err != nil {
		t.Fatalf("convertOpenCodeExport: %v", err)
	}
	if len(msgs) != 7 {
		t.Fatalf("msgs len=%d, want 7: %+v", len(msgs), msgs)
	}
	if !msgs[4].IsCompactionSummary {
		t.Fatalf("checkpoint not at the marker position: %+v", msgs)
	}
	if !hasWarning(report.Warnings, "no tail_start_id") {
		t.Fatalf("warnings=%q, want a missing-tail warning", report.Warnings)
	}
}

func TestConvertOpenCodeExport_CompactionWithoutPairedSummarySkipsMarker(t *testing.T) {
	data := `{
  "messages": [
    {"info":{"id":"msg-1","role":"user"},"parts":[{"id":"p1","type":"text","text":"hello"}]},
    {"info":{"id":"msg-2","role":"user"},"parts":[{"id":"p2","type":"compaction","auto":true,"tail_start_id":"msg-1"}]},
    {"info":{"id":"msg-3","role":"assistant"},"parts":[{"id":"p3","type":"text","text":"after"}]}
  ]
}`
	var report ImportReport
	msgs, err := convertOpenCodeExport([]byte(data), ReasoningStrict, &report)
	if err != nil {
		t.Fatalf("convertOpenCodeExport: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("msgs len=%d, want 2: %+v", len(msgs), msgs)
	}
	if msgs[0].Content != "hello" || msgs[1].Content != "after" {
		t.Fatalf("history not kept around the unpaired marker: %+v", msgs)
	}
	for _, msg := range msgs {
		if msg.IsCompactionSummary {
			t.Fatalf("no checkpoint may exist without a paired summary: %+v", msg)
		}
	}
	if !hasWarning(report.Warnings, "no completed summary") {
		t.Fatalf("warnings=%q, want a pairing warning", report.Warnings)
	}
	if report.OpenCode == nil || report.OpenCode.MarkersSkipped != 1 {
		t.Fatalf("OpenCode report = %+v, want one skipped marker", report.OpenCode)
	}
}

func TestConvertOpenCodeExport_CompactionPairedSummaryWithErrorIsNotUsable(t *testing.T) {
	data := `{
  "messages": [
    {"info":{"id":"msg-1","role":"user"},"parts":[{"id":"p1","type":"text","text":"hello"}]},
    {"info":{"id":"msg-2","role":"user"},"parts":[{"id":"p2","type":"compaction","auto":true,"tail_start_id":"msg-1"}]},
    {"info":{"id":"msg-3","role":"assistant","parentID":"msg-2","summary":true,"error":{"name":"ProviderError"},"time":{"created":1}},"parts":[{"id":"p3","type":"text","text":"failed summary"}]},
    {"info":{"id":"msg-4","role":"assistant"},"parts":[{"id":"p4","type":"text","text":"after"}]}
  ]
}`
	var report ImportReport
	msgs, err := convertOpenCodeExport([]byte(data), ReasoningStrict, &report)
	if err != nil {
		t.Fatalf("convertOpenCodeExport: %v", err)
	}
	if len(msgs) != 3 {
		t.Fatalf("msgs len=%d, want 3: %+v", len(msgs), msgs)
	}
	for _, msg := range msgs {
		if msg.IsCompactionSummary {
			t.Fatalf("a failed summary must not become a checkpoint: %+v", msg)
		}
	}
	if msgs[1].Content != "failed summary" {
		t.Fatalf("the failed summary should stay as ordinary history: %+v", msgs[1])
	}
	if !hasWarning(report.Warnings, "no completed summary") {
		t.Fatalf("warnings=%q, want a pairing warning", report.Warnings)
	}
}

func TestConvertOpenCodeExport_MultipleCompactionsKeepOnlyTheNewest(t *testing.T) {
	data := `{
  "messages": [
    {"info":{"id":"msg-1","role":"user"},"parts":[{"id":"p1","type":"text","text":"first request"}]},
    {"info":{"id":"msg-2","role":"user"},"parts":[{"id":"p2","type":"text","text":"recent request"}]},
    {"info":{"id":"msg-3","role":"user"},"parts":[{"id":"p3","type":"compaction","auto":false,"tail_start_id":"msg-1"}]},
    {"info":{"id":"msg-4","role":"assistant","parentID":"msg-3","summary":true,"time":{"created":1,"completed":2}},"parts":[{"id":"p4","type":"text","text":"older summary"}]},
    {"info":{"id":"msg-5","role":"user"},"parts":[{"id":"p5","type":"text","text":"middle request"}]},
    {"info":{"id":"msg-6","role":"user"},"parts":[{"id":"p6","type":"compaction","auto":false,"tail_start_id":"msg-2"}]},
    {"info":{"id":"msg-7","role":"assistant","parentID":"msg-6","summary":true,"time":{"created":3,"completed":4}},"parts":[{"id":"p7","type":"text","text":"newest summary"}]},
    {"info":{"id":"msg-8","role":"user"},"parts":[{"id":"p8","type":"text","text":"final request"}]}
  ]
}`
	var report ImportReport
	msgs, err := convertOpenCodeExport([]byte(data), ReasoningStrict, &report)
	if err != nil {
		t.Fatalf("convertOpenCodeExport: %v", err)
	}
	// The newest compaction replaces everything before its tail. The older
	// marker's part is skipped inside that tail and the older summary stays as
	// the assistant text the source kept beside it.
	if len(msgs) != 5 {
		t.Fatalf("msgs len=%d, want 5: %+v", len(msgs), msgs)
	}
	if !msgs[0].IsCompactionSummary || !strings.Contains(msgs[0].Content, "newest summary") {
		t.Fatalf("first message is not the newest checkpoint: %+v", msgs[0])
	}
	if strings.Contains(msgs[0].Content, "older summary") {
		t.Fatalf("checkpoint carried the superseded summary: %q", msgs[0].Content)
	}
	if msgs[1].Content != "recent request" {
		t.Fatalf("tail not kept after the checkpoint: %+v", msgs[1])
	}
	if msgs[2].Role != message.RoleAssistant || msgs[2].Content != "older summary" {
		t.Fatalf("older summary not kept as history inside the tail: %+v", msgs[2])
	}
	if msgs[3].Content != "middle request" || msgs[4].Content != "final request" {
		t.Fatalf("subsequent history not kept: %+v", msgs[3:])
	}
	if report.OpenCode == nil || report.OpenCode.CompactionCheckpoints != 1 || report.OpenCode.MarkersSkipped != 1 {
		t.Fatalf("OpenCode report = %+v, want one checkpoint and one superseded marker", report.OpenCode)
	}
	for _, warning := range report.Warnings {
		if strings.Contains(warning, "compaction") {
			t.Fatalf("unexpected compaction warning: %q", warning)
		}
	}
}

func TestConvertOpenCodeExport_CompactionMarkerKeepsItsOtherParts(t *testing.T) {
	data := `{
  "messages": [
    {"info":{"id":"msg-1","role":"user"},"parts":[{"id":"p1","type":"text","text":"first request"}]},
    {"info":{"id":"msg-2","role":"user"},"parts":[
      {"id":"p2","type":"text","text":"request that triggered compaction"},
      {"id":"p3","type":"compaction","auto":false,"tail_start_id":"msg-2"}
    ]},
    {"info":{"id":"msg-3","role":"assistant","parentID":"msg-2","summary":true,"time":{"created":1,"completed":2}},"parts":[{"id":"p4","type":"text","text":"summary body"}]}
  ]
}`
	var report ImportReport
	msgs, err := convertOpenCodeExport([]byte(data), ReasoningStrict, &report)
	if err != nil {
		t.Fatalf("convertOpenCodeExport: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("msgs len=%d, want 2: %+v", len(msgs), msgs)
	}
	if !msgs[0].IsCompactionSummary {
		t.Fatalf("first message is not the checkpoint: %+v", msgs[0])
	}
	// The marker's own text survives: only the compaction part is absorbed.
	if msgs[1].Role != message.RoleUser || msgs[1].Content != "request that triggered compaction" {
		t.Fatalf("marker's other parts not kept: %+v", msgs[1])
	}
}

func TestConvertOpenCodeExport_CompactionHeadWithoutUserTextCarriesNoAnchor(t *testing.T) {
	data := `{
  "messages": [
    {"info":{"id":"msg-1","role":"assistant"},"parts":[{"id":"p1","type":"text","text":"assistant only head"}]},
    {"info":{"id":"msg-2","role":"user"},"parts":[{"id":"p2","type":"text","text":"recent request"}]},
    {"info":{"id":"msg-3","role":"user"},"parts":[{"id":"p3","type":"compaction","auto":false,"tail_start_id":"msg-2"}]},
    {"info":{"id":"msg-4","role":"assistant","parentID":"msg-3","summary":true,"time":{"created":1,"completed":2}},"parts":[{"id":"p4","type":"text","text":"summary body"}]}
  ]
}`
	var report ImportReport
	msgs, err := convertOpenCodeExport([]byte(data), ReasoningStrict, &report)
	if err != nil {
		t.Fatalf("convertOpenCodeExport: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("msgs len=%d, want 2: %+v", len(msgs), msgs)
	}
	if msgs[0].Content != "summary body" {
		t.Fatalf("checkpoint content = %q, want the bare summary", msgs[0].Content)
	}
	if strings.Contains(msgs[0].Content, "Session Anchors") {
		t.Fatalf("no user request in the head, so no anchor belongs here: %q", msgs[0].Content)
	}
}

func TestConvertOpenCodeExport_FlatCompactionAnchorsContentArrayUserText(t *testing.T) {
	data := `{
  "messages": [
    {"id":"msg-1","type":"user","content":[
      "first request",
      {"type":"text","text":"more details"},
      {"type":"text","text":"ignored text","ignored":true},
      {"type":"text","text":"synthetic guidance","synthetic":true},
      {"type":"tool","text":"tool output"},
      {"type":"image","text":"image caption"}
    ]},
    {"id":"msg-2","type":"assistant","text":"head reply"},
    {"id":"msg-3","type":"user","text":"recent request"},
    {"id":"msg-4","type":"compaction","status":"completed","summary":"summary body","metadata":{"keepID":"msg-3"}}
  ]
}`
	var report ImportReport
	msgs, err := convertOpenCodeExport([]byte(data), ReasoningStrict, &report)
	if err != nil {
		t.Fatalf("convertOpenCodeExport: %v", err)
	}
	if len(msgs) != 2 || !msgs[0].IsCompactionSummary {
		t.Fatalf("msgs = %+v, want checkpoint followed by kept tail", msgs)
	}
	want := message.CompactionAnchorsOpenTag + message.CompactionAnchorsRequestLabel +
		"\n- first request more details\n" + message.CompactionAnchorsCloseTag +
		"\n\nsummary body"
	if msgs[0].Content != want {
		t.Fatalf("checkpoint content =\n%q\nwant\n%q", msgs[0].Content, want)
	}
	if msgs[1].Content != "recent request" {
		t.Fatalf("kept tail = %+v, want recent request", msgs[1])
	}
}

// Flat export shape: the compaction is a message of its own, summary inline.
func TestConvertOpenCodeExport_FlatCompactionMessageBecomesCheckpoint(t *testing.T) {
	data := `{
  "messages": [
    {"id":"msg-1","type":"user","text":"first request","time":{"created":1}},
    {"id":"msg-2","type":"assistant","content":[{"type":"text","text":"head reply"}],"time":{"created":2}},
    {"id":"msg-3","type":"user","text":"recent request","time":{"created":3}},
    {"id":"msg-4","type":"compaction","status":"completed","reason":"auto","summary":"flat summary body","metadata":{"keepID":"msg-3"},"time":{"created":4}},
    {"id":"msg-5","type":"user","text":"next request","time":{"created":5}}
  ]
}`
	var report ImportReport
	msgs, err := convertOpenCodeExport([]byte(data), ReasoningStrict, &report)
	if err != nil {
		t.Fatalf("convertOpenCodeExport: %v", err)
	}
	if len(msgs) != 3 {
		t.Fatalf("msgs len=%d, want 3: %+v", len(msgs), msgs)
	}
	if !msgs[0].IsCompactionSummary || !strings.Contains(msgs[0].Content, "flat summary body") {
		t.Fatalf("first message is not the flat checkpoint: %+v", msgs[0])
	}
	if !strings.Contains(msgs[0].Content, "Original request:\n- first request") {
		t.Fatalf("flat checkpoint lost the original request anchor: %q", msgs[0].Content)
	}
	if msgs[0].CompactionSummaryMode != message.CompactionSummaryModeModelSummary {
		t.Fatalf("CompactionSummaryMode=%q, want model_summary", msgs[0].CompactionSummaryMode)
	}
	if msgs[1].Content != "recent request" {
		t.Fatalf("tail not kept after the checkpoint: %+v", msgs[1])
	}
	if msgs[2].Content != "next request" {
		t.Fatalf("subsequent history not kept: %+v", msgs[2])
	}
	if report.OpenCode == nil || report.OpenCode.CompactionHeadReplaced != 2 || report.OpenCode.CompactionTailKept != 1 {
		t.Fatalf("OpenCode report = %+v, want 2 replaced and 1 kept", report.OpenCode)
	}
	for _, warning := range report.Warnings {
		if strings.Contains(warning, "compaction") {
			t.Fatalf("unexpected compaction warning: %q", warning)
		}
	}
}

func TestConvertOpenCodeExport_FlatCompactionNativeWindowWarnsAndMapsSummary(t *testing.T) {
	data := `{
  "messages": [
    {"id":"msg-1","type":"user","text":"first request","time":{"created":1}},
    {"id":"msg-2","type":"user","text":"recent request","time":{"created":2}},
    {"id":"msg-3","type":"compaction","status":"completed","reason":"auto","summary":"native summary body","native":{"messages":[{"role":"user","content":"opaque"}]},"metadata":{"keepID":"msg-2"},"time":{"created":3}}
  ]
}`
	var report ImportReport
	msgs, err := convertOpenCodeExport([]byte(data), ReasoningStrict, &report)
	if err != nil {
		t.Fatalf("convertOpenCodeExport: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("msgs len=%d, want 2: %+v", len(msgs), msgs)
	}
	if !msgs[0].IsCompactionSummary || !strings.Contains(msgs[0].Content, "native summary body") {
		t.Fatalf("native compaction did not map its summary: %+v", msgs[0])
	}
	if !hasWarning(report.Warnings, "provider-native window") {
		t.Fatalf("warnings=%q, want a native-window warning", report.Warnings)
	}
}

func TestConvertOpenCodeExport_FlatCompactionTailStartUnresolvableKeepsHistory(t *testing.T) {
	data := `{
  "messages": [
    {"id":"msg-1","type":"user","text":"first request","time":{"created":1}},
    {"id":"msg-2","type":"assistant","content":[{"type":"text","text":"head reply"}],"time":{"created":2}},
    {"id":"msg-3","type":"compaction","status":"completed","reason":"auto","summary":"flat summary body","metadata":{"keepID":"msg-missing"},"time":{"created":3}},
    {"id":"msg-4","type":"user","text":"next request","time":{"created":4}}
  ]
}`
	var report ImportReport
	msgs, err := convertOpenCodeExport([]byte(data), ReasoningStrict, &report)
	if err != nil {
		t.Fatalf("convertOpenCodeExport: %v", err)
	}
	// Fail-open, like the v1 shape: the full history stays and the checkpoint
	// stands where the marker was — the message the checkpoint speaks for is
	// absorbed, so the checkpoint still has to be placed before it.
	if len(msgs) != 4 {
		t.Fatalf("msgs len=%d, want 4: %+v", len(msgs), msgs)
	}
	if !msgs[2].IsCompactionSummary || !strings.Contains(msgs[2].Content, "flat summary body") {
		t.Fatalf("checkpoint not at the marker position: %+v", msgs)
	}
	if msgs[0].Content != "first request" || msgs[1].Content != "head reply" || msgs[3].Content != "next request" {
		t.Fatalf("history around the checkpoint not kept: %+v", msgs)
	}
	if report.OpenCode == nil || report.OpenCode.CompactionCheckpoints != 1 || report.OpenCode.CompactionHeadReplaced != 0 {
		t.Fatalf("OpenCode report = %+v, want one checkpoint and no head replacement", report.OpenCode)
	}
	if !hasWarning(report.Warnings, "could not be located") {
		t.Fatalf("warnings=%q, want a keepID warning", report.Warnings)
	}
}

func TestConvertOpenCodeExport_FlatCompactionNotCompletedIsSkipped(t *testing.T) {
	data := `{
  "messages": [
    {"id":"msg-1","type":"user","text":"first request","time":{"created":1}},
    {"id":"msg-2","type":"compaction","status":"running","reason":"auto","summary":"pending summary","time":{"created":2}},
    {"id":"msg-3","type":"user","text":"next request","time":{"created":3}}
  ]
}`
	var report ImportReport
	msgs, err := convertOpenCodeExport([]byte(data), ReasoningStrict, &report)
	if err != nil {
		t.Fatalf("convertOpenCodeExport: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("msgs len=%d, want 2: %+v", len(msgs), msgs)
	}
	for _, msg := range msgs {
		if msg.IsCompactionSummary {
			t.Fatalf("an unfinished compaction must not become a checkpoint: %+v", msg)
		}
	}
	if !hasWarning(report.Warnings, "no completed summary") {
		t.Fatalf("warnings=%q, want a pairing warning", report.Warnings)
	}
}

func TestOpenCodeOriginalRequestAnchorRendersTheSharedAnchorLine(t *testing.T) {
	request := "- ## Ship the importer\nwith details"
	anchor := openCodeOriginalRequestAnchor(request)
	want := message.CompactionAnchorsOpenTag + message.CompactionAnchorsRequestLabel + "\n- " +
		message.CompactionAnchorLine(request, message.CompactionAnchorRequestChars) + "\n" +
		message.CompactionAnchorsCloseTag
	if anchor != want {
		t.Fatalf("anchor =\n%q\nwant\n%q", anchor, want)
	}
	if got := openCodeOriginalRequestAnchor("   "); got != "" {
		t.Fatalf("anchor(blank) = %q, want empty", got)
	}
	// The bound is the shared one, so a long request keeps both of its ends.
	long := strings.Repeat("head ", 200) + "tail"
	line := message.CompactionAnchorLine(long, message.CompactionAnchorRequestChars)
	if n := len([]rune(line)); n > message.CompactionAnchorRequestChars {
		t.Fatalf("anchor line runes = %d, want at most %d", n, message.CompactionAnchorRequestChars)
	}
	if !strings.HasPrefix(line, "head") || !strings.HasSuffix(line, "tail") {
		t.Fatalf("anchor line dropped an end: %q", line)
	}
}

// TestConvertOpenCodeExport_UnusableMarkerReportsWhatActuallyHappened pins the
// outcome a skipped marker reports: an unusable marker only leaves the full
// history alone when no newer compaction shaped the import, so the warning
// must not claim otherwise just because it is emitted before the choice.
func TestConvertOpenCodeExport_UnusableMarkerReportsWhatActuallyHappened(t *testing.T) {
	data := `{
  "messages": [
    {"info":{"id":"m1","role":"user"},"parts":[{"type":"text","text":"first request"}]},
    {"info":{"id":"m2","role":"user"},"parts":[{"type":"compaction","tail_start_id":"m1"}]},
    {"info":{"id":"m3","role":"user"},"parts":[{"type":"text","text":"recent"}]},
    {"info":{"id":"m4","role":"user"},"parts":[{"type":"compaction","tail_start_id":"m2"}]},
    {"info":{"id":"m5","role":"assistant","parentID":"m4","summary":true,"time":{"created":1,"completed":2}},"parts":[{"type":"text","text":"the summary"}]}
  ]
}`
	var report ImportReport
	msgs, err := convertOpenCodeExport([]byte(data), ReasoningStrict, &report)
	if err != nil {
		t.Fatalf("convertOpenCodeExport: %v", err)
	}
	// The newer usable compaction still replaces the head it speaks for.
	if len(msgs) != 2 || !msgs[0].IsCompactionSummary || msgs[1].Content != "recent" {
		t.Fatalf("msgs = %+v, want the checkpoint followed by the kept tail", msgs)
	}
	if report.OpenCode == nil || report.OpenCode.CompactionHeadReplaced != 1 {
		t.Fatalf("OpenCode report = %+v, want the head replaced", report.OpenCode)
	}
	if hasWarning(report.Warnings, "the full history was kept") {
		t.Fatalf("warnings = %q, must not claim the history was kept", report.Warnings)
	}
	if !hasWarning(report.Warnings, "a newer usable compaction shaped the import") {
		t.Fatalf("warnings = %q, want the newer compaction named as what shaped the import", report.Warnings)
	}
}

// TestConvertOpenCodeExport_UnusableMarkerAfterTheChosenOneSaysEarlier covers
// the other order: a compaction still running when the session was exported is
// newer than the usable one that shaped the import, so naming it "newer" would
// point the reader at the wrong compaction.
func TestConvertOpenCodeExport_UnusableMarkerAfterTheChosenOneSaysEarlier(t *testing.T) {
	data := `{
  "messages": [
    {"info":{"id":"m1","role":"user"},"parts":[{"type":"text","text":"first request"}]},
    {"info":{"id":"m2","role":"user"},"parts":[{"type":"compaction","tail_start_id":"m1"}]},
    {"info":{"id":"m3","role":"assistant","parentID":"m2","summary":true,"time":{"created":1,"completed":2}},"parts":[{"type":"text","text":"the summary"}]},
    {"info":{"id":"m4","role":"user"},"parts":[{"type":"compaction","tail_start_id":"m1"}]}
  ]
}`
	var report ImportReport
	msgs, err := convertOpenCodeExport([]byte(data), ReasoningStrict, &report)
	if err != nil {
		t.Fatalf("convertOpenCodeExport: %v", err)
	}
	if len(msgs) != 2 || !msgs[0].IsCompactionSummary {
		t.Fatalf("msgs = %+v, want the checkpoint followed by the kept tail", msgs)
	}
	if !hasWarning(report.Warnings, "an earlier usable compaction shaped the import") {
		t.Fatalf("warnings = %q, want the earlier compaction named as what shaped the import", report.Warnings)
	}
	if hasWarning(report.Warnings, "a newer usable compaction shaped the import") {
		t.Fatalf("warnings = %q, the newer marker did not shape the import", report.Warnings)
	}
}

// TestConvertOpenCodeExport_EmptySummaryKeepsHistoryInsteadOfABlankCheckpoint
// guards the invariant an empty checkpoint would break: without a summary body
// or an original request, there is nothing to stand in for the folded head, so
// the import keeps that history instead of writing a blank message over it.
func TestConvertOpenCodeExport_EmptySummaryKeepsHistoryInsteadOfABlankCheckpoint(t *testing.T) {
	data := `{
  "messages": [
    {"id":"m1","type":"user","text":"first request"},
    {"id":"m2","type":"compaction","status":"completed","summary":""}
  ]
}`
	var report ImportReport
	msgs, err := convertOpenCodeExport([]byte(data), ReasoningStrict, &report)
	if err != nil {
		t.Fatalf("convertOpenCodeExport: %v", err)
	}
	if len(msgs) != 1 || msgs[0].Content != "first request" {
		t.Fatalf("msgs = %+v, want only the untouched history", msgs)
	}
	for _, msg := range msgs {
		if msg.IsCompactionSummary {
			t.Fatalf("a summary-less compaction must not become a checkpoint: %+v", msg)
		}
		if strings.TrimSpace(msg.Content) == "" {
			t.Fatalf("import emitted a blank message: %+v", msg)
		}
	}
	if !hasWarning(report.Warnings, "carries no summary to import") {
		t.Fatalf("warnings = %q, want the empty compaction reported", report.Warnings)
	}
	if report.OpenCode == nil || report.OpenCode.CompactionCheckpoints != 0 || report.OpenCode.MarkersSkipped != 1 {
		t.Fatalf("OpenCode report = %+v, want no checkpoint and one skipped marker", report.OpenCode)
	}
}

// TestConvertOpenCodeExport_CompactionWithoutSummaryKeepsTheOriginalRequest
// covers the one case that is neither mapped with a summary nor skipped: a
// usable compaction whose summary text is empty still anchors the checkpoint
// with the original request of the head it replaces, so that request is all
// the model gets and the report has to say the summary was missing.
func TestConvertOpenCodeExport_CompactionWithoutSummaryKeepsTheOriginalRequest(t *testing.T) {
	data := `{
  "messages": [
    {"info":{"id":"m1","role":"user"},"parts":[{"type":"text","text":"first request"}]},
    {"info":{"id":"m2","role":"user"},"parts":[{"type":"text","text":"recent request"}]},
    {"info":{"id":"m3","role":"user"},"parts":[{"type":"compaction","tail_start_id":"m2"}]},
    {"info":{"id":"m4","role":"assistant","parentID":"m3","summary":true,"time":{"created":1,"completed":2}},"parts":[{"type":"reasoning","text":"summarize only"}]}
  ]
}`
	var report ImportReport
	msgs, err := convertOpenCodeExport([]byte(data), ReasoningStrict, &report)
	if err != nil {
		t.Fatalf("convertOpenCodeExport: %v", err)
	}
	if len(msgs) != 2 || !msgs[0].IsCompactionSummary || msgs[1].Content != "recent request" {
		t.Fatalf("msgs = %+v, want the checkpoint followed by the kept tail", msgs)
	}
	want := message.CompactionAnchorsOpenTag + message.CompactionAnchorsRequestLabel + "\n- first request\n" + message.CompactionAnchorsCloseTag
	if msgs[0].Content != want {
		t.Fatalf("checkpoint content =\n%q\nwant the bare original-request anchor\n%q", msgs[0].Content, want)
	}
	if report.OpenCode == nil || report.OpenCode.CompactionHeadReplaced != 1 {
		t.Fatalf("OpenCode report = %+v, want the head replaced", report.OpenCode)
	}
	if !hasWarning(report.Warnings, "carries no summary text") {
		t.Fatalf("warnings = %q, want the missing summary reported", report.Warnings)
	}
}

// TestConvertOpenCodeExport_ForwardTailStartIDKeepsHistory pins the other way a
// tail boundary can be unusable: the id exists but names a message after the
// marker, which cannot be a kept tail. It must not be reported as missing.
func TestConvertOpenCodeExport_ForwardTailStartIDKeepsHistory(t *testing.T) {
	data := `{
  "messages": [
    {"id":"m1","type":"user","text":"first request"},
    {"id":"m2","type":"compaction","status":"completed","summary":"flat summary","metadata":{"keepID":"m3"}},
    {"id":"m3","type":"user","text":"recent request"}
  ]
}`
	var report ImportReport
	msgs, err := convertOpenCodeExport([]byte(data), ReasoningStrict, &report)
	if err != nil {
		t.Fatalf("convertOpenCodeExport: %v", err)
	}
	if len(msgs) != 3 || !msgs[1].IsCompactionSummary || msgs[0].Content != "first request" || msgs[2].Content != "recent request" {
		t.Fatalf("msgs = %+v, want the full history with the checkpoint at the marker", msgs)
	}
	if !hasWarning(report.Warnings, "names a message after the compaction marker") {
		t.Fatalf("warnings = %q, want the forward boundary reported", report.Warnings)
	}
	if report.OpenCode == nil || report.OpenCode.CompactionHeadReplaced != 0 {
		t.Fatalf("OpenCode report = %+v, want no head replacement", report.OpenCode)
	}
}

// TestConvertOpenCodeExport_FlatCompactionAcceptsEveryKeepIDSpelling verifies
// the flat export's kept-tail boundary is read under the same spellings the v1
// part carries: an export that writes another of those names still has its
// compaction mapped instead of falling back to the full history.
func TestConvertOpenCodeExport_FlatCompactionAcceptsEveryKeepIDSpelling(t *testing.T) {
	for _, metadata := range []string{
		`"metadata":{"keepID":"m2"}`,
		`"metadata":{"keep_id":"m2"}`,
		`"metadata":{"tail_start_id":"m2"}`,
		`"metadata":{"tailStartID":"m2"}`,
		`"metadata":{"tailStartId":"m2"}`,
		`"keepID":"m2"`,
		`"keep_id":"m2"`,
		`"tail_start_id":"m2"`,
		`"tailStartID":"m2"`,
		`"tailStartId":"m2"`,
	} {
		data := `{
  "messages": [
    {"id":"m1","type":"user","text":"first request"},
    {"id":"m2","type":"user","text":"recent request"},
    {"id":"m3","type":"compaction","status":"completed","summary":"flat summary",` + metadata + `}
  ]
}`
		var report ImportReport
		msgs, err := convertOpenCodeExport([]byte(data), ReasoningStrict, &report)
		if err != nil {
			t.Fatalf("convertOpenCodeExport: %v", err)
		}
		if len(msgs) != 2 || !msgs[0].IsCompactionSummary || !strings.Contains(msgs[0].Content, "flat summary") {
			t.Fatalf("metadata=%s: msgs = %+v, want the checkpoint then the kept tail", metadata, msgs)
		}
		if msgs[1].Content != "recent request" {
			t.Fatalf("metadata=%s: tail = %+v, want the recent request", metadata, msgs[1])
		}
		if report.OpenCode == nil || report.OpenCode.CompactionHeadReplaced != 1 {
			t.Fatalf("metadata=%s: report = %+v, want the head replaced", metadata, report.OpenCode)
		}
		if hasWarning(report.Warnings, "tail_start_id") {
			t.Fatalf("metadata=%s: warnings = %q, want no tail warning", metadata, report.Warnings)
		}
	}
}
