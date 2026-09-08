package agent

import (
	"encoding/json"
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
	}
}

// parseCheckpointTypedState reads the typed state block out of a checkpoint
// body. It returns ok=false when the body carries no typed state (a
// usage-driven summary, an old checkpoint, or a checkpoint that predates the
// block). Unknown JSON fields — including the legacy "constraints" key, which
// was declared but never populated — are ignored.
func parseCheckpointTypedState(body string) (checkpointTypedState, bool) {
	if body == "" {
		return checkpointTypedState{}, false
	}
	idx := strings.Index(body, typedStateSectionHeading)
	if idx < 0 {
		return checkpointTypedState{}, false
	}
	rest := strings.TrimSpace(body[idx+len(typedStateSectionHeading):])
	line := strings.TrimSpace(strings.SplitN(rest, "\n", 2)[0])
	line = strings.TrimSpace(strings.TrimPrefix(line, "-"))
	var state struct {
		Decisions    []string `json:"decisions"`
		OpenIssues   []string `json:"open_issues"`
		EvidenceRefs []string `json:"evidence_refs"`
		StageID      string   `json:"stage_id"`
		StageStatus  string   `json:"stage_status"`
		Kind         string   `json:"checkpoint_kind"`
	}
	if json.Unmarshal([]byte(line), &state) != nil {
		return checkpointTypedState{}, false
	}
	return checkpointTypedState{
		Decisions:    state.Decisions,
		OpenIssues:   state.OpenIssues,
		EvidenceRefs: state.EvidenceRefs,
		StageID:      strings.TrimSpace(state.StageID),
		StageStatus:  strings.TrimSpace(state.StageStatus),
		Kind:         strings.TrimSpace(state.Kind),
	}, true
}

// renderTypedStateJSON renders the typed state block as a single JSON line.
// Carried items over the per-item cap are truncated with an explicit marker;
// the JSON stays parseable by parseCheckpointTypedState.
func renderTypedStateJSON(state checkpointTypedState) string {
	payload := struct {
		Decisions    []string `json:"decisions,omitempty"`
		OpenIssues   []string `json:"open_issues,omitempty"`
		EvidenceRefs []string `json:"evidence_refs,omitempty"`
		StageID      string   `json:"stage_id,omitempty"`
		StageStatus  string   `json:"stage_status,omitempty"`
		Kind         string   `json:"checkpoint_kind,omitempty"`
	}{
		Decisions:    boundTypedStateItems(state.Decisions),
		OpenIssues:   boundTypedStateItems(state.OpenIssues),
		EvidenceRefs: boundTypedStateItems(state.EvidenceRefs),
		StageID:      state.StageID,
		StageStatus:  state.StageStatus,
		Kind:         state.Kind,
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
// one (the model re-declares the current stage on every submission). Carried
// items fill the remaining list capacity oldest-last, and the entries that do
// not fit are dropped with a disclosed omission count instead of silently
// growing the list without bound. The returned lists are item-bounded, so the
// readable sections and the machine typed block carry exactly the same text.
func mergeCheckpointTypedStates(prior, current checkpointTypedState) (merged checkpointTypedState, omitted int) {
	merged.Decisions, omitted = mergeTypedStateList(prior.Decisions, current.Decisions, typedStateCarryMaxDecisions)
	merged.OpenIssues, _ = mergeTypedStateList(prior.OpenIssues, current.OpenIssues, typedStateCarryMaxOpenIssues)
	merged.EvidenceRefs, _ = mergeTypedStateList(prior.EvidenceRefs, current.EvidenceRefs, typedStateCarryMaxEvidenceRefs)
	merged.Decisions = boundTypedStateItems(merged.Decisions)
	merged.OpenIssues = boundTypedStateItems(merged.OpenIssues)
	merged.EvidenceRefs = boundTypedStateItems(merged.EvidenceRefs)
	merged.StageID = current.StageID
	if merged.StageID == "" {
		merged.StageID = prior.StageID
	}
	merged.StageStatus = current.StageStatus
	if merged.StageStatus == "" {
		merged.StageStatus = prior.StageStatus
	}
	merged.Kind = current.Kind
	if merged.Kind == "" {
		merged.Kind = prior.Kind
	}
	return merged, omitted
}

func mergeTypedStateList(prior, current []string, cap int) ([]string, int) {
	capacity := cap
	if capacity <= 0 {
		return nil, len(prior) + len(current)
	}
	out := make([]string, 0, min(capacity, len(current)+len(prior)))
	out = append(out, current...)
	omitted := 0
	for _, item := range prior {
		if len(out) >= capacity {
			omitted++
			continue
		}
		out = append(out, item)
	}
	return out, omitted
}
