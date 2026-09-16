package tools

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/keakon/chord/internal/pathutil"
)

func claimEvidenceText(claims map[string][]string) string {
	var parts []string
	for claim, refs := range claims {
		parts = append(parts, claim+"\n"+strings.Join(refs, ","))
	}
	slices.Sort(parts)
	return strings.Join(parts, "\n")
}

func claimKindsText(kinds map[string]string) string {
	parts := make([]string, 0, len(kinds))
	for claim, kind := range kinds {
		parts = append(parts, claim+"\n"+kind)
	}
	slices.Sort(parts)
	return strings.Join(parts, "\n")
}

// Claim-kind, checkpoint-kind and stage-status vocabulary of compact_context.
// These exported constants are the single definition of the enum values the
// tool schema declares and the MainAgent runtime enforces. The tool package
// cannot import the agent package, so the agent mirrors these values by
// referencing the exported constants instead of repeating the literals.
const (
	CompactContextClaimObserved = "observed"
	CompactContextClaimDerived  = "derived"
	CompactContextClaimAssumed  = "assumed"
	CompactContextClaimProposed = "proposed"

	CompactContextCheckpointKindProvisional = "provisional"
	CompactContextCheckpointKindCommitted   = "committed"

	CompactContextStageActive     = "active"
	CompactContextStageCandidate  = "candidate"
	CompactContextStageCompleted  = "completed"
	CompactContextStageBlocked    = "blocked"
	CompactContextStageSuperseded = "superseded"
)

var (
	compactContextClaimKinds      = []string{CompactContextClaimObserved, CompactContextClaimDerived, CompactContextClaimAssumed, CompactContextClaimProposed}
	compactContextCheckpointKinds = []string{CompactContextCheckpointKindProvisional, CompactContextCheckpointKindCommitted}
	compactContextStageStatuses   = []string{CompactContextStageActive, CompactContextStageCandidate, CompactContextStageCompleted, CompactContextStageBlocked, CompactContextStageSuperseded}
)

// CompactContextArgs is the structured continuation state the model submits
// when requesting a model-driven context checkpoint. Every field is
// model-authored; runtime facts (current user request, todos, subagents,
// anchors, history map) are captured separately at checkpoint build time.
type CompactContextArgs struct {
	ActiveObjective   string              `json:"active_objective"`
	Completed         []string            `json:"completed"`
	Decisions         []string            `json:"decisions"`
	OpenIssues        []string            `json:"open_issues"`
	RetiredItems      []string            `json:"retired_items"`
	NextStep          string              `json:"next_step"`
	StateFiles        []string            `json:"state_files"`
	PlannedStateFiles []string            `json:"planned_state_files"`
	EvidenceRefs      []string            `json:"evidence_refs"`
	ClaimEvidence     map[string][]string `json:"claim_evidence"`
	ClaimKinds        map[string]string   `json:"claim_kinds"`
	StageID           string              `json:"stage_id"`
	StageStatus       string              `json:"stage_status"`
	CheckpointKind    string              `json:"checkpoint_kind"`
}

// TokenEstimator estimates the input-token cost of a string. Defaults to a
// conservative bytes/3 heuristic; the MainAgent runtime overrides it with the
// usage-calibrated estimator so the continuation-state budget uses the same
// accounting convention as other context-pressure decisions.
type TokenEstimator func(text string) int

// The continuation state has no per-field or per-item character caps: a
// dense single item (e.g. one bullet carrying several commit summaries) can
// legitimately exceed a few hundred characters, and rejecting it forces the
// model to rewrite an otherwise valid state on the compaction barrier's
// critical path — a full round trip that costs far more than the few extra
// tokens the item adds. The aggregated estimated-token budget
// (ContinuationStateMaxTokens) is the only length constraint, so the schema
// in Parameters() must stay in sync by declaring no maxLength values.

// CompactContextValidator validates CompactContext tool arguments without
// touching the filesystem or the permission system: state_files resolution is
// pure lexical work (tilde expansion via the environment, Clean/Join/Rel
// against the project root), so this tool can never act as a read-permission
// bypass, an existence probe, or a symlink-resolution oracle.
type CompactContextValidator struct {
	// ContinuationStateMaxTokens caps the estimated token cost of all text
	// fields combined; zero means no cap. It is the only size limit on the
	// state — there are no per-field or per-item caps — and it is budgeted
	// independently of the evidence tier, so a long single field is fine as
	// long as the whole state fits.
	ContinuationStateMaxTokens int
	// EstimateTokens converts text to an estimated token count; nil falls
	// back to len(text)/3.
	EstimateTokens TokenEstimator
	// ProjectRoot returns the absolute project root that state_files
	// spellings resolve against. nil or an empty result keeps the strict
	// subset: only plain workspace-relative entries are accepted. With a
	// root, absolute and "~"/"./"/"../"-prefixed spellings are accepted when
	// they lexically resolve inside it and are normalized to
	// workspace-relative form before storage.
	ProjectRoot func() string
	// TodoWriteVisible reports whether todo_write is part of the same
	// live, model-appropriate tool surface the MainAgent builds for prompts
	// (registered and not denied). When true, Description() adds guidance to
	// sync the todo list with todo_write before requesting a checkpoint:
	// the deterministic checkpoint snapshots runtime todos verbatim, and a
	// list that has drifted behind actual progress misleads the
	// continuation after the reset. It is baked at registration time rather
	// than queried live so the tool description stays stable within a
	// session — the description participates in the tool-surface hash and
	// frozen prompt prefix, so a live value would churn both.
	TodoWriteVisible bool
}

func (v CompactContextValidator) estimateTokens(text string) int {
	if v.EstimateTokens != nil {
		return v.EstimateTokens(text)
	}
	return len(text) / 3
}

// ParseCompactContextArgs trims all strings, rejects empty required fields,
// empty list items and arrays over their declared limits, validates
// state-file constraints lexically, and rejects state whose combined
// estimated token cost exceeds the configured budget. It never stats, reads,
// or resolves symlinks. The generic schema validator only enforces types and
// minItems, so the model-facing limits live here; both Execute and the
// MainAgent runtime barrier run this same parser.
func (v CompactContextValidator) ParseCompactContextArgs(raw json.RawMessage) (CompactContextArgs, error) {
	var args CompactContextArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return CompactContextArgs{}, fmt.Errorf("invalid arguments: %w", err)
	}
	args.ActiveObjective = strings.TrimSpace(args.ActiveObjective)
	args.NextStep = strings.TrimSpace(args.NextStep)
	args.StageID = strings.TrimSpace(args.StageID)
	args.StageStatus = strings.TrimSpace(args.StageStatus)
	args.CheckpointKind = strings.TrimSpace(args.CheckpointKind)
	if args.ActiveObjective == "" {
		return CompactContextArgs{}, fmt.Errorf("missing required argument: active_objective")
	}
	if args.NextStep == "" {
		return CompactContextArgs{}, fmt.Errorf("missing required argument: next_step")
	}
	if args.StageStatus != "" && !slices.Contains(compactContextStageStatuses, args.StageStatus) {
		return CompactContextArgs{}, fmt.Errorf("invalid stage_status %q", args.StageStatus)
	}
	if args.CheckpointKind != "" && !slices.Contains(compactContextCheckpointKinds, args.CheckpointKind) {
		return CompactContextArgs{}, fmt.Errorf("invalid checkpoint_kind %q", args.CheckpointKind)
	}
	var err error
	if args.Completed, err = validateCompactContextList(args.Completed, 12, "completed"); err != nil {
		return CompactContextArgs{}, err
	}
	if args.Decisions, err = validateCompactContextList(args.Decisions, 8, "decisions"); err != nil {
		return CompactContextArgs{}, err
	}
	if args.OpenIssues, err = validateCompactContextList(args.OpenIssues, 8, "open_issues"); err != nil {
		return CompactContextArgs{}, err
	}
	if args.RetiredItems, err = validateCompactContextList(args.RetiredItems, 40, "retired_items"); err != nil {
		return CompactContextArgs{}, err
	}
	stateFiles, err := validateStateFiles(args.StateFiles, 16, v.currentProjectRoot())
	if err != nil {
		return CompactContextArgs{}, err
	}
	args.StateFiles = stateFiles
	plannedStateFiles, err := validateStateFiles(args.PlannedStateFiles, 16, v.currentProjectRoot())
	if err != nil {
		return CompactContextArgs{}, fmt.Errorf("validate planned_state_files: %w", err)
	}
	args.PlannedStateFiles = plannedStateFiles
	args.PlannedStateFiles = removeRegisteredStateFiles(args.PlannedStateFiles, args.StateFiles)
	if args.EvidenceRefs, err = validateCompactContextList(args.EvidenceRefs, 24, "evidence_refs"); err != nil {
		return CompactContextArgs{}, err
	}
	// Claim keys are natural-language assertions, not indices into
	// completed/decisions: the model may paraphrase an entry instead of
	// copying it verbatim, and downstream render/carry treat claim text as a
	// standalone key. Evidence-ID validity is still enforced in the
	// MainAgent runtime, so anchoring claim text to completed/decisions only
	// added critical-path friction (verbatim-copy failures) without a
	// functional payoff.
	claimEvidence, err := normalizeCompactContextClaims(args.ClaimEvidence, "claim_evidence")
	if err != nil {
		return CompactContextArgs{}, err
	}
	args.ClaimEvidence = claimEvidence
	for claim, refs := range claimEvidence {
		normalized, err := validateCompactContextList(refs, 8, "claim_evidence")
		if err != nil {
			return CompactContextArgs{}, err
		}
		claimEvidence[claim] = normalized
	}
	claimKinds, err := normalizeCompactContextClaims(args.ClaimKinds, "claim_kinds")
	if err != nil {
		return CompactContextArgs{}, err
	}
	args.ClaimKinds = claimKinds
	for claim, kind := range claimKinds {
		if !slices.Contains(compactContextClaimKinds, kind) {
			return CompactContextArgs{}, fmt.Errorf("invalid claim_kinds value %q for %q", kind, claim)
		}
	}
	refs := slices.Clone(args.EvidenceRefs)
	claimKeys := make([]string, 0, len(claimEvidence))
	for claim := range claimEvidence {
		claimKeys = append(claimKeys, claim)
	}
	slices.Sort(claimKeys)
	for _, claim := range claimKeys {
		for _, ref := range claimEvidence[claim] {
			if !slices.Contains(refs, ref) {
				refs = append(refs, ref)
			}
		}
	}
	if args.EvidenceRefs, err = validateCompactContextList(refs, 24, "evidence_refs"); err != nil {
		return CompactContextArgs{}, err
	}

	// The continuation-state budget uses the same usage-calibrated token
	// accounting as other context-pressure decisions. state_files paths are
	// model-authored text too and count against the budget, so a checkpoint's
	// self-description cannot crowd out an entire evidence tier.
	fields := []struct {
		name string
		text string
	}{
		{"active_objective", args.ActiveObjective},
		{"next_step", args.NextStep},
		{"completed", strings.Join(args.Completed, "\n")},
		{"decisions", strings.Join(args.Decisions, "\n")},
		{"open_issues", strings.Join(args.OpenIssues, "\n")},
		{"retired_items", strings.Join(args.RetiredItems, "\n")},
		{"state_files", strings.Join(args.StateFiles, "\n")},
		{"planned_state_files", strings.Join(args.PlannedStateFiles, "\n")},
		{"evidence_refs", strings.Join(args.EvidenceRefs, "\n")},
		{"stage_id", args.StageID},
		{"stage_status", args.StageStatus},
		{"checkpoint_kind", args.CheckpointKind},
		{"claim_evidence", claimEvidenceText(args.ClaimEvidence)},
		{"claim_kinds", claimKindsText(args.ClaimKinds)},
	}
	texts := make([]string, len(fields))
	for i, f := range fields {
		texts[i] = f.text
	}
	aggregated := strings.Join(texts, "\n")
	cost := v.estimateTokens(aggregated)
	if limit := v.ContinuationStateMaxTokens; limit > 0 && cost > limit {
		// Name the heaviest fields. With no per-field cap left, one dense
		// field can consume the whole budget on its own, and a rejection that
		// only lists every field name makes the model shorten the state
		// blindly — usually trimming the fields that were never the problem.
		type fieldCost struct {
			name string
			cost int
		}
		costs := make([]fieldCost, 0, len(fields))
		for _, f := range fields {
			if f.text == "" {
				continue
			}
			costs = append(costs, fieldCost{name: f.name, cost: v.estimateTokens(f.text)})
		}
		// Descending by cost, ties by field order, so the message is stable.
		slices.SortStableFunc(costs, func(a, b fieldCost) int { return cmp.Compare(b.cost, a.cost) })
		largest := ""
		if len(costs) > 0 {
			parts := make([]string, 0, 2)
			for _, c := range costs[:min(2, len(costs))] {
				parts = append(parts, fmt.Sprintf("%s≈%d", c.name, c.cost))
			}
			largest = fmt.Sprintf("; largest: %s", strings.Join(parts, ", "))
		}
		// Name exactly the fields that carry cost (stage_id/stage_status/
		// checkpoint_kind are budgeted like every other field), so the model
		// shortens what the budget actually counted; empty fields are omitted
		// because there is nothing to shorten in them.
		shorten := make([]string, 0, len(costs))
		for _, c := range costs {
			shorten = append(shorten, c.name)
		}
		return CompactContextArgs{}, fmt.Errorf("continuation state exceeds the token budget (estimated_cost=%d, budget=%d)%s; shorten %s and retry", cost, limit, largest, strings.Join(shorten, "/"))
	}
	return args, nil
}

func removeRegisteredStateFiles(planned, registered []string) []string {
	if len(planned) == 0 || len(registered) == 0 {
		return planned
	}
	seen := make(map[string]struct{}, len(registered))
	for _, path := range registered {
		seen[path] = struct{}{}
	}
	kept := planned[:0]
	for _, path := range planned {
		if _, exists := seen[path]; !exists {
			kept = append(kept, path)
		}
	}
	return kept
}

// maxCompactContextClaims caps how many claims claim_evidence and claim_kinds
// may carry. It is the same limit the schema in Parameters() declares as
// maxProperties for both objects.
const maxCompactContextClaims = 20

// normalizeCompactContextClaims trims every claim key, rejects empty keys,
// caps the claim count at maxCompactContextClaims, and returns a new map.
// claim_evidence and claim_kinds join on exact claim keys downstream (an
// observed claim_kinds entry must line up with the claim_evidence entry that
// carries its evidence), so both maps must go through the same
// normalization: a whitespace-padded key surviving in one map but not the
// other would otherwise surface as a false "observed claim has no evidence"
// rejection, or split one claim across two entries.
func normalizeCompactContextClaims[V any](claims map[string]V, name string) (map[string]V, error) {
	normalized := make(map[string]V, len(claims))
	for claim, value := range claims {
		claim = strings.TrimSpace(claim)
		if claim == "" {
			return nil, fmt.Errorf("%s contains an empty claim", name)
		}
		normalized[claim] = value
	}
	if len(normalized) > maxCompactContextClaims {
		return nil, fmt.Errorf("%s contains %d claims, exceeding the maximum of %d", name, len(normalized), maxCompactContextClaims)
	}
	return normalized, nil
}

// validateCompactContextList trims every item, rejects empty items and arrays
// over the declared maxItems, and returns the trimmed items. Items carry no
// length cap of their own; the aggregated token budget bounds them.
func validateCompactContextList(items []string, maxItems int, name string) ([]string, error) {
	out := make([]string, 0, len(items))
	for i, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			return nil, fmt.Errorf("argument %s contains an empty item at index %d", name, i)
		}
		if !slices.Contains(out, item) {
			out = append(out, item)
		}
	}
	if len(out) > maxItems {
		return nil, fmt.Errorf("argument %s contains %d items, exceeding the maximum of %d", name, len(out), maxItems)
	}
	return out, nil
}

// currentProjectRoot returns the project root spellings resolve against, or
// "" when no provider is wired (strict plain-relative mode).
func (v CompactContextValidator) currentProjectRoot() string {
	if v.ProjectRoot == nil {
		return ""
	}
	return v.ProjectRoot()
}

// validateStateFiles applies the lexical project-root path contract to
// state_files. Entries may be workspace-relative or absolute / "~"-prefixed /
// "./"- / "../"-prefixed spellings of files inside the project root; accepted
// entries are normalized to their workspace-relative form so equivalent
// spellings deduplicate and the reference stays valid after the project moves
// or a session is restored elsewhere. Spellings that resolve outside the root
// are rejected. The filesystem is never touched: resolution is pure lexical
// (tilde expansion via the environment, Clean/Join/Rel), so the tool cannot
// act as an existence probe or read-permission bypass. Without a project root
// only plain relative entries are accepted (cleaned and escape-checked).
func validateStateFiles(paths []string, maxItems int, projectRoot string) ([]string, error) {
	if len(paths) > maxItems {
		return nil, fmt.Errorf("state_files contains %d paths, exceeding the maximum of %d", len(paths), maxItems)
	}
	root := ""
	if trimmed := strings.TrimSpace(projectRoot); trimmed != "" {
		if abs, err := filepath.Abs(filepath.Clean(trimmed)); err == nil {
			root = abs
		}
	}
	seen := make(map[string]bool, len(paths))
	out := make([]string, 0, len(paths))
	for i, p := range paths {
		p = strings.TrimSpace(p)
		if p == "" {
			return nil, fmt.Errorf("state_files contains an empty path at index %d", i)
		}
		if hasControlChars(p) {
			return nil, fmt.Errorf("state_files path %q must not contain control characters or newlines", p)
		}
		if strings.Contains(p, `\`) {
			return nil, fmt.Errorf("state_files path %q must use '/' separators", p)
		}
		expanded, err := expandTildePath(p)
		if err != nil {
			return nil, fmt.Errorf("state_files path %q starts with \"~\" but the home directory is unavailable; pass a workspace-relative path (e.g. \"docs/usage.md\"), or remove the entry and capture the state in completed/decisions/open_issues text instead", p)
		}
		candidate := expanded
		if root != "" && !filepath.IsAbs(expanded) {
			// Relative spellings (including "./" and "../" forms) anchor at
			// the project root, matching the root-relative contract.
			candidate = filepath.Join(root, expanded)
		}
		cleaned := filepath.Clean(candidate)
		var rel string
		within := false
		if root != "" {
			rel, within = pathutil.RelToBase(cleaned, root)
		} else if !filepath.IsAbs(cleaned) {
			// Without a root only plain relative spellings are verifiable;
			// require the clean form not to walk above the (unknown) root.
			rel = cleaned
			within = rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
		}
		if !within {
			return nil, fmt.Errorf("state_files path %q must resolve inside the project root: pass a workspace-relative path (e.g. \"docs/usage.md\") or an absolute / \"~\" / \"./\" / \"../\" spelling of a file inside the project; state outside the project cannot be referenced, so remove the entry and capture it in completed/decisions/open_issues text instead", p)
		}
		if rel == "." {
			return nil, fmt.Errorf("state_files path %q resolves to the project root directory itself; list a file or directory inside it", p)
		}
		// Store the normalized workspace-relative form so equivalent
		// spellings (absolute, ~-, ./-, ../-, plain) collapse to one entry.
		// Paths carry no length cap of their own; the aggregated token budget
		// bounds them like any other model-authored text.
		normalized := filepath.ToSlash(rel)
		if !seen[normalized] {
			seen[normalized] = true
			out = append(out, normalized)
		}
	}
	return out, nil
}

// hasControlChars reports whether s contains C0 control characters or
// newlines. Model-authored state paths must never smuggle formatting that
// could break out of the checkpoint's Externalized State section.
func hasControlChars(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

// CompactContextTool requests a model-driven context checkpoint from the
// MainAgent runtime. Its Execute only validates the request and returns an
// "accepted" signal; the actual checkpoint barrier runs on the main event
// loop, so this tool never mutates session state by itself.
type CompactContextTool struct {
	validator CompactContextValidator
}

func NewCompactContextTool(validator CompactContextValidator) CompactContextTool {
	return CompactContextTool{validator: validator}
}

func (CompactContextTool) Name() string { return NameCompactContext }

func (t CompactContextTool) Description() string {
	// The combined continuation-state budget is the only length limit, so the
	// model is told it up front instead of learning it from a rejection after
	// it has already authored the whole state.
	budget := ""
	if limit := t.validator.ContinuationStateMaxTokens; limit > 0 {
		budget = fmt.Sprintf("All text fields together (active_objective, next_step, completed, decisions, open_issues, retired_items, state_files, planned_state_files, evidence_refs, stage_id, stage_status, checkpoint_kind, claim_evidence, claim_kinds) must fit a combined budget of about %d estimated tokens; there are no per-field or per-item caps, so a long item is fine as long as the whole state stays within the budget.\n", limit)
	}
	// The todo-sync line is rendered only when todo_write is visible in the
	// same surface, so the description never pushes a tool the model cannot
	// call. Like the budget, it is baked at registration time.
	todoSync := ""
	if t.validator.TodoWriteVisible {
		todoSync = "- your todo list reflects actual progress (the checkpoint snapshots runtime todos verbatim; sync drifted entries with todo_write before requesting);\n"
	}
	return "Request a durable context checkpoint to reduce the cost of remaining work. This is a costed state transition, not a routine progress save: runtime rewrites session history and continues the same turn. A checkpoint never completes the task or replaces the final response.\n" +
		"Do not call it when the task is complete and only the final response remains. If work requires user input or confirmation, use the normal question or waiting mechanism. A terminal TODO state alone is not a reason to checkpoint.\n" +
		"Call it alone (no sibling tool calls in the same response), at a safe stop after the current atomic operation has ended. Unfinished background work may continue; do not describe it as completed.\n" +
		"When context is comfortable, checkpoint only if expected savings justify the reset and recovery cost. Under context pressure, stop optional exploration, preserve the minimum recovery state, and request a provisional checkpoint even if the stage remains active or candidate.\n" +
		"Capture every fact needed to resume in structured arguments or state_files; do not repeat full file contents in both. Refresh files you rely on before referencing them. Leave state_files empty when the structured arguments fully carry the recovery state, and do not create or modify files solely to request a checkpoint. Record unfinished updates as open_issues, not saved state.\n" +
		"Use planned_state_files only for future paths; they do not externalize state. File paths and evidence requirements are defined by the corresponding parameter descriptions.\n" +
		"Only active_objective and next_step are required. Report new progress and changed decisions; bounded prior completed work, decisions and open issues carry forward automatically. Use retired_items to remove resolved or superseded entries by their exact checkpoint text. Omission alone never deletes an entry. Evidence and stage metadata are optional; do not invent evidence IDs.\n" +
		todoSync +
		"A success result only means the request was accepted; a later model-driven [Context Summary] checkpoint confirms the reset was applied. A skip is a normal policy result, not an error: continue actual work or deliver the final response, rather than repeatedly retrying unchanged input.\n" +
		budget +
		"If the arguments are rejected, fix the reported problem and retry; re-submitting the same values cannot succeed. Never work around the limits by splitting the checkpoint."
}

func (CompactContextTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"active_objective": map[string]any{
				"type":        "string",
				"minLength":   1,
				"description": "The single current goal to keep advancing after the reset. Do not restate the user request.",
			},
			"completed": map[string]any{
				"type":        "array",
				"maxItems":    12,
				"items":       map[string]any{"type": "string", "minLength": 1},
				"description": "Verified outcomes with how each was verified (tests, commands, files). Do not restate the todo list: runtime todos are snapshotted automatically and reconcile against this section after the reset.",
			},
			"decisions": map[string]any{
				"type":        "array",
				"maxItems":    8,
				"items":       map[string]any{"type": "string", "minLength": 1},
				"description": "Each item must be one non-empty string containing an important decision and, when useful, its one-line reason; do not use objects such as {\"value\":\"...\",\"reason\":\"...\"}.",
			},
			"open_issues": map[string]any{
				"type":        "array",
				"maxItems":    8,
				"items":       map[string]any{"type": "string", "minLength": 1},
				"description": "Unresolved blockers, risks, or facts awaiting confirmation.",
			},
			"next_step": map[string]any{
				"type":        "string",
				"minLength":   1,
				"description": "One concrete action executable immediately after the checkpoint applies, subordinate to the latest user request. Do not use a checkpoint just to wait for user input or deliver the final response.",
			},
			"retired_items": map[string]any{
				"type": "array", "maxItems": 40,
				"items":       map[string]any{"type": "string", "minLength": 1},
				"description": "Exact text of prior completed, decision, open issue or claim entries to retire. Use for resolved issues or superseded conclusions; supply replacements in the normal fields. Unknown entries are harmless. Never removes runtime facts or user instructions.",
			},
			"state_files": map[string]any{
				"type":        "array",
				"maxItems":    16,
				"items":       map[string]any{"type": "string", "minLength": 1},
				"description": "Existing files carrying recovery state: paths must resolve inside the project root and are stored workspace-relative. References only: the tool never reads them and never verifies that they exist. After a reset eligible files may have a bounded head injected only when the current read permission allows it; do not assume the full file was loaded. Keep its head self-contained. Leave empty when structured arguments carry the state. Capture out-of-project state in structured arguments.",
			},
			"planned_state_files": map[string]any{
				"type": "array", "maxItems": 16,
				"items":       map[string]any{"type": "string", "minLength": 1},
				"description": "Workspace-relative paths planned for future state. They are not evidence that a file exists or that work is complete.",
			},
			"evidence_refs": map[string]any{
				"type": "array", "maxItems": 24,
				"items":       map[string]any{"type": "string", "minLength": 1},
				"description": "Stable evidence IDs supporting completed work or decisions, copied verbatim from the Evidence ID lines of a [Context Evidence] pack visible in this conversation (each ID is ev- plus 12 hex characters). Leave this empty when no Evidence ID line is visible, and write nothing else in its place: invented or non-ID values are rejected, and only observed claims and committed checkpoints require evidence — not every completed stage. Evidence IDs in claim_evidence are automatically included in evidence_refs; do not repeat them here.",
			},
			"stage_id":        map[string]any{"type": "string", "description": "Stable identifier for the current work stage."},
			"stage_status":    map[string]any{"type": "string", "enum": compactContextStageStatuses, "description": "State of this work stage, not the whole user request. A completed stage does not end the turn or replace the final response."},
			"checkpoint_kind": map[string]any{"type": "string", "enum": compactContextCheckpointKinds, "description": "Provisional reduces context but is not authoritative; committed requires runtime validation, and additionally requires stage_status=completed with at least one valid evidence_refs entry."},
			"claim_evidence":  map[string]any{"type": "object", "maxProperties": maxCompactContextClaims, "additionalProperties": map[string]any{"type": "array", "maxItems": 8, "items": map[string]any{"type": "string", "minLength": 1}}, "description": "Maps each claim to the evidence IDs supporting it. Claim keys are natural language: usually a condensed conclusion from completed/decisions, where paraphrasing is fine and verbatim matching is never required; standalone claims are also allowed. Every value must be an evidence ID visible in this conversation, copied verbatim from an Evidence ID line of a [Context Evidence] pack or from an ID a rejected compact_context result listed (each ID is ev- plus 12 hex characters). File paths, tool names, URLs, descriptions and claim kinds (observed/derived/assumed/proposed) are not evidence IDs and are rejected. An observed claim needs at least one supporting ID here; if no ID is visible, do not fill this in — classify the claim derived or assumed instead. These IDs are automatically included in evidence_refs; no duplicate entry is required."},
			"claim_kinds":     map[string]any{"type": "object", "maxProperties": maxCompactContextClaims, "additionalProperties": map[string]any{"type": "string", "enum": compactContextClaimKinds}, "description": "Classifies each claim (usually from completed/decisions); observed requires an evidence ID visible in this conversation and listed in claim_evidence/evidence_refs, derived is inferred from evidence, assumed is unverified, and proposed is future work. When no evidence ID is visible, the claim cannot be observed: classify it derived, assumed or proposed instead of substituting anything for the missing ID."},
		},
		"required":             []string{"active_objective", "next_step"},
		"additionalProperties": false,
	}
}

func (CompactContextTool) IsReadOnly() bool { return false }

// ConcurrencyPolicy declares exclusive scheduling: compact_context rewrites
// session history and must never run alongside sibling tool calls.
func (CompactContextTool) ConcurrencyPolicy(_ json.RawMessage) ConcurrencyPolicy {
	return ConcurrencyPolicy{Resource: "tool:" + NameCompactContext, Mode: ConcurrencyModeExclusive}
}

func (t CompactContextTool) Execute(_ context.Context, raw json.RawMessage) (string, error) {
	if _, err := t.validator.ParseCompactContextArgs(raw); err != nil {
		return "", err
	}
	return "Context checkpoint request accepted. No reset has occurred yet; only a later model-driven context checkpoint confirms successful application.", nil
}
