package agent

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/keakon/chord/internal/tools"
)

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
