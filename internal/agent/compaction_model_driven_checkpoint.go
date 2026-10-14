package agent

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/ctxmgr"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

// --------------------------------------------------------------- checkpoint ---

// modelDrivenCheckpointBuilder holds the parts of a model-driven checkpoint
// that are identical on both renders of the same draft: the deterministic
// summary body, the archival evidence selection, the retained recent messages
// and the archived-history map of the already-committed chain. The draft
// renders the checkpoint twice — once for the pre-export low-gain preflight
// and once after the archive was written — and only the history map differs
// between them (by exactly the one line of the just-exported archive). Sharing
// the builder keeps the expensive parts (anchor resolution over the whole
// snapshot, one directory listing plus one meta read per archive) to a single
// pass instead of paying them twice per checkpoint.
type modelDrivenCheckpointBuilder struct {
	bundle        modelDrivenBarrierSnapshot
	req           *modelDrivenCheckpointRequest
	summaryText   string
	evidenceItems []evidenceItem
	// requestSources identifies full instruction sources across renderings.
	requestSources map[string]string
	// refMetadata indexes the classification and validity the runtime resolved
	// for every evidence ID this checkpoint's submission may declare, so a
	// declared ref the pack itself does not carry can be re-rendered as a
	// machine row (see carriedEvidencePackRows).
	refMetadata map[string]evidencePackRefMeta
	// retainedRecent is the rendered `## Retained Recent Messages` section.
	retainedRecent string
	// historyLines are the rendered map lines of the committed archive chain
	// (this draft's own archive is added at render time by the post-export
	// call, which is the only caller that knows its path).
	historyLines []string
}

// newModelDrivenCheckpointBuilder performs the one-time work for a draft's
// checkpoint renders. All inputs come from the immutable barrier snapshot;
// ctx bounds the build-time repository probe. The runtime-owned machine
// sections (worklog and repository state) are applied here so both renders of
// one draft share them.
func (a *MainAgent) newModelDrivenCheckpointBuilder(ctx context.Context, bundle modelDrivenBarrierSnapshot, snapshot []message.Message, headSplit int, req *modelDrivenCheckpointRequest) *modelDrivenCheckpointBuilder {
	historyChain, historyMetas, err := listCheckpointHistoryReferences(bundle.sessionDir, "")
	if err != nil {
		log.Warnf("model-driven checkpoint: list history references error=%v", err)
	}
	headSnapshot := snapshot[:headSplit]
	summaryText := applyCheckpointMachineState(a.buildModelDrivenCheckpointSummary(bundle, snapshot, headSplit, req), headSnapshot, buildCheckpointRepositoryState(ctx, bundle.workDir), func(text string) int {
		return bundle.estimateTokens([]message.Message{{Role: message.RoleUser, Content: text}})
	})
	return &modelDrivenCheckpointBuilder{
		bundle:         bundle,
		req:            req,
		summaryText:    summaryText,
		requestSources: buildCheckpointRequestSources(summaryText, headSnapshot, bundle.originalRequest, bundle.evidenceItems),
		evidenceItems:  filterCompactionEvidenceForArchival(bundle.evidenceItems),
		refMetadata:    checkpointEvidenceRefMetadata(bundle, headSnapshot),
		// The newest real user messages of the archived head (and any dangling
		// interrupted reply) stay verbatim inside the checkpoint within the
		// retention budget, deterministic like the rest of this path — no model
		// call is involved, and the preflight counts the section because it is
		// part of checkpointContent.
		retainedRecent: renderCheckpointRetainedRecentMessages(headSnapshot, compactRetainRecentUserMessages, bundle.retainRecentTokens, func(text string) int {
			return ctxmgr.EstimateMessagesTokensWithRatio([]message.Message{{Role: message.RoleUser, Content: text}}, bundle.calibratedRatio)
		}),
		historyLines: formatHistoryMapLines(historyChain, historyMetas),
	}
}

// render renders the deterministic checkpoint the way it will appear in the
// transcript: the summary sections wrapped in the canonical checkpoint
// envelope (header, archived history map, evidence artifact) plus the
// model-driven wrapper copy. exportedArchive, when non-empty, is this
// draft's freshly written archive: it is appended to the map with its own
// topics (it is the newest index, so it sorts last). The returned stats
// describe the checkpoint for the low-gain preflight and lifecycle telemetry.
func (b *modelDrivenCheckpointBuilder) render(exportedArchive string) (string, modelDrivenPreflightStats) {
	historyRefs := b.historyLines
	if exportedArchive != "" {
		base := filepath.Base(exportedArchive)
		exportedLine := formatHistoryMapLines([]string{exportedArchive}, map[string]*compactionHistoryMeta{
			base: {Topics: evidenceItemTopics(b.evidenceItems)},
		})
		historyRefs = append(append(make([]string, 0, len(b.historyLines)+1), b.historyLines...), exportedLine...)
	}
	checkpointContent := buildCompactionCheckpointMessage(b.summaryText, historyRefs, compactionSummaryModeModelDriven, b.evidenceItems, b.retainedRecent)
	if rows := b.carriedEvidencePackRows(); rows != "" {
		checkpointContent = appendCarriedEvidencePackRows(checkpointContent, rows)
	}

	var historyMapBytes int
	for _, ref := range historyRefs {
		historyMapBytes += len(ref)
	}
	// AnchorBytes measures the latest-request anchor section (the checkpoint's
	// continuation core), not the raw summary head/tail whitespace.
	anchorBytes := 0
	if _, section, ok := strings.Cut(b.summaryText, "## Current User Request"); ok {
		if before, _, found := strings.Cut(section, "\n## "); found {
			section = before
		}
		anchorBytes = len("## Current User Request") + len(section)
	}
	req := b.req
	return checkpointContent, modelDrivenPreflightStats{
		CheckpointBytes:    len(checkpointContent),
		AnchorBytes:        anchorBytes,
		HistoryMapBytes:    historyMapBytes,
		ContinuationTokens: b.bundle.estimateTokens([]message.Message{{Role: message.RoleUser, Content: req.Args.ActiveObjective + "\n" + req.Args.NextStep + "\n" + strings.Join(req.Args.Completed, "\n") + "\n" + strings.Join(req.Args.Decisions, "\n") + "\n" + strings.Join(req.Args.OpenIssues, "\n") + "\n" + strings.Join(req.Args.StateFiles, "\n")}}),
	}
}

// carriedEvidencePackRows renders the machine rows for the evidence references
// the checkpoint declares but its evidence pack does not carry. The declared
// refs come from the summary's `## Evidence References` section — the only
// place the merged set (the fresh submission plus the carried typed state)
// exists after the carry merge — and each row carries the runtime's own
// classification and validity for that ID, so the next generation, which
// resolves refs against checkpoint packs alone, sees the same facts the live
// tracker gave this one. A ref the runtime cannot classify renders as a bare
// Evidence ID row: it resolves by existence, and an observed claim citing it is
// rejected with the existing "without classification" error, which is what
// tells the model to reclassify the claim instead of resubmitting the ref.
func (b *modelDrivenCheckpointBuilder) carriedEvidencePackRows() string {
	refs := checkpointDeclaredEvidenceRefs(b.summaryText)
	if len(refs) == 0 {
		return ""
	}
	inPack := evidenceItemsByID(b.evidenceItems)
	var rows []string
	for _, ref := range refs {
		if _, ok := inPack[ref]; ok {
			continue
		}
		rows = append(rows, "Evidence ID: "+ref)
		meta, ok := b.refMetadata[ref]
		if !ok {
			continue
		}
		if meta.kind != "" {
			rows = append(rows, "Evidence Kind: "+string(meta.kind))
		}
		if meta.invalidated {
			rows = append(rows, "Validity: "+string(evidenceValidityInvalidated))
		}
	}
	if len(rows) == 0 {
		return ""
	}
	return carriedEvidenceRefsHeading + "\n" + strings.Join(rows, "\n")
}

// carriedEvidenceRefsHeading labels the machine rows that re-render declared
// evidence references the pack above does not carry. The line is prose, so the
// pack parser ignores it and reads only the Evidence ID/Kind/Validity rows it
// introduces.
const carriedEvidenceRefsHeading = "Carried evidence references (declared by this checkpoint; their excerpts are in the archived history):"

// checkpointDeclaredEvidenceRefs returns the evidence references the
// `## Evidence References` section of a model-driven checkpoint summary
// declares. The section is the runtime's own rendering of the merged,
// already-validated ref set, and the extraction still matches the evidence-ID
// spelling; a string that merely echoes the shape (a user message quoted into
// the anchors block, say) can at most contribute an existence-only row, and an
// unclassified ID can never support an observed claim.
func checkpointDeclaredEvidenceRefs(summary string) []string {
	_, section, ok := strings.Cut(summary, "\n## Evidence References\n")
	if !ok {
		return nil
	}
	if end := strings.Index(section, "\n## "); end >= 0 {
		section = section[:end]
	}
	var refs []string
	for line := range strings.SplitSeq(section, "\n") {
		ref, isBullet := strings.CutPrefix(strings.TrimSpace(line), "- ")
		ref = strings.TrimSpace(ref)
		if !isBullet || !tools.EvidenceIDShape.MatchString(ref) {
			continue
		}
		refs = append(refs, ref)
	}
	return refs
}

// appendCarriedEvidencePackRows appends the rows to the checkpoint's evidence
// region, opening the region when the pack itself rendered no items: the pack
// parser reads that region and nothing else, so the rows must land after the
// [Context Evidence] tag.
func appendCarriedEvidencePackRows(checkpointContent, rows string) string {
	checkpointContent = strings.TrimRight(checkpointContent, "\n")
	if !strings.Contains(checkpointContent, message.CompactionEvidenceTag) {
		checkpointContent += "\n" + message.CompactionEvidenceTag
	}
	return checkpointContent + "\n" + rows
}
