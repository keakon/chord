package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"unicode"

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

// NormalizeApplyPatchArgs converts the legacy single-file {path, patch}
// arguments into the current Codex apply_patch envelope. Current envelope
// arguments are returned in canonical {patch} form unchanged.
func NormalizeApplyPatchArgs(raw json.RawMessage) (json.RawMessage, error) {
	var args struct {
		Path  string `json:"path"`
		Patch string `json:"patch"`
	}
	if err := json.Unmarshal(unwrapToolArgs(raw), &args); err != nil {
		return nil, fmt.Errorf("invalid arguments: %w", err)
	}
	args.Path = strings.TrimSpace(args.Path)
	if strings.TrimSpace(args.Patch) == "" {
		return nil, fmt.Errorf("patch is required")
	}
	patch := args.Patch
	// ParseApplyPatch trims surrounding whitespace, so an envelope with a
	// leading newline is still valid; trim before sniffing the prefix so such
	// a patch is not misrouted through the legacy single-file wrapping.
	if !strings.HasPrefix(strings.TrimSpace(patch), "*** Begin Patch") {
		if args.Path == "" {
			return nil, fmt.Errorf("legacy apply_patch arguments require path")
		}
		patch = "*** Begin Patch\n*** Update File: " + args.Path + "\n" + strings.TrimRight(patch, "\r\n") + "\n*** End Patch"
	}
	return json.Marshal(ApplyPatchArgs{Patch: patch})
}

type MutationKind string

const (
	MutationAdd    MutationKind = "add"
	MutationUpdate MutationKind = "update"
	MutationDelete MutationKind = "delete"
	MutationMove   MutationKind = "move"
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
	AfterText          string
	Added              int
	Removed            int
	diffInput          unifiedFileDiff
	diffable           bool
}

type MutationPlan struct {
	Mutations []PlannedMutation
}

// ApplyPatchDiffCollector receives the complete multi-file diff produced by a
// successful ApplyPatchTool execution.
type ApplyPatchDiffCollector struct {
	mu      sync.Mutex
	summary DiffSummary
}

func (c *ApplyPatchDiffCollector) set(summary DiffSummary) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.summary = summary
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
func (t ApplyPatchTool) Description() string {
	return "Apply a Codex-compatible patch to one or more files. The patch must begin with `*** Begin Patch` and end with `*** End Patch`. Supported operations are `*** Add File:`, `*** Delete File:`, `*** Update File:`, and `*** Move to:`. All operations are planned and validated before any file is modified."
}
func (ApplyPatchTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"patch": map[string]any{
				"type":        "string",
				"description": "Complete Codex apply_patch text including Begin Patch and End Patch markers.",
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
	plan, err := BuildApplyPatchPlan(ctx, args.Patch, t.BaseDir)
	if err != nil {
		return "", err
	}
	if err := CommitMutationPlan(plan); err != nil {
		return "", err
	}
	if collector := applyPatchDiffCollectorFromContext(ctx); collector != nil {
		collector.set(applyPatchMutationDiffSummary(plan))
	}
	return t.finishApplyPatch(plan), nil
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
	lines := strings.Split(text, "\n")
	if len(lines) < 2 || strings.TrimSpace(lines[0]) != "*** Begin Patch" {
		return applyPatchDocument{}, fmt.Errorf("invalid apply_patch: first line must be `*** Begin Patch`")
	}
	if strings.TrimSpace(lines[len(lines)-1]) != "*** End Patch" {
		return applyPatchDocument{}, fmt.Errorf("invalid apply_patch: last line must be `*** End Patch`")
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
		case strings.HasPrefix(line, "*** Add File: "):
			op.Kind = MutationAdd
			op.Path = strings.TrimSpace(strings.TrimPrefix(line, "*** Add File: "))
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
		case strings.HasPrefix(line, "*** Delete File: "):
			op.Kind = MutationDelete
			op.Path = strings.TrimSpace(strings.TrimPrefix(line, "*** Delete File: "))
			i++
		case strings.HasPrefix(line, "*** Update File: "):
			op.Kind = MutationUpdate
			op.Path = strings.TrimSpace(strings.TrimPrefix(line, "*** Update File: "))
			i++
			if i < len(lines)-1 && strings.HasPrefix(strings.TrimSpace(lines[i]), "*** Move to: ") {
				op.MovePath = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(lines[i]), "*** Move to: "))
				i++
			}
			for i < len(lines)-1 && !isApplyPatchMarker(lines[i]) {
				if lines[i] == "" {
					if next, ok := skipApplyPatchSeparatorRun(lines, i, true); ok {
						i = next
						continue
					}
				}
				if !strings.HasPrefix(lines[i], "@@") {
					return applyPatchDocument{}, fmt.Errorf("invalid update hunk at line %d: expected @@", i+1)
				}
				h := applyPatchHunk{Header: strings.TrimSpace(strings.TrimPrefix(lines[i], "@@"))}
				i++
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
	targetIndexBySource := make(map[string]int)
	for _, op := range doc.Operations {
		source, err := resolveApplyPatchPath(op.Path, baseDir)
		if err != nil {
			return nil, err
		}
		target := source
		kind := op.Kind
		if op.MovePath != "" {
			target, err = resolveApplyPatchPath(op.MovePath, baseDir)
			if err != nil {
				return nil, err
			}
			if target == source {
				return nil, fmt.Errorf("apply_patch move source and target are the same: %s", source)
			}
			kind = MutationMove
		}
		if op.Kind == MutationUpdate && op.MovePath == "" {
			if index, ok := targetIndexBySource[source]; ok {
				existing := targets[index]
				if existing.Kind == MutationUpdate && existing.SourcePath == existing.TargetPath {
					continue
				}
			}
		}
		paths := []string{source}
		if target != source {
			paths = append(paths, target)
		}
		for _, candidate := range paths {
			if _, dup := seen[candidate]; dup {
				return nil, fmt.Errorf("apply_patch contains duplicate operations for %s", candidate)
			}
			for existing := range seen {
				if isCleanPathWithin(candidate, existing) || isCleanPathWithin(existing, candidate) {
					return nil, fmt.Errorf("apply_patch contains overlapping operations for %s and %s", existing, candidate)
				}
			}
			seen[candidate] = struct{}{}
		}
		targets = append(targets, MutationTarget{Kind: kind, SourcePath: source, TargetPath: target})
		targetIndexBySource[source] = len(targets) - 1
	}
	sort.Slice(targets, func(i, j int) bool {
		if targets[i].SourcePath != targets[j].SourcePath {
			return targets[i].SourcePath < targets[j].SourcePath
		}
		return targets[i].TargetPath < targets[j].TargetPath
	})
	return targets, nil
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
	bySource := make(map[string]MutationTarget, len(targets))
	for _, target := range targets {
		bySource[target.SourcePath] = target
	}
	plainUpdatesBySource := make(map[string][]applyPatchOperation)
	for _, op := range doc.Operations {
		if op.Kind != MutationUpdate || op.MovePath != "" {
			continue
		}
		source, err := resolveApplyPatchPath(op.Path, baseDir)
		if err != nil {
			return MutationPlan{}, err
		}
		plainUpdatesBySource[source] = append(plainUpdatesBySource[source], op)
	}

	type existingTarget struct {
		path string
		info os.FileInfo
	}
	existingTargets := make([]existingTarget, 0, len(targets)*2)
	recordExistingTarget := func(path string, info os.FileInfo) error {
		for _, existing := range existingTargets {
			if os.SameFile(existing.info, info) {
				return fmt.Errorf("apply_patch contains overlapping operations for %s and %s", existing.path, path)
			}
		}
		existingTargets = append(existingTargets, existingTarget{path: path, info: info})
		return nil
	}

	plan := MutationPlan{Mutations: make([]PlannedMutation, 0, len(doc.Operations))}
	plannedPlainUpdates := make(map[string]struct{})
	for _, op := range doc.Operations {
		// resolveApplyPatchTargets resolved this same doc above, so resolution
		// cannot fail here; the check guards against the two paths drifting.
		source, err := resolveApplyPatchPath(op.Path, baseDir)
		if err != nil {
			return MutationPlan{}, err
		}
		if op.Kind == MutationUpdate && op.MovePath == "" {
			if _, planned := plannedPlainUpdates[source]; planned {
				continue
			}
			plannedPlainUpdates[source] = struct{}{}
		}
		target := bySource[source]
		mutation := PlannedMutation{Kind: target.Kind, SourcePath: source, TargetPath: target.TargetPath}
		switch op.Kind {
		case MutationAdd:
			if _, err := os.Lstat(source); err == nil {
				return MutationPlan{}, fmt.Errorf("cannot add file that already exists: %s. No files were modified", op.Path)
			} else if !os.IsNotExist(err) {
				return MutationPlan{}, fmt.Errorf("inspect add target %s: %w. No files were modified", op.Path, err)
			}
			mutation.AfterBytes = []byte(op.Content)
			mutation.AfterText = op.Content
			mutation.Added = lineCountForMutation(op.Content)
			mutation.diffInput = unifiedFileDiff{NewContent: op.Content, OldFilename: op.Path, NewFilename: op.Path}
			mutation.diffable = true
		case MutationDelete:
			before, info, err := readApplyPatchSource(source, op.Path)
			if err != nil {
				return MutationPlan{}, err
			}
			if err := recordExistingTarget(source, info); err != nil {
				return MutationPlan{}, err
			}
			mutation.BeforeExists = true
			mutation.BeforeBytes = before
			mutation.BeforeMode = info.Mode()
			mutation.Removed = lineCountForMutation(string(before))
			if decoded, decodeErr := decodeTextBytes(before, source); decodeErr == nil {
				mutation.diffInput = unifiedFileDiff{OldContent: decoded.Text, OldFilename: op.Path, NewFilename: op.Path}
				mutation.diffable = true
			}
		case MutationUpdate:
			before, info, err := readApplyPatchSource(source, op.Path)
			if err != nil {
				return MutationPlan{}, err
			}
			if err := recordExistingTarget(source, info); err != nil {
				return MutationPlan{}, err
			}
			mutation.BeforeExists = true
			mutation.BeforeBytes = before
			mutation.BeforeMode = info.Mode()

			if len(op.Hunks) == 0 {
				// Rename-only: carry the bytes unchanged so mixed EOL,
				// binary, and regional encodings survive a round-trip.
				mutation.AfterBytes = before
				if decoded, decodeErr := decodeTextBytes(before, source); decodeErr == nil {
					mutation.AfterText = decoded.Text
					mutation.diffInput = unifiedFileDiff{OldContent: decoded.Text, NewContent: decoded.Text, OldFilename: op.Path, NewFilename: op.MovePath}
					mutation.diffable = true
				}
			} else {
				// Decode the bytes already read for the revalidation
				// snapshot instead of re-reading the file: a second read
				// would double the I/O and could observe different content
				// than BeforeBytes.
				decoded, err := decodeTextBytes(before, source)
				if err != nil {
					return MutationPlan{}, fmt.Errorf("read update source %s: %w. No files were modified", op.Path, err)
				}
				after := decoded.Text
				updates := []applyPatchOperation{op}
				if op.MovePath == "" {
					updates = plainUpdatesBySource[source]
				}
				for _, update := range updates {
					after, err = applyApplyPatchHunks(ctx, after, update.Hunks)
					if err != nil {
						return MutationPlan{}, fmt.Errorf("update %s: %w", update.Path, err)
					}
				}
				encoded, err := encodeString(after, decoded.Encoding)
				if err != nil {
					return MutationPlan{}, fmt.Errorf("encode update %s: %w. No files were modified", op.Path, err)
				}
				mutation.AfterBytes = encoded
				mutation.AfterText = after
				newPath := op.Path
				if op.MovePath != "" {
					newPath = op.MovePath
				}
				mutation.diffInput = unifiedFileDiff{OldContent: decoded.Text, NewContent: after, OldFilename: op.Path, NewFilename: newPath}
				mutation.diffable = true
				diff := GenerateUnifiedDiffSummary(decoded.Text, after, op.Path)
				mutation.Added, mutation.Removed = diff.Added, diff.Removed
			}
			if op.MovePath != "" {
				mutation.Kind = MutationMove
				if target.TargetPath != source {
					if data, err := os.ReadFile(target.TargetPath); err == nil {
						info, statErr := os.Stat(target.TargetPath)
						if statErr != nil {
							return MutationPlan{}, fmt.Errorf("inspect move target %s: %w. No files were modified", op.MovePath, statErr)
						}
						if err := ensureRegularFilePath(op.MovePath, info); err != nil {
							return MutationPlan{}, fmt.Errorf("%w. No files were modified", err)
						}
						if err := recordExistingTarget(target.TargetPath, info); err != nil {
							return MutationPlan{}, err
						}
						mutation.TargetBeforeExists = true
						mutation.TargetBeforeBytes = data
						mutation.TargetBeforeMode = info.Mode()
					} else if !os.IsNotExist(err) {
						return MutationPlan{}, fmt.Errorf("inspect move target %s: %w. No files were modified", op.MovePath, err)
					}
				}
			}
		}
		plan.Mutations = append(plan.Mutations, mutation)
	}
	return plan, nil
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

func readApplyPatchSource(path, display string) ([]byte, os.FileInfo, error) {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, withPathSuggestionsInDir(
				fmt.Sprintf("read apply_patch source %s: file not found: %s. No files were modified", display, display),
				display, "", PathTargetRegularFile,
			)
		}
		return nil, nil, fmt.Errorf("read apply_patch source %s: %w. No files were modified", display, err)
	}
	if err := ensureRegularFilePath(display, info); err != nil {
		return nil, nil, fmt.Errorf("%w. No files were modified", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("read apply_patch source %s: %w. No files were modified", display, err)
	}
	return data, info, nil
}

func applyApplyPatchHunks(ctx context.Context, content string, hunks []applyPatchHunk) (string, error) {
	newline := "\n"
	if strings.Contains(content, "\r\n") {
		newline = "\r\n"
	}
	logical := strings.ReplaceAll(content, "\r\n", "\n")
	finalNewline := strings.HasSuffix(logical, "\n")
	var fileLines []string
	if logical != "" {
		fileLines = strings.Split(strings.TrimSuffix(logical, "\n"), "\n")
	}
	searchStart := 0
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
			// A single pure insertion is unambiguous only for an existing empty
			// file. Non-empty files still require context so a stale model cannot
			// silently insert at the wrong location.
			if len(fileLines) != 0 || len(hunks) != 1 || hunk.Header != "" || hunk.EndOfFile {
				return "", fmt.Errorf("hunk has no context or removed lines; add unchanged context. No files were modified")
			}
			match = 0
		} else {
			match = findApplyPatchSequence(fileLines, oldSeq, searchStart, hunk.EndOfFile)
		}
		if len(oldSeq) > 0 && match < 0 && headerPos >= 0 {
			// The canonical format puts context strictly after the @@ header, but
			// models often repeat the header text as the hunk's first context line.
			// Retry from the header itself so both styles anchor to one location.
			match = findApplyPatchSequence(fileLines, oldSeq, headerPos, hunk.EndOfFile)
		}
		if match < 0 {
			return "", fmt.Errorf("hunk not found; re-read the current file before retrying. No files were modified")
		}
		matched := fileLines[match : match+len(oldSeq)]
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
		replaced := make([]string, 0, len(fileLines)-len(oldSeq)+len(newSeq))
		replaced = append(replaced, fileLines[:match]...)
		replaced = append(replaced, newSeq...)
		replaced = append(replaced, fileLines[match+len(oldSeq):]...)
		fileLines = replaced
		searchStart = match + len(newSeq)
	}
	out := strings.Join(fileLines, "\n")
	if finalNewline {
		out += "\n"
	}
	if newline == "\r\n" {
		out = strings.ReplaceAll(out, "\n", "\r\n")
	}
	return out, nil
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
	normalizers := []func(string) string{
		func(s string) string { return s },
		func(s string) string { return strings.TrimRightFunc(s, unicode.IsSpace) },
		strings.TrimSpace,
		normalizePatchUnicodeLine,
	}
	for _, normalize := range normalizers {
		for i := from; i <= len(lines)-len(pattern); i++ {
			matched := true
			for j := range pattern {
				if normalize(lines[i+j]) != normalize(pattern[j]) {
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
		return fmt.Errorf("no files were modified")
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
	switch m.Kind {
	case MutationAdd:
		if err := os.MkdirAll(filepath.Dir(m.TargetPath), 0755); err != nil {
			return false, err
		}
		return writeNewFileNoFollowMode(m.TargetPath, m.AfterBytes, 0644, false)
	case MutationUpdate:
		if err := os.MkdirAll(filepath.Dir(m.TargetPath), 0755); err != nil {
			return false, err
		}
		err := writeFileNoFollow(m.TargetPath, m.AfterBytes, 0644)
		return err != nil, err
	case MutationDelete:
		return false, os.Remove(m.SourcePath)
	case MutationMove:
		if err := os.MkdirAll(filepath.Dir(m.TargetPath), 0755); err != nil {
			return false, err
		}
		if m.TargetBeforeExists {
			if err := writeFileNoFollowExactMode(m.TargetPath, m.AfterBytes, m.BeforeMode); err != nil {
				if rollbackErr := rollbackMoveTarget(m); rollbackErr != nil {
					return false, fmt.Errorf("%w; rollback move target failed: %v", err, rollbackErr)
				}
				return false, err
			}
		} else if created, err := writeNewFileNoFollowMode(m.TargetPath, m.AfterBytes, m.BeforeMode, true); err != nil {
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

func (t ApplyPatchTool) finishApplyPatch(plan MutationPlan) string {
	var lines []string
	for _, mutation := range plan.Mutations {
		marker, path := "M", mutation.SourcePath
		switch mutation.Kind {
		case MutationAdd:
			marker, path = "A", mutation.TargetPath
		case MutationDelete:
			marker = "D"
		case MutationMove:
			marker, path = "R", mutation.SourcePath+" -> "+mutation.TargetPath
		}
		lines = append(lines, marker+" "+displayPathForBaseDir(path, t.BaseDir))
		if t.LSP != nil {
			switch mutation.Kind {
			case MutationAdd, MutationUpdate:
				t.LSP.MarkTouched(mutation.TargetPath)
			case MutationMove:
				t.LSP.MarkTouched(mutation.TargetPath)
			}
		}
		invalidatePathCache(mutation.SourcePath)
		invalidatePathCache(mutation.TargetPath)
	}
	sort.Strings(lines)
	return "Applied patch:\n" + strings.Join(lines, "\n")
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
