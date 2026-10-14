package filecompletion

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"unicode"
)

func isPathLikeAtMentionQuery(query string) bool {
	if strings.HasPrefix(query, "/") || strings.HasPrefix(query, "~") || strings.HasPrefix(query, ".") {
		return true
	}
	return strings.Contains(query, "/")
}

// ShouldUsePathMatches selects direct path browsing unless the index has an exact match.
func ShouldUsePathMatches(files []string, query string) bool {
	if !isPathLikeAtMentionQuery(query) {
		return false
	}
	if !strings.Contains(query, "/") {
		return true
	}
	if exact, ok := ExactIndexedMatch(files, query); ok && exact.Path != "" {
		return false
	}
	return true
}

// NormalizeQuery unescapes a query and normalizes its path separators.
func NormalizeQuery(query string) string {
	return filepath.ToSlash(strings.TrimSpace(UnescapePath(query)))
}

func normalizeAtMentionQueryForFS(query, workingDir string) string {
	normalized := NormalizeQuery(query)
	if path, ok := splitAtMentionLineRangeSuffix(normalized); ok && !PathExists(normalized, workingDir) {
		return path
	}
	return normalized
}

func normalizeAtMentionQueryForMatching(query string) string {
	normalized := NormalizeQuery(query)
	if path, ok := splitAtMentionLineRangeSuffix(normalized); ok {
		return path
	}
	return normalized
}

// PathExists reports whether the path resolves to an existing filesystem entry.
func PathExists(path, workingDir string) bool {
	resolved := ResolvePath(path, workingDir)
	_, err := os.Stat(resolved)
	return err == nil
}

func splitAtMentionLineRangeSuffix(query string) (string, bool) {
	matches := atMentionLineRangeSuffixRE.FindStringSubmatch(query)
	if matches == nil {
		return "", false
	}
	path := strings.TrimSpace(matches[1])
	return path, path != ""
}

// SplitLineRangeSuffix separates a valid numeric line-range suffix from a raw query.
func SplitLineRangeSuffix(query string) (path, suffix string, ok bool) {
	matches := atMentionLineRangeSuffixRE.FindStringSubmatchIndex(query)
	if matches == nil {
		return "", "", false
	}
	path = strings.TrimSpace(query[matches[2]:matches[3]])
	if path == "" {
		return "", "", false
	}
	start, err := strconv.Atoi(query[matches[4]:matches[5]])
	if err != nil || start <= 0 {
		return "", "", false
	}
	if matches[6] >= 0 {
		end, err := strconv.Atoi(query[matches[6]:matches[7]])
		if err != nil || end < start {
			return "", "", false
		}
	}
	return path, query[matches[3]:], true
}

// ExactPathMatch returns an exact filesystem candidate for the query.
func ExactPathMatch(query, workingDir string) (Option, bool) {
	normalized := normalizeAtMentionQueryForFS(query, workingDir)
	if normalized == "" || strings.HasSuffix(normalized, "/") {
		return Option{}, false
	}
	if normalized == "." || normalized == ".." || strings.HasSuffix(normalized, "/.") || strings.HasSuffix(normalized, "/..") {
		return Option{}, false
	}
	if normalized == "~" {
		return Option{Path: "~/", IsDir: true}, true
	}
	resolved := ResolvePath(normalized, workingDir)
	info, err := os.Stat(resolved)
	if err != nil {
		return Option{}, false
	}
	path := normalized
	if info.IsDir() {
		path = strings.TrimSuffix(path, "/") + "/"
	}
	return Option{Path: escapeAtMentionPath(path), IsDir: info.IsDir()}, true
}

// ExactIndexedMatch finds the query in the caller-owned file index.
func ExactIndexedMatch(files []string, query string) (Option, bool) {
	normalized := normalizeAtMentionQueryForMatching(query)
	if normalized == "" {
		return Option{}, false
	}
	for _, file := range files {
		if file == normalized {
			return Option{Path: escapeAtMentionPath(file)}, true
		}
	}
	return Option{}, false
}

func splitAtMentionPathQuery(query string) (dirPart, basePrefix string) {
	if strings.HasSuffix(query, "/") {
		return query, ""
	}
	idx := strings.LastIndex(query, "/")
	if idx < 0 {
		return "", query
	}
	return query[:idx+1], query[idx+1:]
}

func atMentionHiddenSegmentsAllowed(query string) bool {
	normalized := NormalizeQuery(query)
	if strings.HasSuffix(normalized, "/.") || strings.HasSuffix(normalized, "/..") {
		return true
	}
	for segment := range strings.SplitSeq(normalized, "/") {
		if segment == "" || segment == "." || segment == ".." || segment == "~" {
			continue
		}
		if strings.HasPrefix(segment, ".") {
			return true
		}
	}
	return false
}

func atMentionIsHiddenPath(path string) bool {
	normalized := NormalizeQuery(path)
	if normalized == "" {
		return false
	}
	for segment := range strings.SplitSeq(normalized, "/") {
		if segment == "" || segment == "." || segment == ".." || segment == "~" {
			continue
		}
		if strings.HasPrefix(segment, ".") {
			return true
		}
	}
	return false
}

// PathMatches lists direct filesystem candidates for a path query.
func PathMatches(query, workingDir string) []Option {
	if !isPathLikeAtMentionQuery(query) {
		return nil
	}
	matchQuery := normalizeAtMentionQueryForFS(query, workingDir)
	if matchQuery == ".." {
		return []Option{{Path: "../", IsDir: true}}
	}
	if matchQuery == "~" {
		return []Option{{Path: "~/", IsDir: true}}
	}
	if matchQuery == "." {
		entries, err := os.ReadDir(workingDir)
		if err != nil {
			return nil
		}
		matches := make([]Option, 0, len(entries))
		for _, entry := range entries {
			name := entry.Name()
			if !strings.HasPrefix(name, ".") || name == "." || name == ".." {
				continue
			}
			path := escapeAtMentionPath(name)
			if entry.IsDir() {
				path += "/"
			}
			matches = append(matches, Option{Path: path, IsDir: entry.IsDir()})
		}
		slices.SortFunc(matches, func(a, b Option) int {
			if a.IsDir != b.IsDir {
				if a.IsDir {
					return -1
				}
				return 1
			}
			return strings.Compare(a.Path, b.Path)
		})
		if len(matches) > 50 {
			matches = matches[:50]
		}
		return matches
	}
	if exact, ok := ExactPathMatch(matchQuery, workingDir); ok {
		return []Option{exact}
	}

	dirPart, basePrefix := splitAtMentionPathQuery(matchQuery)
	dirPartFS := UnescapePath(dirPart)
	basePrefixLower := strings.ToLower(UnescapePath(basePrefix))
	searchDir := workingDir
	if strings.HasPrefix(dirPartFS, "/") || strings.HasPrefix(dirPartFS, "~/") {
		searchDir = filepath.Clean(ResolvePath(dirPartFS, workingDir))
	} else if dirPartFS != "" {
		searchDir = filepath.Join(workingDir, filepath.FromSlash(dirPartFS))
	}
	if searchDir == "" {
		searchDir = "."
	}
	allowHidden := atMentionHiddenSegmentsAllowed(matchQuery)

	entries, err := os.ReadDir(searchDir)
	if err != nil {
		return nil
	}

	matches := make([]Option, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".") && !allowHidden {
			continue
		}
		if basePrefixLower != "" && !strings.HasPrefix(strings.ToLower(name), basePrefixLower) {
			continue
		}
		path := dirPart + escapeAtMentionPath(name)
		if entry.IsDir() {
			path += "/"
		}
		matches = append(matches, Option{Path: filepath.ToSlash(path), IsDir: entry.IsDir()})
	}

	slices.SortFunc(matches, func(a, b Option) int {
		if a.IsDir != b.IsDir {
			if a.IsDir {
				return -1
			}
			return 1
		}
		return strings.Compare(a.Path, b.Path)
	})
	if len(matches) > 50 {
		matches = matches[:50]
	}
	return matches
}

// RootPrefixMatches finds working-directory entries matching a bare name prefix.
func RootPrefixMatches(query, workingDir string) []Option {
	normalized := normalizeAtMentionQueryForFS(query, workingDir)
	if normalized == "" || isPathLikeAtMentionQuery(normalized) {
		return nil
	}
	if workingDir == "" {
		workingDir = "."
	}
	entries, err := os.ReadDir(workingDir)
	if err != nil {
		return nil
	}
	prefix := strings.ToLower(normalized)
	out := make([]Option, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(strings.ToLower(name), prefix) {
			continue
		}
		if skipIndexedPath(name) {
			continue
		}
		path := escapeAtMentionPath(name)
		if entry.IsDir() {
			path += "/"
		}
		out = append(out, Option{Path: path, IsDir: entry.IsDir()})
	}
	slices.SortFunc(out, func(a, b Option) int {
		if a.IsDir != b.IsDir {
			if a.IsDir {
				return -1
			}
			return 1
		}
		return strings.Compare(a.Path, b.Path)
	})
	if len(out) > 50 {
		out = out[:50]
	}
	return out
}

// MergeOptions combines candidate sources while preserving exact-case prefix priority.
func MergeOptions(primary, secondary []Option, query string) []Option {
	if len(primary) == 0 {
		return secondary
	}
	if len(secondary) == 0 {
		return primary
	}
	exactCaseSecondary, otherSecondary := splitAtMentionExactCasePrefixOptions(secondary, query)
	if len(exactCaseSecondary) > 0 {
		primary = append(exactCaseSecondary, primary...)
		secondary = otherSecondary
		if len(secondary) == 0 {
			if len(primary) > 50 {
				return primary[:50]
			}
			return primary
		}
	}
	seen := make(map[string]bool, len(primary)+len(secondary))
	out := make([]Option, 0, len(primary)+len(secondary))
	const limit = 50
	appendUnique := func(options []Option) {
		for _, option := range options {
			if seen[option.Path] {
				continue
			}
			seen[option.Path] = true
			out = append(out, option)
			if len(out) >= limit {
				return
			}
		}
	}
	appendUnique(primary)
	addedSecondary := false
	for _, option := range secondary {
		if seen[option.Path] {
			continue
		}
		seen[option.Path] = true
		if len(out) < limit {
			out = append(out, option)
			addedSecondary = true
			continue
		}
		if addedSecondary {
			break
		}
		out[len(out)-1] = option
		break
	}
	return out
}

func splitAtMentionExactCasePrefixOptions(options []Option, query string) ([]Option, []Option) {
	query = NormalizeQuery(query)
	if query == "" || isPathLikeAtMentionQuery(query) || !atMentionQueryHasUpper(query) {
		return nil, options
	}
	exact := make([]Option, 0, len(options))
	other := make([]Option, 0, len(options))
	for _, option := range options {
		path := strings.TrimSuffix(UnescapePath(option.Path), "/")
		if strings.HasPrefix(path, query) {
			exact = append(exact, option)
		} else {
			other = append(other, option)
		}
	}
	return exact, other
}

func atMentionQueryHasUpper(query string) bool {
	for _, r := range query {
		if unicode.IsUpper(r) {
			return true
		}
	}
	return false
}

// OptionsMissingFromIndex retains candidates not already covered by the file index.
func OptionsMissingFromIndex(options []Option, files []string) []Option {
	if len(options) == 0 {
		return nil
	}
	if len(files) == 0 {
		return options
	}
	indexed := make(map[string]struct{}, len(files))
	for _, file := range files {
		indexed[file] = struct{}{}
	}
	out := make([]Option, 0, len(options))
	for _, option := range options {
		path := strings.TrimSuffix(UnescapePath(option.Path), "/")
		if _, ok := indexed[path]; ok {
			continue
		}
		out = append(out, option)
	}
	return out
}
