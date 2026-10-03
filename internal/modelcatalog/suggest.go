package modelcatalog

import (
	"slices"
	"sort"
	"strconv"
	"strings"
)

// Suggestion is one ranked catalog match for a user-typed wire model name.
type Suggestion struct {
	ModelID   string  // stable catalog identity, e.g. "openai/gpt-6-sol"
	Endpoint  string  // preset of a verified binding for display; may be empty
	WireModel string  // wire model ID of that binding, if any
	Score     float64 // 0..1; deterministic ordering below SuggestionMinScore is impossible
	Facts     ModelFacts
}

// SuggestionMinScore is the lowest similarity the suggestion list may show.
// A confidently wrong top hit is worse than no suggestion, so matches below
// this score are never returned and callers must not lower it per site.
const SuggestionMinScore = 0.35

// SuggestModels ranks verified catalog models against a user-typed wire model
// name. The result is deterministic: equal scores order by model ID, then by
// endpoint. At most one suggestion per model is returned.
func SuggestModels(query string, limit int) []Suggestion {
	return suggestModels(query, limit, effective())
}

func suggestModels(query string, limit int, c *Catalog) []Suggestion {
	if limit <= 0 {
		return nil
	}
	queryTokens := tokenizeModelName(query)
	if len(queryTokens) == 0 {
		return nil
	}
	var out []Suggestion
	seen := make(map[string]bool, len(c.Models))
	for i := range c.Models {
		facts := c.Models[i]
		if seen[facts.ID] {
			continue
		}
		seen[facts.ID] = true
		score := similarity(query, facts.ID)
		if score < SuggestionMinScore {
			continue
		}
		s := Suggestion{ModelID: facts.ID, Score: score, Facts: facts}
		if b, ok := bestBindingForModel(c, facts.ID); ok {
			s.Endpoint = b.Endpoint
			s.WireModel = b.WireModelID
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		if out[i].ModelID != out[j].ModelID {
			return out[i].ModelID < out[j].ModelID
		}
		return out[i].Endpoint < out[j].Endpoint
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// SuggestNewerVersion reports the highest-scoring verified model that shares a
// family with currentModelID, was matched against the same user-typed wire
// name, and carries a strictly greater version. It backs the stale-borrow
// advisory: a catalog reference that a newer release of the same family likely
// supersedes. The heuristic is advisory-only input; callers must never rebind
// automatically.
func SuggestNewerVersion(wireQuery, currentModelID string) (Suggestion, bool) {
	return suggestNewerVersion(wireQuery, currentModelID, effective())
}

func suggestNewerVersion(wireQuery, currentModelID string, c *Catalog) (Suggestion, bool) {
	for _, s := range suggestModels(wireQuery, len(c.Models)+1, c) {
		if s.ModelID == currentModelID {
			continue
		}
		if !sameFamily(currentModelID, s.ModelID) {
			continue
		}
		currentV, currentOK := modelVersion(modelNamePart(currentModelID))
		candidateV, candidateOK := modelVersion(modelNamePart(s.ModelID))
		if currentOK && candidateOK && candidateV.newerThan(currentV) {
			return s, true
		}
	}
	return Suggestion{}, false
}

// CandidateSuggestion is one ranked candidate entry for a user-typed wire
// name. The candidate's scope decides whether it plausibly describes the
// endpoint being configured; the caller annotates that, the score here only
// orders the list.
type CandidateSuggestion struct {
	Candidate Candidate
	Score     float64 // 0..1, same floor as SuggestionMinScore
}

// SuggestCandidates ranks refreshed candidate entries against a user-typed
// wire model name. Candidates arrive only through the refresh cache; without
// one there is nothing to suggest. The result is deterministic: equal scores
// order by wire model ID, then by scope.
func SuggestCandidates(query string, limit int) []CandidateSuggestion {
	candidates := EffectiveCandidates()
	if limit <= 0 || len(candidates) == 0 {
		return nil
	}
	var out []CandidateSuggestion
	for _, c := range candidates {
		score := similarity(query, c.WireModelID)
		if score < SuggestionMinScore {
			continue
		}
		out = append(out, CandidateSuggestion{Candidate: c, Score: score})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		if out[i].Candidate.WireModelID != out[j].Candidate.WireModelID {
			return out[i].Candidate.WireModelID < out[j].Candidate.WireModelID
		}
		return out[i].Candidate.Scope < out[j].Candidate.Scope
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

func bestBindingForModel(c *Catalog, modelID string) (Binding, bool) {
	var best Binding
	found := false
	for _, b := range c.Bindings {
		if b.ModelID != modelID {
			continue
		}
		if !found || b.Endpoint < best.Endpoint {
			best = b
			found = true
		}
	}
	return best, found
}

// similarity scores a user-typed wire name against a catalog model ID using
// token overlap plus version affinity. Both sides split on separators, so
// "gpt-6-sol-messages" overlaps "gpt-6-sol" strongly while unrelated families
// share nothing.
func similarity(query, modelID string) float64 {
	queryTokens := tokenizeModelName(query)
	candidateTokens := tokenizeModelName(modelNamePart(modelID))
	if len(candidateTokens) == 0 || len(queryTokens) == 0 {
		return 0
	}
	candidateSet := make(map[string]bool, len(candidateTokens))
	for _, t := range candidateTokens {
		candidateSet[t] = true
	}
	intersect, union := 0, len(candidateSet)
	seen := make(map[string]bool, len(queryTokens))
	for _, t := range queryTokens {
		if seen[t] {
			continue
		}
		seen[t] = true
		if candidateSet[t] {
			intersect++
			continue
		}
		union++
	}
	if union == 0 {
		return 0
	}
	base := float64(intersect) / float64(union)
	qv, qOK := modelVersion(query)
	cv, cOK := modelVersion(modelNamePart(modelID))
	if qOK && cOK && qv != cv {
		// Same family, different generation: near misses still rank, distant
		// ones fall away.
		distance := absFloat(float64(qv.major - cv.major))
		if qv.major == cv.major {
			distance = absFloat(float64(qv.minor-cv.minor)) / 10
		}
		base *= 1 / (1 + distance)
	}
	return base
}

// tokenizeModelName lowercases and splits a wire or catalog model name into
// comparable tokens. Version groups are split on dots as well so "6.1" and
// "6" stay distinct while "5-5" and "5.5" collapse onto the same tokens.
func tokenizeModelName(name string) []string {
	name = strings.ToLower(strings.TrimSpace(name))
	if slash := strings.LastIndex(name, "/"); slash >= 0 {
		name = name[slash+1:]
	}
	split := func(r rune) bool {
		return r == '-' || r == '_' || r == '.' || r == ' ' || r == '/' || r == ':'
	}
	var tokens []string
	for _, part := range strings.FieldsFunc(name, split) {
		if part != "" {
			tokens = append(tokens, part)
		}
	}
	return tokens
}

func modelNamePart(modelID string) string {
	if slash := strings.LastIndex(modelID, "/"); slash >= 0 {
		return modelID[slash+1:]
	}
	return modelID
}

type modelGeneration struct{ major, minor int }

func (v modelGeneration) newerThan(other modelGeneration) bool {
	return v.major > other.major || v.major == other.major && v.minor > other.minor
}

// modelVersion compares generation components as integers, so 6.10 follows 6.9.
func modelVersion(name string) (modelGeneration, bool) {
	i := strings.IndexFunc(name, func(r rune) bool { return r >= '0' && r <= '9' })
	if i < 0 {
		return modelGeneration{}, false
	}
	j := i
	for j < len(name) && name[j] >= '0' && name[j] <= '9' {
		j++
	}
	major, err := strconv.Atoi(name[i:j])
	if err != nil || major > 100 {
		return modelGeneration{}, false
	}
	minor := 0
	if j < len(name) && (name[j] == '.' || name[j] == '-') {
		k := j + 1
		for k < len(name) && name[k] >= '0' && name[k] <= '9' {
			k++
		}
		if k > j+1 && (k == len(name) || !isDigitLetter(name[k])) {
			minor, err = strconv.Atoi(name[j+1 : k])
			if err != nil {
				return modelGeneration{}, false
			}
		}
	}
	return modelGeneration{major, minor}, true
}

func isDigitLetter(r byte) bool {
	return (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
}

func isVersionToken(t string) bool {
	if t == "" {
		return false
	}
	for _, r := range t {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// sameFamily compares complete family names, keeping Sonnet and Opus separate.
func sameFamily(a, b string) bool {
	av, _, _ := strings.Cut(a, "/")
	bv, _, _ := strings.Cut(b, "/")
	if av != bv {
		return false
	}
	family := func(id string) []string {
		var tokens []string
		for _, token := range tokenizeModelName(id) {
			if !isVersionToken(token) {
				tokens = append(tokens, token)
			}
		}
		return tokens
	}
	af, bf := family(a), family(b)
	return len(af) > 0 && slices.Equal(af, bf)
}

func absFloat(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
