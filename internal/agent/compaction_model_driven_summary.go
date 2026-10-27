package agent

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/keakon/chord/internal/agent/checkpointfmt"
	"github.com/keakon/chord/internal/agent/requesttrim"
	"github.com/keakon/chord/internal/message"
)

// buildModelDrivenCheckpointSummary renders the deterministic checkpoint
// sections. Current User Request comes from the authoritative runtime
// resolver (real user messages / Done-rejected reasons in message order, with
// inheritance from the previous checkpoint when the tail has nothing new),
// never from the model-authored args. Every model-authored field is rendered
// verbatim except that column-zero ATX heading markers are stripped, so the
// model cannot open a new section from inside one; state_files are labeled as
// model-declared references.
//
// snapshot is the whole barrier snapshot and headSplit its archive boundary:
// the anchor resolver reads the full history, the anchors/carry only the
// archived head. Both are slices of the same immutable snapshot — copying the
// history into a fresh slice just to concatenate head and tail would duplicate
// the entire conversation for no gain.
func (a *MainAgent) buildModelDrivenCheckpointSummary(bundle modelDrivenBarrierSnapshot, snapshot []message.Message, headSplit int, req *modelDrivenCheckpointRequest) string {
	// The prior checkpoint's machine-carryable state (decisions, open issues,
	// evidence references, stage, claims) is merged into this submission and
	// rendered below; the whole prior body is never carried as natural-language
	// Markdown. stateCarryOmitted reports carried list items dropped by the
	// merge's list bounds and claimsCarryOmitted the carried-only claims the
	// claim-set cap evicted, so the renderer discloses both omissions;
	// typedCarryUnreadable reports a prior typed block that could not be
	// parsed. mergePriorTypedCheckpointState parses the body chosen by
	// latestPriorTypedCheckpointBody — the nearest checkpoint that actually
	// carries a parseable typed block — because a body truncated to the
	// display carry cap (latestPriorCheckpointBody) would drop the typed JSON
	// line before the parse.
	var stateCarryOmitted int
	var claimsCarryOmitted int
	var openIssuesCarryOmitted int
	var typedCarryUnreadable bool
	// The prior typed state comes from the nearest checkpoint that actually
	// carries a parseable typed block: a usage-driven or truncate-only
	// summary sandwiched between two model-driven checkpoints declares none,
	// and merging against its stripped body alone would silently drop the
	// older chain's machine-carryable state (see
	// latestPriorTypedCheckpointBody).
	priorTypedBody, typedCarryBroken := latestPriorTypedCheckpointBody(snapshot[:headSplit])
	if typedCarryBroken {
		typedCarryUnreadable = true
	} else if priorTypedBody != "" {
		req, stateCarryOmitted, claimsCarryOmitted, openIssuesCarryOmitted, _ = mergePriorTypedCheckpointState(req, priorTypedBody)
	}
	markTypedClaimsInvalidated(req, bundle.evidenceItems)
	headSnapshot := snapshot[:headSplit]
	anchor := resolveLatestUserRequestAnchor(snapshot)
	constraints := renderEvidenceKindForFallback(&compactionInput{
		EvidenceItems:         bundle.evidenceItems,
		OriginalRequestSource: checkpointRequestSource(bundle.originalRequest),
	}, evidenceUserCorrection, "- No preserved user constraints.")
	completed := renderModelStateList(req.Args.Completed, "(none reported by the model)")
	// Completed work is the primary readable home for facts that are already
	// done. Do not repeat an identical item in decisions or open issues; keep
	// near-matches because they may intentionally describe a different state.
	decisions := renderModelStateList(removeCheckpointItems(req.Args.Decisions, req.Args.Completed), "(none reported by the model)")
	openIssues := renderOpenProblemsSection(req, openIssuesCarryOmitted)
	// The Externalized State footer describes the archive the continuation
	// actually receives, which is the archival-filtered pack
	// (newModelDrivenCheckpointBuilder filters the same way), not the raw
	// evidence the barrier snapshot held: an invalidated item the pack dropped
	// must not inflate the count of IDs that remain reachable, nor count as a
	// reference resolved. markTypedClaimsInvalidated and the constraints
	// section above read the raw set on purpose — they need to know which
	// claims and corrections were touched, including the invalidated ones.
	archivedEvidence := filterCompactionEvidenceForArchival(bundle.evidenceItems)
	stateFiles := renderStateFilesSection(req.Args.StateFiles, archivedEvidence, req.Args.EvidenceRefs)
	plannedStateFiles := renderPlannedStateFilesSection(req.Args.PlannedStateFiles)
	evidenceRefs := renderEvidenceRefsSection(req.Args.EvidenceRefs)
	claims := renderCheckpointClaims(req)
	stage := renderModelDrivenStageSection(req.Args.StageID, req.Args.StageStatus, req.Args.CheckpointKind)
	typedState := renderTypedCheckpointState(req)
	if claimsCarryOmitted > 0 {
		// The dropped claims exist only in the archived history files. The
		// note sits right below the typed JSON line — the authoritative claim
		// block — and the typed parser reads only the first line of the
		// section, so the disclosure never breaks the machine block.
		typedState += "\n" + typedStateClaimsOmittedNote
	}
	if stateCarryOmitted-openIssuesCarryOmitted > 0 {
		// The dropped entries exist only in the archived history files. The
		// note must say so: a bounded carry that silently looked complete
		// would read as the full decision record. The open-issue share of the
		// omissions is disclosed inside ## Open Problems instead (see
		// renderOpenProblemsSection), so this note covers the lists rendered
		// here.
		decisions += "\n" + typedStateOmittedNote
	}
	if typedCarryUnreadable {
		decisions += "\n" + typedStateUnreadableNote
	}

	sections := []fallbackSummarySection{
		{"## Current User Request", modelDrivenCurrentUserRequestSection(anchor)},
		{checkpointfmt.UserConstraintsHeading, constraints},
		{"## Active Objective", renderModelState(req.Args.ActiveObjective)},
		// Intentional placeholder body, not a dropped classification: the
		// model-driven path has no model-authored goals section, so this heading
		// carries only the fixed subordination rule the continuation must apply.
		{"## Background Goals", "- Earlier goals and the model-declared Active Objective are subordinate to the Current User Request and User Constraints."},
		{"## Next Step", renderModelState(req.Args.NextStep)},
		{checkpointfmt.OpenProblemsHeading, openIssues},
		{checkpointfmt.ProgressHeading, completed},
		{checkpointfmt.KeyDecisionsHeading, decisions},
		// Intentional placeholder body: this builder selects no key files, so the
		// section only points at the wrapper's archived history list.
		{"## Files and Evidence", "- Precise archived history is listed under the checkpoint wrapper's archived history files."},
		{"## Externalized State", stateFiles},
		{"## Planned Externalized State", plannedStateFiles},
		{"## Evidence References", evidenceRefs},
		{"## Claims", claims},
		{"## Checkpoint Stage", stage},
		{message.CompactionTypedStateHeading, typedState},
		{"## Todo State", formatTodosAsRelevanceBullets(bundle.todos, anchor)},
		{"## SubAgent State", formatSubAgentsAsBullets(bundle.subAgents)},
	}
	summary := renderFallbackSummarySections(sections)
	// The model-driven checkpoint has no model classification to fill the
	// relevance skeleton above, so the runtime-owned snapshot carries the
	// complete todo state — the same guarantee the summarization runner
	// applies, and the line-escaping that keeps todo content from escaping
	// its section. The stale bucket must stay empty: restore only drops
	// runtime todos when it sees entries there.
	summary = ensureCompactionTodoSnapshot(summary, bundle.todos)
	// Skills loaded in the archived head are recorded by name, exactly as the
	// summarization runner does: the instructions live only in the transcript,
	// so this reset would otherwise leave the model following a workflow it can
	// no longer see — and unable to tell that it should re-load it.
	skillNames, skillsOmitted := collectCheckpointSkillNames(headSnapshot)
	summary = ensureCheckpointSkillsSection(summary, skillNames, skillsOmitted, checkpointModelLoadable(a.callableSkillsSnapshot()))
	// Live jobs are the third runtime-owned snapshot: the model submission
	// cannot know them, and after the reset their handles are gone from the
	// transcript, so the checkpoint must carry what is still running. The
	// states were frozen at the barrier capture; the label instant and the
	// elapsed / deadline numbers are taken once here, and preflight and render
	// both read this same rendered body, so the two never disagree about the
	// snapshot_at label. The staleness fingerprint covers backgroundObjects, so
	// a job that changed between the barrier capture and the apply rejects the
	// draft instead of shipping a snapshot that was already wrong when it
	// landed.
	summary = ensureActiveBackgroundJobSnapshot(summary, bundle.backgroundObjects, time.Now())
	// The same runtime-owned composition the summarization runner applies: the
	// authoritative latest request and the unsettled tool calls never depend on
	// model wording on either path. The anchor above is reused so the same
	// compaction does not scan the transcript for it twice.
	summary = applyCompactionRecoveryState(summary, buildCompactionRecoveryStateWithAnchor(anchor, headSnapshot))
	// The previous checkpoint's machine-carryable state was merged into the
	// typed state block above; its natural-language body is deliberately NOT
	// carried forward. The model re-states its current objective on every
	// submission, and carried completed work/decisions/open issues/evidence
	// references/stage travel structurally through the typed block — so two
	// consecutive model-driven resets cannot erase prior verified decisions,
	// and everything else the model did not restate exists in the archived
	// history files rather than as a growing verbatim appendix.
	anchors := buildCompactionAnchors(latestCompactionAnchors(headSnapshot), bundle.originalRequest, bundle.evidenceItems)
	return withCompactionAnchors(summary, anchors)
}

func removeCheckpointItems(items, excluded []string) []string {
	if len(items) == 0 || len(excluded) == 0 {
		return items
	}
	excludedKeys := make(map[string]struct{}, len(excluded))
	for _, item := range excluded {
		excludedKeys[checkpointItemKey(item)] = struct{}{}
	}
	kept := make([]string, 0, len(items))
	for _, item := range items {
		if _, found := excludedKeys[checkpointItemKey(item)]; !found {
			kept = append(kept, item)
		}
	}
	return kept
}

// effectiveCheckpointClaims returns the authoritative claim set of a request:
// the merged carry when one exists (see modelDrivenCheckpointRequest.Claims),
// otherwise the claims derivable from the fresh submission args.
func effectiveCheckpointClaims(req *modelDrivenCheckpointRequest) map[string]checkpointClaim {
	if req == nil {
		return nil
	}
	if len(req.Claims) > 0 {
		return req.Claims
	}
	return typedClaimsFromArgs(req.Args)
}

// markTypedClaimsInvalidated downgrades claims whose evidence can no longer
// back them. A claim is invalidated when one of its evidence refs is
// invalidated in the runtime evidence (a tracked file revision changed or the
// underlying tool result was corrected). A ref that resolves to no item at all
// means the evidence left the runtime evidence of this generation — its source
// messages were archived by an earlier apply — and a claim the fresh
// submission re-asserted against it (status active or unset) must not keep
// rendering as an observed assertion: without this downgrade a carried claim
// could keep an active/observed posture across generations while its evidence
// was long gone. Already-demoted postures (stale carried claims, invalidated
// claims) are left alone.
func markTypedClaimsInvalidated(req *modelDrivenCheckpointRequest, evidenceItems []evidenceItem) {
	claims := effectiveCheckpointClaims(req)
	if len(claims) == 0 {
		return
	}
	byID := evidenceItemsByID(evidenceItems)
	for claim, item := range claims {
		demote := false
		for _, ref := range item.EvidenceRefs {
			evidence, ok := byID[ref]
			if ok {
				if evidence.Validity == evidenceValidityInvalidated {
					demote = true
					break
				}
				continue
			}
			status := strings.TrimSpace(item.Status)
			if status == "" || status == typedClaimStatusActive {
				demote = true
				break
			}
		}
		if demote {
			if req.ClaimStatuses == nil {
				req.ClaimStatuses = map[string]string{}
			}
			req.ClaimStatuses[claim] = typedClaimStatusInvalidated
		}
	}
}

// mergePriorTypedCheckpointState merges the typed state carried by the most
// recent prior checkpoint (inside the archived head) into the fresh model
// submission, then writes the merged state back into the request arguments so
// the rendered sections and the typed state block both reflect the carry. The
// fresh submission wins (its items come first; its stage metadata overrides
// the carried one and a carried completed/committed stage is never inherited
// on an empty restate — see mergeCheckpointTypedStates); carried items fill
// the remaining capacity. omitted reports how many carried list items were
// dropped to bound the lists, claimsOmitted how many carried-only claims the
// claim-set cap evicted, and openIssuesOmitted how many carried open issues
// the shared open-issue budget dropped, so the renderer can disclose each.
// malformed reports that the prior body carried a typed block that could not
// be parsed, which the renderer must disclose as an unreadable carry rather
// than silently treating it as absent. A nil request, an empty prior body, or
// a prior body without a typed state block leaves the submission untouched.
//
// The open issues the submission did not restate do not land in Args: they
// travel on CarriedOpenIssues and render apart from the current blockers (see
// renderOpenProblemsSection).
//
// prior must be the body of a checkpoint that actually carries a parseable
// typed block (see latestPriorTypedCheckpointBody): the caller scans the head
// for the nearest such checkpoint instead of passing a body truncated to the
// display carry cap, which would drop the typed JSON line before the parse.
func mergePriorTypedCheckpointState(req *modelDrivenCheckpointRequest, prior string) (mergedReq *modelDrivenCheckpointRequest, omitted int, claimsOmitted int, openIssuesOmitted int, malformed bool) {
	if req == nil {
		return req, 0, 0, 0, false
	}
	priorState, found, broken := typedStateFromBody(prior)
	if !found {
		return req, 0, 0, 0, false
	}
	if broken {
		return req, 0, 0, 0, true
	}
	priorState = retireCheckpointItems(priorState, req.Args.RetiredItems)
	merged, omitted, claimsOmitted, openIssuesOmitted := mergeCheckpointTypedStates(priorState, typedStateFromArgs(req.Args))
	copyReq := *req
	copyReq.Args.Completed = merged.Completed
	copyReq.Args.Decisions = merged.Decisions
	// Args.OpenIssues holds only the current generation's confirmed issues;
	// the ones this submission did not restate travel on the historical set
	// below so they can never render as current blockers.
	copyReq.Args.OpenIssues = merged.OpenIssues
	copyReq.CarriedOpenIssues = merged.CarriedOpenIssues
	copyReq.Args.EvidenceRefs = merged.EvidenceRefs
	copyReq.Args.StageID = merged.StageID
	copyReq.Args.StageStatus = merged.StageStatus
	copyReq.Args.CheckpointKind = merged.Kind
	// The whole merged claim set travels on the request (see
	// effectiveCheckpointClaims): the fresh submission's own claims plus the
	// carried ones it did not restate, each keeping its classification,
	// evidence association and status (a carried claim the fresh submission
	// does not restate is demoted from active to stale instead of keeping the
	// trusted current-generation posture).
	// Rebuilding the claims from the fresh Args alone — as the code did before
	// carrying them here — dropped every claim the submission did not restate,
	// and a claim invalidated by evidence that has since left the window would
	// come back active.
	copyReq.Claims = merged.Claims
	return &copyReq, omitted, claimsOmitted, openIssuesOmitted, false
}

func renderTypedCheckpointState(req *modelDrivenCheckpointRequest) string {
	if req == nil {
		return "- (none)"
	}
	state := typedStateFromArgs(req.Args)
	// The merged claim set (fresh plus carried) replaces the Args-derived
	// claims when present, so claims the fresh submission did not restate stay
	// in the typed block with their classification, evidence and status.
	if len(req.Claims) > 0 {
		state.Claims = req.Claims
	}
	for claim, status := range req.ClaimStatuses {
		item := state.Claims[claim]
		item.Status = status
		state.Claims[claim] = item
	}
	// Args only ever holds the current generation's confirmed open issues, so
	// the historical set and the carry ordinal travel on the request: the
	// typed block is the only channel that hands the historical bucket to the
	// next checkpoint, and the ordinal is what keeps a carried entry's
	// provenance stable across generations.
	state.CarriedOpenIssues = req.CarriedOpenIssues
	return renderTypedStateJSON(state)
}

// inheritedCheckpointLabel is the label prefixed to a `## Current User Request`
// section inherited from the previous checkpoint. It lives in one place so the
// accumulation guard in modelDrivenCurrentUserRequestSection strips exactly
// the same text the resolver stamps on inherited anchors.
const inheritedCheckpointLabel = "Inherited from the previous context checkpoint"

// modelDrivenCurrentUserRequestSection renders the `## Current User Request`
// section of a deterministic checkpoint from the latest-request anchor. It is the
// canonical renderer for both compaction paths: the summarization runner
// replaces the summarizer's own body with it (ensureCompactionLatestRequestAnchor),
// so a weak or fallback summary cannot lose or stale the authoritative request.
func modelDrivenCurrentUserRequestSection(anchor fallbackAnchor) string {
	if anchor.Kind != "" {
		if anchor.Kind == "inherited_checkpoint" {
			// The inherited body is the previous checkpoint's own bullet
			// ("- Latest user request: ..."); strip the bullet marker so the
			// inherited label reads naturally. The body may itself carry the
			// inherited label from an earlier checkpoint in a chain of resets
			// (a checkpoint built with no new user request inherits the prior
			// one), so strip it too: the label must never accumulate across
			// successive checkpoints.
			body := strings.TrimSpace(strings.TrimPrefix(anchor.Text, "- "))
			body = strings.TrimSpace(strings.TrimPrefix(body, inheritedCheckpointLabel+": "))
			return "- " + inheritedCheckpointLabel + ": " + body
		}
		text := strings.TrimSpace(strings.ReplaceAll(anchor.Text, "\r\n", "\n"))
		// Keep the request's own line structure (lists, code blocks) as an
		// indented continuation of the bullet. The indent also keeps every
		// request line, including the tail after compactTextSnippet's
		// omission separator, off column zero: a line starting with "## "
		// there would forge a section boundary, and the next inheritance
		// pass would cut the request tail plus the truncation notice below.
		snippet := strings.ReplaceAll(requesttrim.TextSnippet(text, modelDrivenAnchorMaxRunes), "\n", "\n  ")
		section := stripColumnZeroHeadings("- " + anchor.Label + ": " + snippet)
		if utf8.RuneCountInString(text) > modelDrivenAnchorMaxRunes {
			section += "\n" + truncatedRequestNotice + " Recover the full request from the archived history files before making decisions that depend on omitted requirements; this excerpt is not the complete specification."
		}
		return section
	}
	return "- Unknown: no reliable latest user request was preserved; do not infer the active task from stale context."
}

// renderModelState renders a model-authored field into a checkpoint section.
// The text is trusted verbatim except for column-zero ATX heading markers,
// which are stripped: the checkpoint readers locate sections by "^## " lines,
// and a heading is the only line-level structure that would let the text
// escape its section. Quoted lines ("> ...") and every other markdown
// construct are preserved, so a blockquote the model wrote renders as one.
func renderModelState(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return "- (none)"
	}
	return stripColumnZeroHeadings(text)
}

// stripColumnZeroHeadings removes column-zero ATX heading markers from every
// line of a rendered section. Checkpoint readers locate sections by "^## "
// lines, so no text rendered inside a section may start a line with a heading
// marker, no matter which path (model-authored field, request excerpt) put it
// there.
func stripColumnZeroHeadings(text string) string {
	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		out = append(out, stripLeadingHeadingMarkers(line))
	}
	return strings.Join(out, "\n")
}

// renderOpenProblemsSection renders the ## Open Problems section: the current
// generation's confirmed open issues first, then the historical issues an
// earlier checkpoint confirmed and this submission did not restate.
//
// The two sets are rendered apart on purpose. A carried-only issue is not a
// blocker of this checkpoint — the model restated its current blockers and
// this one was not among them — so listing it beside them made a stale entry
// read as freshly confirmed and invited the continuation to re-investigate
// finished work. The historical block stays visible, and its label says both
// what it is and what to do with it, because an unrestated risk must never be
// silently read as resolved while it must also not cost a re-check per item.
//
// The overlap with completed work keeps the existing exact-text display
// de-duplication: it is deliberately not a semantic match, so an issue that
// merely resembles a completed entry ("tests not written" against "tests
// passed") survives instead of being auto-resolved.
func renderOpenProblemsSection(req *modelDrivenCheckpointRequest, carriedOmitted int) string {
	if req == nil {
		return "- (none reported by the model)"
	}
	confirmed := removeCheckpointItems(req.Args.OpenIssues, req.Args.Completed)
	section := renderModelStateList(confirmed, "(none reported by the model)")
	if len(req.CarriedOpenIssues) > 0 {
		completedKeys := make(map[string]struct{}, len(req.Args.Completed))
		for _, item := range req.Args.Completed {
			completedKeys[checkpointItemKey(item)] = struct{}{}
		}
		var carried []string
		for _, item := range req.CarriedOpenIssues {
			text := strings.TrimSpace(item)
			if text == "" {
				continue
			}
			if _, done := completedKeys[checkpointItemKey(text)]; done {
				continue
			}
			carried = append(carried, text)
		}
		if len(carried) > 0 {
			section += "\n- " + typedStateCarriedIssuesLabel + "\n" + renderModelStateList(carried, "")
		}
	}
	if carriedOmitted > 0 {
		section += "\n" + typedStateOpenIssuesOmittedNote
	}
	return section
}

// renderModelStateList renders model-authored list items as bullets
// ("- item" per line), stripping only column-zero heading markers the same
// way renderModelState does. The fallback text is used when the list is empty.
func renderModelStateList(items []string, empty string) string {
	if len(items) == 0 {
		return "- " + empty
	}
	var sb strings.Builder
	for i, item := range items {
		if i > 0 {
			sb.WriteByte('\n')
		}
		for line := range strings.SplitSeq(item, "\n") {
			sb.WriteString("- ")
			sb.WriteString(stripLeadingHeadingMarkers(line))
			sb.WriteByte('\n')
		}
	}
	return strings.TrimRight(sb.String(), "\n")
}

// stripLeadingHeadingMarkers removes an ATX heading marker ("# ".."###### ")
// from the start of a line. A run longer than six hashes is not a heading and
// stays untouched; "#tag" / "#1" (no blank after the run) are plain text and
// survive. The strip loops so a line like "# # x" cannot re-render as a
// heading after one pass.
func stripLeadingHeadingMarkers(line string) string {
	for {
		n := 0
		for n < len(line) && line[n] == '#' {
			n++
		}
		if n == 0 || n > 6 {
			return line
		}
		if n < len(line) && line[n] != ' ' && line[n] != '\t' {
			return line
		}
		j := n
		for j < len(line) && (line[j] == ' ' || line[j] == '\t') {
			j++
		}
		line = line[j:]
	}
}

// renderStateFilesSection renders the Externalized State section: every path
// is a model-declared reference rendered as its own bullet.
//
// The model submits these paths as part of its checkpoint; the system treats
// each as an unverified claim and never stats it. Existence is therefore not
// checked at checkpoint time — stamping it would turn a checkpoint request
// into a cross-permission-boundary existence probe. What the continuation may
// re-load is decided later, from the same bullet list: only a file this
// session already read or wrote, and only while the read permission rule still
// allows it (see compactionContinuationFiles). Anything else — including a
// missing file — surfaces when the model reads it with the read tool.
func renderStateFilesSection(paths []string, archivedEvidence []evidenceItem, evidenceRefs []string) string {
	if len(paths) == 0 {
		// An empty list is legitimate (pure analysis, final delivery, a role
		// without write tools), so it is never a rejection. The continuation
		// still gets an actionable line: use the checkpoint and archive now,
		// and register notes when reusable details accumulate and writing is
		// permitted.
		//
		// The archived-evidence counts are stated as facts rather than left as
		// a generic reminder, because this message repeats on every later
		// request: the counts tell the continuation whether the record it
		// needs is still reachable by ID from the archive it received (or from
		// a later checkpoint's carry), and the ID route is what the evidence
		// pack's rows and the /restore recovery path both expect.
		cited := evidenceItemsByID(archivedEvidence)
		referenced := 0
		for _, ref := range evidenceRefs {
			if _, ok := cited[strings.TrimSpace(ref)]; ok {
				referenced++
			}
		}
		prefix := "- (none reported by the model)\n" +
			"- Model-declared references only; existence is not verified.\n"
		if len(archivedEvidence) == 0 {
			// With nothing archived there is no ID to cite, so offering the ID
			// route would ask for an action the continuation cannot take. Future
			// reusable details can be saved in notes when writing is permitted.
			return prefix +
				"- No durable state file was registered and no archived evidence exists to cite: when reusable working details accumulate and file writing is permitted, save them in task notes and register the file at the next checkpoint."
		}
		return prefix +
			fmt.Sprintf("- No durable state file was registered: %d archived evidence item(s) exist, and this state references %d of them. Cite an archived ID to make its detail survive later checkpoints. When reusable working details accumulate and file writing is permitted, save them in task notes and register the file at the next checkpoint.", len(archivedEvidence), referenced)
	}
	var sb strings.Builder
	sb.WriteString("- Model-declared references only; existence is not verified. Use these task notes for working details; read only the sections needed for the next action unless already injected:\n")
	for _, p := range paths {
		for line := range strings.SplitSeq(p, "\n") {
			sb.WriteString("- ")
			sb.WriteString(line)
			sb.WriteByte('\n')
		}
	}
	return strings.TrimRight(sb.String(), "\n")
}

// checkpointInlineValue flattens a model-authored value for embedding inside a
// single "- " bullet. Every line's column-zero ATX heading markers are
// stripped (reusing stripLeadingHeadingMarkers, the same escape the prose
// sections apply) and embedded newlines become spaces, so a multiline "## "
// value can neither open a fake top-level checkpoint section nor leak a raw
// continuation line at column zero. Structured reference fields (claims, stage
// metadata, planned file paths) are single-line by nature, so flattening loses
// nothing readable while closing the injection.
func checkpointInlineValue(value string) string {
	if !strings.Contains(value, "\n") {
		return stripLeadingHeadingMarkers(value)
	}
	lines := strings.Split(value, "\n")
	out := make([]string, len(lines))
	for i, line := range lines {
		out[i] = stripLeadingHeadingMarkers(line)
	}
	return strings.Join(out, " ")
}

func renderPlannedStateFilesSection(paths []string) string {
	if len(paths) == 0 {
		return "- (none reported by the model)"
	}
	var sb strings.Builder
	sb.WriteString("- Planned references only; they are not completion evidence and are not verified:\n")
	for _, path := range paths {
		sb.WriteString("- ")
		sb.WriteString(checkpointInlineValue(path))
		sb.WriteByte('\n')
	}
	return strings.TrimRight(sb.String(), "\n")
}

func renderEvidenceRefsSection(refs []string) string {
	if len(refs) == 0 {
		return "- (none reported by the model)"
	}
	return "- Model-declared evidence references; runtime verified that these IDs exist:\n- " + strings.Join(refs, "\n- ")
}

func renderModelDrivenStageSection(id, status, kind string) string {
	if id == "" && status == "" && kind == "" {
		return "- No stage metadata reported by the model."
	}
	return fmt.Sprintf("- Stage ID: %s\n- Stage status: %s\n- Checkpoint kind: %s\n- Stage metadata is model-declared; runtime facts and acceptance evidence remain authoritative.", checkpointInlineValue(id), checkpointInlineValue(status), checkpointInlineValue(kind))
}
