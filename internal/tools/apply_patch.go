package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/lsp"
)

// ApplyPatchTool applies the Codex apply_patch protocol. Every operation is
// planned from a single filesystem snapshot before any mutation is committed.
type ApplyPatchTool struct {
	LSP     *lsp.Manager
	BaseDir string
}

type ApplyPatchArgs struct {
	Patch string `json:"patch"`
}

// NormalizeApplyPatchArgs validates the current {patch} arguments and returns
// them in canonical form. It tolerates three input shapes: the canonical
// {patch} object, a JSON string wrapping it (legacy function-call history), and
// bare freeform text (custom_tool_call history or anomalous gateways). A
// gateway-lowered {"input": ...} payload without a patch field is reported as a
// distinct misconfiguration with an actionable hint.
//
// Unknown top-level fields — including the legacy single-file `path` wrapper —
// are deliberately ignored rather than rejected, matching the generic
// sanitization applied to tool arguments before Execute (see
// SanitizeUnknownArgsWithDiagnostics): only `patch` participates, and it must
// satisfy the apply_patch constraints (non-empty, and the Codex `*** Begin
// Patch` envelope enforced by ParseApplyPatch).
func NormalizeApplyPatchArgs(raw json.RawMessage) (json.RawMessage, error) {
	unwrapped := unwrapToolArgs(raw)
	if len(unwrapped) == 0 || unwrapped[0] != '{' {
		// Bare freeform text (custom_tool_call wire input or anomalous
		// gateway): treat the entire payload as the patch.
		return json.Marshal(ApplyPatchArgs{Patch: string(unwrapped)})
	}
	if json.Valid(unwrapped) {
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(unwrapped, &obj); err == nil {
			if _, hasPatch := obj["patch"]; !hasPatch {
				if _, hasInput := obj["input"]; hasInput {
					return nil, fmt.Errorf("gateway appears to have lowered custom apply_patch incorrectly; set compat.apply_patch.freeform: false")
				}
			}
		}
	}
	var args ApplyPatchArgs
	if err := json.Unmarshal(unwrapped, &args); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	if strings.TrimSpace(args.Patch) == "" {
		return nil, fmt.Errorf("patch is required")
	}
	return json.Marshal(ApplyPatchArgs{Patch: args.Patch})
}

type MutationKind string

const (
	MutationAdd    MutationKind = "add"
	MutationUpdate MutationKind = "update"
	MutationDelete MutationKind = "delete"
	MutationMove   MutationKind = "move"
)

// ApplyPatch operation section markers. These are the Codex freeform patch
// format's operation headers; the parser and every display that summarizes a
// streamed patch must agree on them.
const (
	ApplyPatchAddFileMarker    = "*** Add File: "
	ApplyPatchDeleteFileMarker = "*** Delete File: "
	ApplyPatchUpdateFileMarker = "*** Update File: "
	ApplyPatchMoveToMarker     = "*** Move to: "
)

// applyPatchHunkMarker introduces a hunk. It opens Chord's own header spelling
// ("@@ func greet():") and closes the unified-diff line range models sometimes
// emit ("@@ -19,10 +19,8 @@"), so the scanner and the header normalizer must
// agree on it. The other unified-diff header patterns in this repository match
// different things — the TUI captures line numbers to renumber a rendered
// diff, and context reduction detects whole header lines including combined
// diffs — and live in packages that already depend on this one, so they cannot
// share this definition without inverting that dependency.
const applyPatchHunkMarker = "@@"

type MutationTarget struct {
	Kind       MutationKind
	SourcePath string
	TargetPath string
}

// ApplyPatchDisplayTarget describes one model-facing file operation for UI
// display. Paths remain exactly as written in the patch.
type ApplyPatchDisplayTarget struct {
	Kind       MutationKind
	SourcePath string
	TargetPath string
	Added      int
	Removed    int
}

type PlannedMutation struct {
	Kind       MutationKind
	SourcePath string
	TargetPath string
	// DisplayPath is the working-directory-relative spelling of SourcePath
	// captured while the plan was built. Commit-time errors are reported with
	// it so the model is told the short form it should resubmit instead of a
	// resolved absolute path it must shorten itself.
	DisplayPath string

	BeforeExists       bool
	BeforeBytes        []byte
	BeforeMode         os.FileMode
	TargetBeforeExists bool
	TargetBeforeBytes  []byte
	TargetBeforeMode   os.FileMode
	AfterBytes         []byte
	AfterMode          os.FileMode
	AfterText          string
	Added              int
	Removed            int
	PunctuationHunks   int
	FuzzyHunks         int
	// FuzzyReplacements records, per accepted fuzzy hunk, the hunk's claimed
	// removed line, the file line it actually replaced, and the added line
	// written in its place so the result note can audit what changed.
	FuzzyReplacements []applyPatchFuzzyReplacement
	// StrippedInvisible reports the invisible runes cleaned from the
	// model-added text of this mutation while the plan was built — the
	// floating-mark strip runs on added (+) lines and add-file content,
	// never on the file's untouched bytes (see cleanApplyPatchAddedLines).
	// The Execute result note sums these per-mutation counts.
	StrippedInvisible map[rune]int
	diffInput         unifiedFileDiff
	diffable          bool
}

type MutationPlan struct {
	Mutations []PlannedMutation
}

// ApplyPatchChange describes one committed net mutation using resolved paths.
type ApplyPatchChange struct {
	Kind       MutationKind
	SourcePath string
	TargetPath string
	Added      int
	Removed    int
}

// ApplyPatchDiffCollector receives the complete multi-file diff and per-file
// mutation metadata produced by an ApplyPatchTool execution.
type ApplyPatchDiffCollector struct {
	mu      sync.Mutex
	summary DiffSummary
	changes []ApplyPatchChange
}

func (c *ApplyPatchDiffCollector) set(plan MutationPlan) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.summary = applyPatchMutationDiffSummary(plan)
	c.changes = make([]ApplyPatchChange, len(plan.Mutations))
	for i, mutation := range plan.Mutations {
		c.changes[i] = ApplyPatchChange{
			Kind:       mutation.Kind,
			SourcePath: mutation.SourcePath,
			TargetPath: mutation.TargetPath,
			Added:      mutation.Added,
			Removed:    mutation.Removed,
		}
	}
	c.mu.Unlock()
}

func (c *ApplyPatchDiffCollector) Summary() DiffSummary {
	if c == nil {
		return DiffSummary{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.summary
}

func (c *ApplyPatchDiffCollector) Changes() []ApplyPatchChange {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]ApplyPatchChange(nil), c.changes...)
}

type applyPatchDiffCollectorKey struct{}

func WithApplyPatchDiffCollector(ctx context.Context, collector *ApplyPatchDiffCollector) context.Context {
	if collector == nil {
		return ctx
	}
	return context.WithValue(ctx, applyPatchDiffCollectorKey{}, collector)
}

func applyPatchDiffCollectorFromContext(ctx context.Context) *ApplyPatchDiffCollector {
	if ctx == nil {
		return nil
	}
	collector, _ := ctx.Value(applyPatchDiffCollectorKey{}).(*ApplyPatchDiffCollector)
	return collector
}

func (ApplyPatchTool) Name() string     { return "apply_patch" }
func (ApplyPatchTool) IsReadOnly() bool { return false }
func (t ApplyPatchTool) ConcurrencyPolicy(json.RawMessage) ConcurrencyPolicy {
	return ConcurrencyPolicy{Resource: "workspace", Mode: ConcurrencyModeExclusive}
}
func (ApplyPatchTool) Description() string {
	// Opens with the Codex apply_patch description for gpt-5-and-later models,
	// but omits its FREEFORM sentence: Chord emits apply_patch as either a
	// freeform custom tool or a JSON function tool, and "do not wrap the patch
	// in JSON" is only valid for the former. Format guidance follows because
	// this description also serves non-freeform models (JSON function shape on
	// non-Responses endpoints and hosts that reject custom tools) that have no
	// patch training signal; the wire description is overridden with the exact
	// Codex text in the freeform custom-tool shape (see
	// convertToolsToResponsesForTarget). Path semantics (relative to the
	// session working directory, or absolute) live in the patch parameter
	// description instead, which JSON function models always see.
	return "The `apply_patch` tool can be used to edit files. " +
		"Your patch is a unified diff wrapped in a `*** Begin Patch` / `*** End Patch` envelope. " +
		"Each operation starts with one of `*** Add File: <path>`, `*** Delete File: <path>`, `*** Update File: <path>` (optionally followed by `*** Move to: <new path>`). " +
		"Hunks are introduced by `@@` and each line's first character is its marker: `+` (added), `-` (removed), or a space (context); new file contents are `+` lines. " +
		"One `@@` line starts exactly one hunk: put any section context on that same line (`@@ func greet():`) rather than on a line after it, since a later `@@` starts the next hunk, and never prefix it with a unified-diff range like `@@ -19,10 +19,8 @@` — Chord anchors on the header text, not on line numbers, so a range is dead weight and any useful anchor is the section name itself. " +
		"`+` or `-` must be the first character of the line; preserve source indentation after the marker (`-old` is a deletion, while ` -old` is context text). Every hunk must contain at least one `+` or `-` line. " +
		"Context lines are literal complete source lines, not placeholders: a blank or whitespace-only line is a real source line, and `...` never omits context. " +
		"Prefer the smallest hunk with distinctive context; after a mismatch, re-read the current target range and rebuild the hunk instead of retrying it unchanged."
}
func (ApplyPatchTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"patch": map[string]any{
				"type":        "string",
				"description": "Complete Codex apply_patch text: a `*** Begin Patch` / `*** End Patch` envelope wrapping Add/Delete/Update operations with `@@` hunks; new file contents are `+` lines. Prefer paths relative to the session working directory when the file is inside it; use an absolute path only for files outside it. One `@@` line starts exactly one hunk, so any section context belongs on that line (`@@ func greet():`) and a second `@@` starts the next hunk. The first character of each hunk line must be its marker (`+` added, `-` removed, space context); do not add a space before `+` or `-`, and preserve source indentation after it (`-old` is a deletion, while ` -old` is context text). Every hunk must contain at least one `+` or `-` line. Context lines must be literal complete source lines; blank or whitespace-only lines are real source lines, not omission placeholders, and `...` never omits context. Prefer small hunks with distinctive context, and rebuild a hunk from a fresh read after a mismatch.",
			},
		},
		"required":             []string{"patch"},
		"additionalProperties": false,
	}
}

func (t ApplyPatchTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	raw, err := NormalizeApplyPatchArgs(raw)
	if err != nil {
		return "", err
	}
	var args ApplyPatchArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	// Strip orphaned variation selectors the way replace_edit does: models leak
	// them into patch text and the tolerance matching layers already absorb
	// them. Orphaned combining marks are handled separately — they stay in the
	// patch so the tolerance matcher can fold them (which is what fires the
	// "punctuation/whitespace-tolerant" note for headings like "### ̄.2.1"),
	// and are cleaned off the model-added lines while the plan is built
	// below. A patch that strips to empty had no visible content to begin
	// with; control characters cannot
	// be cleaned safely — reject both and route binary content to a shell
	// command or script.
	patchLen := len([]rune(args.Patch))
	strippedPatch := StripZeroWidthFormat(StripOrphanVariationSelectors(args.Patch))
	cleanedCounts := CountStrippedInvisible(args.Patch, strippedPatch)
	args.Patch = strippedPatch
	strippedSelectors := patchLen - len([]rune(args.Patch))
	if args.Patch == "" && strippedSelectors > 0 {
		return "", fmt.Errorf("patch contains only invisible characters (%d invisible character(s) were stripped); rebuild the patch with the visible changes you want to apply", strippedSelectors)
	}
	if err := validateWritableText(args.Patch); err != nil {
		return "", fmt.Errorf("patch %w", err)
	}
	result, err := buildApplyPatchPlanWithOutcomes(ctx, args.Patch, t.BaseDir)
	// Floating combining marks are cleaned from model-added text while the
	// plan is built — add-file content and the added (+) lines of update
	// hunks (see cleanApplyPatchAddedLines) — never from a mutation's whole
	// AfterBytes, which would rewrite file content the patch never touched.
	// Sum the per-mutation counts here so the result note reports exactly
	// what the write removed.
	if cleanedCounts == nil {
		cleanedCounts = map[rune]int{}
	}
	var strippedCombining int
	for i := range result.Plan.Mutations {
		for r, c := range result.Plan.Mutations[i].StrippedInvisible {
			cleanedCounts[r] += c
			strippedCombining += c
		}
	}
	strippedSelectors += strippedCombining
	if err != nil {
		// Parse or snapshot failed before any operation could commit: nothing
		// was modified, so the atomic error contract is preserved.
		return "", err
	}
	if result.HasFailures() {
		return t.applyPatchPartial(ctx, result)
	}
	plan := result.Plan
	if err := CommitMutationPlan(plan); err != nil {
		return "", err
	}
	if collector := applyPatchDiffCollectorFromContext(ctx); collector != nil {
		collector.set(plan)
	}
	out := t.finishApplyPatch(ctx, plan)
	if strippedSelectors > 0 {
		out += fmt.Sprintf("\nNote: cleaned %d invisible character(s) from the patch text: %s", strippedSelectors, describeInvisibleCounts(cleanedCounts))
	}
	return out, nil
}

// applyPatchPartial commits the successfully-planned operations, wires LSP
// diagnostics and diff/changed-files for them, then returns an error whose
// message tells the model exactly which changes committed, which operation
// groups did not, and why each failed; failed operation groups are not
// written to disk.
func (t ApplyPatchTool) applyPatchPartial(ctx context.Context, result ApplyPatchPlanResult) (string, error) {
	plan := result.Plan
	if err := CommitMutationPlan(plan); err != nil {
		// A write-time failure (permissions, ENOSPC, ...) while committing the
		// successful subset. CommitMutationPlan rolls back its own writes, so
		// surface the raw commit failure without the partial-failure framing.
		return "", err
	}
	if len(plan.Mutations) > 0 {
		if collector := applyPatchDiffCollectorFromContext(ctx); collector != nil {
			collector.set(plan)
		}
	}
	commitOutput := t.finishApplyPatch(ctx, plan)
	type failedFile struct {
		path    string
		reasons []string
	}
	var failedFiles []failedFile
	failedIndex := make(map[string]int)
	var firstFailedErr error
	for _, o := range result.Outcomes {
		if o.succeeded {
			continue
		}
		key := filepath.Clean(strings.TrimSpace(o.op.Path))
		if resolved, resolveErr := resolveApplyPatchPath(o.op.Path, t.BaseDir); resolveErr == nil {
			key = resolved
		}
		index, ok := failedIndex[key]
		if !ok {
			index = len(failedFiles)
			failedIndex[key] = index
			failedFiles = append(failedFiles, failedFile{path: applyPatchPathHint(o.op.Path, t.BaseDir)})
		}
		failedFiles[index].reasons = append(failedFiles[index].reasons, o.reason)
		// rolledBack/skipped ops are descriptive; the root cause is the op
		// whose own application actually failed, which we surface as the
		// wrapped error.
		if o.rolledBack {
			continue
		}
		if firstFailedErr == nil {
			firstFailedErr = o.err
		}
	}
	failed := make([]string, 0, len(failedFiles))
	for _, file := range failedFiles {
		var reasons []string
		for _, reason := range file.reasons {
			trimmed := trimApplyPatchFailurePathPrefix(file.path, reason)
			// Several ops in one group can fail for the same cause; repeating
			// it adds nothing the model can act on.
			if len(reasons) > 0 && reasons[len(reasons)-1] == trimmed {
				continue
			}
			reasons = append(reasons, trimmed)
		}
		failed = append(failed, fmt.Sprintf("- %s: %s", file.path, strings.Join(reasons, "; ")))
	}
	var b strings.Builder
	if len(plan.Mutations) > 0 {
		fmt.Fprintf(&b, "apply_patch partially applied: %s committed; %s not applied.\n",
			applyPatchCount(len(plan.Mutations), "change", "changes"),
			applyPatchCount(len(failedFiles), "file group", "file groups"),
		)
		b.WriteString(commitOutput)
		b.WriteString("\n\n")
	} else {
		b.WriteString("apply_patch failed: no changes were committed.\n\n")
	}
	b.WriteString("Not applied:\n")
	b.WriteString(strings.Join(failed, "\n"))
	if len(plan.Mutations) > 0 {
		b.WriteString("\n\nChanges under \"Applied patch\" are already on disk; resolve each cause above and resubmit only the failed file groups rebuilt from current file contents.")
	}
	if firstFailedErr == nil {
		firstFailedErr = fmt.Errorf("apply_patch failed")
	}
	if len(plan.Mutations) == 0 {
		err := fmt.Errorf("apply_patch failed: %s not applied: %w", applyPatchCount(len(failedFiles), "file group", "file groups"), firstFailedErr)
		return b.String(), markErrorDescribedInResult(err)
	}
	// One mutation per committed file group, matching the displayed line count
	// (a move is one "R src -> dst" line, not two files).
	err := fmt.Errorf("apply_patch partially applied: %s committed, %s not applied: %w",
		applyPatchCount(len(plan.Mutations), "change", "changes"),
		applyPatchCount(len(failedFiles), "file group", "file groups"),
		firstFailedErr,
	)
	return b.String(), markErrorDescribedInResult(markErrorWithCommittedChanges(err))
}

func applyPatchCount(count int, singular, plural string) string {
	if count == 1 {
		return fmt.Sprintf("1 %s", singular)
	}
	return fmt.Sprintf("%d %s", count, plural)
}

func trimApplyPatchFailurePathPrefix(path, reason string) string {
	for _, prefix := range []string{
		"update " + path + ": ",
		"read update source " + path + ": ",
		"encode update " + path + ": ",
		"read apply_patch source " + path + ": ",
	} {
		if trimmed, ok := strings.CutPrefix(reason, prefix); ok {
			return trimmed
		}
	}
	return reason
}

func ApplyPatchTargets(raw json.RawMessage, baseDir string) ([]MutationTarget, error) {
	raw, err := NormalizeApplyPatchArgs(raw)
	if err != nil {
		return nil, err
	}
	// raw is the canonical object marshalled by NormalizeApplyPatchArgs, so no
	// string-unwrap is needed here (matching Execute).
	var args ApplyPatchArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, err
	}
	doc, err := ParseApplyPatch(args.Patch)
	if err != nil {
		return nil, err
	}
	return resolveApplyPatchTargets(doc, baseDir)
}

// ApplyPatchDisplayTargets extracts operation paths without resolving or
// accessing the filesystem. It is intended for tool-call UIs and transcripts.
func ApplyPatchDisplayTargets(raw json.RawMessage) ([]ApplyPatchDisplayTarget, error) {
	raw, err := NormalizeApplyPatchArgs(raw)
	if err != nil {
		return nil, err
	}
	// raw is the canonical object marshalled by NormalizeApplyPatchArgs, so no
	// string-unwrap is needed here (matching Execute).
	var args ApplyPatchArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, err
	}
	doc, err := ParseApplyPatch(args.Patch)
	if err != nil {
		return nil, err
	}
	targets := make([]ApplyPatchDisplayTarget, 0, len(doc.Operations))
	plainUpdateIndexByPath := make(map[string]int)
	for _, op := range doc.Operations {
		added, removed := applyPatchOperationLineStats(op)
		if op.Kind == MutationUpdate && op.MovePath == "" {
			pathKey := filepath.Clean(strings.TrimSpace(op.Path))
			if index, ok := plainUpdateIndexByPath[pathKey]; ok {
				targets[index].Added += added
				targets[index].Removed += removed
				continue
			}
			plainUpdateIndexByPath[pathKey] = len(targets)
		}
		targets = append(targets, ApplyPatchDisplayTarget{
			Kind:       op.Kind,
			SourcePath: op.Path,
			TargetPath: op.MovePath,
			Added:      added,
			Removed:    removed,
		})
	}
	return targets, nil
}

func applyPatchOperationLineStats(op applyPatchOperation) (added, removed int) {
	if op.Kind == MutationAdd {
		return lineCountForMutation(op.Content), 0
	}
	for _, hunk := range op.Hunks {
		for _, line := range hunk.Lines {
			switch line.Kind {
			case '+':
				added++
			case '-':
				removed++
			}
		}
	}
	return added, removed
}

func resolveApplyPatchTargets(doc applyPatchDocument, baseDir string) ([]MutationTarget, error) {
	targets := make([]MutationTarget, 0, len(doc.Operations))
	seen := make(map[string]struct{})
	for _, op := range doc.Operations {
		source, target, err := resolveApplyPatchOperationPaths(op, baseDir)
		if err != nil {
			return nil, err
		}
		kind := op.Kind
		if target != source {
			kind = MutationMove
		}
		paths := []string{source}
		if target != source {
			paths = append(paths, target)
		}
		for _, candidate := range paths {
			if _, duplicate := seen[candidate]; duplicate {
				continue
			}
			for existing := range seen {
				if isCleanPathWithin(candidate, existing) || isCleanPathWithin(existing, candidate) {
					return nil, fmt.Errorf("apply_patch contains overlapping operations for %s and %s", existing, candidate)
				}
			}
			seen[candidate] = struct{}{}
		}
		targets = append(targets, MutationTarget{Kind: kind, SourcePath: source, TargetPath: target})
	}
	sort.Slice(targets, func(i, j int) bool {
		if targets[i].SourcePath != targets[j].SourcePath {
			return targets[i].SourcePath < targets[j].SourcePath
		}
		return targets[i].TargetPath < targets[j].TargetPath
	})
	return targets, nil
}

func resolveApplyPatchOperationPaths(op applyPatchOperation, baseDir string) (source, target string, err error) {
	source, err = resolveApplyPatchPath(op.Path, baseDir)
	if err != nil {
		return "", "", err
	}
	target = source
	if op.MovePath != "" {
		target, err = resolveApplyPatchPath(op.MovePath, baseDir)
		if err != nil {
			return source, "", err
		}
		if target == source {
			return source, target, fmt.Errorf("apply_patch move source and target are the same: %s", applyPatchPathHint(source, baseDir))
		}
	}
	return source, target, nil
}

// MutationTargetPaths returns every source and destination touched by targets.
// Paths are de-duplicated and sorted so callers can acquire locks consistently.
func MutationTargetPaths(targets []MutationTarget) []string {
	seen := make(map[string]struct{}, len(targets)*2)
	for _, target := range targets {
		if path := strings.TrimSpace(target.SourcePath); path != "" {
			seen[path] = struct{}{}
		}
		if path := strings.TrimSpace(target.TargetPath); path != "" {
			seen[path] = struct{}{}
		}
	}
	paths := make([]string, 0, len(seen))
	for path := range seen {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths
}

func resolveApplyPatchPath(path, baseDir string) (string, error) {
	resolved, err := resolveToolPathInDir(path, baseDir)
	if err != nil {
		return "", fmt.Errorf("resolve apply_patch path %q: %w", path, err)
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return "", fmt.Errorf("resolve apply_patch path %q: %w", path, err)
	}
	resolved = filepath.Clean(resolved)
	if isBlockedDevicePath(resolved) {
		return "", fmt.Errorf("cannot apply_patch to blocked device path: %s", path)
	}
	return resolved, nil
}

// applyPatchPathHint renders path the way the model should spell it back in a
// follow-up patch: relative to the session working directory when the file
// lives inside it, ~/... when it lives under home, and absolute only as a last
// resort. Every model-visible hint gets echoed into the next patch, so a long
// absolute prefix is pure token cost on a path the model could have written
// relative in the first place.
func applyPatchPathHint(path, baseDir string) string {
	return formatToolPathInDir(path, baseDir, path)
}

// isCleanPathWithin reports whether child is inside parent or equal to it.
// Both paths must already be filepath.Clean+Abs (as resolveApplyPatchPath
// guarantees), so the check is a zero-allocation prefix comparison.
func isCleanPathWithin(child, parent string) bool {
	if child == parent {
		return false // equality is a duplicate, not a nesting overlap
	}
	sep := string(filepath.Separator)
	return strings.HasPrefix(child, parent+sep)
}

// applyPatchMutationSummary renders one mutation as a single display line
// ("A path", "M path", "D path", "R src -> dst"). Every path goes through
// applyPatchPathHint, the one spelling this tool shows the model, so a summary
// line and a commit-time error name the same file the same way. A move renders
// each side on its own: the joined "src -> dst" string is not a path and would
// survive path formatting unchanged, leaving one line absolute while the rest
// of the patch report stays relative. Both the success path and the
// partial-failure "applied" list go through finishApplyPatch, so committed
// files are always reported identically.
func applyPatchMutationSummary(mutation PlannedMutation, baseDir string) string {
	switch mutation.Kind {
	case MutationAdd:
		return "A " + applyPatchPathHint(mutation.TargetPath, baseDir)
	case MutationDelete:
		return "D " + applyPatchPathHint(mutation.SourcePath, baseDir)
	case MutationMove:
		return "R " + applyPatchPathHint(mutation.SourcePath, baseDir) + " -> " + applyPatchPathHint(mutation.TargetPath, baseDir)
	}
	return "M " + applyPatchPathHint(mutation.SourcePath, baseDir)
}

// applyPatchMutationNotePath is the file a per-hunk note should name: the path
// the patched content ends up at. For a move-with-changes that is the
// destination — the source no longer exists once the patch is applied, so
// pointing the model at it would send it to re-read a deleted file.
func applyPatchMutationNotePath(mutation PlannedMutation) string {
	if mutation.TargetPath != "" {
		return mutation.TargetPath
	}
	return mutation.SourcePath
}

func (t ApplyPatchTool) finishApplyPatch(ctx context.Context, plan MutationPlan) string {
	if len(plan.Mutations) == 0 {
		return "Applied patch:\nNo net file changes"
	}
	var lines []string
	punctuationHunks := 0
	fuzzyHunks := 0
	for _, mutation := range plan.Mutations {
		punctuationHunks += mutation.PunctuationHunks
		fuzzyHunks += mutation.FuzzyHunks
		lines = append(lines, applyPatchMutationSummary(mutation, t.BaseDir))
		invalidatePathCache(mutation.SourcePath)
		invalidatePathCache(mutation.TargetPath)
	}
	sort.Strings(lines)
	if punctuationHunks > 0 {
		lines = append(lines, fmt.Sprintf("Note: used %s matching for %d hunk(s); unchanged text was preserved from the current file", tolerantMatchNote, punctuationHunks))
	}
	if fuzzyHunks > 0 {
		lines = append(lines, fmt.Sprintf("Note: used safe fuzzy matching for %d hunk(s); only a unique near-match with unchanged context was accepted", fuzzyHunks))
		for _, mutation := range plan.Mutations {
			// The audit line names the file and the 1-based line it
			// overwrote: with several mutations in one patch, "the file's
			// actual line" alone does not say which file, and without the
			// number the model cannot go look at what it got wrong. The
			// quoted text goes through the same truncate-and-escape helper as
			// every other tool diagnostic, so trailing whitespace and
			// invisible runes stay visible and one pathological line cannot
			// inflate the result.
			path := applyPatchPathHint(applyPatchMutationNotePath(mutation), t.BaseDir)
			for _, replacement := range mutation.FuzzyReplacements {
				lines = append(lines, fmt.Sprintf("Note: fuzzy hunk replaced %s line %d: the file's actual line %s with %s; your hunk claimed %s",
					path, replacement.line, truncateToolLine(replacement.actual), truncateToolLine(replacement.added), truncateToolLine(replacement.removed)))
			}
		}
	}
	out := "Applied patch:\n" + strings.Join(lines, "\n")
	if t.LSP == nil {
		return out
	}
	baselines := make(map[string][]lsp.Diagnostic)
	outputs := make(map[string]config.DiagnosticOutputConfig)
	extras := make(map[string][]lsp.Diagnostic)
	type finalWrite struct {
		path    string
		content string
		change  lsp.WatchedFileChangeType
	}
	finalWrites := make(map[string]finalWrite)
	var reviewedPaths []string
	for _, mutation := range plan.Mutations {
		switch mutation.Kind {
		case MutationAdd:
			path := normalizedLSPPath(mutation.TargetPath)
			if _, ok := baselines[path]; !ok {
				baselines[path] = nil
			}
			finalWrites[path] = finalWrite{path: mutation.TargetPath, content: mutation.AfterText, change: lsp.WatchedFileCreated}
		case MutationUpdate:
			path := normalizedLSPPath(mutation.TargetPath)
			if _, ok := baselines[path]; !ok {
				baselines[path] = t.LSP.Diagnostics(mutation.TargetPath)
			}
			finalWrites[path] = finalWrite{path: mutation.TargetPath, content: mutation.AfterText, change: lsp.WatchedFileChanged}
		case MutationDelete:
			t.clearLSPDeletedPath(ctx, mutation.SourcePath)
		case MutationMove:
			t.clearLSPDeletedPath(ctx, mutation.SourcePath)
			path := normalizedLSPPath(mutation.TargetPath)
			if _, ok := baselines[path]; !ok {
				baselines[path] = nil
			}
			finalWrites[path] = finalWrite{path: mutation.TargetPath, content: mutation.AfterText, change: lsp.WatchedFileCreated}
		}
	}
	paths := make([]string, 0, len(finalWrites))
	for path := range finalWrites {
		paths = append(paths, path)
	}
	slices.Sort(paths)
	for _, path := range paths {
		write := finalWrites[path]
		t.LSP.MarkTouched(write.path)
		outputs[path] = t.LSP.DiagnosticOutputConfigForPath(path)
		result := t.LSP.AfterFileWriteToolResult(ctx, write.path, write.content, "", false, write.change, t.BaseDir)
		if parsed := lsp.ParseToolOutputDiagnostics(result); len(parsed) > 0 {
			extras[path] = parsed
		}
		reviewedPaths = append(reviewedPaths, write.path)
	}
	slices.Sort(reviewedPaths)
	return t.LSP.AppendLSPDiagnosticsToToolOutputForPaths(out, reviewedPaths, true, baselines, outputs, extras, t.BaseDir)
}

func normalizedLSPPath(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	return filepath.Clean(abs)
}

func (t ApplyPatchTool) clearLSPDeletedPath(ctx context.Context, path string) {
	if ctx == nil {
		ctx = context.Background()
	}
	t.LSP.UnmarkTouched(path)
	_ = t.LSP.NotifyWatchedFileChanged(ctx, path, lsp.WatchedFileDeleted)
	_ = t.LSP.DidCloseErr(ctx, path)
}

func lineCountForMutation(s string) int {
	if s == "" {
		return 0
	}
	count := strings.Count(s, "\n")
	if !strings.HasSuffix(s, "\n") {
		count++
	}
	return count
}
