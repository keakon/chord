package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"unicode"

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

type applyPatchDocument struct {
	Operations []applyPatchOperation
}

type applyPatchOperation struct {
	Kind     MutationKind
	Path     string
	MovePath string
	Content  string
	Hunks    []applyPatchHunk
}

type applyPatchHunk struct {
	Header    string
	Lines     []applyPatchLine
	EndOfFile bool
}

type applyPatchLine struct {
	Kind byte
	Text string
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
				"description": "Complete Codex apply_patch text: a `*** Begin Patch` / `*** End Patch` envelope wrapping Add/Delete/Update operations with `@@` hunks; new file contents are `+` lines. Paths are relative to the session working directory, or absolute. The first character of each hunk line must be its marker (`+` added, `-` removed, space context); do not add a space before `+` or `-`, and preserve source indentation after it (`-old` is a deletion, while ` -old` is context text). Every hunk must contain at least one `+` or `-` line. Context lines must be literal complete source lines; blank or whitespace-only lines are real source lines, not omission placeholders, and `...` never omits context. Prefer small hunks with distinctive context, and rebuild a hunk from a fresh read after a mismatch.",
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
			failedFiles = append(failedFiles, failedFile{path: o.op.Path})
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

// isApplyPatchMarker reports whether a raw patch line starts a protocol
// section such as `*** Update File:`. Only raw lines qualify: hunk and
// add-file content always carries a '+', '-', or ' ' prefix, so trimming
// before this check would misread genuine file lines like ` *** heading`
// as protocol markers and reject the patch.
func isApplyPatchMarker(line string) bool {
	return strings.HasPrefix(line, "*** ")
}

// skipApplyPatchSeparatorRun handles blank lines that models commonly insert
// between operations or hunks. A run of empty lines counts as a separator only
// when it runs all the way to the next protocol boundary (`*** ` marker, a new
// `@@` hunk header when allowHunk is set, or `*** End Patch`); it then reports
// true with the boundary index. Interior blank runs are content, not
// separators, so it reports false — with the end of the run, letting callers
// consume the whole run at once instead of re-scanning per line.
func skipApplyPatchSeparatorRun(lines []string, i int, allowHunk bool) (int, bool) {
	j := i
	for j < len(lines)-1 && lines[j] == "" {
		j++
	}
	if j == i {
		return i, false
	}
	if j == len(lines)-1 || isApplyPatchMarker(lines[j]) || (allowHunk && strings.HasPrefix(lines[j], "@@")) {
		return j, true
	}
	return j, false
}

func ParseApplyPatch(text string) (applyPatchDocument, error) {
	text = strings.ReplaceAll(strings.TrimSpace(text), "\r\n", "\n")
	lines, err := normalizeApplyPatchEnvelope(text)
	if err != nil {
		return applyPatchDocument{}, err
	}

	var doc applyPatchDocument
	for i := 1; i < len(lines)-1; {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			i++
			continue
		}
		var op applyPatchOperation
		switch {
		case strings.HasPrefix(line, ApplyPatchAddFileMarker):
			op.Kind = MutationAdd
			op.Path = strings.TrimSpace(strings.TrimPrefix(line, ApplyPatchAddFileMarker))
			i++
			var content strings.Builder
			for i < len(lines)-1 && !isApplyPatchMarker(lines[i]) {
				if next, ok := skipApplyPatchSeparatorRun(lines, i, false); ok {
					i = next
					continue
				}
				if !strings.HasPrefix(lines[i], "+") {
					return applyPatchDocument{}, fmt.Errorf("invalid add-file line %d: each line must start with +", i+1)
				}
				content.WriteString(lines[i][1:])
				content.WriteByte('\n')
				i++
			}
			op.Content = content.String()
			if op.Content == "" {
				return applyPatchDocument{}, fmt.Errorf("invalid add-file operation for %s: content is required", op.Path)
			}
		case strings.HasPrefix(line, ApplyPatchDeleteFileMarker):
			op.Kind = MutationDelete
			op.Path = strings.TrimSpace(strings.TrimPrefix(line, ApplyPatchDeleteFileMarker))
			i++
		case strings.HasPrefix(line, ApplyPatchUpdateFileMarker):
			op.Kind = MutationUpdate
			op.Path = strings.TrimSpace(strings.TrimPrefix(line, ApplyPatchUpdateFileMarker))
			i++
			if i < len(lines)-1 && strings.HasPrefix(strings.TrimSpace(lines[i]), ApplyPatchMoveToMarker) {
				op.MovePath = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(lines[i]), ApplyPatchMoveToMarker))
				i++
			}
			for i < len(lines)-1 && !isApplyPatchMarker(lines[i]) {
				if lines[i] == "" {
					if next, ok := skipApplyPatchSeparatorRun(lines, i, true); ok {
						i = next
						continue
					}
				}
				implicitFirstHunk := len(op.Hunks) == 0 && (lines[i] == "" || strings.ContainsRune(" +-", rune(lines[i][0])))
				if !strings.HasPrefix(lines[i], "@@") && !implicitFirstHunk {
					return applyPatchDocument{}, fmt.Errorf("invalid update hunk at line %d: expected @@", i+1)
				}
				var h applyPatchHunk
				hunkStartLine := i + 1
				if !implicitFirstHunk {
					h.Header = strings.TrimSpace(strings.TrimPrefix(lines[i], "@@"))
					i++
				}
				for i < len(lines)-1 && !strings.HasPrefix(lines[i], "@@") && !isApplyPatchMarker(lines[i]) {
					if lines[i] == "" {
						next, ok := skipApplyPatchSeparatorRun(lines, i, true)
						if ok {
							i = next
							continue
						}
						for i < next {
							h.Lines = append(h.Lines, applyPatchLine{Kind: ' ', Text: ""})
							i++
						}
						continue
					}
					kind := lines[i][0]
					if kind != ' ' && kind != '+' && kind != '-' {
						return applyPatchDocument{}, fmt.Errorf("invalid patch line %d: expected space, +, or - marker", i+1)
					}
					h.Lines = append(h.Lines, applyPatchLine{Kind: kind, Text: lines[i][1:]})
					i++
				}
				if i < len(lines)-1 && strings.TrimSpace(lines[i]) == "*** End of File" {
					h.EndOfFile = true
					i++
				}
				if len(h.Lines) == 0 {
					return applyPatchDocument{}, fmt.Errorf("invalid empty update hunk for %s", op.Path)
				}
				if !slices.ContainsFunc(h.Lines, func(line applyPatchLine) bool {
					return line.Kind == '+' || line.Kind == '-'
				}) {
					return applyPatchDocument{}, fmt.Errorf("invalid update hunk for %s at line %d: at least one added or removed line is required; context-only or whitespace-only lines are not omission placeholders", op.Path, hunkStartLine)
				}
				op.Hunks = append(op.Hunks, h)
			}
			if len(op.Hunks) == 0 && op.MovePath == "" {
				return applyPatchDocument{}, fmt.Errorf("invalid update operation for %s: at least one hunk is required", op.Path)
			}
		default:
			return applyPatchDocument{}, fmt.Errorf("invalid apply_patch operation at line %d: %s", i+1, line)
		}
		if strings.TrimSpace(op.Path) == "" {
			return applyPatchDocument{}, fmt.Errorf("apply_patch operation at line %d has an empty path", i+1)
		}
		doc.Operations = append(doc.Operations, op)
	}
	if len(doc.Operations) == 0 {
		return applyPatchDocument{}, fmt.Errorf("no files were modified")
	}
	return doc, nil
}

func normalizeApplyPatchEnvelope(text string) ([]string, error) {
	lines := strings.Split(text, "\n")
	strictBegin := strings.TrimSpace(lines[0]) == "*** Begin Patch"
	strictEnd := strings.TrimSpace(lines[len(lines)-1]) == "*** End Patch"
	if strictBegin && strictEnd {
		return lines, nil
	}

	normalized := append([]string(nil), lines...)
	if !strictBegin {
		first := strings.TrimSpace(normalized[0])
		switch {
		case strings.HasPrefix(first, "*** Begin Patch"):
			normalized[0] = "*** Begin Patch"
		case isApplyPatchTopLevelOperation(first):
			normalized = append([]string{"*** Begin Patch"}, normalized...)
		default:
			return nil, fmt.Errorf("invalid apply_patch: first line must be `*** Begin Patch`")
		}
	}
	if strings.TrimSpace(normalized[len(normalized)-1]) != "*** End Patch" {
		last := strings.TrimSpace(normalized[len(normalized)-1])
		switch {
		case strings.HasPrefix(last, "*** End Patch"):
			normalized[len(normalized)-1] = "*** End Patch"
		case isApplyPatchImplicitEOF(normalized[len(normalized)-1]):
			normalized = append(normalized, "*** End Patch")
		default:
			return nil, fmt.Errorf("invalid apply_patch: last line must be `*** End Patch`")
		}
	}
	return normalized, nil
}

func isApplyPatchTopLevelOperation(line string) bool {
	return strings.HasPrefix(line, ApplyPatchAddFileMarker) ||
		strings.HasPrefix(line, ApplyPatchDeleteFileMarker) ||
		strings.HasPrefix(line, ApplyPatchUpdateFileMarker)
}

func isApplyPatchImplicitEOF(line string) bool {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || trimmed == "*** End of File" {
		return true
	}
	if strings.HasPrefix(trimmed, ApplyPatchDeleteFileMarker) || strings.HasPrefix(trimmed, ApplyPatchMoveToMarker) {
		return true
	}
	if strings.HasPrefix(line, "@@") {
		return true
	}
	kind := line[0]
	return kind == ' ' || kind == '+' || kind == '-'
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
			return source, target, fmt.Errorf("apply_patch move source and target are the same: %s", source)
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

func BuildApplyPatchPlan(ctx context.Context, patch, baseDir string) (MutationPlan, error) {
	doc, err := ParseApplyPatch(patch)
	if err != nil {
		return MutationPlan{}, err
	}
	targets, err := resolveApplyPatchTargets(doc, baseDir)
	if err != nil {
		return MutationPlan{}, err
	}
	states, err := snapshotApplyPatchStates(targets)
	if err != nil {
		return MutationPlan{}, err
	}
	for _, op := range doc.Operations {
		if err := applyPatchOperationToVirtualState(ctx, states, op, baseDir); err != nil {
			return MutationPlan{}, err
		}
	}
	return buildApplyPatchMutationPlan(states), nil
}

// applyPatchOpResult describes one patch operation's application outcome.
// succeeded + rolledBack marks an op that matched in memory but was reverted
// when a later op in the same group failed; its err is descriptive only and
// does not contribute to the wrapped root-cause error.
type applyPatchOpResult struct {
	op         applyPatchOperation
	succeeded  bool
	rolledBack bool
	err        error
	reason     string
}

// ApplyPatchPlanResult bundles the commit-ready plan (built only from
// successful operations) with per-operation outcomes, so callers can report
// which files were applied and which failed. Operations touching a path left
// unchanged by a prior failed operation are marked as cascade-failed so callers
// and the model understand the dependency.
type ApplyPatchPlanResult struct {
	Plan     MutationPlan
	Outcomes []applyPatchOpResult
}

// HasFailures reports whether any operation failed (including cascade failures
// from a touched file being left unmodified by a prior failure).
func (r ApplyPatchPlanResult) HasFailures() bool {
	for _, o := range r.Outcomes {
		if !o.succeeded {
			return true
		}
	}
	return false
}

// buildApplyPatchPlanWithOutcomes keeps BuildApplyPatchPlan as the successful
// common path, then tolerates per-file failure only after atomic planning fails.
// Files are atomic units — if any operation touching a file fails, none of that
// file's operations (including earlier successful ones on the same source) are
// committed, and every operation in that group is reported as failed. Groups
// that depend on a failed move are discarded too. The failure path rebuilds
// virtual state from the envelope snapshot by replaying only the remaining
// successful operations, so move chains and dependency cycles cannot restore
// an intermediate state. Operations on independent files still apply.
func buildApplyPatchPlanWithOutcomes(ctx context.Context, patch, baseDir string) (ApplyPatchPlanResult, error) {
	plan, atomicErr := BuildApplyPatchPlan(ctx, patch, baseDir)
	if atomicErr == nil {
		return ApplyPatchPlanResult{Plan: plan}, nil
	}
	if ctx.Err() != nil {
		return ApplyPatchPlanResult{}, atomicErr
	}
	doc, err := ParseApplyPatch(patch)
	if err != nil {
		return ApplyPatchPlanResult{}, err
	}
	targets, err := resolveApplyPatchTargets(doc, baseDir)
	if err != nil {
		return ApplyPatchPlanResult{}, err
	}
	states, err := snapshotApplyPatchStates(targets)
	if err != nil {
		return ApplyPatchPlanResult{}, err
	}
	outcomes := make([]applyPatchOpResult, 0, len(doc.Operations))
	// failedPaths records every path touched by a failed group. Later operations
	// touching one of those paths must stay in the same unapplied dependency
	// chain: otherwise revising a failed move later could overwrite an apparently
	// independent operation committed to its destination in the meantime.
	failedPaths := make(map[string]struct{})
	// groupOpIndices tracks outcome indices by source so that when a file group
	// fails we can retroactively roll back every op in the group.
	groupOpIndices := make(map[string][]int)
	groupPathsBySource := make(map[string]map[string]struct{})
	// latestGroupByPath and dependentGroups capture ordering dependencies
	// between source groups. If group A touches a path and a later group B
	// touches the same path, B must be rolled back when A later fails; otherwise
	// A's rollback can erase B while B is incorrectly reported as committed.
	latestGroupByPath := make(map[string]string)
	dependentGroups := make(map[string][]string)
	dependentGroupSet := make(map[string]map[string]struct{})
	addGroupDependency := func(prerequisite, dependent string) {
		if prerequisite == "" || dependent == "" || prerequisite == dependent {
			return
		}
		if dependentGroupSet[prerequisite] == nil {
			dependentGroupSet[prerequisite] = make(map[string]struct{})
		}
		if _, exists := dependentGroupSet[prerequisite][dependent]; exists {
			return
		}
		dependentGroupSet[prerequisite][dependent] = struct{}{}
		dependentGroups[prerequisite] = append(dependentGroups[prerequisite], dependent)
	}
	rollbackGroup := func(source, reason string) error {
		if source == "" {
			return nil
		}
		rollbackSet := make(map[string]struct{})
		var collect func(string)
		collect = func(group string) {
			if group == "" {
				return
			}
			if _, seen := rollbackSet[group]; seen {
				return
			}
			rollbackSet[group] = struct{}{}
			for _, dependent := range dependentGroups[group] {
				collect(dependent)
			}
		}
		collect(source)
		for group := range rollbackSet {
			for path := range groupPathsBySource[group] {
				failedPaths[path] = struct{}{}
			}
		}
		for group := range rollbackSet {
			groupReason := reason
			if group != source {
				groupReason = fmt.Sprintf("an earlier operation group for %s failed, so this dependent group was not written to disk; keep both groups together when revising", source)
			}
			for _, idx := range groupOpIndices[group] {
				if outcomes[idx].rolledBack {
					// A later failure in an already rolled-back group must not
					// overwrite the first, more specific rollback attribution.
					continue
				}
				outcomes[idx].succeeded = false
				outcomes[idx].rolledBack = true
				rolledBackErr := fmt.Errorf("rolled back: %s", groupReason)
				outcomes[idx].err = rolledBackErr
				outcomes[idx].reason = rolledBackErr.Error()
			}
		}
		if err := replaySuccessfulApplyPatchOperations(ctx, states, outcomes, baseDir); err != nil {
			return fmt.Errorf("rebuild apply_patch partial plan: %w. No files were modified", err)
		}
		return nil
	}
	for i := 0; i < len(doc.Operations); i++ {
		op := doc.Operations[i]
		source, target, resolveErr := resolveApplyPatchOperationPaths(op, baseDir)
		groupPaths := []string{source}
		if target != "" && target != source {
			groupPaths = append(groupPaths, target)
		}
		if resolveErr != nil {
			if err := rollbackGroup(source, fmt.Sprintf("a later operation that modified %s could not be resolved; keep it with the failed operation when revising", source)); err != nil {
				return ApplyPatchPlanResult{}, err
			}
			for _, path := range groupPaths {
				if path != "" {
					failedPaths[path] = struct{}{}
				}
			}
			outcomes = append(outcomes, failedApplyPatchOpResult(op, resolveErr))
			continue
		}
		if groupPathsBySource[source] == nil {
			groupPathsBySource[source] = make(map[string]struct{})
		}
		for _, path := range groupPaths {
			groupPathsBySource[source][path] = struct{}{}
		}
		var dependencyPath string
		for _, path := range groupPaths {
			if _, failed := failedPaths[path]; failed {
				dependencyPath = path
				break
			}
		}
		if dependencyPath != "" {
			if err := rollbackGroup(source, fmt.Sprintf("a prior operation touching %s failed, so this operation was not applied and must remain with that dependency when revised", dependencyPath)); err != nil {
				return ApplyPatchPlanResult{}, err
			}
			idx := len(outcomes)
			outcomes = append(outcomes, failedApplyPatchOpResult(op, fmt.Errorf("skipped: a prior operation touching %s failed, so this operation was not applied and must remain with that dependency when revised", dependencyPath)))
			outcomes[idx].rolledBack = true
			continue
		}
		for _, path := range groupPaths {
			addGroupDependency(latestGroupByPath[path], source)
		}
		if err := applyPatchOperationToVirtualState(ctx, states, op, baseDir); err != nil {
			// File-level atomicity: discard the whole group for this source.
			// Earlier matched-but-uncommitted ops in this same group must remain
			// with the failing operation: nothing from this group was committed,
			// so revising it must preserve the matched hunks too.
			if replayErr := rollbackGroup(source, fmt.Sprintf("a later operation that modified %s failed, so this matched operation was not written to disk; keep it with the failed operation when revising the group", source)); replayErr != nil {
				return ApplyPatchPlanResult{}, replayErr
			}
			outcomes = append(outcomes, failedApplyPatchOpResult(op, err))
			continue
		}
		idx := len(outcomes)
		outcomes = append(outcomes, applyPatchOpResult{op: op, succeeded: true})
		groupOpIndices[source] = append(groupOpIndices[source], idx)
		for _, path := range groupPaths {
			latestGroupByPath[path] = source
		}
	}
	return ApplyPatchPlanResult{
		Plan:     buildApplyPatchMutationPlan(states),
		Outcomes: outcomes,
	}, nil
}

func replaySuccessfulApplyPatchOperations(ctx context.Context, states map[string]*applyPatchVirtualFile, outcomes []applyPatchOpResult, baseDir string) error {
	for path, state := range states {
		state.displayPath = path
		state.originPath = ""
		state.exists = state.initialExists
		state.bytes = append(state.bytes[:0], state.initialBytes...)
		state.mode = state.initialMode
		state.touched = false
		state.punctuationHunks = 0
		state.fuzzyHunks = 0
		state.fuzzyReplacements = nil
		state.cleanedInvisible = nil
		if state.initialExists {
			state.originPath = path
		}
	}
	for _, outcome := range outcomes {
		if !outcome.succeeded {
			continue
		}
		if err := applyPatchOperationToVirtualState(ctx, states, outcome.op, baseDir); err != nil {
			return err
		}
	}
	return nil
}

// failedApplyPatchOpResult builds an outcome for an operation that could not be
// applied, carrying the original error so callers can wrap it with %w (keeping
// error-substring assertions working) while also exposing a flat reason string
// for display.
func failedApplyPatchOpResult(op applyPatchOperation, err error) applyPatchOpResult {
	if err == nil {
		err = fmt.Errorf("apply_patch operation failed for %s", op.Path)
	}
	return applyPatchOpResult{op: op, err: err, reason: err.Error()}
}

type applyPatchVirtualFile struct {
	path              string
	displayPath       string
	originPath        string
	initialExists     bool
	initialBytes      []byte
	initialMode       os.FileMode
	exists            bool
	bytes             []byte
	mode              os.FileMode
	touched           bool
	punctuationHunks  int
	fuzzyHunks        int
	fuzzyReplacements []applyPatchFuzzyReplacement
	// cleanedInvisible accumulates the runes stripped from this file's
	// model-added text while the plan is built (see cleanApplyPatchAddedLines)
	// and is copied onto the committed mutation so the result note can
	// report them.
	cleanedInvisible map[rune]int
}

func snapshotApplyPatchStates(targets []MutationTarget) (map[string]*applyPatchVirtualFile, error) {
	states := make(map[string]*applyPatchVirtualFile, len(targets)*2)
	type existingFile struct {
		path string
		info os.FileInfo
	}
	var existingFiles []existingFile
	for _, target := range targets {
		for _, path := range []string{target.SourcePath, target.TargetPath} {
			if path == "" {
				continue
			}
			if _, ok := states[path]; ok {
				continue
			}
			state := &applyPatchVirtualFile{path: path, displayPath: path}
			info, err := os.Lstat(path)
			if os.IsNotExist(err) {
				states[path] = state
				continue
			}
			if err != nil {
				return nil, fmt.Errorf("inspect apply_patch path %s: %w. No files were modified", path, err)
			}
			if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
				return nil, fmt.Errorf("apply_patch path is not a regular file: %s. No files were modified", path)
			}
			for _, existing := range existingFiles {
				if os.SameFile(existing.info, info) {
					return nil, fmt.Errorf("apply_patch contains overlapping operations for %s and %s", existing.path, path)
				}
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return nil, fmt.Errorf("read apply_patch path %s: %w. No files were modified", path, err)
			}
			state.initialExists = true
			// ReadFile's buffer has no other referent: transfer ownership to
			// the immutable initial snapshot and copy only the working bytes.
			state.initialBytes = data
			state.initialMode = info.Mode()
			state.originPath = path
			state.exists = true
			state.bytes = append([]byte(nil), data...)
			state.mode = info.Mode()
			states[path] = state
			existingFiles = append(existingFiles, existingFile{path: path, info: info})
		}
	}
	return states, nil
}

func applyPatchOperationToVirtualState(ctx context.Context, states map[string]*applyPatchVirtualFile, op applyPatchOperation, baseDir string) error {
	source, err := resolveApplyPatchPath(op.Path, baseDir)
	if err != nil {
		return err
	}
	state := states[source]
	state.displayPath = op.Path
	state.touched = true
	switch op.Kind {
	case MutationAdd:
		if state.exists {
			return fmt.Errorf("cannot add file that already exists: %s", op.Path)
		}
		state.mode = 0o644
		state.exists = true
		// Add-file content is entirely model text: strip floating combining
		// marks here, the same rule the update path applies to its + lines.
		content := op.Content
		if cleaned := StripOrphanCombiningMarks(content); cleaned != content {
			state.cleanedInvisible = CountStrippedInvisible(content, cleaned)
			content = cleaned
		}
		state.bytes = []byte(content)
		state.originPath = ""
		return nil
	case MutationDelete:
		if !state.exists {
			return applyPatchMissingSourceError(op.Path, baseDir)
		}
		state.exists = false
		state.bytes = nil
		state.originPath = ""
		state.cleanedInvisible = nil
		return nil
	case MutationUpdate:
		if !state.exists {
			return applyPatchMissingSourceError(op.Path, baseDir)
		}
		if len(op.Hunks) > 0 {
			// Only the model's added (+) lines carry model text into the
			// file — context and removed lines mirror the file's own bytes —
			// so floating combining marks are stripped from those lines
			// before the hunks apply. Untouched file content is never
			// rewritten by the invisible-character clean.
			hunks, stripped := cleanApplyPatchAddedLines(op.Hunks)
			decoded, err := decodeTextBytes(state.bytes, source)
			if err != nil {
				return fmt.Errorf("read update source %s: %w", op.Path, err)
			}
			after, punctuationHunks, fuzzyHunks, fuzzyReplacements, err := applyApplyPatchHunks(ctx, decoded.Text, hunks)
			if err != nil {
				return fmt.Errorf("update %s: %w", op.Path, err)
			}
			state.punctuationHunks += punctuationHunks
			state.fuzzyHunks += fuzzyHunks
			state.fuzzyReplacements = append(state.fuzzyReplacements, fuzzyReplacements...)
			if len(stripped) > 0 {
				if state.cleanedInvisible == nil {
					state.cleanedInvisible = map[rune]int{}
				}
				for r, n := range stripped {
					state.cleanedInvisible[r] += n
				}
			}
			state.bytes, err = encodeString(after, decoded.Encoding)
			if err != nil {
				return fmt.Errorf("encode update %s: %w", op.Path, err)
			}
		}
		if op.MovePath == "" {
			return nil
		}
		targetPath, err := resolveApplyPatchPath(op.MovePath, baseDir)
		if err != nil {
			return err
		}
		if targetPath == source {
			return fmt.Errorf("apply_patch move source and target are the same: %s", source)
		}
		target := states[targetPath]
		target.displayPath = op.MovePath
		target.touched = true
		target.exists = true
		target.bytes = append([]byte(nil), state.bytes...)
		target.mode = state.mode
		target.originPath = state.originPath
		target.punctuationHunks += state.punctuationHunks
		target.fuzzyHunks += state.fuzzyHunks
		target.fuzzyReplacements = append(target.fuzzyReplacements, state.fuzzyReplacements...)
		if len(state.cleanedInvisible) > 0 {
			if target.cleanedInvisible == nil {
				target.cleanedInvisible = map[rune]int{}
			}
			for r, n := range state.cleanedInvisible {
				target.cleanedInvisible[r] += n
			}
			state.cleanedInvisible = nil
		}
		state.exists = false
		state.bytes = nil
		state.originPath = ""
		state.punctuationHunks = 0
		state.fuzzyHunks = 0
		state.fuzzyReplacements = nil
		return nil
	default:
		return fmt.Errorf("unsupported apply_patch operation %q", op.Kind)
	}
}

func applyPatchMissingSourceError(displayPath, baseDir string) error {
	return withPathSuggestionsInDir(
		fmt.Sprintf("read apply_patch source %s: file not found: %s", displayPath, displayPath),
		displayPath, baseDir, PathTargetRegularFile,
	)
}

func buildApplyPatchMutationPlan(states map[string]*applyPatchVirtualFile) MutationPlan {
	paths := make([]string, 0, len(states))
	for path, state := range states {
		if state.touched {
			paths = append(paths, path)
		}
	}
	sort.Strings(paths)
	plan := MutationPlan{Mutations: make([]PlannedMutation, 0, len(paths))}
	consumed := make(map[string]struct{})
	for _, sourcePath := range paths {
		source := states[sourcePath]
		if !source.initialExists || source.exists {
			continue
		}
		for _, targetPath := range paths {
			target := states[targetPath]
			if targetPath == sourcePath || !target.exists || target.originPath != sourcePath {
				continue
			}
			mutation := PlannedMutation{
				Kind:               MutationMove,
				SourcePath:         sourcePath,
				TargetPath:         targetPath,
				BeforeExists:       true,
				BeforeBytes:        append([]byte(nil), source.initialBytes...),
				BeforeMode:         source.initialMode,
				TargetBeforeExists: target.initialExists,
				TargetBeforeBytes:  append([]byte(nil), target.initialBytes...),
				TargetBeforeMode:   target.initialMode,
				AfterBytes:         append([]byte(nil), target.bytes...),
				AfterMode:          target.mode,
				PunctuationHunks:   target.punctuationHunks,
				FuzzyHunks:         target.fuzzyHunks,
				FuzzyReplacements:  target.fuzzyReplacements,
				StrippedInvisible:  target.cleanedInvisible,
			}
			populateApplyPatchMutationDiff(&mutation, source.displayPath, target.displayPath)
			plan.Mutations = append(plan.Mutations, mutation)
			consumed[sourcePath] = struct{}{}
			consumed[targetPath] = struct{}{}
			break
		}
	}
	for _, path := range paths {
		if _, ok := consumed[path]; ok {
			continue
		}
		state := states[path]
		if !state.initialExists && !state.exists {
			continue
		}
		if state.initialExists && state.exists && state.initialMode == state.mode && bytes.Equal(state.initialBytes, state.bytes) {
			continue
		}
		mutation := PlannedMutation{
			SourcePath:        path,
			TargetPath:        path,
			BeforeExists:      state.initialExists,
			BeforeBytes:       append([]byte(nil), state.initialBytes...),
			BeforeMode:        state.initialMode,
			AfterBytes:        append([]byte(nil), state.bytes...),
			AfterMode:         state.mode,
			PunctuationHunks:  state.punctuationHunks,
			FuzzyHunks:        state.fuzzyHunks,
			FuzzyReplacements: state.fuzzyReplacements,
		}
		switch {
		case !state.initialExists && state.exists:
			mutation.Kind = MutationAdd
		case state.initialExists && !state.exists:
			mutation.Kind = MutationDelete
		default:
			mutation.Kind = MutationUpdate
		}
		if mutation.Kind != MutationDelete {
			// A delete writes no bytes, so nothing stripped for it is
			// reported; add/update mutations carry their planned clean
			// counts for the Execute result note.
			mutation.StrippedInvisible = state.cleanedInvisible
		}
		populateApplyPatchMutationDiff(&mutation, state.displayPath, state.displayPath)
		plan.Mutations = append(plan.Mutations, mutation)
	}
	sort.Slice(plan.Mutations, func(i, j int) bool {
		if plan.Mutations[i].SourcePath != plan.Mutations[j].SourcePath {
			return plan.Mutations[i].SourcePath < plan.Mutations[j].SourcePath
		}
		return plan.Mutations[i].TargetPath < plan.Mutations[j].TargetPath
	})
	return plan
}

func populateApplyPatchMutationDiff(mutation *PlannedMutation, oldDisplayPath, newDisplayPath string) {
	if mutation == nil {
		return
	}
	oldText, oldTextOK := decodedText{}, false
	if mutation.BeforeExists {
		if decoded, err := decodeTextBytes(mutation.BeforeBytes, mutation.SourcePath); err == nil {
			oldText, oldTextOK = decoded, true
		}
	}
	newText, newTextOK := decodedText{}, false
	if mutation.Kind != MutationDelete {
		if decoded, err := decodeTextBytes(mutation.AfterBytes, mutation.TargetPath); err == nil {
			newText, newTextOK = decoded, true
			mutation.AfterText = decoded.Text
		}
	}
	switch mutation.Kind {
	case MutationAdd:
		if newTextOK {
			mutation.Added = lineCountForMutation(newText.Text)
			mutation.diffInput = unifiedFileDiff{NewContent: newText.Text, OldFilename: oldDisplayPath, NewFilename: newDisplayPath}
			mutation.diffable = true
		}
	case MutationDelete:
		if oldTextOK {
			mutation.Removed = lineCountForMutation(oldText.Text)
			mutation.diffInput = unifiedFileDiff{OldContent: oldText.Text, OldFilename: oldDisplayPath, NewFilename: newDisplayPath}
			mutation.diffable = true
		}
	case MutationUpdate, MutationMove:
		if oldTextOK && newTextOK {
			diff := GenerateUnifiedDiffSummary(oldText.Text, newText.Text, oldDisplayPath)
			mutation.Added, mutation.Removed = diff.Added, diff.Removed
			mutation.diffInput = unifiedFileDiff{OldContent: oldText.Text, NewContent: newText.Text, OldFilename: oldDisplayPath, NewFilename: newDisplayPath}
			mutation.diffable = true
		}
	}
}

func applyPatchMutationDiffSummary(plan MutationPlan) DiffSummary {
	files := make([]unifiedFileDiff, 0, len(plan.Mutations))
	added := 0
	removed := 0
	for _, mutation := range plan.Mutations {
		added += mutation.Added
		removed += mutation.Removed
		if mutation.diffable {
			files = append(files, mutation.diffInput)
		}
	}
	summary := generateMultiFileUnifiedDiffSummary(files)
	summary.Added = added
	summary.Removed = removed
	return summary
}

// cleanApplyPatchAddedLines strips floating combining marks (see
// StripOrphanCombiningMarks) from the model-added (+) lines of the hunks and
// returns the cleaned copy together with what was removed. Only + lines carry
// model text into the file — context and removed lines mirror the file's own
// bytes — so the invisible-character clean never rewrites file content the
// patch left alone. Returns the original hunks and a nil count map when
// nothing was stripped.
func cleanApplyPatchAddedLines(hunks []applyPatchHunk) ([]applyPatchHunk, map[rune]int) {
	needClean := false
	for _, h := range hunks {
		for _, l := range h.Lines {
			if l.Kind == '+' && hasOrphanMarkCandidate(l.Text) {
				needClean = true
				break
			}
		}
		if needClean {
			break
		}
	}
	if !needClean {
		return hunks, nil
	}
	cleaned := make([]applyPatchHunk, len(hunks))
	var counts map[rune]int
	for i, h := range hunks {
		lines := make([]applyPatchLine, len(h.Lines))
		for j, l := range h.Lines {
			if l.Kind == '+' {
				if text := StripOrphanCombiningMarks(l.Text); text != l.Text {
					for r, n := range CountStrippedInvisible(l.Text, text) {
						if counts == nil {
							counts = map[rune]int{}
						}
						counts[r] += n
					}
					l.Text = text
				}
			}
			lines[j] = l
		}
		h.Lines = lines
		cleaned[i] = h
	}
	return cleaned, counts
}

func applyApplyPatchHunks(ctx context.Context, content string, hunks []applyPatchHunk) (string, int, int, []applyPatchFuzzyReplacement, error) {
	newline := "\n"
	if strings.Contains(content, "\r\n") {
		newline = "\r\n"
	}
	logical := strings.ReplaceAll(content, "\r\n", "\n")
	var fileLines []string
	if logical != "" {
		fileLines = strings.Split(strings.TrimSuffix(logical, "\n"), "\n")
	}
	searchStart := 0
	punctuationHunks := 0
	fuzzyHunks := 0
	var fuzzyReplacements []applyPatchFuzzyReplacement
	for i, hunk := range hunks {
		if len(hunks) > 1 {
			reportToolProgress(ctx, ToolProgressSnapshot{Text: fmt.Sprintf("matching hunk %d/%d", i+1, len(hunks))})
		}
		headerPos := -1
		if hunk.Header != "" {
			if pos := findApplyPatchSequence(fileLines, []string{hunk.Header}, searchStart, false); pos >= 0 {
				headerPos = pos
				searchStart = pos + 1
			}
		}
		oldSeq := make([]string, 0, len(hunk.Lines))
		for _, line := range hunk.Lines {
			if line.Kind == ' ' || line.Kind == '-' {
				oldSeq = append(oldSeq, line.Text)
			}
		}
		match := -1
		if len(oldSeq) == 0 {
			match = len(fileLines)
		} else {
			match = findApplyPatchSequence(fileLines, oldSeq, searchStart, hunk.EndOfFile)
		}
		if len(oldSeq) > 0 && match < 0 && headerPos >= 0 {
			// The canonical format puts context strictly after the @@ header, but
			// models often repeat the header text as the hunk's first context line.
			// Retry from the header itself so both styles anchor to one location.
			match = findApplyPatchSequence(fileLines, oldSeq, headerPos, hunk.EndOfFile)
		}
		punctuationMatch := false
		fuzzyMatch := false
		fuzzyRemovedIndex := -1
		var punctuationCandidates []int
		var fuzzyCandidates []int
		if match < 0 && len(oldSeq) > 0 {
			match, punctuationCandidates = findUniqueApplyPatchSequence(
				fileLines, oldSeq, searchStart, hunk.EndOfFile, normalizePatchTolerantLine,
			)
			if match >= 0 {
				punctuationMatch = true
			}
		}
		if match < 0 && len(oldSeq) > 0 {
			match, fuzzyRemovedIndex, fuzzyCandidates = findUniqueFuzzyApplyPatchMatch(fileLines, hunk, oldSeq, searchStart)
			if match >= 0 {
				fuzzyMatch = true
			}
		}
		if match < 0 {
			return "", 0, 0, nil, applyPatchPartialHunkError(applyPatchHunkNotFoundErrorWithHints(fileLines, oldSeq, searchStart, i, len(hunks), hunk.EndOfFile, punctuationCandidates, fuzzyCandidates, hunkHasWhitespaceOnlyContext(hunk)), i, len(hunks))
		}
		matched := fileLines[match : match+len(oldSeq)]
		newSeq := buildApplyPatchNewSequence(hunk, matched)
		if punctuationMatch {
			var ok bool
			newSeq, ok = buildPunctuationTolerantApplyPatchSequence(hunk, matched)
			if !ok {
				return "", 0, 0, nil, applyPatchPartialHunkError(applyPatchUnsafePunctuationMatchError(oldSeq, i, len(hunks), match), i, len(hunks))
			}
			punctuationHunks++
		}
		if fuzzyMatch {
			fuzzyHunks++
			// The removed line's offset comes from the matcher itself, which
			// derived it from the same guards that admitted the hunk. Deriving
			// it a second time here would be a copy of those guards that a
			// future relaxation could silently outgrow — the old copy indexed
			// oldSeq[-1] whenever the removed line was no longer guaranteed to
			// exist.
			var addedText string
			for _, line := range hunk.Lines {
				if line.Kind == '+' {
					addedText = line.Text
				}
			}
			fuzzyReplacements = append(fuzzyReplacements, applyPatchFuzzyReplacement{
				removed: oldSeq[fuzzyRemovedIndex],
				actual:  fileLines[match+fuzzyRemovedIndex],
				added:   addedText,
				line:    match + fuzzyRemovedIndex + 1,
			})
		}
		replaced := make([]string, 0, len(fileLines)-len(oldSeq)+len(newSeq))
		replaced = append(replaced, fileLines[:match]...)
		replaced = append(replaced, newSeq...)
		replaced = append(replaced, fileLines[match+len(oldSeq):]...)
		fileLines = replaced
		if len(oldSeq) > 0 {
			searchStart = match + len(newSeq)
		}
	}
	out := strings.Join(fileLines, "\n")
	if len(fileLines) > 0 {
		out += "\n"
	}
	if newline == "\r\n" {
		out = strings.ReplaceAll(out, "\n", "\r\n")
	}
	return out, punctuationHunks, fuzzyHunks, fuzzyReplacements, nil
}

// The fuzzy layer's acceptance gates. It is the only layer that writes the
// model's line over a file line the model quoted wrongly, so it is bounded on
// two axes at once:
//
//   - maxApplyPatchFuzzyRuneDistance is the hard limit: at most one rune of
//     the normalized removed line may differ from the file's. Everything a
//     tolerant normalizer can forgive (punctuation variants, indentation,
//     inter-word and repeated whitespace) has already been folded out by the
//     time a candidate reaches here, so a surviving difference is real
//     content, and one rune is where a transcription slip stops and a
//     semantic change begins.
//   - minApplyPatchFuzzySimilarity keeps the proportional check as a second,
//     narrower gate: on a very short line even one rune is most of the line.
//     It cannot be the only gate — a ratio grows the allowance with line
//     length (0.9 permits six runes on a 66-rune line), which is backwards:
//     "const n = 30" vs "const n = 10" scores 0.909 while being a real value
//     change.
//
// minApplyPatchFuzzyContextRunes is the distinctiveness floor for context:
// at least one context line must carry this many runes after normalization,
// so a hunk anchored only on "}" and "return" is not treated as uniquely
// placed.
const (
	maxApplyPatchFuzzyRuneDistance = 1
	minApplyPatchFuzzySimilarity   = 0.9
	minApplyPatchFuzzyContextRunes = 4
)

// applyPatchFuzzyReplacement records one fuzzy hunk replacement for the result
// Note so the model can audit what was actually overwritten: removed is the
// hunk's claimed line, actual is the file line it replaced, added is the line
// written in their place, and line is the 1-based position of the replaced
// line in the file as it stood when the hunk was applied (earlier hunks of the
// same patch have already shifted it).
type applyPatchFuzzyReplacement struct {
	removed string
	actual  string
	added   string
	line    int
}

// findUniqueFuzzyApplyPatchMatch permits only a narrow stale-line recovery:
// one changed removed line differing from the file by at most
// maxApplyPatchFuzzyRuneDistance runes after normalization, distinctive
// unchanged context on both sides of it, and exactly one candidate window. The
// matched current line is replaced, while all context bytes are preserved from
// the file.
//
// It returns the matched window start, the removed line's offset inside the
// window (so the caller can quote and locate the replaced line without
// recomputing an offset that must stay in step with these guards), and the
// candidate list for the ambiguity hint. removedIndex is -1 when there is no
// unique match.
func findUniqueFuzzyApplyPatchMatch(fileLines []string, hunk applyPatchHunk, oldSeq []string, searchStart int) (match, removedIndex int, candidates []int) {
	removedIndex = -1
	removedCount := 0
	addedCount := 0
	distinctiveContext := false
	for _, line := range hunk.Lines {
		switch line.Kind {
		case '-':
			removedCount++
		case '+':
			addedCount++
		case ' ':
			norm := normalizePatchTolerantLine(line.Text)
			if norm == "" {
				return -1, -1, nil
			}
			// Distinctiveness is a per-line property, not a sum: "}" plus
			// "return" clears a combined four runes while anchoring the hunk
			// to boilerplate that repeats all over the file.
			if len([]rune(norm)) >= minApplyPatchFuzzyContextRunes {
				distinctiveContext = true
			}
		}
	}
	if removedCount != 1 || addedCount != 1 || !distinctiveContext {
		return -1, -1, nil
	}
	oldIndex := 0
	contextBefore := false
	contextAfter := false
	for _, line := range hunk.Lines {
		switch line.Kind {
		case '-':
			removedIndex = oldIndex
			oldIndex++
		case ' ':
			if removedIndex < 0 {
				contextBefore = true
			} else {
				contextAfter = true
			}
			oldIndex++
		}
	}
	// contextBefore && contextAfter already implies at least two context
	// lines, so no separate count check is needed.
	if removedIndex < 0 || !contextBefore || !contextAfter || oldIndex != len(oldSeq) {
		return -1, -1, nil
	}
	normOld := make([]string, len(oldSeq))
	for i, line := range oldSeq {
		normOld[i] = normalizePatchTolerantLine(line)
	}
	oldRemoved := normOld[removedIndex]
	if oldRemoved == "" {
		return -1, -1, nil
	}
	oldRemovedRunes := len([]rune(oldRemoved))

	maxStart := len(fileLines) - len(oldSeq)
	if maxStart < 0 {
		return -1, -1, nil
	}
	start := max(0, searchStart)
	if hunk.EndOfFile {
		start = maxStart
		maxStart = start
	}
	if start > maxStart {
		return -1, -1, nil
	}
	// Normalize each file line in the search window once, not once per
	// candidate position it participates in — mirroring
	// findUniqueApplyPatchSequence, where the same quadratic re-normalization
	// was the cost being removed.
	normFile := make([]string, len(fileLines)-start)
	for i := start; i < len(fileLines); i++ {
		normFile[i-start] = normalizePatchTolerantLine(fileLines[i])
	}
	// Distance work is charged against a budget, like
	// applyPatchHunkClosestLine's closestScanBudget: a large file must not turn
	// a near-miss hunk into a full-file Levenshtein sweep. An exhausted budget
	// abandons the fuzzy layer entirely (returning no candidate) rather than
	// accepting whatever it found first, so the verdict never depends on where
	// the budget happened to run out.
	const fuzzyScanBudget = 200_000 // total (removed × line) rune-pair budget
	budget := fuzzyScanBudget
	for candidate := start; candidate <= maxStart; candidate++ {
		matches := true
		for i := range oldSeq {
			if i == removedIndex {
				continue
			}
			if normFile[candidate-start+i] != normOld[i] {
				matches = false
				break
			}
		}
		if !matches {
			continue
		}
		actual := normFile[candidate-start+removedIndex]
		if actual == oldRemoved {
			continue
		}
		actualRunes := len([]rune(actual))
		if actualRunes == 0 {
			continue
		}
		// A length gap alone is a lower bound on the edit distance, so an
		// over-long candidate is rejected without running the distance.
		if absInt(actualRunes-oldRemovedRunes) > maxApplyPatchFuzzyRuneDistance {
			continue
		}
		work := oldRemovedRunes * actualRunes
		if work > budget {
			return -1, -1, nil
		}
		budget -= work
		distance := levenshteinDistance(oldRemoved, actual)
		if distance > maxApplyPatchFuzzyRuneDistance {
			continue
		}
		longer := max(oldRemovedRunes, actualRunes)
		if 1-float64(distance)/float64(longer) < minApplyPatchFuzzySimilarity {
			continue
		}
		candidates = append(candidates, candidate)
	}
	if len(candidates) == 1 {
		return candidates[0], removedIndex, candidates
	}
	return -1, -1, candidates
}

func buildApplyPatchNewSequence(hunk applyPatchHunk, matched []string) []string {
	newSeq := make([]string, 0, len(hunk.Lines))
	oldIndex := 0
	for _, line := range hunk.Lines {
		switch line.Kind {
		case ' ':
			newSeq = append(newSeq, matched[oldIndex])
			oldIndex++
		case '-':
			oldIndex++
		case '+':
			newSeq = append(newSeq, line.Text)
		}
	}
	return newSeq
}

func buildPunctuationTolerantApplyPatchSequence(hunk applyPatchHunk, matched []string) ([]string, bool) {
	newSeq := make([]string, 0, len(hunk.Lines))
	oldIndex := 0
	for i := 0; i < len(hunk.Lines); {
		if hunk.Lines[i].Kind == ' ' {
			newSeq = append(newSeq, matched[oldIndex])
			oldIndex++
			i++
			continue
		}

		var removed, added []string
		var matchedRemoved []string
		for i < len(hunk.Lines) && hunk.Lines[i].Kind != ' ' {
			switch hunk.Lines[i].Kind {
			case '-':
				removed = append(removed, hunk.Lines[i].Text)
				matchedRemoved = append(matchedRemoved, matched[oldIndex])
				oldIndex++
			case '+':
				added = append(added, hunk.Lines[i].Text)
			}
			i++
		}

		switch {
		case len(removed) == 0:
			newSeq = append(newSeq, added...)
		case len(added) == 0:
			continue
		case len(removed) != len(added):
			return nil, false
		default:
			for j := range removed {
				line, ok := punctuationTolerantReplacementLine(matchedRemoved[j], removed[j], added[j])
				if !ok {
					return nil, false
				}
				newSeq = append(newSeq, line)
			}
		}
	}
	return newSeq, true
}

func punctuationTolerantReplacementLine(current, oldText, newText string) (string, bool) {
	if current == oldText {
		return newText, true
	}
	currentRunes := []rune(current)
	oldRunes := []rune(oldText)
	newRunes := []rune(newText)
	normCurrent, currentSpans := normalizePunctLineWithSpaceFolding(currentRunes)
	normOld, oldSpans := normalizePunctLineWithSpaceFolding(oldRunes)
	if !slices.Equal(normCurrent, normOld) {
		return "", false
	}
	normNew, newSpans := normalizePunctLineWithSpaceFolding(newRunes)

	// Common prefix/suffix in normalized space, extended only while the
	// original bytes also match: where old/new differ in original bytes
	// (e.g. "：" vs ": "), the difference is the model's intended delta and
	// must come from newText, not from the file's bytes. This keeps the
	// file's own punctuation in any unchanged context.
	prefix := 0
	for prefix < len(normOld) && prefix < len(normNew) &&
		normOld[prefix] == normNew[prefix] &&
		slices.Equal(oldRunes[oldSpans[prefix].start:oldSpans[prefix].end],
			newRunes[newSpans[prefix].start:newSpans[prefix].end]) {
		prefix++
	}
	suffix := 0
	for suffix < len(normOld)-prefix && suffix < len(normNew)-prefix {
		oi := len(normOld) - 1 - suffix
		ni := len(normNew) - 1 - suffix
		if normOld[oi] != normNew[ni] ||
			!slices.Equal(oldRunes[oldSpans[oi].start:oldSpans[oi].end],
				newRunes[newSpans[ni].start:newSpans[ni].end]) {
			break
		}
		suffix++
	}
	// prefix == 0 && suffix == 0 (every rune of the replacement differs from
	// the file's bytes) needs no special refusal: there is no unchanged text
	// to preserve, so splicing the model's new line verbatim is exactly the
	// requested edit — the same outcome the edit tool's tolerant path allows.

	var b strings.Builder
	if prefix > 0 {
		b.WriteString(string(currentRunes[currentSpans[0].start:currentSpans[prefix-1].end]))
	}
	deltaStart := prefix
	deltaEnd := len(normNew) - suffix
	if deltaStart < deltaEnd {
		b.WriteString(string(newRunes[newSpans[deltaStart].start:newSpans[deltaEnd-1].end]))
	}
	if suffix > 0 {
		b.WriteString(string(currentRunes[currentSpans[len(normCurrent)-suffix].start:currentSpans[len(normCurrent)-1].end]))
	}
	return b.String(), true
}

func findUniqueApplyPatchSequence(lines, pattern []string, start int, eof bool, normalize func(string) string) (int, []int) {
	if len(pattern) == 0 || len(pattern) > len(lines) {
		return -1, nil
	}
	from := max(start, 0)
	to := len(lines) - len(pattern)
	if eof {
		from = to
	}
	var candidates []int
	// See findApplyPatchSequence: normalize the pattern once, not per position.
	// File lines are also normalized once (into a slice aligned with `from`)
	// rather than once per candidate position, so a long file does not pay the
	// per-line normalization cost once for every position it appears in.
	normalizedPattern := make([]string, len(pattern))
	for j, line := range pattern {
		normalizedPattern[j] = normalize(line)
	}
	normalizedLines := make([]string, len(lines)-from)
	for i := from; i < len(lines); i++ {
		normalizedLines[i-from] = normalize(lines[i])
	}
	for i := from; i <= to; i++ {
		matched := true
		for j := range normalizedPattern {
			if normalizedLines[i-from+j] != normalizedPattern[j] {
				matched = false
				break
			}
		}
		if matched {
			candidates = append(candidates, i)
		}
	}
	if len(candidates) == 1 {
		return candidates[0], candidates
	}
	return -1, candidates
}

func hunkHasWhitespaceOnlyContext(hunk applyPatchHunk) bool {
	for _, line := range hunk.Lines {
		if line.Kind == ' ' && strings.TrimSpace(line.Text) == "" {
			return true
		}
	}
	return false
}

func applyPatchHunkNotFoundError(fileLines, oldSeq []string, searchStart, index, total int, hunkEndOfFile bool, punctuationCandidates []int) error {
	return applyPatchHunkNotFoundErrorWithHints(fileLines, oldSeq, searchStart, index, total, hunkEndOfFile, punctuationCandidates, nil, false)
}

func applyPatchHunkNotFoundErrorWithHints(fileLines, oldSeq []string, searchStart, index, total int, hunkEndOfFile bool, punctuationCandidates, fuzzyCandidates []int, whitespaceOnlyContext bool) error {
	parts := []string{fmt.Sprintf("hunk not found (%d/%d)", index+1, total)}
	if whitespaceOnlyContext {
		parts = append(parts, "whitespace-only context lines are literal source lines; the leading space marks context, so do not use them as omitted-line placeholders")
	}
	if expected := applyPatchExpectedLineDescription(oldSeq); expected != "" {
		parts = append(parts, expected)
	}
	if len(fuzzyCandidates) > 1 {
		parts = append(parts, "safe fuzzy matching is ambiguous at lines "+formatApplyPatchCandidateLines(fuzzyCandidates))
	}
	if len(punctuationCandidates) > 1 {
		parts = append(parts, tolerantMatchNote+" matching is ambiguous at lines "+formatApplyPatchCandidateLines(punctuationCandidates))
	}
	if earlier := findApplyPatchSequence(fileLines, oldSeq, 0, false); earlier >= 0 && earlier < searchStart {
		parts = append(parts, fmt.Sprintf("matching context exists earlier at line %d, but hunks must follow file order", earlier+1))
	} else if line := findApplyPatchSubstringLine(fileLines, oldSeq, searchStart, hunkEndOfFile); line >= 0 {
		parts = append(parts, fmt.Sprintf("the expected text is only part of current line %d; include that complete line in the hunk", line+1))
	} else if line, matched := applyPatchHunkMismatchLine(fileLines, oldSeq, searchStart, hunkEndOfFile); matched >= 1 && matched < len(oldSeq) {
		// The first expected line exists in the file (normalized, at or after
		// the hunk's legal search window), but the hunk's multi-line sequence
		// breaks somewhere. Pinpoint the first diverging line so the model can
		// see what actually changed.
		detail := fmt.Sprintf("the first %d line(s) of the hunk match at line %d, but the next expected line differs from the file", matched, line+1)
		expected := truncateToolLine(oldSeq[matched])
		if line+matched < len(fileLines) {
			detail += fmt.Sprintf(": expected %s, found %s", expected, truncateToolLine(fileLines[line+matched]))
			// Same invisible-difference visibility as edit: name the exact
			// first differing rune by code point, so a dropped space or an
			// orphan variation selector shows up instead of rendering
			// identically to the expected line.
			if off, er, ar, ep, ap := firstRuneDiffLoc(oldSeq[matched], fileLines[line+matched]); ep || ap {
				yourTok, fileTok := toolRuneToken(er, ep), toolRuneToken(ar, ap)
				if !ep {
					yourTok = "no characters (your line is empty)"
				}
				if !ap {
					fileTok = "no characters (file line is empty)"
				}
				detail += fmt.Sprintf("; first mismatch at rune %d: your line has %s, file has %s", off, yourTok, fileTok)
			}
		} else {
			detail += fmt.Sprintf(": expected %s, but the file has no more lines", expected)
		}
		// Same whole-line-drift visibility as edit: when the hunk and the
		// file window differ by whole lines (extra blanks, a heading shifted
		// by one line), the line-level alignment names the drift so the model
		// is not left staring at two unrelated position-shifted lines.
		window := fileLines
		if line+len(oldSeq) <= len(fileLines) {
			window = fileLines[line : line+len(oldSeq)]
		} else if line < len(fileLines) {
			window = fileLines[line:]
		}
		oldExtra, srcExtra, blankOnly := alignEditWindowLines(oldSeq, window, 0, normalizePatchTolerantLine)
		if oldExtra > 0 || srcExtra > 0 {
			blankNote := ""
			if blankOnly {
				blankNote = " — the extra lines are blank, so the blank-line count differs"
			}
			detail += fmt.Sprintf("; line-count difference: your hunk has %d extra line(s), the file has %d extra line(s)%s", oldExtra, srcExtra, blankNote)
		}
		parts = append(parts, detail+"; the file may have changed — re-read the current target range and rebuild this hunk from current complete lines")
	} else if len(punctuationCandidates) <= 1 && len(fuzzyCandidates) <= 1 {
		// With multiple tolerant candidates the match is ambiguous; a single
		// closest line would masquerade as the unique suggestion and
		// contradict the ambiguity note above, so require more context
		// instead of guessing.
		if line, sim := applyPatchHunkClosestLine(fileLines, oldSeq, searchStart, hunkEndOfFile); line >= 0 && sim >= minEditSuggestionSimilarity {
			// No line of the file matches the hunk's first expected line (even
			// under normalization). Point at the file line most similar to it
			// so the model sees what the file
			// actually contains instead of guessing. Below the threshold the
			// mismatch is too large for a helpful near-match, so fall through to
			// the generic missing-line hint.
			detail := fmt.Sprintf("the first line of the hunk matches no file line; the closest file line is %d (%d%% similar): found %s, expected %s", line+1, int(math.Round(sim*100)), truncateToolLine(fileLines[line]), truncateToolLine(oldSeq[0]))
			parts = append(parts, detail+"; the file may have changed — re-read the current target range and rebuild this hunk from current complete lines")
		} else if applyPatchExpectedLineMissing(fileLines, oldSeq) {
			parts = append(parts, "the expected line does not exist in the current file; the file may have changed since it was last read, or the line was invented — re-read the current target range and rebuild this hunk from current complete lines")
		}
	}
	parts = append(parts, "do not retry the same hunk unchanged")
	return fmt.Errorf("%s", strings.Join(parts, "; "))
}

// applyPatchHunkWindowStart returns the first file line index a hunk may
// match at. Hunks must follow file order (the searchStart bound), so all
// diagnostic scans share this lower bound; an EOF hunk's old sequence must
// also match the file's tail, shifting the window start to the suffix
// position. All scans must respect the same window so a suggestion never
// points at a position a retry could not use.
func applyPatchHunkWindowStart(fileLines, oldSeq []string, searchStart int, eof bool) int {
	from := max(searchStart, 0)
	if eof && len(oldSeq) > 0 {
		// EOF hunks may only match against the tail: the whole file, since
		// oldSeq must be a suffix. A suffix match also cannot start before
		// searchStart (hunks must follow file order), so start at the later
		// of the two bounds.
		from = max(from, len(fileLines)-len(oldSeq))
	}
	if from < 0 {
		return 0
	}
	return from
}

// applyPatchHunkClosestLine scans fileLines for the line most similar to the
// hunk's first expected line (compared under the tolerance normalizer}, when
// no line of the hunk matches at all. It only considers the legal match
// window: lines at or after searchStart for ordered hunks, and the tail
// window for EOF hunks — mirroring findUniqueApplyPatchSequence — so a
// suggestion never points at a position a retry could not use. It returns the
// 0-based line index and the similarity (0..1); line is -1 when the file is
// empty or the window has no comparable lines. This is the "hunk is
// completely unrelated" fallback: the model sees the file's actual closest
// content instead of a bare re-read hint.
func applyPatchHunkClosestLine(fileLines, oldSeq []string, searchStart int, eof bool) (line int, sim float64) {
	if len(oldSeq) == 0 || len(fileLines) == 0 {
		return -1, 0
	}
	needle := normalizePatchTolerantLine(oldSeq[0])
	if needle == "" {
		return -1, 0
	}
	needleRunes := len([]rune(needle))
	from := applyPatchHunkWindowStart(fileLines, oldSeq, searchStart, eof)
	if from >= len(fileLines) {
		return -1, 0
	}
	// Guard the failure path against pathological inputs: a full Levenshtein
	// on every file line of a large file with long lines can reach billions
	// of rune operations. Pre-filter by rune-length delta (a candidate whose
	// length differs from the needle by more than the current best distance
	// can never beat it) and cap the total work; past the budget the
	// suggestion degrades to the generic re-read hint instead of stalling.
	const closestScanBudget = 200_000 // total (needle × line) rune-pair budget
	bestLine, bestSim := -1, -1.0
	bestDist := -1
	budget := closestScanBudget
	for i := from; i < len(fileLines); i++ {
		norm := normalizePatchTolerantLine(fileLines[i])
		if norm == "" {
			continue
		}
		normRunes := len([]rune(norm))
		if bestDist >= 0 && absInt(normRunes-needleRunes) >= bestDist {
			continue // cannot beat the current best edit distance
		}
		work := needleRunes * normRunes
		if budget <= 0 || work > budget {
			// An exhausted budget stops the scan; a single oversized line must
			// not — later lines can be far cheaper, and the budget is only
			// spent by lines actually processed, so skipping keeps the total
			// work bounded while letting the whole window compete.
			if budget <= 0 {
				break
			}
			continue
		}
		budget -= work
		dist := levenshteinDistance(needle, norm)
		// Similarity relative to the longer of the two lines so a short
		// file line next to a long hunk line is not over-rated.
		longer := max(needleRunes, normRunes)
		s := 1.0 - float64(dist)/float64(longer)
		if s > bestSim {
			bestSim, bestLine, bestDist = s, i, dist
		}
	}
	if bestLine < 0 {
		return -1, 0
	}
	return bestLine, bestSim
}

// applyPatchHunkMismatchLine locates the longest contiguous run of oldSeq
// (compared under the tolerance normalizer) that appears in fileLines within
// the hunk's legal match window (see applyPatchHunkWindowStart), returning
// the file line index where it starts and how many lines matched. The window
// bound keeps the suggestion from pointing at a position a retry could not
// use. It diagnoses multi-line hunks whose first expected line exists but
// whose full sequence does not, so the error can report which line first
// diverges.
func applyPatchHunkMismatchLine(fileLines, oldSeq []string, searchStart int, eof bool) (line, matched int) {
	if len(oldSeq) == 0 || len(fileLines) == 0 {
		return -1, 0
	}
	normLines := make([]string, len(fileLines))
	for i, l := range fileLines {
		normLines[i] = normalizePatchTolerantLine(l)
	}
	normSeq := make([]string, len(oldSeq))
	for i, l := range oldSeq {
		normSeq[i] = normalizePatchTolerantLine(l)
	}
	bestLine, bestMatched := -1, 0
	for i := applyPatchHunkWindowStart(fileLines, oldSeq, searchStart, eof); i < len(normLines); i++ {
		k := 0
		for k < len(normSeq) && i+k < len(normLines) && normLines[i+k] == normSeq[k] {
			k++
		}
		if k > bestMatched {
			bestMatched, bestLine = k, i
		}
	}
	return bestLine, bestMatched
}

// applyPatchExpectedLineMissing reports whether the first expected line of
// oldSeq exists anywhere in fileLines under the tolerance normalizer (or as a
// substring of a longer line, which applyPatchHunkNotFoundError reports
// separately). The tool matches the on-disk file only — read history is the
// model's context, not a matching source — so a missing line means the patch
// is based on stale or invented content and the model should re-read.
func applyPatchExpectedLineMissing(fileLines, oldSeq []string) bool {
	if len(oldSeq) == 0 {
		return false
	}
	needle := normalizePatchTolerantLine(oldSeq[0])
	if needle == "" {
		return false
	}
	for _, line := range fileLines {
		if normalizePatchTolerantLine(line) == needle {
			return false
		}
	}
	return true
}

// applyPatchPartialHunkError layers a "prior hunks matched but were not
// applied" note onto a single-file hunk failure when earlier hunks in the same
// file already matched successfully. The file is atomic, so those earlier
// hunks must be included again even though they matched in memory.
func applyPatchPartialHunkError(err error, index, total int) error {
	if err == nil || index == 0 {
		return err
	}
	return fmt.Errorf("%w; note: hunks 1..%d matched successfully in memory but were not applied because this hunk failed, so keep all hunks 1..%d together when rebuilding this operation", err, index, total)
}

func applyPatchUnsafePunctuationMatchError(oldSeq []string, index, total, match int) error {
	parts := []string{
		fmt.Sprintf("hunk not found (%d/%d)", index+1, total),
		fmt.Sprintf("a %s candidate exists at line %d, but the replacement cannot preserve unchanged text safely", tolerantMatchNote, match+1),
	}
	if expected := applyPatchExpectedLineDescription(oldSeq); expected != "" {
		parts = append(parts, expected)
	}
	parts = append(parts, "re-read the current target range and use exact complete lines")
	return fmt.Errorf("%s", strings.Join(parts, "; "))
}

func applyPatchExpectedLineDescription(oldSeq []string) string {
	if len(oldSeq) == 0 {
		return ""
	}
	runes := []rune(oldSeq[0])
	const maxRunes = 120
	if len(runes) > maxRunes {
		return "first expected line prefix: " + quoteToolLine(string(runes[:maxRunes]))
	}
	return "first expected complete line: " + quoteToolLine(string(runes))
}

func findApplyPatchSubstringLine(fileLines, oldSeq []string, searchStart int, eof bool) int {
	if len(oldSeq) != 1 {
		return -1
	}
	needle := normalizePatchTolerantLine(oldSeq[0])
	if needle == "" {
		return -1
	}
	for i := applyPatchHunkWindowStart(fileLines, oldSeq, searchStart, eof); i < len(fileLines); i++ {
		normalized := normalizePatchTolerantLine(fileLines[i])
		if normalized != needle && strings.Contains(normalized, needle) {
			return i
		}
	}
	return -1
}

func formatApplyPatchCandidateLines(candidates []int) string {
	const maxCandidates = 3
	parts := make([]string, 0, min(len(candidates), maxCandidates))
	for _, candidate := range candidates[:min(len(candidates), maxCandidates)] {
		parts = append(parts, fmt.Sprintf("%d", candidate+1))
	}
	if len(candidates) > maxCandidates {
		parts = append(parts, "…")
	}
	return strings.Join(parts, ", ")
}

func findApplyPatchSequence(lines, pattern []string, start int, eof bool) int {
	if len(pattern) == 0 || len(pattern) > len(lines) {
		return -1
	}
	from := start
	if eof {
		// An "*** End of File" hunk only matches the tail of the file, so the
		// scan is pinned to the single trailing position.
		from = len(lines) - len(pattern)
	}
	if from < 0 {
		from = 0
	}
	// Whitespace-only layers. Punctuation tolerance is deliberately NOT a layer
	// here: it runs through findUniqueApplyPatchSequence instead, which rejects
	// ambiguous matches and splices the replacement over the file's original
	// bytes. Folding it into this first-match-wins cascade would silently pick
	// one of several equally plausible positions.
	normalizers := []func(string) string{
		func(s string) string { return s },
		func(s string) string { return strings.TrimRightFunc(s, unicode.IsSpace) },
		strings.TrimSpace,
	}
	for layer, normalize := range normalizers {
		// Normalize the pattern once per layer instead of at every scan
		// position: on a mismatch-heavy file (the common hunk-not-found
		// path) the inner loop otherwise re-normalizes pattern lines
		// O(positions) times, and the unicode layer allocates per call. Keep
		// the exact-match layer allocation-free because it is the common path.
		normalizedPattern := pattern
		if layer > 0 {
			normalizedPattern = make([]string, len(pattern))
			for j, line := range pattern {
				normalizedPattern[j] = normalize(line)
			}
		}
		for i := from; i <= len(lines)-len(pattern); i++ {
			matched := true
			for j := range normalizedPattern {
				if normalize(lines[i+j]) != normalizedPattern[j] {
					matched = false
					break
				}
			}
			if matched {
				return i
			}
		}
	}
	return -1
}

func CommitMutationPlan(plan MutationPlan) error {
	if len(plan.Mutations) == 0 {
		return nil
	}
	for _, mutation := range plan.Mutations {
		if err := revalidateMutation(mutation); err != nil {
			return err
		}
	}

	committed := make([]PlannedMutation, 0, len(plan.Mutations))
	for _, mutation := range plan.Mutations {
		failedMutationDirty, err := commitMutation(mutation)
		if err != nil {
			var rollbackErrs []string
			if failedMutationDirty {
				if failedErr := rollbackFailedMutation(mutation); failedErr != nil {
					rollbackErrs = append(rollbackErrs, failedErr.Error())
				}
			}
			if committedErr := rollbackMutations(committed); committedErr != nil {
				rollbackErrs = append(rollbackErrs, committedErr.Error())
			}
			if len(rollbackErrs) > 0 {
				return fmt.Errorf("apply_patch commit failed: %w; rollback also failed: %v", err, strings.Join(rollbackErrs, "; "))
			}
			return fmt.Errorf("apply_patch commit failed: %w; all changes were rolled back", err)
		}
		committed = append(committed, mutation)
	}
	return nil
}

// rollbackFailedMutation restores the file that a failed commitMutation may
// have left half-written: the Add/Update write path truncates in place, so a
// write error strands a partial file whose pre-image exists only in the plan.
// Move rolls its target back inside commitMutation and never removes the
// source on failure, and a failed Delete leaves the file untouched.
func rollbackFailedMutation(m PlannedMutation) error {
	switch m.Kind {
	case MutationAdd:
		if err := os.Remove(m.TargetPath); err != nil && !os.IsNotExist(err) {
			return err
		}
	case MutationUpdate:
		return writeFileNoFollowExactMode(m.TargetPath, m.BeforeBytes, m.BeforeMode)
	}
	return nil
}

func revalidateMutation(m PlannedMutation) error {
	current, err := os.ReadFile(m.SourcePath)
	currentInfo, statErr := os.Stat(m.SourcePath)
	switch m.Kind {
	case MutationAdd:
		if err == nil || !os.IsNotExist(err) {
			return fmt.Errorf("apply_patch target changed after planning: %s. No files were modified", m.SourcePath)
		}
	case MutationUpdate, MutationDelete, MutationMove:
		if err != nil || statErr != nil || currentInfo.Mode() != m.BeforeMode || !bytes.Equal(current, m.BeforeBytes) {
			return fmt.Errorf("apply_patch source changed after planning: %s. No files were modified", m.SourcePath)
		}
	}
	if m.Kind == MutationMove && m.TargetPath != m.SourcePath {
		target, targetErr := os.ReadFile(m.TargetPath)
		targetInfo, targetStatErr := os.Stat(m.TargetPath)
		if m.TargetBeforeExists {
			if targetErr != nil || targetStatErr != nil || targetInfo.Mode() != m.TargetBeforeMode || !bytes.Equal(target, m.TargetBeforeBytes) {
				return fmt.Errorf("apply_patch move target changed after planning: %s. No files were modified", m.TargetPath)
			}
		} else if targetErr == nil || !os.IsNotExist(targetErr) {
			return fmt.Errorf("apply_patch move target changed after planning: %s. No files were modified", m.TargetPath)
		}
	}
	return nil
}

func commitMutation(m PlannedMutation) (bool, error) {
	afterMode := mutationAfterMode(m)
	switch m.Kind {
	case MutationAdd:
		if err := os.MkdirAll(filepath.Dir(m.TargetPath), 0755); err != nil {
			return false, err
		}
		return writeNewFileNoFollowMode(m.TargetPath, m.AfterBytes, afterMode, false)
	case MutationUpdate:
		if err := os.MkdirAll(filepath.Dir(m.TargetPath), 0755); err != nil {
			return false, err
		}
		err := writeFileNoFollowExactMode(m.TargetPath, m.AfterBytes, afterMode)
		return err != nil, err
	case MutationDelete:
		return false, os.Remove(m.SourcePath)
	case MutationMove:
		if err := os.MkdirAll(filepath.Dir(m.TargetPath), 0755); err != nil {
			return false, err
		}
		if m.TargetBeforeExists {
			if err := writeFileNoFollowExactMode(m.TargetPath, m.AfterBytes, afterMode); err != nil {
				if rollbackErr := rollbackMoveTarget(m); rollbackErr != nil {
					return false, fmt.Errorf("%w; rollback move target failed: %v", err, rollbackErr)
				}
				return false, err
			}
		} else if created, err := writeNewFileNoFollowMode(m.TargetPath, m.AfterBytes, afterMode, true); err != nil {
			if created {
				if rollbackErr := rollbackMoveTarget(m); rollbackErr != nil {
					return false, fmt.Errorf("%w; rollback move target failed: %v", err, rollbackErr)
				}
			}
			return false, err
		}
		if m.TargetPath != m.SourcePath {
			if err := os.Remove(m.SourcePath); err != nil {
				if rollbackErr := rollbackMoveTarget(m); rollbackErr != nil {
					return false, fmt.Errorf("%w; rollback move target failed: %v", err, rollbackErr)
				}
				return false, err
			}
		}
	}
	return false, nil
}

func mutationAfterMode(m PlannedMutation) os.FileMode {
	if m.AfterMode != 0 {
		return m.AfterMode
	}
	if m.Kind == MutationMove && m.TargetBeforeExists && m.TargetBeforeMode != 0 {
		return m.TargetBeforeMode
	}
	if m.BeforeMode != 0 {
		return m.BeforeMode
	}
	return 0o644
}

func rollbackMoveTarget(m PlannedMutation) error {
	if !m.TargetBeforeExists {
		if err := os.Remove(m.TargetPath); err != nil && !os.IsNotExist(err) {
			return err
		}
		return nil
	}
	current, err := os.ReadFile(m.TargetPath)
	currentInfo, statErr := os.Stat(m.TargetPath)
	if err == nil && statErr == nil && currentInfo.Mode() == m.TargetBeforeMode && bytes.Equal(current, m.TargetBeforeBytes) {
		return nil
	}
	return writeFileNoFollowExactMode(m.TargetPath, m.TargetBeforeBytes, m.TargetBeforeMode)
}

func rollbackMutations(committed []PlannedMutation) error {
	var failures []string
	for _, m := range slices.Backward(committed) {
		switch m.Kind {
		case MutationAdd:
			if err := os.Remove(m.TargetPath); err != nil && !os.IsNotExist(err) {
				failures = append(failures, err.Error())
			}
		case MutationUpdate:
			if err := writeFileNoFollowExactMode(m.SourcePath, m.BeforeBytes, m.BeforeMode); err != nil {
				failures = append(failures, err.Error())
			}
		case MutationDelete:
			if err := writeFileNoFollowExactMode(m.SourcePath, m.BeforeBytes, m.BeforeMode); err != nil {
				failures = append(failures, err.Error())
			}
		case MutationMove:
			if err := writeFileNoFollowExactMode(m.SourcePath, m.BeforeBytes, m.BeforeMode); err != nil {
				failures = append(failures, err.Error())
			}
			if m.TargetPath != m.SourcePath {
				if m.TargetBeforeExists {
					if err := writeFileNoFollowExactMode(m.TargetPath, m.TargetBeforeBytes, m.TargetBeforeMode); err != nil {
						failures = append(failures, err.Error())
					}
				} else if err := os.Remove(m.TargetPath); err != nil && !os.IsNotExist(err) {
					failures = append(failures, err.Error())
				}
			}
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("%s", strings.Join(failures, "; "))
	}
	return nil
}

// applyPatchMutationSummary renders one mutation as a single display line
// ("A path", "M path", "D path", "R src -> dst"), with paths shown relative to
// baseDir. Both the success path and the partial-failure "applied" list go
// through finishApplyPatch, so committed files are always reported identically.
func applyPatchMutationSummary(mutation PlannedMutation, baseDir string) string {
	marker, path := "M", mutation.SourcePath
	switch mutation.Kind {
	case MutationAdd:
		marker, path = "A", mutation.TargetPath
	case MutationDelete:
		marker = "D"
	case MutationMove:
		marker, path = "R", mutation.SourcePath+" -> "+mutation.TargetPath
	}
	return marker + " " + displayPathForBaseDir(path, baseDir)
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
			path := displayPathForBaseDir(applyPatchMutationNotePath(mutation), t.BaseDir)
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
