package tui

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/keakon/chord/internal/filectx"
	"github.com/keakon/chord/internal/message"
)

var atMentionTokenRE = regexp.MustCompile(`@(?:\\.|[^\s@])+`)

// Characters that commonly delimit a file reference in prose. They are only
// trimmed when the full candidate does not resolve to a file.
const atMentionTrimSuffixes = ".,;:!?)]}>\"'，。；：！？）】》」』’”"

// Characters that can separate an @ file reference from following prose. These
// are only used as a fallback after the full candidate and simple suffix trims
// fail, and candidates are tried from right to left to preserve the longest
// existing path when punctuation is part of a filename.
const atMentionProseDelimiters = ",;:!?)]}>\"'，。；：！？、）】》」』’”"

type atMentionOption struct {
	Path  string
	IsDir bool
}

func dedupeResolvedFileRefs(refs []string, workingDir string) []string {
	if len(refs) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(refs))
	out := make([]string, 0, len(refs))
	for _, ref := range refs {
		resolved := filepath.Clean(resolveAtMentionFSPath(ref, workingDir))
		if seen[resolved] {
			continue
		}
		seen[resolved] = true
		out = append(out, ref)
	}
	return out
}

// buildFileRefParts scans displayText and extraTexts for @-mentions, reads each
// referenced file into <file path=...> parts, and places them after composerParts
// so the model sees the user's prompt first, then file contents. Each composer
// text part keeps its full Text (raw pasted content) and optional DisplayText
// (inline placeholder for the composer UI), so multiple large pastes stay in the
// correct positions in the transcript and in the payload to the model.
func (m *Model) buildFileRefParts(displayText string, composerParts []message.ContentPart, extraTexts ...string) []message.ContentPart {
	texts := make([]string, 0, 1+len(extraTexts))
	if displayText != "" {
		texts = append(texts, displayText)
	}
	for _, extra := range extraTexts {
		if extra != "" {
			texts = append(texts, extra)
		}
	}
	refs := dedupeResolvedFileRefs(atMentionFileRefs(texts, m.workingDir), m.workingDir)
	if len(refs) == 0 {
		return nil
	}
	fileParts := filectx.BuildFileParts(refs, m.resolveFileRefPath)
	if len(fileParts) == 0 {
		return nil
	}
	parts := make([]message.ContentPart, 0, len(composerParts)+len(fileParts))
	for _, p := range composerParts {
		if p.Type != "text" || message.IsFileRefContent(p.Text) {
			continue
		}
		parts = append(parts, p)
	}
	parts = append(parts, fileParts...)
	return parts
}

func (m *Model) resolveFileRefPath(path string) string {
	return resolveAtMentionFSPath(path, m.workingDir)
}

func expandAtMentionPathPrefix(path string) string {
	if path == "~" {
		return "~/"
	}
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return path
		}
		if path == "~/" {
			return filepath.ToSlash(home) + "/"
		}
		return filepath.ToSlash(filepath.Join(home, filepath.FromSlash(strings.TrimPrefix(path, "~/"))))
	}
	return path
}

func resolveAtMentionFSPath(path, workingDir string) string {
	expanded := filepath.FromSlash(expandAtMentionPathPrefix(path))
	if filepath.IsAbs(expanded) {
		return expanded
	}
	if workingDir == "" {
		return expanded
	}
	return filepath.Join(workingDir, expanded)
}

func escapeAtMentionPath(path string) string {
	var b strings.Builder
	b.Grow(len(path))
	for _, r := range path {
		if unicode.IsSpace(r) || r == '\\' || r == '@' {
			b.WriteRune('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

func unescapeAtMentionPath(path string) string {
	if !strings.Contains(path, "\\") {
		return path
	}
	var b strings.Builder
	b.Grow(len(path))
	escaped := false
	for _, r := range path {
		switch {
		case escaped:
			b.WriteRune(r)
			escaped = false
		case r == '\\':
			escaped = true
		default:
			b.WriteRune(r)
		}
	}
	if escaped {
		b.WriteRune('\\')
	}
	return b.String()
}

func hasAtMentionBoundaryBefore(text string, start int) bool {
	if start <= 0 {
		return true
	}
	r, _ := utf8.DecodeLastRuneInString(text[:start])
	if r == utf8.RuneError {
		return false
	}
	return unicode.IsSpace(r) || strings.ContainsRune("([<{\"'“‘", r)
}

func atMentionCandidateExists(candidate, workingDir string, tried map[string]bool) bool {
	if candidate == "" || tried[candidate] {
		return false
	}
	tried[candidate] = true
	resolved := resolveAtMentionFSPath(candidate, workingDir)
	info, err := os.Stat(resolved)
	return err == nil && !info.IsDir()
}

func trimAtMentionCandidate(candidate string) (string, bool) {
	if candidate == "" {
		return "", false
	}
	r, size := utf8.DecodeLastRuneInString(candidate)
	if r == utf8.RuneError || !strings.ContainsRune(atMentionTrimSuffixes, r) {
		return "", false
	}
	return candidate[:len(candidate)-size], true
}

func proseDelimitedAtMentionCandidates(candidate string) []string {
	var out []string
	for i := len(candidate); i > 0; {
		r, size := utf8.DecodeLastRuneInString(candidate[:i])
		if r == utf8.RuneError {
			break
		}
		i -= size
		if !strings.ContainsRune(atMentionProseDelimiters, r) {
			continue
		}
		prefix := strings.TrimSpace(candidate[:i])
		if prefix != "" {
			out = append(out, prefix)
		}
	}
	return out
}

func resolveAtMentionCandidate(candidate, workingDir string) (string, bool) {
	candidate = filepath.ToSlash(unescapeAtMentionPath(candidate))
	candidate = strings.TrimSpace(candidate)
	if candidate == "" {
		return "", false
	}
	tried := map[string]bool{}
	for trimmed := candidate; trimmed != ""; {
		if atMentionCandidateExists(trimmed, workingDir, tried) {
			return trimmed, true
		}
		next, ok := trimAtMentionCandidate(trimmed)
		if !ok {
			break
		}
		trimmed = next
	}
	for _, delimited := range proseDelimitedAtMentionCandidates(candidate) {
		for trimmed := delimited; trimmed != ""; {
			if atMentionCandidateExists(trimmed, workingDir, tried) {
				return trimmed, true
			}
			next, ok := trimAtMentionCandidate(trimmed)
			if !ok {
				break
			}
			trimmed = next
		}
	}
	return "", false
}

func atMentionFileRefs(texts []string, workingDir string) []string {
	var refs []string
	seen := make(map[string]bool)
	for _, text := range texts {
		if text == "" {
			continue
		}
		for _, loc := range atMentionTokenRE.FindAllStringIndex(text, -1) {
			if len(loc) != 2 {
				continue
			}
			start, end := loc[0], loc[1]
			if !hasAtMentionBoundaryBefore(text, start) {
				continue
			}
			raw := text[start+1 : end]
			ref, ok := resolveAtMentionCandidate(raw, workingDir)
			if !ok {
				continue
			}
			resolved := filepath.Clean(resolveAtMentionFSPath(ref, workingDir))
			if seen[resolved] {
				continue
			}
			seen[resolved] = true
			refs = append(refs, ref)
		}
	}
	return refs
}

func inputLineAt(value string, line int) (string, bool) {
	lines := strings.Split(value, "\n")
	if line < 0 || line >= len(lines) {
		return "", false
	}
	return lines[line], true
}

func inputTokenAt(value string, line, startCol, endCol int) (string, bool) {
	row, ok := inputLineAt(value, line)
	if !ok {
		return "", false
	}
	runes := []rune(row)
	if startCol < 0 || startCol > len(runes) {
		return "", false
	}
	if endCol > len(runes) {
		endCol = len(runes)
	}
	segment := string(runes[startCol:endCol])
	escaped := false
	for _, r := range segment {
		switch {
		case escaped:
			escaped = false
		case r == '\\':
			escaped = true
		case r == ' ' || r == '\t':
			return "", false
		}
	}
	return segment, true
}

func canTriggerAtMention(row string, col int) bool {
	if col == 0 {
		return true
	}
	runes := []rune(row)
	if col < 0 || col > len(runes) {
		return false
	}
	return runes[col-1] == ' '
}

func isPathLikeAtMentionQuery(query string) bool {
	if strings.HasPrefix(query, "/") || strings.HasPrefix(query, "~") || strings.HasPrefix(query, ".") {
		return true
	}
	return strings.Contains(query, "/")
}

func atMentionShouldUsePathMatches(files []string, query string) bool {
	if !isPathLikeAtMentionQuery(query) {
		return false
	}
	if !strings.Contains(query, "/") {
		return true
	}
	if exact, ok := atMentionExactIndexedMatch(files, query); ok && exact.Path != "" {
		return false
	}
	return true
}

func normalizeAtMentionQuery(query string) string {
	return filepath.ToSlash(strings.TrimSpace(unescapeAtMentionPath(query)))
}

func atMentionExactPathMatch(query, workingDir string) (atMentionOption, bool) {
	normalized := normalizeAtMentionQuery(query)
	if normalized == "" || strings.HasSuffix(normalized, "/") {
		return atMentionOption{}, false
	}
	if normalized == "." || normalized == ".." || strings.HasSuffix(normalized, "/.") || strings.HasSuffix(normalized, "/..") {
		return atMentionOption{}, false
	}
	if normalized == "~" {
		return atMentionOption{Path: "~/", IsDir: true}, true
	}
	resolved := resolveAtMentionFSPath(normalized, workingDir)
	info, err := os.Stat(resolved)
	if err != nil {
		return atMentionOption{}, false
	}
	path := normalized
	if info.IsDir() {
		path = strings.TrimSuffix(path, "/") + "/"
	}
	return atMentionOption{Path: escapeAtMentionPath(path), IsDir: info.IsDir()}, true
}

func atMentionExactIndexedMatch(files []string, query string) (atMentionOption, bool) {
	normalized := normalizeAtMentionQuery(query)
	if normalized == "" {
		return atMentionOption{}, false
	}
	for _, file := range files {
		if file == normalized {
			return atMentionOption{Path: escapeAtMentionPath(file)}, true
		}
	}
	return atMentionOption{}, false
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
	normalized := normalizeAtMentionQuery(query)
	if strings.HasSuffix(normalized, "/.") || strings.HasSuffix(normalized, "/..") {
		return true
	}
	for _, segment := range strings.Split(normalized, "/") {
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
	normalized := normalizeAtMentionQuery(path)
	if normalized == "" {
		return false
	}
	for _, segment := range strings.Split(normalized, "/") {
		if segment == "" || segment == "." || segment == ".." || segment == "~" {
			continue
		}
		if strings.HasPrefix(segment, ".") {
			return true
		}
	}
	return false
}

func atMentionPathMatches(query, workingDir string) []atMentionOption {
	if !isPathLikeAtMentionQuery(query) {
		return nil
	}
	if query == "." {
		return []atMentionOption{{Path: "./", IsDir: true}}
	}
	if query == ".." {
		return []atMentionOption{{Path: "../", IsDir: true}}
	}
	if query == "~" {
		return []atMentionOption{{Path: "~/", IsDir: true}}
	}
	if exact, ok := atMentionExactPathMatch(query, workingDir); ok {
		return []atMentionOption{exact}
	}

	dirPart, basePrefix := splitAtMentionPathQuery(query)
	dirPartFS := unescapeAtMentionPath(dirPart)
	basePrefixLower := strings.ToLower(unescapeAtMentionPath(basePrefix))
	searchDir := workingDir
	if strings.HasPrefix(dirPartFS, "/") || strings.HasPrefix(dirPartFS, "~/") {
		searchDir = filepath.Clean(resolveAtMentionFSPath(dirPartFS, workingDir))
	} else if dirPartFS != "" {
		searchDir = filepath.Join(workingDir, filepath.FromSlash(dirPartFS))
	}
	if searchDir == "" {
		searchDir = "."
	}
	allowHidden := atMentionHiddenSegmentsAllowed(query)

	entries, err := os.ReadDir(searchDir)
	if err != nil {
		return nil
	}

	matches := make([]atMentionOption, 0, len(entries))
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
		matches = append(matches, atMentionOption{Path: filepath.ToSlash(path), IsDir: entry.IsDir()})
	}

	slices.SortFunc(matches, func(a, b atMentionOption) int {
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

func atMentionSubsequencePositions(haystack, needle string) ([]int, bool) {
	haystackRunes := []rune(haystack)
	needleRunes := []rune(needle)
	if len(needleRunes) == 0 {
		return nil, true
	}
	positions := make([]int, 0, len(needleRunes))
	cur := 0
	for _, want := range needleRunes {
		found := -1
		for cur < len(haystackRunes) {
			if haystackRunes[cur] == want {
				found = cur
				cur++
				break
			}
			cur++
		}
		if found < 0 {
			return nil, false
		}
		positions = append(positions, found)
	}
	return positions, true
}

func atMentionBoundaryHits(path string, positions []int) int {
	if len(positions) == 0 {
		return 0
	}
	runes := []rune(path)
	hits := 0
	for _, pos := range positions {
		if pos <= 0 {
			hits++
			continue
		}
		switch runes[pos-1] {
		case '/', '-', '_', '.':
			hits++
		}
	}
	return hits
}

func atMentionBasenameStart(path string) int {
	idx := strings.LastIndex(path, "/")
	if idx < 0 {
		return 0
	}
	return utf8.RuneCountInString(path[:idx+1])
}

func splitAtMentionStemExt(name string) (stem, ext string) {
	idx := strings.LastIndex(name, ".")
	if idx <= 0 || idx >= len(name)-1 {
		return name, ""
	}
	return name[:idx], name[idx+1:]
}

func atMentionSegmentScore(segmentLower, querySeg string) (int, bool) {
	segmentRunes := []rune(segmentLower)
	queryRunes := []rune(querySeg)
	if len(queryRunes) == 0 {
		return 0, true
	}
	lengthPenalty := len(segmentRunes) - len(queryRunes)
	if segmentLower == querySeg {
		return lengthPenalty, true
	}
	queryStem, queryExt := splitAtMentionStemExt(querySeg)
	segmentStem, segmentExt := splitAtMentionStemExt(segmentLower)
	if queryExt != "" && queryExt == segmentExt {
		stemLengthPenalty := utf8.RuneCountInString(segmentStem) - utf8.RuneCountInString(queryStem)
		if strings.HasPrefix(segmentStem, queryStem) {
			return 25 + stemLengthPenalty, true
		}
		if idx := strings.Index(segmentStem, queryStem); idx >= 0 {
			return 125 + utf8.RuneCountInString(segmentStem[:idx])*25 + stemLengthPenalty, true
		}
	}
	if strings.HasPrefix(segmentLower, querySeg) {
		return 50 + lengthPenalty, true
	}
	if idx := strings.Index(segmentLower, querySeg); idx >= 0 {
		return 150 + utf8.RuneCountInString(segmentLower[:idx])*20 + lengthPenalty, true
	}
	positions, ok := atMentionSubsequencePositions(segmentLower, querySeg)
	if !ok {
		return 0, false
	}
	span := positions[len(positions)-1] - positions[0] + 1
	gaps := span - len(queryRunes)
	score := 500 + gaps*20 + positions[0]*12 + lengthPenalty
	score -= atMentionBoundaryHits(segmentLower, positions) * 20
	return score, true
}

func atMentionPathSegmentScore(fileLower, query string) (int, bool) {
	querySegs := strings.Split(query, "/")
	if len(querySegs) <= 1 {
		return 0, false
	}
	fileSegs := strings.Split(fileLower, "/")
	if len(fileSegs) < len(querySegs) {
		return 0, false
	}
	firstQuery := strings.Join(querySegs[:len(querySegs)-1], "/")
	lastQuery := querySegs[len(querySegs)-1]
	best := 0
	matched := false
	for i := 0; i < len(fileSegs)-1; i++ {
		prefix := strings.Join(fileSegs[:i+1], "/")
		firstScore, ok := atMentionSegmentScore(prefix, firstQuery)
		if !ok {
			continue
		}
		for j := i + 1; j < len(fileSegs); j++ {
			lastScore, ok := atMentionSegmentScore(fileSegs[j], lastQuery)
			if !ok {
				continue
			}
			score := firstScore + lastScore + (j-i-1)*120
			if i == 0 {
				score -= 50
			}
			if j == len(fileSegs)-1 {
				score -= 200
			}
			if !matched || score < best {
				best = score
				matched = true
			}
		}
	}
	if !matched {
		return 0, false
	}
	return best, true
}

func atMentionScoreBasic(fileLower, query string) (int, bool) {
	fileRunes := []rune(fileLower)
	queryRunes := []rune(query)
	if len(queryRunes) == 0 {
		return 0, true
	}
	basenameStart := atMentionBasenameStart(fileLower)
	basename := string(fileRunes[basenameStart:])
	lengthPenalty := len(fileRunes) - len(queryRunes)

	bestScore := 0
	matched := false
	setBest := func(score int) {
		if !matched || score < bestScore {
			bestScore = score
			matched = true
		}
	}

	if strings.HasPrefix(basename, query) {
		setBest(-4000 + lengthPenalty)
	}
	if idx := strings.Index(basename, query); idx >= 0 {
		setBest(-3000 + utf8.RuneCountInString(basename[:idx])*20 + lengthPenalty)
	}
	if strings.HasPrefix(fileLower, query) {
		setBest(-2500 + lengthPenalty)
	}
	if idx := strings.Index(fileLower, query); idx >= 0 {
		score := -2000 + utf8.RuneCountInString(fileLower[:idx])*10 + lengthPenalty
		if utf8.RuneCountInString(fileLower[:idx]) >= basenameStart {
			score -= 200
		}
		setBest(score)
	}

	positions, ok := atMentionSubsequencePositions(fileLower, query)
	if ok {
		span := positions[len(positions)-1] - positions[0] + 1
		gaps := span - len(queryRunes)
		score := 5000 + gaps*20 + positions[0]*8 + lengthPenalty
		if positions[0] >= basenameStart {
			score -= 300
		}
		score -= atMentionBoundaryHits(fileLower, positions) * 25
		setBest(score)
	}
	return bestScore, matched
}

// atMentionScore returns a relevance score for a file path matching a query.
// Lower score = better match. The bool reports whether the path matched at all.
func atMentionScore(fileLower, query string) (int, bool) {
	if query == "" {
		return 0, true
	}
	if strings.Contains(query, "/") {
		if segmentScore, ok := atMentionPathSegmentScore(fileLower, query); ok {
			return segmentScore, true
		}
		return 0, false
	}
	if score, ok := atMentionScoreBasic(fileLower, query); ok {
		return score, true
	}
	return 0, false
}

func atMentionSortTieBreak(a, b string, query string) int {
	if !strings.Contains(query, "/") {
		return strings.Compare(a, b)
	}
	parts := strings.Split(query, "/")
	if len(parts) < 2 {
		return strings.Compare(a, b)
	}
	prefix := strings.Join(parts[:len(parts)-1], "/") + "/"
	aHasPrefix := strings.Contains(strings.ToLower(a), prefix)
	bHasPrefix := strings.Contains(strings.ToLower(b), prefix)
	if aHasPrefix != bHasPrefix {
		if aHasPrefix {
			return -1
		}
		return 1
	}
	return strings.Compare(a, b)
}

func atMentionFuzzyMatches(files []string, query string) []atMentionOption {
	allowHidden := atMentionHiddenSegmentsAllowed(query)
	query = strings.ToLower(normalizeAtMentionQuery(query))
	type scored struct {
		path       string
		score      int
		queryLower string
	}
	var matched []scored
	for _, file := range files {
		if !allowHidden && atMentionIsHiddenPath(file) {
			continue
		}
		if query == "" {
			matched = append(matched, scored{path: file, score: 0, queryLower: query})
			if len(matched) >= 50 {
				break
			}
			continue
		}
		fileLower := strings.ToLower(file)
		score := -1
		matchedScore := false
		if strings.Contains(query, "/") {
			querySegs := strings.Split(query, "/")
			if len(querySegs) >= 2 {
				prefix := strings.Join(querySegs[:len(querySegs)-1], "/")
				if strings.Contains(fileLower, prefix+"/") {
					base := fileLower[strings.LastIndex(fileLower, "/")+1:]
					if lastScore, ok := atMentionSegmentScore(base, querySegs[len(querySegs)-1]); ok {
						score = lastScore - 500
						matchedScore = true
					}
				}
			}
			if !matchedScore {
				if segScore, ok := atMentionPathSegmentScore(fileLower, query); ok {
					score = segScore
					matchedScore = true
				}
			}
			if !matchedScore {
				if basicScore, ok := atMentionScoreBasic(fileLower, query); ok {
					score = basicScore
					matchedScore = true
				}
			}
		} else {
			score, matchedScore = atMentionScore(fileLower, query)
		}
		if matchedScore {
			matched = append(matched, scored{path: file, score: score, queryLower: query})
		}
	}
	if query != "" {
		slices.SortFunc(matched, func(a, b scored) int {
			if a.score != b.score {
				return a.score - b.score
			}
			return atMentionSortTieBreak(a.path, b.path, a.queryLower)
		})
		if len(matched) > 50 {
			matched = matched[:50]
		}
	}
	matches := make([]atMentionOption, len(matched))
	for i, match := range matched {
		matches[i] = atMentionOption{Path: escapeAtMentionPath(match.path)}
	}
	return matches
}
