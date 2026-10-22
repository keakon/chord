package sessionimport

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/keakon/chord/internal/message"
)

// openCodeCompactionPlan describes how one compaction in an OpenCode export
// maps onto Chord's durable checkpoint.
//
// OpenCode renders a completed compaction as a single checkpoint message — the
// summary plus the recent messages the compaction kept — and a request only
// ever reads history from that point on. The export, by contrast, still carries
// every message the session ever wrote, the folded-away head included, so
// importing it verbatim would show the model the very history a summary calls
// replaced, followed by that summary.
//
// The plan mirrors what the source model saw: the paired summary becomes a
// Chord checkpoint (user + IsCompactionSummary + model_summary mode) standing
// immediately before the oldest message the compaction kept, the messages
// between tail_start_id and the marker stay real messages after it, and the
// head before the tail is replaced by that checkpoint. Only the newest usable
// compaction shapes the import — earlier ones were already invisible to the
// source model by the time the last one ran, because each compaction
// consolidates the ones before it. The marker's own position is where the
// checkpoint stands only when the tail boundary is unknown.
type openCodeCompactionPlan struct {
	// active reports whether a checkpoint stands in the imported history.
	active bool
	// checkpoint is the Chord checkpoint message.
	checkpoint message.Message
	// insertBefore is the message index the checkpoint precedes: the tail
	// start when the head was replaced, the marker itself otherwise.
	insertBefore int
	// replaceHead is the number of leading messages the checkpoint speaks
	// for. Zero keeps the full history, which is the fail-open shape used
	// when the tail boundary cannot be located.
	replaceHead int
	// absorbed holds message indexes the checkpoint speaks for: the paired
	// summary message, and the compaction message itself in a flat export.
	absorbed map[int]bool
	// headReplaced counts the messages the checkpoint replaced.
	headReplaced int
	// tailKept counts the messages kept between the tail start and the
	// marker.
	tailKept int
	// markersSkipped counts compaction markers that did not become the
	// checkpoint, whether superseded by a newer one or unusable.
	markersSkipped int
}

// absorbs reports whether the message at idx is spoken for by the checkpoint
// (or by the replaced head) and must not be converted on its own.
func (p *openCodeCompactionPlan) absorbs(idx int) bool {
	if p == nil {
		return false
	}
	if p.absorbed[idx] {
		return true
	}
	return p.replaceHead > 0 && idx < p.replaceHead
}

// insertsCheckpointBefore reports whether the checkpoint stands immediately
// before the message at idx.
func (p *openCodeCompactionPlan) insertsCheckpointBefore(idx int) bool {
	return p != nil && p.active && idx == p.insertBefore
}

// openCodeCompactionMarker is one compaction found in an export. A v1 export
// carries it as a part inside a user message with the summary in a paired
// assistant message; a flat export carries it as the compaction message
// itself, summary inline.
type openCodeCompactionMarker struct {
	index int
	// tailStartID names the first message the compaction kept. Empty when the
	// export records none.
	tailStartID string
	// summaryIndex is the paired assistant summary message, or -1 when the
	// summary is inline (flat export) or unpaired.
	summaryIndex int
	// summaryText is the body the checkpoint carries.
	summaryText string
	// usable reports whether the compaction completed and can shape the
	// import: a paired, error-free, completed summary message in a v1 export;
	// a completed status in a flat one.
	usable bool
	// native reports a provider-native window, which has no Chord
	// equivalent.
	native bool
	// flat reports the flat-export shape, where the marker message itself is
	// absorbed by the checkpoint.
	flat bool
}

// planOpenCodeCompaction scans an export's messages for compactions and builds
// the import plan for the newest usable one. Markers without a usable pairing
// are counted and warned about; their part is skipped by the converter, so the
// rest of the message still imports as ordinary history.
func planOpenCodeCompaction(messages []json.RawMessage, report *ImportReport) *openCodeCompactionPlan {
	markers, ids, raws := scanOpenCodeCompactionMarkers(messages)
	var chosen *openCodeCompactionMarker
	var unusable []openCodeCompactionMarker
	for i := range markers {
		if markers[i].usable {
			chosen = &markers[i]
			continue
		}
		unusable = append(unusable, markers[i])
	}

	skip := chosen == nil
	var warnings []string
	tailIndex := -1
	original := ""
	var checkpoint message.Message
	if !skip {
		// The tail boundary decides how much history the checkpoint replaces.
		// Locating it is the only place a cut is made; when it cannot be
		// located the import keeps the full history rather than guessing
		// where the source session's recent context began.
		switch {
		case chosen.tailStartID == "":
			warnings = append(warnings, "compaction records no tail_start_id; the full history before the checkpoint was kept")
		default:
			idx, ok := ids[chosen.tailStartID]
			switch {
			case !ok || idx < 0:
				warnings = append(warnings, fmt.Sprintf("compaction tail_start_id %q could not be located; the full history before the checkpoint was kept", chosen.tailStartID))
			case idx > chosen.index:
				warnings = append(warnings, fmt.Sprintf("compaction tail_start_id %q names a message after the compaction marker; the full history before the checkpoint was kept", chosen.tailStartID))
			default:
				tailIndex = idx
			}
		}
		if tailIndex >= 0 {
			// The head before the tail is the only witness of the original
			// first request, so it is captured into the checkpoint's anchors
			// before the replacement drops it.
			original = openCodeFirstUserRequestText(raws, tailIndex)
		}
		checkpoint = openCodeCompactionCheckpoint(chosen.summaryText, original)
		// Nothing stands in for the history a compaction folds away when it
		// carries no summary text and no original request to anchor it: the
		// checkpoint would be a blank message sitting where that history was.
		if strings.TrimSpace(checkpoint.Content) == "" {
			skip = true
			warnings = nil
			report.warnf("opencode message[%d]: compaction carries no summary to import; the marker was skipped and the full history was kept", chosen.index)
		} else if strings.TrimSpace(chosen.summaryText) == "" {
			// An anchored checkpoint that lost its summary still speaks for
			// the replaced head, but only through that request: say so, or the
			// loss stays invisible in the import and its report.
			warnings = append(warnings, "compaction carries no summary text; the checkpoint carries only the original request of the history it replaced")
		}
		if !skip && chosen.native {
			warnings = append(warnings, "compaction carries a provider-native window with no Chord equivalent; the summary text was mapped instead")
		}
	}

	// A skipped marker is reported with the outcome that actually applies: it
	// only leaves the full history alone when nothing shaped the import, and
	// whether the compaction that did is newer than this marker depends on
	// where the two stand.
	for _, marker := range unusable {
		outcome := "the full history was kept"
		if !skip {
			if marker.index < chosen.index {
				outcome = "a newer usable compaction shaped the import"
			} else {
				outcome = "an earlier usable compaction shaped the import"
			}
		}
		report.warnf("opencode message[%d]: compaction marker has no completed summary; the marker was skipped and %s", marker.index, outcome)
	}
	if skip {
		if len(markers) > 0 {
			report.ensureOpenCodeReport().MarkersSkipped = len(markers)
		}
		return nil
	}
	for _, w := range warnings {
		report.warnf("opencode message[%d]: %s", chosen.index, w)
	}

	plan := &openCodeCompactionPlan{
		active:         true,
		insertBefore:   chosen.index,
		checkpoint:     checkpoint,
		absorbed:       map[int]bool{},
		markersSkipped: len(markers) - 1,
	}
	if chosen.flat {
		plan.absorbed[chosen.index] = true
	} else if chosen.summaryIndex >= 0 {
		plan.absorbed[chosen.summaryIndex] = true
	}
	if tailIndex >= 0 {
		plan.replaceHead = tailIndex
		plan.insertBefore = tailIndex
		plan.headReplaced = tailIndex
		plan.tailKept = chosen.index - tailIndex
	}

	section := report.ensureOpenCodeReport()
	section.CompactionCheckpoints = 1
	section.CompactionHeadReplaced = plan.headReplaced
	section.CompactionTailKept = plan.tailKept
	section.MarkersSkipped = plan.markersSkipped
	return plan
}

// scanOpenCodeCompactionMarkers finds every compaction marker in the export
// and pairs each v1 marker with its summary message. It also returns the
// decoded messages, and the first message index for every message id, which is
// how tail_start_id resolves.
func scanOpenCodeCompactionMarkers(messages []json.RawMessage) ([]openCodeCompactionMarker, map[string]int, []openCodeRawMessage) {
	ids := make(map[string]int, len(messages))
	raws := make([]openCodeRawMessage, len(messages))
	for i, raw := range messages {
		var obj map[string]json.RawMessage
		if json.Unmarshal(raw, &obj) != nil {
			continue
		}
		info := openCodeMessageInfo(obj)
		entry := openCodeRawMessage{obj: obj, info: info, id: pickStringField(info, "id"), role: openCodeRawMessageRole(obj)}
		if entry.id != "" {
			if _, seen := ids[entry.id]; !seen {
				ids[entry.id] = i
			}
		}
		raws[i] = entry
	}

	var markers []openCodeCompactionMarker
	for i := range raws {
		entry := raws[i]
		if entry.obj == nil {
			continue
		}
		// A flat export carries the compaction as the message itself.
		if strings.EqualFold(pickStringField(entry.obj, "type", "kind"), openCodeEventCompaction) {
			marker := openCodeCompactionMarker{
				index:       i,
				summaryText: pickStringField(entry.obj, "summary"),
				tailStartID: openCodeFlatTailStartID(entry.obj),
				usable:      strings.EqualFold(strings.TrimSpace(pickStringField(entry.obj, "status")), openCodeStatusCompleted),
				flat:        true,
			}
			if raw, ok := entry.obj["native"]; ok && openCodeRawIsPresent(raw) {
				marker.native = true
			}
			markers = append(markers, marker)
			continue
		}
		// A v1 export carries it as a part of a user message.
		if entry.role != message.RoleUser {
			continue
		}
		compactionPart := openCodeCompactionPart(entry.obj)
		if compactionPart == nil {
			continue
		}
		marker := openCodeCompactionMarker{
			index:       i,
			tailStartID: openCodePartString(compactionPart, "tail_start_id", "tailStartID", "tailStartId"),
		}
		if summaryIndex, summaryText, ok := openCodePairedSummary(raws, entry.id); ok {
			marker.summaryIndex = summaryIndex
			marker.summaryText = summaryText
			marker.usable = true
		}
		markers = append(markers, marker)
	}
	return markers, ids, raws
}

// openCodeFlatTailStartID reads the kept-tail boundary a flat export records.
// It accepts the v1 part's spellings as well, since an export that records the
// same boundary under another of those names must still be readable.
func openCodeFlatTailStartID(obj map[string]json.RawMessage) string {
	if id := pickNestedStringField(obj, "metadata", "keepID", "keep_id", "tail_start_id", "tailStartID", "tailStartId"); id != "" {
		return id
	}
	return pickStringField(obj, "keepID", "keep_id", "tail_start_id", "tailStartID", "tailStartId")
}

// openCodePairedSummary finds the assistant message that carries a v1
// compaction marker's summary: the one flagged summary, parented by the marker
// message, and completed without error. The first match wins, matching the
// pairing rule the source applies when it rebuilds the compaction; a summary
// the source itself recorded as failed or unfinished leaves the marker
// unusable rather than pairing it with a later candidate.
func openCodePairedSummary(raws []openCodeRawMessage, markerID string) (int, string, bool) {
	if markerID == "" {
		return -1, "", false
	}
	for i := range raws {
		entry := raws[i]
		if entry.obj == nil || entry.role != message.RoleAssistant {
			continue
		}
		if pickStringField(entry.info, "parentID", "parentId", "parent_id") != markerID {
			continue
		}
		if !openCodeRawBoolField(entry.info, "summary") {
			continue
		}
		if openCodeHasError(entry.info) || !openCodeTimeCompleted(entry.info) {
			return -1, "", false
		}
		return i, openCodeSummaryText(entry.obj), true
	}
	return -1, "", false
}

// openCodeCompactionPart returns the compaction part of a v1 message, or nil.
func openCodeCompactionPart(obj map[string]json.RawMessage) map[string]any {
	rawParts, ok := obj["parts"]
	if !ok {
		return nil
	}
	var parts []map[string]any
	if json.Unmarshal(rawParts, &parts) != nil {
		return nil
	}
	for _, part := range parts {
		if typ, _ := part["type"].(string); strings.EqualFold(strings.TrimSpace(typ), openCodePartTypeCompaction) {
			return part
		}
	}
	return nil
}

// openCodeFirstUserRequestText returns the plain text of the first
// user-authored message among the leading end decoded messages, which is the
// original request the replaced head was the only witness of. It returns ""
// when that range holds no user text, in which case the checkpoint carries no
// anchor.
func openCodeFirstUserRequestText(raws []openCodeRawMessage, end int) string {
	for idx := 0; idx < end && idx < len(raws); idx++ {
		entry := raws[idx]
		if entry.obj == nil || entry.role != message.RoleUser {
			continue
		}
		if text := openCodeRawUserText(entry.obj); text != "" {
			return text
		}
	}
	return ""
}

// openCodeCompactionCheckpoint builds the Chord checkpoint an OpenCode
// compaction maps onto: a user-role summary of the archived head, recorded as
// model-produced. The content is the source summary verbatim, prefixed by the
// original-request anchor when the replaced head held one.
func openCodeCompactionCheckpoint(summaryText, originalFirstUser string) message.Message {
	return message.Message{
		Role:                  message.RoleUser,
		Content:               openCodeCheckpointContent(summaryText, originalFirstUser),
		IsCompactionSummary:   true,
		CompactionSummaryMode: message.CompactionSummaryModeModelSummary,
		Provenance:            importedOpenCodeProvenance(),
	}
}

func openCodeCheckpointContent(summaryText, originalFirstUser string) string {
	body := strings.TrimSpace(summaryText)
	anchor := openCodeOriginalRequestAnchor(originalFirstUser)
	switch {
	case anchor == "":
		return body
	case body == "":
		return anchor
	default:
		return anchor + "\n\n" + body
	}
}

// openCodeOriginalRequestAnchor renders the session-anchors block that carries
// the original first request, in the exact shape the agent-side anchor parser
// reads back. It returns "" when there is no request to carry.
func openCodeOriginalRequestAnchor(text string) string {
	line := message.CompactionAnchorLine(text, message.CompactionAnchorRequestChars)
	if line == "" {
		return ""
	}
	return message.CompactionAnchorsOpenTag + message.CompactionAnchorsRequestLabel + "\n- " + line + "\n" + message.CompactionAnchorsCloseTag
}

// ensureOpenCodeReport returns the report's OpenCode section, creating it on
// first use.
func (r *ImportReport) ensureOpenCodeReport() *OpenCodeImportReport {
	if r.OpenCode == nil {
		r.OpenCode = &OpenCodeImportReport{}
	}
	return r.OpenCode
}
