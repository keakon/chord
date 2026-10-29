package agent

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tools"
)

// evidenceHintMaxIDs bounds how many IDs the rejection hint names: it rides on
// the tool result of a rejected request, so it stays short — the model needs
// one ID that supports its claim, not the whole menu.
const evidenceHintMaxIDs = 8

// resolvableEvidenceHint reports the evidence IDs an observed claim can cite
// in this context right now, or that none are resolvable. Only IDs the
// observed rules accept (a positive kind, not invalidated) are listed, so
// acting on the hint cannot trade one rejection for another.
func (a *MainAgent) resolvableEvidenceHint() string {
	clause, anyResolvable := a.resolvableEvidenceClause()
	if !anyResolvable {
		return clause + ", so no claim can be observed here; classify it as derived/assumed/proposed or drop claim_kinds for it"
	}
	return clause + "; cite the supporting ID in claim_evidence (automatically included in top-level evidence_refs), or classify the claim as derived/assumed/proposed"
}

// resolvableEvidenceClause renders the resolvable-ID part both arm-time
// rejection hints share (observed claim without evidence, unknown evidence
// reference). Both hints offer the stricter observed-compatible subset, not
// every provenance reference. Live records take precedence over archived ones.
func (a *MainAgent) resolvableEvidenceClause() (string, bool) {
	ids, truncated := a.resolvableClaimEvidenceIDs(evidenceHintMaxIDs)
	if len(ids) == 0 {
		return "no evidence ID is resolvable in this context for an observed claim; provenance-only references may still exist", false
	}
	list := strings.Join(ids, ", ")
	if truncated {
		list += fmt.Sprintf(" (first %d; more are resolvable)", len(ids))
	}
	return "evidence IDs resolvable in this context for observed claims: " + list, true
}

// unknownEvidenceRefHint turns the unknown-ID rejection self-correcting the
// way the observed path already is: the reported session's one-hop recoveries
// worked because the rejection named the resolvable IDs, while this exit left
// the model to regenerate an ev-<12hex> shape from memory. An empty menu
// leaves only the non-evidence routes actionable (drop the reference, drop or
// reclassify an observed claim), never citing another rejection.
func (a *MainAgent) unknownEvidenceRefHint() string {
	clause, anyResolvable := a.resolvableEvidenceClause()
	if !anyResolvable {
		return clause + "; drop the unknown reference, and drop or reclassify affected observed claims as derived/assumed/proposed"
	}
	return clause + "; cite a listed ID where the unknown one was, or drop the reference"
}

// resolvableClaimEvidenceIDs lists the evidence IDs currently resolvable that
// could anchor an observed claim: live runtime evidence with a positive kind
// first, then IDs a checkpoint evidence pack still in the transcript renders.
// Negative kinds (tool_error, done_rejected, escalate) and invalidated records
// are omitted because validateObservedClaimEvidence rejects them. Archived
// records additionally require classification; live records retain the
// runtime validator's category rules.
//
// A nil receiver yields no IDs: the live loop and the carried checkpoint packs
// both need agent state, so there is no meaningful non-empty subset to return
// without an agent.
func (a *MainAgent) resolvableClaimEvidenceIDs(limit int) ([]string, bool) {
	if a == nil {
		return nil, false
	}
	seen := make(map[string]bool)
	var out []string
	add := func(id string) {
		if id == "" || seen[id] {
			return
		}
		seen[id] = true
		out = append(out, id)
	}
	items := a.evidence.snapshot()
	live := evidenceItemsByID(items)
	for _, item := range items {
		if !evidenceKindSupportsCompletion(item.Kind) || item.Validity == evidenceValidityInvalidated {
			continue
		}
		add(evidenceItemID(item))
	}
	var carried []string
	for id, meta := range a.contextEvidencePackMetadata() {
		if _, exists := live[id]; exists {
			continue
		}
		if seen[id] || meta.kind == "" || meta.invalidated || !evidenceKindSupportsCompletion(meta.kind) {
			continue
		}
		carried = append(carried, id)
	}
	slices.Sort(carried)
	for _, id := range carried {
		add(id)
	}
	if limit > 0 && len(out) > limit {
		return out[:limit], true
	}
	return out, false
}

// evidenceKindSupportsCompletion reports whether an evidence record of the
// given kind can support a completed/committed stage. Committed and observed
// acceptance semantics anchor to positive outcome records — user corrections,
// stated constraints, tool diffs and similar. tool_error, done_rejected and
// escalate are negative outcome records (a failed action, a rejected Done, an
// open intervention request) and cannot support a claim of completion.
func evidenceKindSupportsCompletion(kind evidenceKind) bool {
	switch kind {
	case evidenceToolError, evidenceDoneRejected, evidenceEscalate:
		return false
	default:
		return true
	}
}

// validateModelDrivenEvidenceRefs rejects evidence references the runtime
// cannot resolve. Resolution covers the runtime evidence tracker (fresh items
// derived from live messages) plus the evidence IDs rendered by checkpoint
// evidence packs that are still in the context: after an apply, a previous
// checkpoint's pack is the only place its Evidence IDs remain visible, and the
// tool surface documents those checkpoint-pack IDs as the referenceable form.
// Invented IDs stay rejected. field names the argument the references came
// from — "evidence_refs" or "claim_evidence[<claim>]" — because the tool folds
// claim_evidence values into the top-level list, so a rejection must say which
// field the model has to edit rather than reporting the merged union.
func (a *MainAgent) validateModelDrivenEvidenceRefs(field string, refs []string) error {
	if len(refs) == 0 {
		return nil
	}
	known := evidenceItemsByID(a.evidence.snapshot())
	var carried map[string]evidencePackRefMeta
	for _, ref := range refs {
		item, ok := known[ref]
		if ok {
			if item.Validity == evidenceValidityInvalidated {
				return fmt.Errorf("compact_context %s contains %s evidence %q", field, item.Validity, ref)
			}
			continue
		}
		if carried == nil {
			carried = a.contextEvidencePackMetadata()
		}
		meta, ok := carried[ref]
		if !ok {
			return fmt.Errorf("compact_context %s contains unknown evidence ID %q; %s", field, ref, a.unknownEvidenceRefHint())
		}
		if meta.invalidated {
			return fmt.Errorf("compact_context %s contains %s evidence %q", field, evidenceValidityInvalidated, ref)
		}
	}
	return nil
}

// evidencePackRefMeta is the machine-readable metadata a checkpoint evidence
// pack renders for one archived evidence ID. kind is empty when the pack
// record carries no classification (an older pack format); invalidated records
// a rendered Validity: invalidated line.
type evidencePackRefMeta struct {
	kind        evidenceKind
	invalidated bool
}

// contextEvidencePackMetadata returns the machine metadata of every stable
// Evidence ID rendered by the evidence packs of checkpoint messages still in
// the live context. A pack item whose source messages were archived stays
// citable — with its classification and validity — while its checkpoint
// remains in the transcript; once that checkpoint is archived by a later
// apply, its IDs stop resolving here and are rejected as unknown again.
func (a *MainAgent) contextEvidencePackMetadata() map[string]evidencePackRefMeta {
	if a == nil || a.ctxMgr == nil {
		return make(map[string]evidencePackRefMeta)
	}
	return checkpointPackMetadataFromMessages(a.ctxMgr.Snapshot())
}

// checkpointPackMetadataFromMessages indexes the machine metadata of every
// Evidence ID rendered by the evidence packs of the checkpoint messages in
// messages. The first occurrence of an ID wins: the scan follows transcript
// order, and the live context holds one pack per checkpoint with the nearest
// generation first.
func checkpointPackMetadataFromMessages(messages []message.Message) map[string]evidencePackRefMeta {
	out := make(map[string]evidencePackRefMeta)
	for _, msg := range messages {
		if msg.Role != message.RoleUser {
			continue
		}
		if !msg.IsCompactionSummary && !message.IsCompactionEvidenceArtifactText(msg.Content) {
			continue
		}
		for id, meta := range parseCheckpointEvidencePackMetadata(msg.Content) {
			if _, exists := out[id]; !exists {
				out[id] = meta
			}
		}
	}
	return out
}

// parseCheckpointEvidencePackMetadata scans a checkpoint message's rendered
// evidence pack region for its Evidence IDs and the machine metadata each row
// renders (the Evidence Kind and Validity lines).
//
// Only column-0 lines are pack rows: renderEvidenceArtifactContent indents
// every excerpt line by two spaces, and an excerpt quotes raw evidence text
// that can contain a line shaped exactly like a row. A quoted line can
// therefore never be read as metadata (including a well-formed Evidence ID
// that would otherwise clear the excerpt). Lines after an Excerpt: line are
// ignored until the next column-0 row.
func parseCheckpointEvidencePackMetadata(content string) map[string]evidencePackRefMeta {
	region := evidencePackRegion(content)
	if region == "" {
		return nil
	}
	out := make(map[string]evidencePackRefMeta)
	current := ""
	inExcerpt := false
	for line := range strings.SplitSeq(region, "\n") {
		if rest, ok := strings.CutPrefix(line, "Evidence ID: "); ok {
			// Only the minted ID spelling enters the index; a quoted line can
			// look like a row, so an unshaped ID must not clear the excerpt or
			// make an unrelated reference resolvable.
			id := strings.TrimSpace(rest)
			if !tools.EvidenceIDShape.MatchString(id) {
				continue
			}
			current = id
			inExcerpt = false
			if _, exists := out[current]; !exists {
				out[current] = evidencePackRefMeta{}
			}
			continue
		}
		if current == "" || inExcerpt {
			continue
		}
		if rest, ok := strings.CutPrefix(line, "Evidence Kind: "); ok {
			meta := out[current]
			meta.kind = evidenceKind(strings.TrimSpace(rest))
			out[current] = meta
			continue
		}
		if rest, ok := strings.CutPrefix(line, "Validity: "); ok {
			if strings.TrimSpace(rest) == string(evidenceValidityInvalidated) {
				meta := out[current]
				meta.invalidated = true
				out[current] = meta
			}
			continue
		}
		if strings.HasPrefix(line, "Excerpt:") {
			inExcerpt = true
		}
	}
	return out
}

// evidencePackRegion returns the machine-rendered evidence pack region of a
// checkpoint message (between the [Context Evidence] tag and the end of the
// message; a legacy display-hint marker, when still present in an older
// checkpoint, ends the region early), or "" when the message carries no pack.
func evidencePackRegion(content string) string {
	start := strings.Index(content, message.CompactionEvidenceTag)
	if start < 0 {
		return ""
	}
	region := content[start:]
	if idx := strings.Index(region, message.CompactionDisplayHint); idx >= 0 {
		region = region[:idx]
	}
	return region
}

// checkpointEvidenceRefMetadata indexes the classification and validity the
// runtime resolved for the evidence IDs a checkpoint submission may declare:
// every live tracker item the barrier captured, plus the pack rows of the
// prior checkpoints inside the archived head. A live item wins over a pack row
// for the same ID, matching resolvableClaimEvidenceIDs.
//
// The index exists because the rendered pack is not a complete record of what
// the checkpoint declares: the archival filter drops the kinds the pack never
// carries (user_request, subagent_done, done_rejected) and every invalidated
// item, and the evidence caps can leave a live item out — yet the merged
// `## Evidence References` section still lists those refs. Re-rendering each
// declared ref with its classification is what keeps it resolvable one
// generation later, when the new checkpoint's pack is the only resolution
// source left.
func checkpointEvidenceRefMetadata(bundle modelDrivenBarrierSnapshot, headSnapshot []message.Message) map[string]evidencePackRefMeta {
	metadata := make(map[string]evidencePackRefMeta, len(bundle.evidenceItems))
	for _, item := range bundle.evidenceItems {
		metadata[evidenceItemID(item)] = evidencePackRefMeta{
			kind:        item.Kind,
			invalidated: item.Validity == evidenceValidityInvalidated,
		}
	}
	for id, meta := range checkpointPackMetadataFromMessages(headSnapshot) {
		if _, exists := metadata[id]; !exists {
			metadata[id] = meta
		}
	}
	return metadata
}

// observedEvidenceResolver classifies why a resolvable reference cannot
// support an observed claim. A reference that resolves to a checkpoint evidence pack in
// the context has no runtime item behind it (its source messages were
// archived); live and carried references share the same category and validity
// checks, so a rendered negative kind or invalidation disqualifies a carried
// reference exactly like a live one. defect is a predicate phrase that follows
// the claim ("cannot use tool_error evidence ..."), empty when the reference
// supports the claim; unknown reports a reference neither source resolves.
type observedEvidenceResolver struct {
	a       *MainAgent
	byID    map[string]evidenceItem
	carried map[string]evidencePackRefMeta
}

func (a *MainAgent) newObservedEvidenceResolver() *observedEvidenceResolver {
	return &observedEvidenceResolver{a: a, byID: evidenceItemsByID(a.evidence.snapshot())}
}

func (r *observedEvidenceResolver) defect(ref string) (defect string, invalidated, unknown bool) {
	if item, ok := r.byID[ref]; ok {
		if !evidenceKindSupportsCompletion(item.Kind) {
			// The negative kind supplies the reason, but the invalidation flag
			// still has to travel with it: the downgrade path drops invalidated
			// references (no field may carry them) while it keeps merely weak
			// ones, so a negative item that went stale must report both.
			return fmt.Sprintf("cannot use %s evidence %q", item.Kind, ref), item.Validity == evidenceValidityInvalidated, false
		}
		if item.Validity == evidenceValidityInvalidated {
			return fmt.Sprintf("cannot use %s evidence %q", item.Validity, ref), true, false
		}
		return "", false, false
	}
	if r.carried == nil {
		r.carried = r.a.contextEvidencePackMetadata()
	}
	meta, ok := r.carried[ref]
	switch {
	case !ok:
		return "", false, true
	case meta.kind == "":
		return fmt.Sprintf("references evidence %q without classification", ref), false, false
	case !evidenceKindSupportsCompletion(meta.kind):
		return fmt.Sprintf("cannot use %s evidence %q", meta.kind, ref), meta.invalidated, false
	case meta.invalidated:
		return fmt.Sprintf("cannot use %s evidence %q", evidenceValidityInvalidated, ref), true, false
	}
	return "", false, false
}

func (a *MainAgent) validateObservedClaimEvidence(args tools.CompactContextArgs) error {
	resolver := a.newObservedEvidenceResolver()
	for claim, kind := range args.ClaimKinds {
		if kind != claimKindObserved {
			continue
		}
		for _, ref := range args.ClaimEvidence[claim] {
			defect, _, unknown := resolver.defect(ref)
			if unknown {
				return fmt.Errorf("observed claim %q references unknown evidence %q", claim, ref)
			}
			if defect != "" {
				return fmt.Errorf("observed claim %q %s", claim, defect)
			}
		}
	}
	return nil
}

// downgradeUnsupportedObservedClaims reclassifies an observed claim as derived
// when it cites no evidence or any evidence that cannot support it (a
// negative, unclassified or invalidated record), even alongside valid
// evidence — the same per-reference verdict strict validation applies — so one
// weak claim no longer rejects an otherwise valid provisional checkpoint. Invalidated references
// the downgraded claims cited are dropped because no field may carry them.
// Unknown IDs are left for the reference check to reject: an invented ID is a
// fabrication, not weak evidence. Committed or completed checkpoints are not
// downgraded; their acceptance semantics stay strict. The returned notes tell
// the model what was reclassified.
func (a *MainAgent) downgradeUnsupportedObservedClaims(args tools.CompactContextArgs) (tools.CompactContextArgs, []string) {
	if args.CheckpointKind == checkpointKindCommitted || args.StageStatus == stageStatusCompleted {
		return args, nil
	}
	claims := make([]string, 0, len(args.ClaimKinds))
	for claim, kind := range args.ClaimKinds {
		if kind == claimKindObserved {
			claims = append(claims, claim)
		}
	}
	if len(claims) == 0 {
		return args, nil
	}
	slices.Sort(claims)
	resolver := a.newObservedEvidenceResolver()
	var notes []string
	dropped := map[string]bool{}
	for _, claim := range claims {
		refs := args.ClaimEvidence[claim]
		if len(refs) == 0 {
			args.ClaimKinds[claim] = tools.CompactContextClaimDerived
			notes = append(notes, fmt.Sprintf("%q cited no claim_evidence", claim))
			continue
		}
		var reason string
		var kept, invalid []string
		citesUnknown := false
		for _, ref := range refs {
			defect, invalidated, unknown := resolver.defect(ref)
			if unknown {
				citesUnknown = true
				break
			}
			if defect == "" {
				kept = append(kept, ref)
				continue
			}
			if reason == "" {
				reason = defect
			}
			if invalidated {
				invalid = append(invalid, ref)
			} else {
				kept = append(kept, ref)
			}
		}
		if citesUnknown || reason == "" {
			continue
		}
		args.ClaimKinds[claim] = tools.CompactContextClaimDerived
		notes = append(notes, fmt.Sprintf("%q %s", claim, reason))
		for _, ref := range invalid {
			dropped[ref] = true
		}
		if len(kept) == 0 {
			delete(args.ClaimEvidence, claim)
		} else {
			args.ClaimEvidence[claim] = kept
		}
	}
	if len(dropped) > 0 {
		stillCited := map[string]bool{}
		for _, refs := range args.ClaimEvidence {
			for _, ref := range refs {
				stillCited[ref] = true
			}
		}
		args.EvidenceRefs = slices.DeleteFunc(args.EvidenceRefs, func(ref string) bool {
			return dropped[ref] && !stillCited[ref]
		})
	}
	return args, notes
}

// downgradedClaimsNotice renders the reclassification for the tool result.
func downgradedClaimsNotice(notes []string) string {
	if len(notes) == 0 {
		return ""
	}
	return "\nRecorded as derived instead of observed: " + strings.Join(notes, "; ") + ". Cite positive, valid evidence to keep a claim observed."
}

func (a *MainAgent) validateCommittedEvidence(args tools.CompactContextArgs) error {
	if args.CheckpointKind != checkpointKindCommitted && args.StageStatus != stageStatusCompleted {
		return nil
	}
	byID := evidenceItemsByID(a.evidence.snapshot())
	var carried map[string]evidencePackRefMeta
	for _, ref := range args.EvidenceRefs {
		item, ok := byID[ref]
		if ok {
			// Completed/committed semantics anchor to acceptance evidence only:
			// user corrections, stated constraints, tool diffs and similar
			// positive records. tool_error, done_rejected and escalate are
			// negative outcome records (a failed action, a rejected Done, an
			// open intervention request) and cannot support a claim of
			// completion, so committed treats them uniformly.
			if !evidenceKindSupportsCompletion(item.Kind) {
				return fmt.Errorf("committed checkpoint cannot use %s evidence %q", item.Kind, ref)
			}
			if item.Validity == evidenceValidityInvalidated {
				return fmt.Errorf("committed checkpoint cannot use %s evidence %q", item.Validity, ref)
			}
			continue
		}
		// The reference resolves through a checkpoint evidence pack in the
		// context. A committed upgrade must prove the evidence is a positive
		// acceptance record: a pack row that renders the item's kind is judged
		// by the same category and validity rules as a live item, while a row
		// that only proves the ID exists (an older pack format without a
		// classification) cannot support the committed upgrade at all.
		if carried == nil {
			carried = a.contextEvidencePackMetadata()
		}
		meta, ok := carried[ref]
		if !ok {
			return fmt.Errorf("committed checkpoint cannot cite evidence %q that is not resolvable in the live context", ref)
		}
		if meta.kind == "" {
			return fmt.Errorf("committed checkpoint cannot cite evidence %q: the archived record does not carry the classification needed to verify acceptance semantics; reference live evidence or rebuild the checkpoint first", ref)
		}
		if !evidenceKindSupportsCompletion(meta.kind) {
			return fmt.Errorf("committed checkpoint cannot use %s evidence %q", meta.kind, ref)
		}
		if meta.invalidated {
			return fmt.Errorf("committed checkpoint cannot use %s evidence %q", evidenceValidityInvalidated, ref)
		}
	}
	return nil
}

// checkpointClaimNeedsEvidenceError marks the arm-time rejection a runtime hint
// can make self-correcting: an observed claim with no claim_evidence. The
// refusal text still states the contract; the arming path wraps it with the
// evidence IDs this context actually offers, so the model can either cite one
// or reclassify the claim instead of guessing what "observed" required.
type checkpointClaimNeedsEvidenceError struct {
	claim string
}

func (e checkpointClaimNeedsEvidenceError) Error() string {
	return fmt.Sprintf("claim_kinds %q is observed but has no claim_evidence", e.claim)
}

func validateModelDrivenCheckpointKind(args tools.CompactContextArgs) error {
	var problems []error
	add := func(err error) { problems = append(problems, err) }
	if args.CheckpointKind == checkpointKindCommitted && args.StageStatus != stageStatusCompleted {
		add(fmt.Errorf("committed compact_context requires stage_status=completed; retry with checkpoint_kind=provisional when the stage is not authoritative yet"))
	}
	if args.CheckpointKind == checkpointKindCommitted && len(args.EvidenceRefs) == 0 {
		add(fmt.Errorf("committed compact_context requires at least one evidence_refs entry from the checkpoint evidence pack; retry with checkpoint_kind=provisional when no evidence pack is in view"))
	}
	claims := make([]string, 0, len(args.ClaimKinds))
	for claim := range args.ClaimKinds {
		claims = append(claims, claim)
	}
	slices.Sort(claims)
	for _, claim := range claims {
		if args.ClaimKinds[claim] != claimKindObserved {
			continue
		}
		if len(args.ClaimEvidence[claim]) == 0 {
			add(checkpointClaimNeedsEvidenceError{claim: claim})
			continue
		}
		refs := slices.Clone(args.ClaimEvidence[claim])
		slices.Sort(refs)
		for _, ref := range slices.Compact(refs) {
			if !slices.Contains(args.EvidenceRefs, ref) {
				add(fmt.Errorf("observed claim %q references evidence %q that is not listed in evidence_refs", claim, ref))
			}
		}
	}
	// Bound rejection output while reporting independent repairs together.
	const maxReportedProblems = 8
	if len(problems) > maxReportedProblems {
		omitted := len(problems) - maxReportedProblems
		problems = append(problems[:maxReportedProblems:maxReportedProblems], fmt.Errorf("%d additional checkpoint validation errors omitted; correct the reported fields and retry", omitted))
	}
	return errors.Join(problems...)
}
