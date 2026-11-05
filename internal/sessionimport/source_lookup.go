package sessionimport

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type SourceFileLookup struct {
	Source string
	Root   string
	Path   string
}

func resolveImportInputPath(source string, inputPath string, sourceID string, root string) (SourceFileLookup, error) {
	source = strings.ToLower(strings.TrimSpace(source))
	inputPath = strings.TrimSpace(inputPath)
	sourceID = strings.TrimSpace(sourceID)
	root = strings.TrimSpace(root)
	if inputPath != "" {
		return SourceFileLookup{Source: source, Root: root, Path: inputPath}, nil
	}
	if sourceID == "" {
		return SourceFileLookup{}, fmt.Errorf("import: either input path or --id is required")
	}
	switch source {
	case "codex":
		if root == "" {
			root = filepath.Join(userHomeDir(), ".codex", "sessions")
		}
		path, err := findCodexRolloutByID(root, sourceID)
		if err != nil {
			return SourceFileLookup{}, err
		}
		return SourceFileLookup{Source: source, Root: root, Path: path}, nil
	case "claude":
		if root == "" {
			root = filepath.Join(userHomeDir(), ".claude", "projects")
		}
		path, err := findClaudeTranscriptByID(root, sourceID)
		if err != nil {
			return SourceFileLookup{}, err
		}
		return SourceFileLookup{Source: source, Root: root, Path: path}, nil
	case "opencode":
		return SourceFileLookup{}, fmt.Errorf("opencode import by --id is not supported directly; run `opencode export %s > file.json` and import that file", sourceID)
	default:
		return SourceFileLookup{}, fmt.Errorf("import: unsupported source %q", source)
	}
}

func findClaudeTranscriptByID(root string, sourceID string) (string, error) {
	var matches []string
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d == nil || d.IsDir() {
			return nil
		}
		base := filepath.Base(path)
		if !strings.HasSuffix(base, ".jsonl") {
			return nil
		}
		if strings.TrimSuffix(base, ".jsonl") == sourceID {
			matches = append(matches, path)
		}
		return nil
	})
	if len(matches) == 0 {
		return "", fmt.Errorf("claude import: no session found for id %q under %q; verify the id or pass an explicit file path", sourceID, root)
	}
	sort.Strings(matches)
	if len(matches) > 1 {
		return "", fmt.Errorf("claude import: multiple files matched id %q under %q; pass an explicit file path to choose one:\n%s", sourceID, root, strings.Join(matches, "\n"))
	}
	return matches[0], nil
}

// codexRolloutTimestampLayout is the fixed-width timestamp codex embeds in
// rollout file names. Lexicographic comparison of the 19 rendered characters
// is chronological, so the string is kept as-is for ordering.
const codexRolloutTimestampLayout = "2006-01-02T15-04-05"

// codexRolloutFileName is the parsed canonical rollout basename, matching
// codex's own RolloutFileName parser. Ordinary rollouts encode one id that is
// both the thread id and the rollout id; reverted threads append an
// underscore and a distinct rollout id after the stable thread id.
type codexRolloutFileName struct {
	Timestamp  string // fixed-width UTC wall clock; lexicographic == chronological
	ThreadID   string
	RolloutID  string
	Compressed bool // the file is a .jsonl.zst sidecar of the rollout
}

// parseCodexRolloutFileName parses a rollout base name of the form
// rollout-<YYYY-MM-DDTHH-MM-SS>-<thread-id>[_<rollout-id>].jsonl[.zst].
// Names that do not match the canonical shape (including non-UUID id
// segments) report false, mirroring codex's own parser.
func parseCodexRolloutFileName(name string) (codexRolloutFileName, bool) {
	parsed := codexRolloutFileName{}
	if strings.HasSuffix(name, ".zst") {
		parsed.Compressed = true
		name = strings.TrimSuffix(name, ".zst")
	}
	core, ok := strings.CutPrefix(name, "rollout-")
	if !ok {
		return parsed, false
	}
	core, ok = strings.CutSuffix(core, ".jsonl")
	if !ok || len(core) < 21 || core[19] != '-' {
		return parsed, false
	}
	parsed.Timestamp = core[:19]
	if _, err := time.Parse(codexRolloutTimestampLayout, parsed.Timestamp); err != nil {
		return parsed, false
	}
	ids := core[20:]
	threadID, rolloutID, hasSuffix := strings.Cut(ids, "_")
	if !hasSuffix {
		threadID, rolloutID = ids, ids
	}
	if !isCodexUUID(threadID) || rolloutID == "" || !isCodexUUID(rolloutID) {
		return parsed, false
	}
	parsed.ThreadID = strings.ToLower(threadID)
	parsed.RolloutID = strings.ToLower(rolloutID)
	return parsed, true
}

// isCodexUUID reports whether s is a canonical 36-character hyphenated UUID,
// the shape codex writes for thread and rollout ids in file names.
func isCodexUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i := 0; i < len(s); i++ {
		switch i {
		case 8, 13, 18, 23:
			if s[i] != '-' {
				return false
			}
		default:
			c := s[i]
			if (c < '0' || '9' < c) && (c < 'a' || 'f' < c) && (c < 'A' || 'F' < c) {
				return false
			}
		}
	}
	return true
}

// codexRolloutMatch is a rollout file found by a codex lookup.
type codexRolloutMatch struct {
	Path       string
	Compressed bool
}

// findCodexRolloutFile walks the given roots and returns the rollout file the
// matcher selects, applying codex's newest-file rule: the latest file name
// timestamp wins, ties break by the greater rollout id (the canonical UUID
// string compares like its byte order), and a full tie prefers the plain file
// over its compressed form.
func findCodexRolloutFile(roots []string, match func(codexRolloutFileName) bool) (codexRolloutMatch, bool) {
	var best codexRolloutMatch
	var bestName codexRolloutFileName
	found := false
	for _, root := range roots {
		_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil || d == nil || d.IsDir() {
				return nil
			}
			parsed, ok := parseCodexRolloutFileName(filepath.Base(path))
			if !ok || !match(parsed) {
				return nil
			}
			if !found || codexRolloutNameNewer(parsed, bestName) {
				best = codexRolloutMatch{Path: path, Compressed: parsed.Compressed}
				bestName = parsed
				found = true
			}
			return nil
		})
	}
	return best, found
}

// codexRolloutNameNewer reports whether a is strictly newer than b under the
// (timestamp, rollout id, plain-over-compressed) ordering.
func codexRolloutNameNewer(a, b codexRolloutFileName) bool {
	if a.Timestamp != b.Timestamp {
		return a.Timestamp > b.Timestamp
	}
	if a.RolloutID != b.RolloutID {
		return a.RolloutID > b.RolloutID
	}
	return !a.Compressed && b.Compressed
}

// findCodexRolloutByID follows Codex's filesystem fallback: match the stable
// thread id before any `_rollout-id` suffix and take the newest filename.
// Codex itself can prefer a path recorded in SQLite; import does not read it.
func findCodexRolloutByID(root string, sourceID string) (string, error) {
	match, found := findCodexRolloutFile([]string{root}, func(name codexRolloutFileName) bool {
		return strings.EqualFold(name.ThreadID, sourceID)
	})
	if !found {
		return "", fmt.Errorf("codex import: no session found for id %q under %q; verify the id with `codex resume %s`, or pass an explicit file path", sourceID, root, sourceID)
	}
	if match.Compressed {
		return "", fmt.Errorf("codex import: newest rollout for id %q is compressed (%s); compressed rollouts are not supported", sourceID, match.Path)
	}
	return match.Path, nil
}

// findCodexRolloutByRolloutID locates one immutable rollout file by its
// rollout id across the given roots, mirroring codex's
// find_rollout_path_by_rollout_id: unlike the thread-id lookup it selects
// exactly one page and is how codex itself follows SessionMeta.history_base.
func findCodexRolloutByRolloutID(roots []string, rolloutID string) (codexRolloutMatch, bool) {
	return findCodexRolloutFile(roots, func(name codexRolloutFileName) bool {
		return strings.EqualFold(name.RolloutID, rolloutID)
	})
}

// codexSearchRoots returns the directories searched for history_base base
// pages: the explicit --root alone when given, otherwise codex's default
// sessions and archived_sessions roots.
func codexSearchRoots(explicitRoot string) []string {
	if root := strings.TrimSpace(explicitRoot); root != "" {
		return []string{root}
	}
	home := userHomeDir()
	if home == "" {
		return nil
	}
	return []string{
		filepath.Join(home, ".codex", "sessions"),
		filepath.Join(home, ".codex", "archived_sessions"),
	}
}

func userHomeDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return home
}
