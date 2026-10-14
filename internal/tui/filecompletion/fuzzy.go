package filecompletion

import (
	"slices"
	"strings"
	"unicode/utf8"
)

// NarrowCache carries the previous fuzzy match's full candidate set so
// the next query that extends it (characters appended at the end, no new path
// separator) rescoring only the previous matches instead of the whole index.
// Every scorer's weakest tier is subsequence matching, and subsequence
// containment is transitive, so the match set shrinks monotonically under such
// extensions: files outside the previous set cannot match the extended query.
// Any other query change (backspace, cursor move, a typed "/") misses the
// prefix guard and falls back to a full scan.
// The zero value is empty. Callers must reset it when the source index changes;
// a cache belongs to one input query stream and is not safe for concurrent use.
type NarrowCache struct {
	valid       bool
	query       string
	allowHidden bool
	matched     []string
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
		if before, _, ok := strings.Cut(segmentStem, queryStem); ok {
			return 125 + utf8.RuneCountInString(before)*25 + stemLengthPenalty, true
		}
	}
	if strings.HasPrefix(segmentLower, querySeg) {
		return 50 + lengthPenalty, true
	}
	if before, _, ok := strings.Cut(segmentLower, querySeg); ok {
		return 150 + utf8.RuneCountInString(before)*20 + lengthPenalty, true
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
	if before, _, ok := strings.Cut(basename, query); ok {
		setBest(-3000 + utf8.RuneCountInString(before)*20 + lengthPenalty)
	}
	if strings.HasPrefix(fileLower, query) {
		setBest(-2500 + lengthPenalty)
	}
	if before, _, ok := strings.Cut(fileLower, query); ok {
		score := -2000 + utf8.RuneCountInString(before)*10 + lengthPenalty
		if utf8.RuneCountInString(before) >= basenameStart {
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

// FuzzyMatches scores the index against the query. lowerByFile
// carries the precomputed lowercase forms built once per index load; without it
// (ad-hoc callers) each file lowercases per keystroke, which on a 10k-file
// index dominates the per-key cost. narrow, when non-nil, remembers the full
// match set of the scored query and lets an appending query rescore only the
// previous matches.
func FuzzyMatches(files []string, lowerByFile map[string]string, query string, narrow *NarrowCache) []Option {
	matchQuery := normalizeAtMentionQueryForMatching(query)
	allowHidden := atMentionHiddenSegmentsAllowed(matchQuery)
	query = strings.ToLower(matchQuery)
	candidates := files
	if narrow != nil && narrow.valid && allowHidden == narrow.allowHidden &&
		len(matchQuery) > len(narrow.query) && strings.HasPrefix(matchQuery, narrow.query) &&
		!strings.Contains(matchQuery[len(narrow.query):], "/") {
		candidates = narrow.matched
	}
	type scored struct {
		path       string
		score      int
		queryLower string
	}
	var matched []scored
	for _, file := range candidates {
		if !allowHidden && atMentionIsHiddenPath(file) {
			continue
		}
		if query == "" {
			matched = append(matched, scored{path: file, score: 0, queryLower: query})
			continue
		}
		var fileLower string
		if lowerByFile != nil {
			fileLower = lowerByFile[file]
		}
		if fileLower == "" {
			fileLower = strings.ToLower(file)
		}
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
	slices.SortFunc(matched, func(a, b scored) int {
		if !strings.Contains(query, "/") {
			aDepth := atMentionPathDepth(a.path)
			bDepth := atMentionPathDepth(b.path)
			if aDepth != bDepth {
				return aDepth - bDepth
			}
		}
		if a.score != b.score {
			return a.score - b.score
		}
		return atMentionSortTieBreak(a.path, b.path, a.queryLower)
	})
	if narrow != nil {
		narrow.valid = true
		narrow.query = matchQuery
		narrow.allowHidden = allowHidden
		narrow.matched = make([]string, len(matched))
		for i, match := range matched {
			narrow.matched[i] = match.path
		}
	}
	if len(matched) > 50 {
		matched = matched[:50]
	}
	matches := make([]Option, len(matched))
	for i, match := range matched {
		matches[i] = Option{Path: escapeAtMentionPath(match.path)}
	}
	return matches
}

func atMentionPathDepth(path string) int {
	path = strings.Trim(path, "/")
	if path == "" {
		return 0
	}
	return strings.Count(path, "/")
}

// BuildLowerIndex precomputes the lowercase form of every indexed
// path once per index load, so per-keystroke fuzzy scoring does not re-lower
// the whole index on every keypress.
func BuildLowerIndex(files []string) map[string]string {
	if len(files) == 0 {
		return nil
	}
	lower := make(map[string]string, len(files))
	for _, file := range files {
		lower[file] = strings.ToLower(file)
	}
	return lower
}
