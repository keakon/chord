package agent

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/keakon/chord/internal/tools"
)

type applyPatchRetryRecord struct {
	paths []string
	// revisions snapshots each target file's content hash at the moment the
	// failure armed the block. A later unchanged-patch retry compares the
	// live hash against it so a target that changed underneath the model
	// (edit/write/shell or an external editor — not only a successful Read)
	// releases the block. An unreadable/missing file records "".
	revisions map[string]string
}

// applyPatchRetryGuard blocks an unchanged apply_patch after that exact patch
// failed to match in the current turn. A successful Read of any target proves
// the model refreshed its source view and removes the block; a target file
// whose content changed since the failure is an equally fresh basis, so the
// guard re-checks file revisions before rejecting. Tool execution goroutines
// consult the guard while result handlers and turn transitions update it, so
// access is synchronized.
type applyPatchRetryGuard struct {
	mu      sync.Mutex
	blocked map[[sha256.Size]byte]applyPatchRetryRecord
}

// applyPatchTargetRevisions hashes each blocked target at the moment the
// failure arms the guard. Only approximate-match failures — already rare —
// pay for whole-file hashing, and a content hash, not stat mtime alone, is
// what lets a later retry recognize a rewrite that preserved size and mtime.
func applyPatchTargetRevisions(paths []string) map[string]string {
	revisions := make(map[string]string, len(paths))
	for _, path := range paths {
		revisions[path] = computeFileHash(path)
	}
	return revisions
}

func applyPatchRetrySignature(raw json.RawMessage, baseDir string) ([sha256.Size]byte, []string, bool) {
	normalized, err := tools.NormalizeApplyPatchArgs(raw)
	if err != nil {
		return [sha256.Size]byte{}, nil, false
	}
	var args tools.ApplyPatchArgs
	if err := json.Unmarshal(normalized, &args); err != nil {
		return [sha256.Size]byte{}, nil, false
	}
	targets, err := tools.ApplyPatchTargets(normalized, baseDir)
	if err != nil {
		return [sha256.Size]byte{}, nil, false
	}
	paths := tools.MutationTargetPaths(targets)
	if len(paths) == 0 {
		return [sha256.Size]byte{}, nil, false
	}
	patch := strings.ReplaceAll(strings.TrimSpace(args.Patch), "\r\n", "\n")
	fingerprint := sha256.Sum256([]byte(strings.Join(paths, "\x00") + "\x00" + patch))
	return fingerprint, paths, true
}

func (g *applyPatchRetryGuard) reject(tcName string, raw json.RawMessage, baseDir string) error {
	if g == nil || tools.NormalizeName(tcName) != tools.NameApplyPatch {
		return nil
	}
	fingerprint, _, ok := applyPatchRetrySignature(raw, baseDir)
	if !ok {
		return nil
	}
	g.mu.Lock()
	record, blocked := g.blocked[fingerprint]
	g.mu.Unlock()
	if !blocked {
		return nil
	}
	// Release the block when any target demonstrably changed since the
	// failure: the patch may now match the fresh content (a rewrite by
	// edit/write/shell or an external editor is as new a basis as a Read).
	// An unchanged retry fails again and re-arms the guard on the new failure.
	for path, revision := range record.revisions {
		if computeFileHash(path) != revision {
			g.mu.Lock()
			delete(g.blocked, fingerprint)
			g.mu.Unlock()
			return nil
		}
	}
	displayPaths := make([]string, len(record.paths))
	for i, path := range record.paths {
		displayPaths[i] = displayPathFromWorkDir(baseDir, path)
	}
	target := displayPaths[0]
	if len(displayPaths) > 1 {
		target = "one of these targets: " + strings.Join(displayPaths, ", ")
	}
	return fmt.Errorf("apply_patch rejected before execution: this exact patch already failed to match %s in the current turn; read the current target range, rebuild the hunk from that fresh output, and submit a changed patch instead of retrying the same patch unchanged", target)
}

func (g *applyPatchRetryGuard) observeResult(name, argsJSON, baseDir string, err error) {
	if g == nil {
		return
	}
	switch tools.NormalizeName(name) {
	case tools.NameApplyPatch:
		if !tools.IsApproximateMatchFailure(tools.NameApplyPatch, err) {
			return
		}
		fingerprint, paths, ok := applyPatchRetrySignature(json.RawMessage(argsJSON), baseDir)
		if !ok {
			return
		}
		g.mu.Lock()
		if g.blocked == nil {
			g.blocked = make(map[[sha256.Size]byte]applyPatchRetryRecord)
		}
		g.blocked[fingerprint] = applyPatchRetryRecord{paths: paths, revisions: applyPatchTargetRevisions(paths)}
		g.mu.Unlock()
	case tools.NameRead:
		if err != nil {
			return
		}
		path := tools.ExtractReadPathFromArgsInDir(json.RawMessage(argsJSON), baseDir)
		if path == "" {
			return
		}
		g.mu.Lock()
		for fingerprint, record := range g.blocked {
			for _, target := range record.paths {
				if target == path {
					delete(g.blocked, fingerprint)
					break
				}
			}
		}
		g.mu.Unlock()
	}
}

func (g *applyPatchRetryGuard) reset() {
	if g == nil {
		return
	}
	g.mu.Lock()
	g.blocked = nil
	g.mu.Unlock()
}

// editRetryAdviceThreshold is the number of repeated approximate-match
// failures for the same edit/apply_patch target before the agent injects a
// fresh-read advisory into the model-visible result. One failure already
// carries the closest-match diagnostic; two or more mean the model is
// re-typing the same drifted block from memory instead of reading it.
const editRetryAdviceThreshold = 2

// editRetryAdviceCap bounds how many advisories accumulate for one target
// within a turn. Beyond the cap the model is clearly not reading the fresh
// output, so repeating the note only adds noise without changing behavior.
// The streak itself keeps counting (a later success still resets it); only
// the appended note is suppressed.
const editRetryAdviceCap = 4

// appendEditRetryAdvice counts repeated approximate-match failures per
// target path and, from the second failure up to the cap, appends a
// model-facing note to the context result (never the display result): the
// target has drifted past what the shown difference conveys, so retrying
// the same old text from memory keeps failing. The note steers toward a
// fresh bounded read, a smaller anchor, or a whole-block write. It is
// injected after the result hooks so user-configured transformations are
// never overwritten by it. streaks is the caller-owned per-turn failure map
// (MainAgent and SubAgent each keep their own); the event loop is the sole
// reader/writer, so it needs no locking.
func appendEditRetryAdvice(streaks *map[string]int, contextResult, name, argsJSON, baseDir string, err error, isError bool) string {
	name = tools.NormalizeName(name)
	if name == tools.NameRead {
		if !isError && *streaks != nil {
			if path := tools.ExtractReadPathFromArgsInDir(json.RawMessage(argsJSON), baseDir); path != "" {
				delete(*streaks, path)
			}
		}
		return contextResult
	}
	if name != tools.NameEdit && name != tools.NameApplyPatch {
		return contextResult
	}
	// A successful result on a turn that has tracked no failures needs no
	// per-path work: the streak map is empty, so there is nothing to clear,
	// and resolving the target path — which for apply_patch parses the whole
	// (possibly large) patch payload — would waste work on the hot success
	// path for no reason.
	if !isError && *streaks == nil {
		return contextResult
	}
	path := editToolTargetPath(name, argsJSON, baseDir)
	if path == "" {
		return contextResult
	}
	if !isError {
		// Success confirms the target text is reachable again; a later
		// failure starts a fresh count. Deleting from a nil map is a no-op.
		delete(*streaks, path)
		return contextResult
	}
	if !tools.IsApproximateMatchFailure(name, err) {
		return contextResult
	}
	if *streaks == nil {
		*streaks = make(map[string]int)
	}
	(*streaks)[path]++
	streak := (*streaks)[path]
	if streak < editRetryAdviceThreshold || streak > editRetryAdviceCap {
		return contextResult
	}
	// The streak key is the resolved path (so path spellings that differ only
	// in form share one counter), but the model knows the file by the path it
	// typed, so the note spells it relative to the tool base dir.
	display := displayPathFromWorkDir(baseDir, path)
	note := fmt.Sprintf(
		"Note: %d repeated approximate-match failures for %s. The target text likely drifted beyond what the displayed difference shows: read the target range fresh (read with offset/limit), rebuild the old text from that output, or switch to write for a whole-block replacement. Do not retry the same old text from memory.",
		streak, display)
	return appendModelContextNote(contextResult, note)
}

// editToolTargetPath extracts the resolved target file path from an
// edit/apply_patch argument payload, for use as the per-turn failure-streak
// key. It reuses the canonical path extraction the pipeline already uses for
// file tracking — string-wrapped args, the legacy filePath alias, and
// relative/absolute path resolution are all handled there — so streak keys
// match the paths other subsystems track. A multi-file patch — whose failure
// cannot be localized to one target from the arguments alone — yields "" so
// the caller skips per-path tracking rather than keying the advisory on the
// wrong file. A missing or unparsable payload also yields "".
func editToolTargetPath(name, argsJSON, baseDir string) string {
	switch tools.NormalizeName(name) {
	case tools.NameEdit:
		return trackedEditPathFromArgs(json.RawMessage(argsJSON), baseDir)
	case tools.NameApplyPatch:
		targets, err := tools.ApplyPatchTargets(json.RawMessage(argsJSON), baseDir)
		if err != nil {
			return ""
		}
		var path string
		for _, t := range targets {
			p := strings.TrimSpace(t.SourcePath)
			if p == "" {
				continue
			}
			if path == "" {
				path = p
				continue
			}
			if path != p {
				return "" // multi-file patch: cannot localize the advisory
			}
		}
		return path
	}
	return ""
}
