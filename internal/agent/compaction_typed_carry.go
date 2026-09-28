package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"
	"unicode"

	"github.com/keakon/chord/internal/tools"
)

// Typed checkpoint carry.
//
// A recursive chain of model-driven checkpoints re-summarizes the session at
// every step, and the model only ever submits the state it considers current.
// What must survive the recursion is the machine-carryable task state — the
// verified decisions, open issues, acceptance-evidence references and stage
// metadata — so it travels as a structured `## Typed Checkpoint State` block
// inside the checkpoint body, parsed back and re-merged by the next
// generation. The whole previous checkpoint body is deliberately NOT carried
// as natural-language Markdown: the model re-states its current objective
// each round, and narrative that a later checkpoint did not restate
// lives in the archived history files, not in a growing verbatim appendix.
//
// The typed block is a single JSON line (like the anchors tag) so the
// rendering, parsing and merging are all unambiguous and testable. Per-item
// and per-list limits bound the carried state: merged lists keep the newest
// entries (the current model's submission first, older carried entries after)
// and disclose what was dropped rather than silently presenting a list that
// looks complete.

const (
	// typedStateSectionHeading introduces the machine-carryable state block
	// inside a checkpoint body.
	typedStateSectionHeading = "## Typed Checkpoint State"
	// typedStateCarryMaxDecisions / MaxOpenIssues / MaxEvidenceRefs bound a
	// merged list, matching the per-field limits the compact_context argument
	// validator enforces on a fresh submission (internal/tools/
	// compact_context.go): the carried list can never grow past what a single
	// fresh submission could express.
	typedStateCarryMaxDecisions    = 8
	typedStateCarryMaxCompleted    = 12
	typedStateCarryMaxOpenIssues   = 8
	typedStateCarryMaxEvidenceRefs = 24
	// typedStateCarryMaxItemRunes caps a single carried item. Items that
	// exceed it are truncated at a rune boundary with an explicit marker, so
	// a half item can never masquerade as a complete decision.
	typedStateCarryMaxItemRunes   = 400
	typedStateItemTruncatedSuffix = " [truncated]"
	// typedStateOmittedNote is appended to a rendered section that dropped
	// carried items to fit the list cap. The drop is disclosed, never silent:
	// the full record remains recoverable from the archived history files.
	typedStateOmittedNote = "- [older checkpoint item(s) omitted to bound the carried state; read the archive for the complete record.]"
	// typedStateUnreadableNote is appended when a checkpoint body carries a
	// typed block that cannot be parsed. Dropping an unreadable carry without
	// a note would read as a complete record of nothing.
	typedStateUnreadableNote = "- [prior checkpoint typed state was present but could not be read; consult the archived history for the complete record.]"
	// typedStateCarryMaxClaims caps the merged claim set of one generation.
	// Fresh submissions are already capped by the compact_context validator
	// (maxCompactContextClaims=20 in internal/tools/compact_context.go), so
	// the doubled cap reserves the whole fresh budget plus one carried
	// generation's worth of claims.
	typedStateCarryMaxClaims = 40
	// typedStateClaimsOmittedNote discloses carried claims dropped by the
	// claim-set cap. It is appended right below the typed JSON line, where
	// the authoritative claim set lives; the typed parser reads only the
	// first line of the section, so the disclosure never breaks the machine
	// block.
	typedStateClaimsOmittedNote = "- [older checkpoint claim(s) omitted to bound the carried claim state; read the archive for the complete record.]"
	// typedIssueStatusUnconfirmed is the confirmation status of a carried open
	// issue the current generation did not restate. It is the only status the
	// historical bucket holds: an issue the current submission restates is
	// confirmed by definition and lives in OpenIssues, never here.
	typedIssueStatusUnconfirmed = "unconfirmed"
	// typedStateOpenIssuesOmittedNote discloses carried open issues dropped by
	// the shared open-issue budget. It is appended inside the ## Open Problems
	// section — where the reader looks for issues — rather than reusing the
	// generic carried-state note, which lands under the decisions it bounds.
	typedStateOpenIssuesOmittedNote = "- [older carried open issue(s) omitted to bound the carried state; read the archive for the complete record.]"
	// typedStateCarriedIssuesLabel introduces the historical open issues of a
	// checkpoint: issues an earlier generation confirmed that the current
	// submission did not restate. The label is the whole point of the split.
	// It tells the continuation these entries are not blockers of this
	// checkpoint and must not be re-investigated one by one, while keeping
	// them visible and recoverable so an unrestated risk is never silently
	// read as resolved. Like every other rendered checkpoint string it stays
	// ASCII, so the section renders identically in any locale.
	typedStateCarriedIssuesLabel = "Carried from earlier checkpoints and not restated here (historical, unconfirmed, not current blockers). Do not re-investigate each; re-check only what bears on the next step or acceptance:"
	// typedClaimStatusActive / typedClaimStatusInvalidated /
	// typedClaimStatusStale are the claim-status vocabulary of the carried
	// typed claims. Active is the posture of a claim the current generation
	// asserted; invalidated marks a claim whose evidence the runtime
	// invalidated; stale is the downgraded posture of a carried-only claim
	// the current generation did not restate.
	typedClaimStatusActive      = "active"
	typedClaimStatusInvalidated = "invalidated"
	typedClaimStatusStale       = "stale"
	// claimKindObserved / checkpointKindCommitted / stageStatusCompleted are
	// the identifiers the runtime's committed/observed validation keys on.
	// The values come from the exported compact_context vocabulary in
	// internal/tools/compact_context.go (the tool schema declares the same
	// enums); the tool package cannot import the agent package, so the agent
	// aliases the exported constants here instead of repeating the literals
	// at each check site.
	claimKindObserved       = tools.CompactContextClaimObserved
	checkpointKindCommitted = tools.CompactContextCheckpointKindCommitted
	stageStatusCompleted    = tools.CompactContextStageCompleted
)

// checkpointTypedState is the machine-carryable task state a checkpoint
// carries across generations. It is exactly the state with a durable,
// cross-generation meaning; everything else the checkpoint renders (current
// user request and active objective) is resolved anew on every submission.
type checkpointTypedState struct {
	Completed          []string
	Decisions          []string
	OpenIssues         []string
	OpenIssuesComplete bool
	OpenIssueIDs       map[string]string
	EvidenceRefs       []string
	StageID            string
	StageStatus        string
	Kind               string
	// CarriedOpenIssues is the historical open-issue bucket: issues an
	// earlier generation confirmed that the current submission did not
	// restate. They are deliberately NOT kept in OpenIssues — a carried-only
	// entry is no longer a blocker of this checkpoint, and rendering it beside
	// the fresh submission's issues made a stale item read exactly like a
	// freshly confirmed one. See mergeTypedStateOpenIssues.
	CarriedOpenIssues []checkpointOpenIssue
	// Generation is the carry ordinal: 0 for a state that was never merged
	// into a successor, and one more than the prior state on every merge. It
	// is the provenance the historical bucket records per entry, so a carried
	// issue keeps the generation that last confirmed it.
	Generation int
	Claims     map[string]checkpointClaim
}

// checkpointOpenIssue is one open issue carried from an earlier generation
// that the current submission did not restate, with its provenance: the text
// (whose stable identity is the lexical openIssueKey, not a new ID space), the
// generation that last confirmed it, and its confirmation status. The
// historical bucket exists so an unrestated issue stays recoverable without
// reading as a current blocker.
type checkpointOpenIssue struct {
	ID     string `json:"id,omitempty"`
	Text   string `json:"text"`
	Source int    `json:"source,omitempty"`
	Status string `json:"status,omitempty"`
}

type checkpointClaim struct {
	Kind         string   `json:"kind,omitempty"`
	EvidenceRefs []string `json:"evidence_refs,omitempty"`
	Status       string   `json:"status,omitempty"`
}

// typedStateFromArgs extracts the machine-carryable subset of a fresh
// compact_context submission.
func typedStateFromArgs(args tools.CompactContextArgs) checkpointTypedState {
	return checkpointTypedState{
		Completed:          append([]string(nil), args.Completed...),
		Decisions:          append([]string(nil), args.Decisions...),
		OpenIssues:         append([]string(nil), args.OpenIssues...),
		OpenIssuesComplete: args.OpenIssuesComplete,
		EvidenceRefs:       append([]string(nil), args.EvidenceRefs...),
		StageID:            args.StageID,
		StageStatus:        args.StageStatus,
		Kind:               args.CheckpointKind,
		Claims:             typedClaimsFromArgs(args),
	}
}

func typedClaimsFromArgs(args tools.CompactContextArgs) map[string]checkpointClaim {
	if len(args.ClaimKinds) == 0 && len(args.ClaimEvidence) == 0 {
		return nil
	}
	out := make(map[string]checkpointClaim, len(args.ClaimKinds)+len(args.ClaimEvidence))
	for claim, kind := range args.ClaimKinds {
		out[claim] = checkpointClaim{Kind: kind, EvidenceRefs: append([]string(nil), args.ClaimEvidence[claim]...), Status: typedClaimStatusActive}
	}
	for claim, refs := range args.ClaimEvidence {
		item := out[claim]
		item.EvidenceRefs = append([]string(nil), refs...)
		if item.Status == "" {
			item.Status = typedClaimStatusActive
		}
		out[claim] = item
	}
	return out
}

// typedStateSectionRanges returns the byte ranges of every standalone typed
// state heading in body. A section is located only by a full heading line at
// column zero (the same rule markdownSectionBounds uses for summary
// sections), so body text that merely quotes the heading mid-line can never
// be mistaken for the machine block; prose that happens to render the exact
// heading as its own line is only a candidate, and a candidate whose content
// is not a JSON document is skipped by the parsers below.
type typedSectionRange struct {
	headingStart int
	contentStart int
	contentEnd   int
}

func typedStateSectionRanges(body string) []typedSectionRange {
	if body == "" {
		return nil
	}
	var out []typedSectionRange
	search := 0
	for {
		rel := findMarkdownHeadingLine(body[search:], typedStateSectionHeading)
		if rel < 0 {
			return out
		}
		headingStart := search + rel
		contentStart := headingStart + len(typedStateSectionHeading)
		contentEnd := len(body)
		if loc := compactionMarkdownHeadingLineRe.FindStringIndex(body[contentStart:]); loc != nil {
			contentEnd = contentStart + loc[0]
		}
		out = append(out, typedSectionRange{headingStart: headingStart, contentStart: contentStart, contentEnd: contentEnd})
		search = contentStart
	}
}

// typedStateJSONLine returns the machine JSON payload of a typed-state
// section: the first non-empty line of the section body with the renderer's
// "- " bullet stripped. When the section's first content line is not a JSON
// document — a prose paragraph impersonating the heading, or a dangling
// heading with no payload — "" is returned so callers never carry or parse a
// non-machine block as typed state. Unknown JSON fields (for example a key a
// future version adds) are ignored by the caller's decoder; the section only
// has to be a JSON document to be treated as machine state.
func typedStateJSONLine(section string) string {
	for candidate := range strings.SplitSeq(section, "\n") {
		line := strings.TrimSpace(candidate)
		if line == "" {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "-"))
		if line == "" || !json.Valid([]byte(line)) {
			return ""
		}
		return line
	}
	return ""
}

// typedStateFromBody extracts the typed state block of a checkpoint body,
// distinguishing "no typed block" (found=false) from "a typed block exists but
// does not parse" (malformed=true) so a caller can disclose an unreadable
// carry instead of silently treating it as absent. Every standalone typed
// heading is a candidate; the first candidate whose payload is a valid JSON
// document wins, so a prose line quoting the heading ahead of the real block
// can no longer shadow it. A body whose only candidates do not parse reports
// malformed.
func typedStateFromBody(body string) (state checkpointTypedState, found bool, malformed bool) {
	ranges := typedStateSectionRanges(body)
	broken := false
	for _, r := range ranges {
		line := typedStateJSONLine(body[r.contentStart:r.contentEnd])
		if line == "" {
			broken = true
			continue
		}
		var decoded struct {
			Completed          []string                   `json:"completed"`
			Decisions          []string                   `json:"decisions"`
			OpenIssues         []string                   `json:"open_issues"`
			OpenIssuesComplete bool                       `json:"open_issues_complete"`
			OpenIssueIDs       map[string]string          `json:"open_issue_ids"`
			CarriedOpenIssues  []checkpointOpenIssue      `json:"carried_open_issues"`
			EvidenceRefs       []string                   `json:"evidence_refs"`
			StageID            string                     `json:"stage_id"`
			StageStatus        string                     `json:"stage_status"`
			Kind               string                     `json:"checkpoint_kind"`
			Generation         int                        `json:"generation"`
			Claims             map[string]checkpointClaim `json:"claims"`
		}
		if json.Unmarshal([]byte(line), &decoded) != nil {
			broken = true
			continue
		}
		return checkpointTypedState{
			Completed:          decoded.Completed,
			Decisions:          decoded.Decisions,
			OpenIssues:         decoded.OpenIssues,
			OpenIssuesComplete: decoded.OpenIssuesComplete,
			OpenIssueIDs:       decoded.OpenIssueIDs,
			EvidenceRefs:       decoded.EvidenceRefs,
			StageID:            strings.TrimSpace(decoded.StageID),
			StageStatus:        strings.TrimSpace(decoded.StageStatus),
			Kind:               strings.TrimSpace(decoded.Kind),
			// A block written before the current/historical split declares no
			// carried set and no generation: it decodes as generation 0 with
			// an empty historical bucket, so its open issues read as the
			// current generation's and only the next merge demotes the ones
			// the fresh submission does not restate. An old block is therefore
			// never silently emptied, and never mistaken for a chain that had
			// already demoted its carried entries.
			CarriedOpenIssues: normalizeCarriedOpenIssues(decoded.CarriedOpenIssues),
			Generation:        max(decoded.Generation, 0),
			Claims:            decoded.Claims,
		}, true, false
	}
	if broken {
		return checkpointTypedState{}, true, true
	}
	return checkpointTypedState{}, false, false
}

// renderTypedStateJSON renders the typed state block as a single JSON line.
// Carried items over the per-item cap are truncated with an explicit marker;
// the JSON stays parseable by typedStateFromBody.
func renderTypedStateJSON(state checkpointTypedState) string {
	payload := struct {
		Completed          []string                   `json:"completed,omitempty"`
		Decisions          []string                   `json:"decisions,omitempty"`
		OpenIssues         []string                   `json:"open_issues,omitempty"`
		OpenIssuesComplete bool                       `json:"open_issues_complete,omitempty"`
		OpenIssueIDs       map[string]string          `json:"open_issue_ids,omitempty"`
		CarriedOpenIssues  []checkpointOpenIssue      `json:"carried_open_issues,omitempty"`
		EvidenceRefs       []string                   `json:"evidence_refs,omitempty"`
		StageID            string                     `json:"stage_id,omitempty"`
		StageStatus        string                     `json:"stage_status,omitempty"`
		Kind               string                     `json:"checkpoint_kind,omitempty"`
		Generation         int                        `json:"generation,omitempty"`
		Claims             map[string]checkpointClaim `json:"claims,omitempty"`
	}{
		Completed:          boundTypedStateItems(state.Completed),
		Decisions:          boundTypedStateItems(state.Decisions),
		OpenIssues:         boundTypedStateItems(state.OpenIssues),
		OpenIssuesComplete: state.OpenIssuesComplete,
		OpenIssueIDs:       openIssueIDsForRendered(state.OpenIssues, state.OpenIssueIDs),
		CarriedOpenIssues:  normalizeCarriedOpenIssues(state.CarriedOpenIssues),
		EvidenceRefs:       boundTypedStateItems(state.EvidenceRefs),
		StageID:            state.StageID,
		StageStatus:        state.StageStatus,
		Kind:               state.Kind,
		Generation:         state.Generation,
		Claims:             state.Claims,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return "- (unavailable)"
	}
	return "- " + string(data)
}

// boundTypedStateItems caps a single item's length, truncating at a rune
// boundary with an explicit marker so a truncated item is never mistaken for
// a complete one.
func boundTypedStateItems(items []string) []string {
	if len(items) == 0 {
		return nil
	}
	out := make([]string, len(items))
	for i, item := range items {
		item = strings.TrimSpace(item)
		if runeLen(item) > typedStateCarryMaxItemRunes {
			item = truncateRunes(item, typedStateCarryMaxItemRunes) + typedStateItemTruncatedSuffix
		}
		out[i] = item
	}
	return out
}

func runeLen(s string) int {
	n := 0
	for range s {
		n++
	}
	return n
}

// truncateRunes returns the longest prefix of s with at most n runes. When
// n is large enough the whole string is returned unchanged, so truncating an
// already-fitting item never allocates.
func truncateRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if runeLen(s) <= n {
		return s
	}
	var b strings.Builder
	b.Grow(n)
	count := 0
	for _, r := range s {
		if count >= n {
			break
		}
		b.WriteRune(r)
		count++
	}
	return b.String()
}

// openIssueKey is the stable lexical identity of an open issue inside the
// open-issue bucket: surrounding whitespace removed, internal whitespace runs
// collapsed to one space, and case folded. The model re-spaces and re-cases an
// issue when it restates or retires it, and the restatement has to be
// recognized as the same issue — otherwise the earlier wording survives as a
// second, historical entry that reads like a distinct unresolved risk, and a
// retirement spelled differently would leave the entry the model believes it
// removed.
//
// This is lexical normalization, not a semantic matcher: two issues that
// differ in wording stay distinct, exactly as checkpointItemKey keeps
// completed work and decisions distinct. The other carried lists keep that
// exact-after-trim identity (a claim is a map key, and the decisions/completed
// retirement contract is unchanged).
//
// The normalization is a single scan that returns the input unchanged when it
// is already in normalized form — the common case — because this runs once per
// open issue on every checkpoint render: strings.Fields/ToLower/Join would
// allocate a slice and a string per call on that path.
func openIssueKey(item string) string {
	item = strings.TrimSpace(item)
	if item == "" || openIssueKeyNormalized(item) {
		return item
	}
	var b strings.Builder
	b.Grow(len(item))
	space := false
	for _, r := range item {
		if unicode.IsSpace(r) {
			if space {
				continue
			}
			space = true
			b.WriteByte(' ')
			continue
		}
		space = false
		b.WriteRune(unicode.ToLower(r))
	}
	return strings.TrimSpace(b.String())
}

// openIssueKeyNormalized reports whether s already has the openIssueKey form:
// no whitespace run longer than a single space, and no rune that case folding
// would change. s is expected to be trimmed already, so a leading or trailing
// space cannot occur.
func openIssueKeyNormalized(s string) bool {
	space := false
	for _, r := range s {
		if unicode.IsSpace(r) {
			if space || r != ' ' {
				return false
			}
			space = true
			continue
		}
		space = false
		if unicode.ToLower(r) != r {
			return false
		}
	}
	return true
}

// normalizeCarriedOpenIssues prepares a historical open-issue set for storage
// or rendering: each entry's text is trimmed and an over-long text is
// truncated at a rune boundary with the shared marker, a missing status
// defaults to unconfirmed (the only posture the bucket holds), and empty
// entries are dropped. The result is nil when nothing survives, so the JSON
// field is omitted entirely rather than rendered as an empty list.
func normalizeCarriedOpenIssues(items []checkpointOpenIssue) []checkpointOpenIssue {
	if len(items) == 0 {
		return nil
	}
	out := make([]checkpointOpenIssue, 0, len(items))
	for _, item := range items {
		text := strings.TrimSpace(item.Text)
		if text == "" {
			continue
		}
		// Derive the stable identity before truncating the display text. The
		// full lexical identity is needed for a later retired_items entry to
		// retire the original long issue.
		if item.ID == "" {
			item.ID = openIssueID(text)
		}
		if runeLen(text) > typedStateCarryMaxItemRunes {
			text = truncateRunes(text, typedStateCarryMaxItemRunes) + typedStateItemTruncatedSuffix
		}
		item.Text = text
		if item.Status == "" {
			item.Status = typedIssueStatusUnconfirmed
		}
		out = append(out, item)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// openIssueID is a compact stable identity for an issue's normalized text.
// It keeps the full identity available after display truncation without
// copying an arbitrarily long issue into every typed checkpoint.
func openIssueID(item string) string {
	key := openIssueKey(item)
	if key == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:16])
}

// openIssueIDsForRendered preserves identities for current issues whose
// display text is truncated by the normal typed-state item bound. Keys are
// the rendered text so the parser can recover the identity on the next carry.
func openIssueIDsForRendered(items []string, ids map[string]string) map[string]string {
	if len(items) == 0 {
		return nil
	}
	if ids == nil {
		needsIdentity := false
		for _, item := range items {
			if runeLen(strings.TrimSpace(item)) > typedStateCarryMaxItemRunes {
				needsIdentity = true
				break
			}
		}
		if !needsIdentity {
			return nil
		}
	}
	rendered := boundTypedStateItems(items)
	var out map[string]string
	for i, item := range items {
		key := openIssueKey(item)
		if key == "" {
			continue
		}
		id := ""
		if ids != nil {
			id = ids[key]
		}
		if id == "" {
			id = openIssueID(item)
		}
		if id != "" && (rendered[i] != item || id != openIssueID(item)) {
			if out == nil {
				out = make(map[string]string, len(items))
			}
			out[rendered[i]] = id
		}
	}
	return out
}

func checkpointOpenIssueID(state checkpointTypedState, item string) string {
	if state.OpenIssueIDs != nil {
		if id := state.OpenIssueIDs[item]; id != "" {
			return id
		}
		if id := state.OpenIssueIDs[openIssueKey(item)]; id != "" {
			return id
		}
	}
	return openIssueID(item)
}

// mergeCheckpointTypedStates merges the carried state of the previous
// checkpoint with a fresh submission. The fresh submission wins: its items
// come first and fill the cap, and its stage metadata overrides the carried
// one (the model re-declares the current stage on every submission; a
// carried completed/committed stage is never inherited on an empty restate —
// see the merge body below). Carried items fill the remaining list capacity
// oldest-last, and the entries that do
// not fit are dropped with a disclosed omission count instead of silently
// growing the list without bound. The returned lists are item-bounded; the
// readable sections may render fewer of them after the display-side
// de-duplication against completed work, while the machine typed block keeps
// the full carried lists because it is the only channel that hands retained
// state to the next checkpoint.
//
// Open issues are split only when the fresh submission explicitly marks its
// list complete; otherwise the incremental compact_context contract keeps
// omitted current issues active (see mergeTypedStateOpenIssues).
// openIssuesOmitted is the part of omitted that the shared open-issue budget
// dropped, so the renderer can disclose it inside the ## Open Problems section
// rather than only under the decisions it also bounds.
func mergeCheckpointTypedStates(prior, current checkpointTypedState) (merged checkpointTypedState, omitted int, claimsOmitted int, openIssuesOmitted int) {
	var dropped int
	merged.Completed, dropped = mergeTypedStateList(prior.Completed, current.Completed, typedStateCarryMaxCompleted)
	omitted += dropped
	merged.Completed = boundTypedStateItems(merged.Completed)
	merged.Decisions, dropped = mergeTypedStateList(prior.Decisions, current.Decisions, typedStateCarryMaxDecisions)
	omitted += dropped
	// Open issues do not merge flat: the current generation's own issues and
	// the historical ones it did not restate share one budget but stay in
	// separate sets, so a carried-only entry can never render as a current
	// blocker. See mergeTypedStateOpenIssues.
	merged.OpenIssues, merged.CarriedOpenIssues, openIssuesOmitted = mergeTypedStateOpenIssues(prior, current, typedStateCarryMaxOpenIssues)
	omitted += openIssuesOmitted
	merged.EvidenceRefs, dropped = mergeTypedStateList(prior.EvidenceRefs, current.EvidenceRefs, typedStateCarryMaxEvidenceRefs)
	omitted += dropped
	merged.Decisions = boundTypedStateItems(merged.Decisions)
	merged.EvidenceRefs = boundTypedStateItems(merged.EvidenceRefs)
	merged.Claims, claimsOmitted = mergeTypedClaims(prior.Claims, current.Claims)
	// The carry ordinal advances once per merge, so every entry the historical
	// bucket records keeps the generation that last confirmed it and a
	// demotion is observable as a growing distance from the current one.
	merged.Generation = prior.Generation + 1
	// Stage metadata is re-declared by the model on every submission and a
	// fresh declaration always wins. When the fresh submission declares
	// nothing, an in-flight carried stage (anything but a completed one) is
	// kept so an empty restate does not lose the still-current stage. A
	// carried completed/committed stage is deliberately NOT inherited:
	// completed carries terminal, acceptance-evidence semantics that only the
	// armed fresh submission can declare — it alone ran the
	// committed-evidence validation — and re-rendering a previous
	// generation's completed stage as the current one would read an
	// already-finished stage as freshly committed. The carried decisions and
	// claims still record the completed work as history.
	carryStage := prior.StageStatus != stageStatusCompleted && prior.Kind != checkpointKindCommitted
	merged.StageID = current.StageID
	if merged.StageID == "" && carryStage {
		merged.StageID = prior.StageID
	}
	merged.StageStatus = current.StageStatus
	if merged.StageStatus == "" && carryStage {
		merged.StageStatus = prior.StageStatus
	}
	merged.Kind = current.Kind
	if merged.Kind == "" && carryStage {
		merged.Kind = prior.Kind
	}
	return merged, omitted, claimsOmitted, openIssuesOmitted
}

// mergeTypedStateOpenIssues merges the open issues of a carried state with a
// fresh submission and splits them into the current generation's confirmed
// issues and the historical (carried-only, unconfirmed) ones.
//
// The split is opt-in. When the model supplies a complete snapshot, an issue
// it does not list is moved to the historical bucket so an old entry cannot
// read exactly like a freshly confirmed blocker. Without that declaration,
// the incremental tool contract keeps omitted issues current. The historical
// bucket keeps demoted entries recoverable and clearly labels them as such.
//
// Confirmed issues fill the budget first — the fresh submission's own
// declaration always wins — then the historical ones newest-first: an issue
// the prior generation confirmed but this one did not restate, then the older
// carried set. Entries past the shared cap are dropped and counted, so the
// renderer discloses the omission instead of presenting a bounded list as
// complete. Identity is openIssueKey, so a restatement the model re-spaced or
// re-cased moves the entry into the confirmed set instead of duplicating it.
func mergeTypedStateOpenIssues(prior, current checkpointTypedState, cap int) (confirmed []string, carried []checkpointOpenIssue, omitted int) {
	if cap <= 0 {
		return nil, nil, len(current.OpenIssues) + len(prior.OpenIssues) + len(prior.CarriedOpenIssues)
	}
	confirmed = make([]string, 0, min(cap, len(current.OpenIssues)))
	seen := make(map[string]struct{}, min(cap, len(current.OpenIssues)+len(prior.OpenIssues)+len(prior.CarriedOpenIssues)))
	// add records one candidate under its normalized identity and reports
	// whether the caller should keep it. A duplicate is dropped silently (it
	// is the same issue, not a lost one); an entry past the shared cap is
	// counted as omitted.
	addWithID := func(item, identity string) bool {
		key := openIssueKey(item)
		if key == "" {
			return false
		}
		if identity != "" {
			key = identity
		}
		if _, dup := seen[key]; dup {
			return false
		}
		if len(confirmed)+len(carried) >= cap {
			omitted++
			return false
		}
		seen[key] = struct{}{}
		return true
	}
	add := func(item string) bool { return addWithID(item, openIssueID(item)) }
	for _, item := range current.OpenIssues {
		if add(item) {
			confirmed = append(confirmed, item)
		}
	}
	if current.OpenIssuesComplete {
		for _, item := range prior.OpenIssues {
			if addWithID(item, checkpointOpenIssueID(prior, item)) {
				carried = append(carried, checkpointOpenIssue{
					ID:     checkpointOpenIssueID(prior, item),
					Text:   item,
					Source: prior.Generation,
					Status: typedIssueStatusUnconfirmed,
				})
			}
		}
	} else {
		// The compact_context contract is incremental by default: omission
		// does not mean resolution. Preserve prior current issues as current
		// unless the model explicitly supplied a complete snapshot.
		priorCurrent := append([]string(nil), prior.OpenIssues...)
		for _, item := range priorCurrent {
			if addWithID(item, checkpointOpenIssueID(prior, item)) {
				confirmed = append(confirmed, item)
			}
		}
	}
	for _, item := range prior.CarriedOpenIssues {
		if addWithID(item.Text, item.ID) {
			carried = append(carried, checkpointOpenIssue{
				ID:     item.ID,
				Text:   item.Text,
				Source: item.Source,
				Status: typedIssueStatusUnconfirmed,
			})
		}
	}
	return boundTypedStateItems(confirmed), normalizeCarriedOpenIssues(carried), omitted
}

func checkpointItemKey(item string) string {
	return strings.TrimSpace(item)
}

// mergeTypedClaims merges a carried claim set with a fresh submission. A
// claim the fresh submission restates is replaced wholesale by the fresh
// identity (fresh claims always win); a carried-only claim keeps its kind and
// evidence association but loses the trusted current-generation posture — a
// claim the model did not re-assert cannot keep reading as an active claim of
// this checkpoint, so its status is demoted from active to stale until a
// later generation restates it. Invalidated and superseded are already
// low-trust postures and stay as they are.
//
// The merged set is bounded at typedStateCarryMaxClaims. Fresh submissions
// already fit the validator cap, so the bound only ever evicts carried-only
// claims; evictions are returned as dropped so the renderer can disclose them
// instead of silently presenting a bounded set as complete. Key order is
// deterministic (sorted), so the surviving set cannot depend on map iteration
// order.
func mergeTypedClaims(prior, current map[string]checkpointClaim) (merged map[string]checkpointClaim, dropped int) {
	if len(prior) == 0 && len(current) == 0 {
		return nil, 0
	}
	out := make(map[string]checkpointClaim, min(typedStateCarryMaxClaims, len(prior)+len(current)))
	for _, claim := range sortedCheckpointClaimKeys(current) {
		item := current[claim]
		if item.Status == "" {
			item.Status = typedClaimStatusActive
		}
		if len(out) >= typedStateCarryMaxClaims {
			dropped++
			continue
		}
		out[claim] = item
	}
	for _, claim := range sortedCheckpointClaimKeys(prior) {
		if _, restated := out[claim]; restated {
			continue
		}
		item := prior[claim]
		if item.Status == typedClaimStatusActive {
			item.Status = typedClaimStatusStale
		}
		if len(out) >= typedStateCarryMaxClaims {
			dropped++
			continue
		}
		out[claim] = item
	}
	return out, dropped
}

// sortedCheckpointClaimKeys returns the keys of a claim set sorted, giving
// mergeTypedClaims a deterministic eviction order.
func sortedCheckpointClaimKeys(claims map[string]checkpointClaim) []string {
	if len(claims) == 0 {
		return nil
	}
	keys := make([]string, 0, len(claims))
	for claim := range claims {
		keys = append(keys, claim)
	}
	slices.Sort(keys)
	return keys
}

// mergeTypedStateList merges a carried list with a fresh submission, newest
// first, up to the list cap. The fresh submission's items come first; carried
// items fill the remaining slots oldest-last, and entries that do not fit are
// dropped with a disclosed omission count. Items with identical text are
// deduplicated across both lists (the fresh copy wins the slot): recursive
// checkpoints commonly restate a carried decision verbatim, and counting the
// duplicate as a fresh slot would halve the effective capacity of the merged
// list and force distinct carried items out of the bounded carry.
func mergeTypedStateList(prior, current []string, cap int) ([]string, int) {
	if cap <= 0 {
		return nil, len(prior) + len(current)
	}
	out := make([]string, 0, min(cap, len(current)+len(prior)))
	seen := make(map[string]struct{}, min(cap, len(current)+len(prior)))
	omitted := 0
	addUnique := func(items []string) {
		for _, item := range items {
			key := checkpointItemKey(item)
			if _, dup := seen[key]; dup {
				continue
			}
			if len(out) >= cap {
				omitted++
				continue
			}
			seen[key] = struct{}{}
			out = append(out, item)
		}
	}
	addUnique(current)
	addUnique(prior)
	return out, omitted
}
