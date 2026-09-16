package tools

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

func BuildApplyPatchPlan(ctx context.Context, patch, baseDir string) (MutationPlan, error) {
	doc, err := ParseApplyPatch(patch)
	if err != nil {
		return MutationPlan{}, err
	}
	targets, err := resolveApplyPatchTargets(doc, baseDir)
	if err != nil {
		return MutationPlan{}, err
	}
	states, err := snapshotApplyPatchStates(targets, baseDir)
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
	states, err := snapshotApplyPatchStates(targets, baseDir)
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
			if err := rollbackGroup(source, fmt.Sprintf("a later operation that modified %s could not be resolved; keep it with the failed operation when revising", applyPatchPathHint(source, baseDir))); err != nil {
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
			dependencyHint := applyPatchPathHint(dependencyPath, baseDir)
			if err := rollbackGroup(source, fmt.Sprintf("a prior operation touching %s failed, so this operation was not applied and must remain with that dependency when revised", dependencyHint)); err != nil {
				return ApplyPatchPlanResult{}, err
			}
			idx := len(outcomes)
			outcomes = append(outcomes, failedApplyPatchOpResult(op, fmt.Errorf("skipped: a prior operation touching %s failed, so this operation was not applied and must remain with that dependency when revised", dependencyHint)))
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
			if replayErr := rollbackGroup(source, fmt.Sprintf("a later operation that modified %s failed, so this matched operation was not written to disk; keep it with the failed operation when revising the group", applyPatchPathHint(source, baseDir))); replayErr != nil {
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
		state.displayPath = applyPatchPathHint(path, baseDir)
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

func snapshotApplyPatchStates(targets []MutationTarget, baseDir string) (map[string]*applyPatchVirtualFile, error) {
	states := make(map[string]*applyPatchVirtualFile, len(targets)*2)
	type existingFile struct {
		display string
		info    os.FileInfo
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
			display := applyPatchPathHint(path, baseDir)
			state := &applyPatchVirtualFile{path: path, displayPath: display}
			info, err := os.Lstat(path)
			if os.IsNotExist(err) {
				states[path] = state
				continue
			}
			if err != nil {
				return nil, fmt.Errorf("inspect apply_patch path %s: %w. No files were modified", display, err)
			}
			if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
				return nil, fmt.Errorf("apply_patch path is not a regular file: %s. No files were modified", display)
			}
			for _, existing := range existingFiles {
				if os.SameFile(existing.info, info) {
					return nil, fmt.Errorf("apply_patch contains overlapping operations for %s and %s", existing.display, display)
				}
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return nil, fmt.Errorf("read apply_patch path %s: %w. No files were modified", display, err)
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
			existingFiles = append(existingFiles, existingFile{display: display, info: info})
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
	display := applyPatchPathHint(op.Path, baseDir)
	state.displayPath = display
	state.touched = true
	switch op.Kind {
	case MutationAdd:
		if state.exists {
			return fmt.Errorf("cannot add file that already exists: %s", display)
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
			return applyPatchMissingSourceError(display, baseDir)
		}
		state.exists = false
		state.bytes = nil
		state.originPath = ""
		state.cleanedInvisible = nil
		return nil
	case MutationUpdate:
		if !state.exists {
			return applyPatchMissingSourceError(display, baseDir)
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
				return fmt.Errorf("read update source %s: %w", display, err)
			}
			after, punctuationHunks, fuzzyHunks, fuzzyReplacements, err := applyApplyPatchHunks(ctx, decoded.Text, hunks)
			if err != nil {
				return fmt.Errorf("update %s: %w", display, err)
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
				return fmt.Errorf("encode update %s: %w", display, err)
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
			return fmt.Errorf("apply_patch move source and target are the same: %s", display)
		}
		target := states[targetPath]
		target.displayPath = applyPatchPathHint(op.MovePath, baseDir)
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
				DisplayPath:        source.displayPath,
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
			DisplayPath:       state.displayPath,
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

// displaySource returns the path spelling the model should resubmit: the
// plan-time relative form when one was captured, otherwise the resolved path.
func (m PlannedMutation) displaySource() string {
	if m.DisplayPath != "" {
		return m.DisplayPath
	}
	return m.SourcePath
}

func revalidateMutation(m PlannedMutation) error {
	current, err := os.ReadFile(m.SourcePath)
	currentInfo, statErr := os.Stat(m.SourcePath)
	switch m.Kind {
	case MutationAdd:
		if err == nil || !os.IsNotExist(err) {
			return fmt.Errorf("apply_patch target changed after planning: %s. No files were modified", m.displaySource())
		}
	case MutationUpdate, MutationDelete, MutationMove:
		if err != nil || statErr != nil || currentInfo.Mode() != m.BeforeMode || !bytes.Equal(current, m.BeforeBytes) {
			return fmt.Errorf("apply_patch source changed after planning: %s. No files were modified", m.displaySource())
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
