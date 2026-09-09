package agent

import (
	"encoding/json"
	"slices"
	"strings"

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
// as natural-language Markdown: the model re-states its current objective and
// progress each round, and narrative that a later checkpoint did not restate
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
	// The compact_context tool schema declares the same enums in
	// internal/tools/compact_context.go; tools cannot import the agent
	// package, so the agent mirrors the values it enforces here instead of
	// repeating the literals at each check site.
	claimKindObserved       = "observed"
	checkpointKindCommitted = "committed"
	stageStatusCompleted    = "completed"
)

// checkpointTypedState is the machine-carryable task state a checkpoint
// carries across generations. It is exactly the state with a durable,
// cross-generation meaning; everything else the checkpoint renders (current
// user request, active objective, progress, claims) is re-stated by the model
// on every submission.
type checkpointTypedState struct {
	Decisions    []string
	OpenIssues   []string
	EvidenceRefs []string
	StageID      string
	StageStatus  string
	Kind         string
	Claims       map[string]checkpointClaim
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
		Decisions:    append([]string(nil), args.Decisions...),
		OpenIssues:   append([]string(nil), args.OpenIssues...),
		EvidenceRefs: append([]string(nil), args.EvidenceRefs...),
		StageID:      args.StageID,
		StageStatus:  args.StageStatus,
		Kind:         args.CheckpointKind,
		Claims:       typedClaimsFromArgs(args),
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

// parseCheckpointTypedState reads the typed state block out of a checkpoint
// body. It returns ok=false when the body carries no typed state (a
// usage-driven summary, an old checkpoint, or a checkpoint that predates the
// block). Unknown JSON fields — including the legacy "constraints" key, which
// was declared but never populated — are ignored. A block that is present but
// unparseable also reports ok=false; callers that must disclose that case use
// typedStateFromBody.
func parseCheckpointTypedState(body string) (checkpointTypedState, bool) {
	state, found, malformed := typedStateFromBody(body)
	if !found || malformed {
		return checkpointTypedState{}, false
	}
	return state, true
}

// typedStateFromBody extracts the typed state block of a checkpoint body,
// distinguishing "no typed block" (found=false) from "a typed block exists but
// does not parse" (malformed=true) so a caller can disclose an unreadable
// carry instead of silently treating it as absent.
func typedStateFromBody(body string) (state checkpointTypedState, found bool, malformed bool) {
	if body == "" {
		return checkpointTypedState{}, false, false
	}
	idx := strings.Index(body, typedStateSectionHeading)
	if idx < 0 {
		return checkpointTypedState{}, false, false
	}
	rest := strings.TrimSpace(body[idx+len(typedStateSectionHeading):])
	line := strings.TrimSpace(strings.SplitN(rest, "\n", 2)[0])
	line = strings.TrimSpace(strings.TrimPrefix(line, "-"))
	var decoded struct {
		Decisions    []string                   `json:"decisions"`
		OpenIssues   []string                   `json:"open_issues"`
		EvidenceRefs []string                   `json:"evidence_refs"`
		StageID      string                     `json:"stage_id"`
		StageStatus  string                     `json:"stage_status"`
		Kind         string                     `json:"checkpoint_kind"`
		Claims       map[string]checkpointClaim `json:"claims"`
	}
	if json.Unmarshal([]byte(line), &decoded) != nil {
		return checkpointTypedState{}, true, true
	}
	return checkpointTypedState{
		Decisions:    decoded.Decisions,
		OpenIssues:   decoded.OpenIssues,
		EvidenceRefs: decoded.EvidenceRefs,
		StageID:      strings.TrimSpace(decoded.StageID),
		StageStatus:  strings.TrimSpace(decoded.StageStatus),
		Kind:         strings.TrimSpace(decoded.Kind),
		Claims:       decoded.Claims,
	}, true, false
}

// renderTypedStateJSON renders the typed state block as a single JSON line.
// Carried items over the per-item cap are truncated with an explicit marker;
// the JSON stays parseable by parseCheckpointTypedState.
func renderTypedStateJSON(state checkpointTypedState) string {
	payload := struct {
		Decisions    []string                   `json:"decisions,omitempty"`
		OpenIssues   []string                   `json:"open_issues,omitempty"`
		EvidenceRefs []string                   `json:"evidence_refs,omitempty"`
		StageID      string                     `json:"stage_id,omitempty"`
		StageStatus  string                     `json:"stage_status,omitempty"`
		Kind         string                     `json:"checkpoint_kind,omitempty"`
		Claims       map[string]checkpointClaim `json:"claims,omitempty"`
	}{
		Decisions:    boundTypedStateItems(state.Decisions),
		OpenIssues:   boundTypedStateItems(state.OpenIssues),
		EvidenceRefs: boundTypedStateItems(state.EvidenceRefs),
		StageID:      state.StageID,
		StageStatus:  state.StageStatus,
		Kind:         state.Kind,
		Claims:       state.Claims,
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

// mergeCheckpointTypedStates merges the carried state of the previous
// checkpoint with a fresh submission. The fresh submission wins: its items
// come first and fill the cap, and its stage metadata overrides the carried
// one (the model re-declares the current stage on every submission; a
// carried completed/committed stage is never inherited on an empty restate —
// see the merge body below). Carried items fill the remaining list capacity
// oldest-last, and the entries that do
// not fit are dropped with a disclosed omission count instead of silently
// growing the list without bound. The returned lists are item-bounded, so the
// readable sections and the machine typed block carry exactly the same text.
func mergeCheckpointTypedStates(prior, current checkpointTypedState) (merged checkpointTypedState, omitted int, claimsOmitted int) {
	var dropped int
	merged.Decisions, dropped = mergeTypedStateList(prior.Decisions, current.Decisions, typedStateCarryMaxDecisions)
	omitted += dropped
	merged.OpenIssues, dropped = mergeTypedStateList(prior.OpenIssues, current.OpenIssues, typedStateCarryMaxOpenIssues)
	omitted += dropped
	merged.EvidenceRefs, dropped = mergeTypedStateList(prior.EvidenceRefs, current.EvidenceRefs, typedStateCarryMaxEvidenceRefs)
	omitted += dropped
	merged.Decisions = boundTypedStateItems(merged.Decisions)
	merged.OpenIssues = boundTypedStateItems(merged.OpenIssues)
	merged.EvidenceRefs = boundTypedStateItems(merged.EvidenceRefs)
	merged.Claims, claimsOmitted = mergeTypedClaims(prior.Claims, current.Claims)
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
	return merged, omitted, claimsOmitted
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
			key := strings.TrimSpace(item)
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
