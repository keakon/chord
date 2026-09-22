package tools

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"

	"github.com/keakon/chord/internal/pathutil"
)

// machineStateDirs are the repository-root directories that hold chord's own
// machine state rather than checkout content. They live under `.chord/`, which
// the repository ignores wholesale (`.`-prefixed paths), so a worktree
// checkout does not contain them; they describe this machine rather than the
// branch, and every read path already falls back to the content root for them.
// A relative write that resolved against a worktree checkout would therefore
// land in a directory that `worktree remove` deletes together with the
// checkout.
//
// Branch-content subdirectories are deliberately absent: `.chord/config.yaml`,
// `.chord/agents/**` and `.chord/skills/**` follow the checkout (a branch may
// carry its own copies), so redirecting their writes would split them from the
// copies reads pick up.
var machineStateDirs = []string{
	".chord/plans",
	".chord/notes",
	".chord/memory",
	// The internal documentation lives in its own private repository and is
	// shared from the main worktree, not copied into checkouts.
	".chord/docs",
	".chord/worktrees",
}

// machineStateFiles are machine-state files that live at the repository root
// instead of under `.chord/`. The ignore rule above does not cover them: a
// project may track MEMORY.md, but it is still this machine's memory rather
// than branch content, so reads already fall back to the content root's copy
// and a write inside a worktree must belong to the same file.
var machineStateFiles = []string{"MEMORY.md"}

// MachineStateTargetsInDir reports whether every path argument of a tool call
// names chord machine state once resolved against baseDir, and whether at least
// one such path was found. Callers use it to anchor the whole call — permission
// scope, file lock, pre-write capture and the tool itself — to the content root
// instead of the session working directory.
//
// The test is on the resolved path, so a relative `.chord/plans/x.md` and an
// absolute `<checkout>/.chord/plans/x.md` are treated alike: inside a worktree
// session the machine-state tree is the content root's, and reads and writes
// map there together. A path that resolves outside baseDir — an absolute path
// somewhere else on the filesystem — is never redirected.
//
// It returns false for calls it cannot fully classify (unknown tool, malformed
// arguments, or a mix of machine state and checkout content), which keeps those
// calls on the ordinary base directory.
func MachineStateTargetsInDir(toolName string, raw json.RawMessage, baseDir string) bool {
	paths, ok := machineStateCallPaths(toolName, raw, baseDir)
	if !ok || len(paths) == 0 {
		return false
	}
	for _, path := range paths {
		if strings.TrimSpace(path) == "" {
			return false
		}
		rel, inside := pathutil.RelToBase(path, baseDir)
		if !inside || !IsMachineStateRelPath(rel) {
			return false
		}
	}
	return true
}

// IsMachineStateRelPath reports whether a repository-relative path names chord
// machine state. The path must already be relative to the repository root.
func IsMachineStateRelPath(rel string) bool {
	rel = strings.TrimSpace(rel)
	if rel == "" {
		return false
	}
	rel = filepath.ToSlash(filepath.Clean(rel))
	if rel == "." || rel == ".." || strings.HasPrefix(rel, "../") {
		return false
	}
	for _, dir := range machineStateDirs {
		if rel == dir || strings.HasPrefix(rel, dir+"/") {
			return true
		}
	}
	return slices.Contains(machineStateFiles, rel)
}

// machineStateCallPaths extracts the paths a call will touch, resolved against
// baseDir. ok is false for tools whose targets cannot be enumerated with
// confidence, so an unclassifiable call is never redirected.
func machineStateCallPaths(toolName string, raw json.RawMessage, baseDir string) ([]string, bool) {
	switch toolName {
	case NameRead, NameWrite, NameViewImage:
		path := stringArg(raw, "path")
		if path == "" {
			return nil, false
		}
		return []string{resolveForMachineStateCheck(path, baseDir)}, true
	case NameEdit:
		path := ExtractEditPathFromArgsInDir(unwrapToolArgs(raw), baseDir)
		if path == "" {
			return nil, false
		}
		return []string{path}, true
	case NameDelete:
		req, err := DecodeDeleteRequestInDir(unwrapToolArgs(raw), baseDir)
		if err != nil {
			return nil, false
		}
		return req.Paths, true
	case NameApplyPatch:
		targets, err := ApplyPatchTargets(unwrapToolArgs(raw), baseDir)
		if err != nil {
			return nil, false
		}
		paths := MutationTargetPaths(targets)
		if len(paths) == 0 {
			return nil, false
		}
		return paths, true
	case NameGrep:
		paths := stringOrListArg(raw, "paths")
		if len(paths) == 0 {
			// "path" is the tolerated singular spelling of "paths".
			paths = stringOrListArg(raw, "path")
		}
		return resolveMachineStatePathList(paths, baseDir)
	case NameGlob:
		path := stringArg(raw, "path")
		if path == "" {
			return nil, false
		}
		return []string{resolveForMachineStateCheck(path, baseDir)}, true
	case NameHandoff:
		path := stringArg(raw, "plan_path")
		if path == "" {
			return nil, false
		}
		return []string{resolveForMachineStateCheck(path, baseDir)}, true
	default:
		return nil, false
	}
}

// resolveForMachineStateCheck resolves a path the way the tool would, so the
// caller compares the same spelling the tool will write. An absolute path is
// returned unchanged, which keeps explicit absolute targets out of the rule.
func resolveForMachineStateCheck(path, baseDir string) string {
	resolved, err := pathutil.ResolveInDir(path, baseDir)
	if err != nil {
		return ""
	}
	return resolved
}

// resolveMachineStatePathList resolves every search root of a read-only search
// call against baseDir. An empty list (the tool then searches the session
// working directory, which is checkout content) or one unresolvable path is not
// classifiable, so the call keeps its ordinary base directory.
func resolveMachineStatePathList(paths []string, baseDir string) ([]string, bool) {
	if len(paths) == 0 {
		return nil, false
	}
	resolved := make([]string, 0, len(paths))
	for _, path := range paths {
		r := resolveForMachineStateCheck(path, baseDir)
		if r == "" {
			return nil, false
		}
		resolved = append(resolved, r)
	}
	return resolved, true
}

// stringOrListArg returns the strings under field, accepting both the
// documented array form and the tolerated single-string form. A missing or
// malformed field yields nil.
func stringOrListArg(raw json.RawMessage, field string) []string {
	var parsed map[string]json.RawMessage
	if json.Unmarshal(unwrapToolArgs(raw), &parsed) != nil {
		return nil
	}
	value, ok := parsed[field]
	if !ok {
		return nil
	}
	list, _, err := DecodeStringOrList(value)
	if err != nil {
		return nil
	}
	return normalizeStringList(list)
}

func stringArg(raw json.RawMessage, field string) string {
	var parsed map[string]json.RawMessage
	if json.Unmarshal(unwrapToolArgs(raw), &parsed) != nil {
		return ""
	}
	value, ok := parsed[field]
	if !ok {
		return ""
	}
	var s string
	if json.Unmarshal(value, &s) != nil {
		return ""
	}
	return strings.TrimSpace(s)
}
