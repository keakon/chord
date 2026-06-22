package tools

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	pathSuggestionTimeout           = 800 * time.Millisecond
	pathSuggestionMaxVisitedEntries = 10_000
	pathSuggestionMaxCandidates     = 2_000
	pathSuggestionMaxResults        = 3
	pathSuggestionMinScore          = 70
)

type pathSuggestionOptions struct {
	Timeout        time.Duration
	MaxVisited     int
	MaxCandidates  int
	MaxSuggestions int
	MinScore       int
}

type pathSuggestion struct {
	Path  string
	Score int
}

var errStopPathSuggestionWalk = errors.New("stop path suggestion walk")

func defaultPathSuggestionOptions() pathSuggestionOptions {
	return pathSuggestionOptions{
		Timeout:        pathSuggestionTimeout,
		MaxVisited:     pathSuggestionMaxVisitedEntries,
		MaxCandidates:  pathSuggestionMaxCandidates,
		MaxSuggestions: pathSuggestionMaxResults,
		MinScore:       pathSuggestionMinScore,
	}
}

func fileNotFoundErrorWithPathSuggestions(path string, kind PathTargetKind) error {
	msg := fmt.Sprintf("file not found: %s", formatToolPathInCurrentDirectory(path, path))
	suggestions := suggestExistingToolPaths(path, kind)
	if len(suggestions) == 0 {
		return errors.New(msg)
	}
	var b strings.Builder
	b.WriteString(msg)
	b.WriteString("\nDid you mean:")
	for _, s := range suggestions {
		b.WriteString("\n- ")
		b.WriteString(s)
	}
	return errors.New(b.String())
}

func suggestExistingToolPaths(path string, kind PathTargetKind) []string {
	return suggestExistingToolPathsWithOptions(path, kind, defaultPathSuggestionOptions())
}

func suggestExistingToolPathsWithOptions(path string, kind PathTargetKind, opts pathSuggestionOptions) []string {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	if opts.Timeout <= 0 {
		opts.Timeout = pathSuggestionTimeout
	}
	if opts.MaxVisited <= 0 {
		opts.MaxVisited = pathSuggestionMaxVisitedEntries
	}
	if opts.MaxCandidates <= 0 {
		opts.MaxCandidates = pathSuggestionMaxCandidates
	}
	if opts.MaxSuggestions <= 0 {
		opts.MaxSuggestions = pathSuggestionMaxResults
	}
	if opts.MinScore <= 0 {
		opts.MinScore = pathSuggestionMinScore
	}

	resolved, err := resolveToolPath(path)
	if err != nil {
		return nil
	}
	deadline := time.Now().Add(opts.Timeout)
	candidateSet := make(map[string]struct{})
	candidates := make([]pathSuggestion, 0, opts.MaxSuggestions)
	addCandidate := func(candidate string, info fs.FileInfo) {
		if len(candidateSet) >= opts.MaxCandidates || time.Now().After(deadline) {
			return
		}
		if !pathSuggestionMatchesKind(info, kind) {
			return
		}
		if _, ok := candidateSet[candidate]; ok {
			return
		}
		score := scorePathSuggestion(resolved, candidate)
		if score < opts.MinScore {
			return
		}
		candidateSet[candidate] = struct{}{}
		candidates = append(candidates, pathSuggestion{Path: formatSuggestedPath(path, candidate), Score: score})
	}

	visited := 0
	if parent := filepath.Dir(resolved); parent != "." && directoryExists(parent) {
		scanDirectoryPathSuggestions(parent, kind, deadline, opts, &visited, addCandidate)
	}

	if len(candidates) == 0 {
		ancestor, ok := nearestExistingAncestorForPathSuggestions(path, resolved)
		if ok && shouldWalkForPathSuggestions(ancestor) {
			matcher := newGitIgnoreMatcher(ancestor)
			_ = filepath.WalkDir(ancestor, func(candidate string, d fs.DirEntry, err error) error {
				if err != nil {
					return nil
				}
				if time.Now().After(deadline) || visited >= opts.MaxVisited || len(candidateSet) >= opts.MaxCandidates {
					return errStopPathSuggestionWalk
				}
				visited++
				if candidate == ancestor {
					return nil
				}
				rel, relErr := filepath.Rel(ancestor, candidate)
				if relErr == nil {
					relSlash := filepath.ToSlash(rel)
					if matcher.Match(relSlash, d.IsDir()) {
						if d.IsDir() {
							return filepath.SkipDir
						}
						return nil
					}
				}
				if d.IsDir() {
					if skipDirNames[d.Name()] {
						return filepath.SkipDir
					}
					return nil
				}
				info, infoErr := d.Info()
				if infoErr != nil {
					return nil
				}
				addCandidate(candidate, info)
				return nil
			})
		}
	}

	if len(candidates) == 0 {
		return nil
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Score != candidates[j].Score {
			return candidates[i].Score > candidates[j].Score
		}
		return candidates[i].Path < candidates[j].Path
	})
	limit := min(opts.MaxSuggestions, len(candidates))
	out := make([]string, 0, limit)
	seen := make(map[string]struct{}, limit)
	for _, c := range candidates {
		if _, ok := seen[c.Path]; ok {
			continue
		}
		seen[c.Path] = struct{}{}
		out = append(out, c.Path)
		if len(out) == limit {
			break
		}
	}
	return out
}

func scanDirectoryPathSuggestions(dir string, kind PathTargetKind, deadline time.Time, opts pathSuggestionOptions, visited *int, addCandidate func(string, fs.FileInfo)) {
	f, err := os.Open(dir)
	if err != nil {
		return
	}
	defer f.Close()
	for time.Now().Before(deadline) && *visited < opts.MaxVisited {
		entries, err := f.ReadDir(128)
		for _, entry := range entries {
			if time.Now().After(deadline) || *visited >= opts.MaxVisited {
				return
			}
			(*visited)++
			if !pathSuggestionDirEntryNeedsInfo(entry, kind) {
				continue
			}
			info, err := entry.Info()
			if err != nil {
				continue
			}
			addCandidate(filepath.Join(dir, entry.Name()), info)
		}
		if errors.Is(err, io.EOF) {
			return
		}
		if err != nil {
			return
		}
	}
}

func pathSuggestionDirEntryNeedsInfo(entry fs.DirEntry, kind PathTargetKind) bool {
	if entry == nil {
		return false
	}
	switch kind {
	case PathTargetRegularFile:
		return entry.Type() == 0
	case PathTargetDirectory:
		return entry.IsDir() || entry.Type() == 0
	default:
		return true
	}
}

func pathSuggestionMatchesKind(info fs.FileInfo, kind PathTargetKind) bool {
	if info == nil {
		return false
	}
	switch kind {
	case PathTargetRegularFile:
		return info.Mode().IsRegular()
	case PathTargetDirectory:
		return info.IsDir()
	default:
		return true
	}
}

func directoryExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func nearestExistingAncestor(path string) (string, bool) {
	cur := filepath.Clean(path)
	for {
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", false
		}
		if directoryExists(parent) {
			return parent, true
		}
		cur = parent
	}
}

func nearestExistingAncestorForPathSuggestions(requested, resolved string) (string, bool) {
	ancestor, ok := nearestExistingAncestor(resolved)
	if !ok || ancestor != "." {
		return ancestor, ok
	}
	if filepath.IsAbs(strings.TrimSpace(requested)) || !directoryLooksLikeProjectRoot(".") {
		return "", false
	}
	return ".", true
}

func directoryLooksLikeProjectRoot(dir string) bool {
	for _, marker := range []string{".git", "go.mod", "package.json", "Cargo.toml", "pyproject.toml"} {
		if _, err := os.Stat(filepath.Join(dir, marker)); err == nil {
			return true
		}
	}
	return false
}

func shouldWalkForPathSuggestions(root string) bool {
	cleaned := filepath.Clean(root)
	return cleaned != string(filepath.Separator)
}

func formatSuggestedPath(requested, candidate string) string {
	if displayPath, ok := toolPathInCurrentDirectory(candidate); ok {
		return displayPath
	}
	trimmed := strings.TrimSpace(requested)
	if filepath.IsAbs(trimmed) {
		return candidate
	}
	wd, err := os.Getwd()
	if err != nil {
		return candidate
	}
	rel, err := filepath.Rel(wd, candidate)
	if err != nil || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || rel == ".." {
		return candidate
	}
	return rel
}

func formatToolPathInCurrentDirectory(path, fallback string) string {
	if displayPath, ok := toolPathInCurrentDirectory(path); ok {
		return displayPath
	}
	return fallback
}

func toolPathInCurrentDirectory(path string) (string, bool) {
	resolved, err := resolveToolPath(path)
	if err != nil {
		return "", false
	}
	wd, err := os.Getwd()
	if err != nil {
		return "", false
	}
	for _, pair := range [][2]string{{wd, resolved}, cleanSymlinkedPathPair(wd, resolved)} {
		rel, ok := relativePathIfWithin(pair[0], pair[1])
		if ok {
			return rel, true
		}
	}
	return "", false
}

func cleanSymlinkedPathPair(root, path string) [2]string {
	cleanedRoot, rootErr := filepath.EvalSymlinks(root)
	if rootErr != nil {
		cleanedRoot = root
	}
	cleanedPath, pathErr := filepath.EvalSymlinks(path)
	if pathErr != nil {
		cleanedPath = path
		if parent, parentErr := filepath.EvalSymlinks(filepath.Dir(path)); parentErr == nil {
			cleanedPath = filepath.Join(parent, filepath.Base(path))
		}
	}
	return [2]string{cleanedRoot, cleanedPath}
}

func relativePathIfWithin(root, path string) (string, bool) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", false
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", false
	}
	rel, err := filepath.Rel(absRoot, absPath)
	if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", false
	}
	return rel, true
}

func scorePathSuggestion(target, candidate string) int {
	targetClean := filepath.Clean(target)
	candidateClean := filepath.Clean(candidate)
	targetBase := strings.ToLower(filepath.Base(targetClean))
	candidateBase := strings.ToLower(filepath.Base(candidateClean))
	if targetBase == "." || targetBase == string(filepath.Separator) || candidateBase == "." {
		return -1
	}

	score := 0
	targetStem := strings.TrimSuffix(targetBase, filepath.Ext(targetBase))
	candidateStem := strings.TrimSuffix(candidateBase, filepath.Ext(candidateBase))
	switch {
	case candidateBase == targetBase:
		score += 120
	case candidateStem != "" && candidateStem == targetStem:
		score += 95
	default:
		baseScore := fuzzyNameScore(targetBase, candidateBase)
		stemScore := fuzzyNameScore(targetStem, candidateStem)
		best := max(baseScore, stemScore)
		if best < 55 {
			return -1
		}
		score += best
	}

	targetExt := strings.ToLower(filepath.Ext(targetBase))
	candidateExt := strings.ToLower(filepath.Ext(candidateBase))
	switch {
	case targetExt != "" && candidateExt == targetExt:
		score += 20
	case targetExt != "" && candidateExt != "":
		score -= 15
	}

	targetParts := pathSuggestionParts(targetClean)
	candidateParts := pathSuggestionParts(candidateClean)
	score += commonSuffixParts(targetParts, candidateParts) * 15
	score += commonPrefixParts(targetParts, candidateParts) * 3
	score -= absInt(len(candidateParts)-len(targetParts)) * 2
	return score
}

func fuzzyNameScore(a, b string) int {
	if a == "" || b == "" {
		return 0
	}
	if strings.Contains(b, a) || strings.Contains(a, b) {
		shorter := min(len([]rune(a)), len([]rune(b)))
		longer := max(len([]rune(a)), len([]rune(b)))
		return 70 + shorter*20/longer
	}
	dist := levenshteinDistance(a, b)
	longer := max(len([]rune(a)), len([]rune(b)))
	if longer == 0 {
		return 0
	}
	return max(0, 100-(dist*100/longer))
}

func levenshteinDistance(a, b string) int {
	ra := []rune(a)
	rb := []rune(b)
	if len(ra) == 0 {
		return len(rb)
	}
	if len(rb) == 0 {
		return len(ra)
	}
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i, ca := range ra {
		cur[0] = i + 1
		for j, cb := range rb {
			cost := 0
			if ca != cb {
				cost = 1
			}
			cur[j+1] = min(min(cur[j]+1, prev[j+1]+1), prev[j]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(rb)]
}

func pathSuggestionParts(path string) []string {
	cleaned := filepath.Clean(path)
	parts := strings.FieldsFunc(cleaned, func(r rune) bool {
		return r == '/' || r == '\\'
	})
	out := parts[:0]
	for _, part := range parts {
		if part != "" && part != "." {
			out = append(out, strings.ToLower(part))
		}
	}
	return out
}

func commonSuffixParts(a, b []string) int {
	count := 0
	for i, j := len(a)-1, len(b)-1; i >= 0 && j >= 0; i, j = i-1, j-1 {
		if a[i] != b[j] {
			break
		}
		count++
	}
	return count
}

func commonPrefixParts(a, b []string) int {
	limit := min(len(a), len(b))
	for i := 0; i < limit; i++ {
		if a[i] != b[i] {
			return i
		}
	}
	return limit
}

func absInt(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
