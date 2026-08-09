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
	AfterMode          os.FileMode
	AfterText          string
	Added              int
	Removed            int
	PunctuationHunks   int
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
	return "Apply a Codex-compatible patch to one or more files. The patch must begin with `*** Begin Patch` and end with `*** End Patch`. Supported operations are `*** Add File:`, `*** Delete File:`, `*** Update File:`, and `*** Move to:`. All operations are planned and validated before any file is modified." + lspMutationFollowUp(t.LSP)
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
	return t.finishApplyPatch(ctx, plan), nil
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
				implicitFirstHunk := len(op.Hunks) == 0 && (lines[i] == "" || strings.ContainsRune(" +-", rune(lines[i][0])))
				if !strings.HasPrefix(lines[i], "@@") && !implicitFirstHunk {
					return applyPatchDocument{}, fmt.Errorf("invalid update hunk at line %d: expected @@", i+1)
				}
				var h applyPatchHunk
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

type applyPatchVirtualFile struct {
	path             string
	displayPath      string
	originPath       string
	initialExists    bool
	initialBytes     []byte
	initialMode      os.FileMode
	exists           bool
	bytes            []byte
	mode             os.FileMode
	touched          bool
	punctuationHunks int
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
			state.initialBytes = append([]byte(nil), data...)
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
			return fmt.Errorf("cannot add file that already exists: %s. No files were modified", op.Path)
		}
		state.mode = 0o644
		state.exists = true
		state.bytes = []byte(op.Content)
		state.originPath = ""
		return nil
	case MutationDelete:
		if !state.exists {
			return applyPatchMissingSourceError(op.Path, baseDir)
		}
		state.exists = false
		state.bytes = nil
		state.originPath = ""
		return nil
	case MutationUpdate:
		if !state.exists {
			return applyPatchMissingSourceError(op.Path, baseDir)
		}
		if len(op.Hunks) > 0 {
			decoded, err := decodeTextBytes(state.bytes, source)
			if err != nil {
				return fmt.Errorf("read update source %s: %w. No files were modified", op.Path, err)
			}
			after, punctuationHunks, err := applyApplyPatchHunks(ctx, decoded.Text, op.Hunks)
			if err != nil {
				return fmt.Errorf("update %s: %w", op.Path, err)
			}
			state.punctuationHunks += punctuationHunks
			state.bytes, err = encodeString(after, decoded.Encoding)
			if err != nil {
				return fmt.Errorf("encode update %s: %w. No files were modified", op.Path, err)
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
		state.exists = false
		state.bytes = nil
		state.originPath = ""
		state.punctuationHunks = 0
		return nil
	default:
		return fmt.Errorf("unsupported apply_patch operation %q", op.Kind)
	}
}

func applyPatchMissingSourceError(displayPath, baseDir string) error {
	return withPathSuggestionsInDir(
		fmt.Sprintf("read apply_patch source %s: file not found: %s. No files were modified", displayPath, displayPath),
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
			SourcePath:       path,
			TargetPath:       path,
			BeforeExists:     state.initialExists,
			BeforeBytes:      append([]byte(nil), state.initialBytes...),
			BeforeMode:       state.initialMode,
			AfterBytes:       append([]byte(nil), state.bytes...),
			AfterMode:        state.mode,
			PunctuationHunks: state.punctuationHunks,
		}
		switch {
		case !state.initialExists && state.exists:
			mutation.Kind = MutationAdd
		case state.initialExists && !state.exists:
			mutation.Kind = MutationDelete
		default:
			mutation.Kind = MutationUpdate
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

func applyApplyPatchHunks(ctx context.Context, content string, hunks []applyPatchHunk) (string, int, error) {
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
		var punctuationCandidates []int
		if match < 0 && len(oldSeq) > 0 {
			match, punctuationCandidates = findUniqueApplyPatchSequence(
				fileLines, oldSeq, searchStart, hunk.EndOfFile, normalizePatchProsePunctuationLine,
			)
			if match >= 0 {
				punctuationMatch = true
			}
		}
		if match < 0 {
			return "", 0, applyPatchHunkNotFoundError(fileLines, oldSeq, searchStart, i, len(hunks), punctuationCandidates)
		}
		matched := fileLines[match : match+len(oldSeq)]
		newSeq := buildApplyPatchNewSequence(hunk, matched)
		if punctuationMatch {
			var ok bool
			newSeq, ok = buildPunctuationTolerantApplyPatchSequence(hunk, matched)
			if !ok {
				return "", 0, applyPatchUnsafePunctuationMatchError(oldSeq, i, len(hunks), match)
			}
			punctuationHunks++
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
	return out, punctuationHunks, nil
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
	if len(currentRunes) != len(oldRunes) || normalizePatchProsePunctuationLine(current) != normalizePatchProsePunctuationLine(oldText) {
		return "", false
	}

	prefix := 0
	for prefix < len(oldRunes) && prefix < len(newRunes) && oldRunes[prefix] == newRunes[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < len(oldRunes)-prefix && suffix < len(newRunes)-prefix && oldRunes[len(oldRunes)-1-suffix] == newRunes[len(newRunes)-1-suffix] {
		suffix++
	}
	if prefix == 0 && suffix == 0 {
		return "", false
	}

	result := make([]rune, 0, prefix+len(newRunes)-prefix-suffix+suffix)
	result = append(result, currentRunes[:prefix]...)
	result = append(result, newRunes[prefix:len(newRunes)-suffix]...)
	result = append(result, currentRunes[len(currentRunes)-suffix:]...)
	return string(result), true
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
	for i := from; i <= to; i++ {
		matched := true
		for j := range pattern {
			if normalize(lines[i+j]) != normalize(pattern[j]) {
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

func applyPatchHunkNotFoundError(fileLines, oldSeq []string, searchStart, index, total int, punctuationCandidates []int) error {
	parts := []string{fmt.Sprintf("hunk not found (%d/%d)", index+1, total)}
	if preview := applyPatchExpectedPreview(oldSeq); preview != "" {
		parts = append(parts, "expected complete line: "+preview)
	}
	if len(punctuationCandidates) > 1 {
		parts = append(parts, "punctuation-tolerant matching is ambiguous at lines "+formatApplyPatchCandidateLines(punctuationCandidates))
	}
	if earlier := findApplyPatchSequence(fileLines, oldSeq, 0, false); earlier >= 0 && earlier < searchStart {
		parts = append(parts, fmt.Sprintf("matching context exists earlier at line %d, but hunks must follow file order", earlier+1))
	} else if line := findApplyPatchSubstringLine(fileLines, oldSeq); line >= 0 {
		parts = append(parts, fmt.Sprintf("the expected text is only part of current line %d; include that complete line in the hunk", line+1))
	}
	parts = append(parts, "re-read the current target range before retrying; do not retry the same hunk unchanged. No files were modified")
	return fmt.Errorf("%s", strings.Join(parts, "; "))
}

func applyPatchUnsafePunctuationMatchError(oldSeq []string, index, total, match int) error {
	parts := []string{
		fmt.Sprintf("hunk not found (%d/%d)", index+1, total),
		fmt.Sprintf("a punctuation-tolerant candidate exists at line %d, but the replacement cannot preserve unchanged punctuation safely", match+1),
	}
	if preview := applyPatchExpectedPreview(oldSeq); preview != "" {
		parts = append(parts, "expected complete line: "+preview)
	}
	parts = append(parts, "re-read the current target range and use exact complete lines. No files were modified")
	return fmt.Errorf("%s", strings.Join(parts, "; "))
}

func applyPatchExpectedPreview(oldSeq []string) string {
	if len(oldSeq) == 0 {
		return ""
	}
	runes := []rune(strings.TrimSpace(oldSeq[0]))
	const maxRunes = 120
	if len(runes) > maxRunes {
		runes = append(runes[:maxRunes], '…')
	}
	return fmt.Sprintf("%q", string(runes))
}

func findApplyPatchSubstringLine(fileLines, oldSeq []string) int {
	if len(oldSeq) != 1 {
		return -1
	}
	needle := normalizePatchProsePunctuationLine(oldSeq[0])
	if needle == "" {
		return -1
	}
	for i, line := range fileLines {
		normalized := normalizePatchProsePunctuationLine(line)
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

func (t ApplyPatchTool) finishApplyPatch(ctx context.Context, plan MutationPlan) string {
	if len(plan.Mutations) == 0 {
		return "Applied patch:\nNo net file changes"
	}
	var lines []string
	punctuationHunks := 0
	for _, mutation := range plan.Mutations {
		punctuationHunks += mutation.PunctuationHunks
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
		invalidatePathCache(mutation.SourcePath)
		invalidatePathCache(mutation.TargetPath)
	}
	sort.Strings(lines)
	if punctuationHunks > 0 {
		lines = append(lines, fmt.Sprintf("Note: used punctuation-tolerant matching for %d hunk(s); unchanged punctuation was preserved from the current file", punctuationHunks))
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
		result := t.LSP.AfterFileWriteToolResult(ctx, write.path, write.content, "", false, write.change)
		if parsed := lsp.ParseToolOutputDiagnostics(result); len(parsed) > 0 {
			extras[path] = parsed
		}
		reviewedPaths = append(reviewedPaths, write.path)
	}
	slices.Sort(reviewedPaths)
	return t.LSP.AppendLSPDiagnosticsToToolOutputForPaths(out, reviewedPaths, true, baselines, outputs, extras)
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
