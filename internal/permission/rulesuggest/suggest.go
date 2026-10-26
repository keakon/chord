package rulesuggest

import (
	"encoding/json"
	"net/url"
	pathpkg "path"
	"sort"
	"strings"

	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/toolname"
)

// Candidate represents a suggested rule pattern for a tool invocation.
type Candidate struct {
	Tool    string // tool the rule names; empty means the confirmed tool
	Pattern string // the rule pattern (e.g. "git log *")
	Summary string // human-readable description (e.g. "same command, any flags")
	Broad   bool   // true if pattern is very broad (e.g. "*", "git *")
	Default bool   // true if this is the recommended default candidate
}

const maxPatternCandidates = 6

// pathDirectoryRule is one reusable directory rule derived from requested
// paths. The pattern is "dir/*": the permission engine's "*" crosses
// separators, so the rule matches every path under dir at any depth.
type pathDirectoryRule struct {
	dir     string
	pattern string
	summary string
}

// rankDirectoryRules derives reusable directory rules from normalized paths,
// ranked by how many paths each covers and then lexically, so a common
// ancestor is not crowded out by its own subdirectories. A directory whose
// ancestor is already selected is folded into that ancestor's rule instead of
// taking another slot. No rule is returned for the working-directory root,
// which callers spell with "**" or "*" instead.
func rankDirectoryRules(normalized []string, limit int) []pathDirectoryRule {
	dirs := make([]string, 0, len(normalized))
	seen := make(map[string]struct{}, len(normalized))
	for _, p := range normalized {
		dir := pathpkg.Dir(p)
		if dir == "." || dir == "" || dir == "/" {
			continue
		}
		if _, ok := seen[dir]; ok {
			continue
		}
		seen[dir] = struct{}{}
		dirs = append(dirs, dir)
	}

	type rankedDirectory struct {
		dir   string
		count int
	}
	ranked := make([]rankedDirectory, 0, len(dirs))
	for _, dir := range dirs {
		prefix := dir + "/"
		count := 0
		for _, p := range normalized {
			if strings.HasPrefix(p, prefix) {
				count++
			}
		}
		if count == 0 {
			continue
		}
		ranked = append(ranked, rankedDirectory{dir: dir, count: count})
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].count != ranked[j].count {
			return ranked[i].count > ranked[j].count
		}
		return ranked[i].dir < ranked[j].dir
	})

	rules := make([]pathDirectoryRule, 0, len(ranked))
	selected := make([]string, 0, len(ranked))
	for _, candidate := range ranked {
		if len(rules) >= limit {
			break
		}
		covered := false
		for _, parent := range selected {
			if strings.HasPrefix(candidate.dir, parent+"/") {
				covered = true
				break
			}
		}
		if covered {
			continue
		}
		selected = append(selected, candidate.dir)
		rules = append(rules, pathDirectoryRule{
			dir:     candidate.dir,
			pattern: pathpkg.Join(candidate.dir, "*"),
			summary: "any path under " + candidate.dir + "/",
		})
	}
	return rules
}

// Suggest generates pattern candidates for a tool
// invocation.
// toolName: the tool name (e.g. "Shell", "Write")
// argsJSON: the tool arguments as JSON
// needsApproval: explicit paths that need approval (for Delete)
// needsApprovalRules: ask rules that already matched, offered as candidates
// scope: path evaluation scope of the call being confirmed, used to spell
// candidate paths the way the permission engine matches them
func Suggest(toolName, argsJSON string, needsApproval []string, needsApprovalRules []permission.Rule, scope permission.PathScope) []Candidate {
	switch toolname.Normalize(toolName) {
	case toolname.Shell:
		return suggestShellPatterns(argsJSON, needsApproval, permission.RulePatterns(needsApprovalRules))
	case toolname.ApplyPatch:
		return suggestApplyPatchPatterns(toolName, needsApproval, needsApprovalRules, scope)
	case toolname.Edit, toolname.Write:
		return suggestFilePatterns(toolName, argsJSON, scope)
	case toolname.WebFetch:
		return suggestWebFetchPatterns(argsJSON)
	case toolname.Delete:
		return suggestDeletePatterns(argsJSON, needsApproval, scope)
	case toolname.Read, toolname.ViewImage, toolname.Grep, toolname.Glob, toolname.Skill:
		return normalizePatternCandidates([]Candidate{
			{Pattern: "*", Summary: "any " + toolName + " call", Broad: true, Default: true},
		})
	default:
		return normalizePatternCandidates([]Candidate{
			{Pattern: "*", Summary: "any tool call", Broad: true, Default: true},
		})
	}
}

// suggestShellPatterns generates pattern candidates for Shell commands.
func suggestShellPatterns(argsJSON string, needsApproval []string, needsApprovalRules []string) []Candidate {
	command := extractShellCommand(argsJSON)
	if command == "" {
		return normalizePatternCandidates([]Candidate{
			{Pattern: "*", Summary: "any Shell command", Broad: true, Default: true},
		})
	}

	if shellCommandIsComplex(command) && len(needsApprovalRules) > 0 {
		return suggestShellPatternsFromMatchedRules(needsApprovalRules)
	}

	// If needsApproval has a specific subcommand, prefer that
	seed := command
	if len(needsApproval) > 0 && strings.TrimSpace(needsApproval[0]) != "" {
		seed = strings.TrimSpace(needsApproval[0])
	}

	return buildBashCandidates(seed)
}

// suggestShellPatternsFromMatchedRules builds candidates for a compound command
// from the user's matched ask rules only: the rules as written (pre-selected),
// each generalized to "cmd *", and a final "*" catch-all. Literal candidates for
// the exact command or blocked subcommands are intentionally omitted because a
// rule carrying concrete file arguments is essentially never reusable.
func suggestShellPatternsFromMatchedRules(needsApprovalRules []string) []Candidate {
	candidates := make([]Candidate, 0, len(needsApprovalRules)*2+1)
	for _, pattern := range needsApprovalRules {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" {
			continue
		}
		candidates = append(candidates, Candidate{Pattern: pattern, Summary: "matched ask rule", Default: true})
	}
	for _, pattern := range generalizeShellRulePatterns(needsApprovalRules) {
		candidates = append(candidates, Candidate{Pattern: pattern, Summary: "broader matched rule", Broad: true})
	}
	candidates = append(candidates, Candidate{Pattern: "*", Summary: "any Shell command", Broad: true})
	return normalizePatternCandidates(candidates)
}

func generalizeShellRulePatterns(patterns []string) []string {
	var result []string
	for _, pattern := range patterns {
		parts := strings.Fields(strings.TrimSpace(pattern))
		if len(parts) < 2 || parts[0] == "*" {
			continue
		}
		result = append(result, parts[0]+" *")
	}
	return result
}

// buildBashCandidates builds pattern candidates from a command string.
func buildBashCandidates(seed string) []Candidate {
	trimmed := strings.TrimSpace(seed)

	// Check for complex commands (pipes, chains, subshells, or multi-line/heredoc)
	isComplex := shellCommandIsComplex(trimmed)

	if isComplex {
		// Complex commands: only literal + very broad
		return normalizePatternCandidates([]Candidate{
			{Pattern: trimmed, Summary: "literal (this exact command)", Default: true},
			{Pattern: "*", Summary: "any Shell command", Broad: true},
		})
	}

	parts := strings.Fields(trimmed)
	if len(parts) == 0 {
		return normalizePatternCandidates([]Candidate{
			{Pattern: "*", Summary: "any Shell command", Broad: true, Default: true},
		})
	}

	// No exact-command candidate: every ladder pattern below already matches the
	// command as written, so the exact text would only repeat the narrowest rule
	// the user can accept. Rules that can only allow this one command need `E`,
	// which takes any pattern.
	var candidates []Candidate

	if len(parts) >= 2 {
		// head2 *: first two words + wildcard
		head2 := strings.Join(parts[:2], " ") + " *"
		head2Summary := "same command, any flags"
		if len(parts) > 2 {
			head2Summary = "same command prefix, any arguments"
		}
		// A flag prefix does not constrain targets (for example rm -rf *).
		broad := strings.HasPrefix(parts[1], "-")
		if broad {
			head2Summary = "same command prefix, any paths or arguments"
		}
		candidates = append(candidates, Candidate{
			Broad:   broad,
			Pattern: head2,
			Summary: head2Summary,
			Default: true,
		})
	}

	// head1 *: first word + wildcard
	head1 := parts[0] + " *"
	candidates = append(candidates, Candidate{
		Pattern: head1,
		Summary: "any " + parts[0] + " subcommand",
		Broad:   true,
		Default: len(parts) == 1,
	})

	// Very broad: *
	candidates = append(candidates, Candidate{
		Pattern: "*",
		Summary: "any Shell command",
		Broad:   true,
	})

	return normalizePatternCandidates(candidates)
}

func shellCommandIsComplex(command string) bool {
	return strings.ContainsAny(command, "|;&") || strings.Contains(command, "$(") || strings.Contains(command, "`") || strings.Contains(command, "\n") || strings.Contains(command, "<<")
}

// suggestFilePatterns generates pattern candidates for Edit/Write tools.
// Paths are normalized through the permission scope, so a candidate is
// spelled the way the permission engine matches the call no matter which
// checkout or subdirectory the session runs in.
func suggestFilePatterns(toolName, argsJSON string, scope permission.PathScope) []Candidate {
	filePath := extractFilePath(argsJSON)
	if filePath == "" {
		return normalizePatternCandidates([]Candidate{
			{Pattern: "*", Summary: "any " + toolName + " call", Broad: true, Default: true},
		})
	}

	normalized := permission.NormalizeRulePath(filePath, scope)
	inScope := permission.RulePathInScope(filePath, scope)

	var candidates []Candidate

	// Literal
	candidates = append(candidates, Candidate{
		Pattern: normalized,
		Summary: "this exact file",
	})

	// The parent directory rule is the narrowest reusable rule for this file
	// and, for an out-of-scope path, it is spelled absolute so it still
	// matches. It stays pre-selected there instead of the one-off exact file.
	for _, rule := range rankDirectoryRules([]string{normalized}, 1) {
		candidates = append(candidates, Candidate{
			Pattern: rule.pattern,
			Summary: rule.summary,
			Default: true,
		})
	}

	// **/*.<ext>
	// A relative "**" pattern only matches in-scope paths, so the candidate is
	// only useful when the file lies on the relative side of the boundary.
	ext := pathpkg.Ext(normalized)
	if ext != "" && inScope {
		candidates = append(candidates, Candidate{
			Pattern: "**/*" + ext,
			Summary: "any " + ext + " file",
			Broad:   true,
		})
	}

	// ** - every path the scope spells relative (the whole repository when the
	// checkout roots are known).
	if inScope {
		candidates = append(candidates, Candidate{
			Pattern: "**",
			Summary: ruleScopeSummary(scope),
			Broad:   true,
		})
	}

	// Very broad
	candidates = append(candidates, Candidate{
		Pattern: "*",
		Summary: "any " + toolName + " call",
		Broad:   true,
	})

	return normalizePatternCandidates(candidates)
}

// ruleScopeSummary describes what a relative "**" rule covers: every checkout
// of the repository when the scope carries checkout roots, otherwise the
// working directory the scope falls back to.
func ruleScopeSummary(scope permission.PathScope) string {
	if len(scope.Roots) > 0 || len(scope.Containers) > 0 {
		return "any path in this repository"
	}
	return "any path under the working directory"
}

// suggestDeletePatterns generates reusable directory-scoped candidates for
// Delete. Exact-file rules are omitted because a successfully deleted path is
// unlikely to be useful again. Directory candidates are ranked by how many
// requested paths they cover, while scope-wide "**" and global "*" candidates
// have reserved slots so a large batch cannot crowd them out.
func suggestDeletePatterns(argsJSON string, needsApproval []string, scope permission.PathScope) []Candidate {
	paths := append([]string(nil), needsApproval...)
	var req struct {
		Paths []string `json:"paths"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &req); err == nil {
		if len(paths) == 0 {
			paths = append(paths, req.Paths...)
		}
	}
	requestedPaths := req.Paths
	if len(requestedPaths) == 0 {
		requestedPaths = paths
	}
	// Rank and deduplicate on rule spellings, not raw arguments: two spellings
	// of one file (for example a checkout-absolute and a cwd-relative path)
	// must collapse to one candidate and count once.
	normalizedPaths := dedupePaths(normalizeRulePaths(paths, scope))

	includeWildcard := allTargetsInRuleScope(requestedPaths, scope)
	reserved := 1 // The global "*" catch-all is always present.
	if includeWildcard {
		reserved++
	}
	directoryLimit := maxPatternCandidates - reserved
	candidates := make([]Candidate, 0, maxPatternCandidates)
	for i, rule := range rankDirectoryRules(normalizedPaths, directoryLimit) {
		candidates = append(candidates, Candidate{
			Pattern: rule.pattern,
			Summary: rule.summary,
			Default: i == 0,
		})
	}
	if includeWildcard {
		candidates = append(candidates, Candidate{
			Pattern: "**",
			Summary: ruleScopeSummary(scope),
			Broad:   true,
		})
	}
	candidates = append(candidates, Candidate{Pattern: "*", Summary: "any Delete call", Broad: true})
	return candidates
}

// allTargetsInRuleScope reports whether every requested path lies on the
// relative side of the scope boundary, so a scope-wide "**" rule would match
// all of them. An empty or entirely out-of-scope request reports false.
func allTargetsInRuleScope(paths []string, scope permission.PathScope) bool {
	found := false
	for _, raw := range paths {
		p := strings.TrimSpace(raw)
		if p == "" {
			continue
		}
		found = true
		if !permission.RulePathInScope(p, scope) {
			return false
		}
	}
	return found
}

// suggestApplyPatchPatterns builds candidates from the paths that asked and
// the specific rules that raised the ask. A patch writes and removes files, so
// the standalone write/delete rules apply alongside apply_patch's own rules
// (an `edit` rule is inherited through the edit/patch counterpart). A
// candidate must name whichever tool's rule it relaxes: an apply_patch allow
// rule cannot lift a delete ask on a moved source path, which is why the
// previous apply_patch-only suggestion kept asking on the next identical call.
// Each blocked tool group offers its narrow directory rules first, then the
// repository-wide "**" when every ask path is in scope, then its tool-wide "*";
// only the narrow rules are pre-selected.
func suggestApplyPatchPatterns(toolName string, needsApproval []string, rules []permission.Rule, scope permission.PathScope) []Candidate {
	askPaths := dedupePaths(needsApproval)
	if len(askPaths) == 0 {
		return normalizePatternCandidates([]Candidate{
			{Pattern: "*", Summary: "any " + toolName + " call", Broad: true, Default: true},
		})
	}

	var deletePaths, writePaths, patchPaths []string
	for _, path := range askPaths {
		deleteBlocked, writeBlocked, patchBlocked, named := false, false, false, false
		for _, rule := range rules {
			if rule.Action != permission.ActionAsk {
				continue
			}
			if rule.CoversPath(toolname.Delete, path, scope) {
				deleteBlocked, named = true, true
			}
			if rule.CoversPath(toolname.Write, path, scope) {
				writeBlocked, named = true, true
			}
			if rule.CoversPath(toolname.ApplyPatch, path, scope) || rule.CoversPath(toolname.Edit, path, scope) {
				patchBlocked, named = true, true
			}
		}
		// No named rule matched: the ask comes from apply_patch's wildcard
		// default, which an apply_patch path rule can override.
		if !named {
			patchBlocked = true
		}
		if deleteBlocked {
			deletePaths = append(deletePaths, path)
		}
		if writeBlocked {
			writePaths = append(writePaths, path)
		}
		if patchBlocked {
			patchPaths = append(patchPaths, path)
		}
	}

	groups := []struct {
		tool  string
		paths []string
	}{
		{toolname.Delete, deletePaths},
		{toolname.Write, writePaths},
		{toolname.ApplyPatch, patchPaths},
	}
	activeGroups := 0
	for _, group := range groups {
		if len(group.paths) > 0 {
			activeGroups++
		}
	}
	perGroup := maxPatternCandidates
	if activeGroups > 0 {
		perGroup = max(1, maxPatternCandidates/activeGroups)
	}
	// A group-wide "**" ranks between the directory rules and "*": it covers
	// every in-scope path for that tool without allowing calls outside the
	// repository. It needs its own slot, so a prompt blocked by all three
	// tools keeps its narrow defaults and one "*" each instead.
	reserveRepoWide := perGroup >= 3

	candidates := make([]Candidate, 0, maxPatternCandidates)
	for _, group := range groups {
		if len(group.paths) == 0 {
			continue
		}
		repoWide := reserveRepoWide && allTargetsInRuleScope(group.paths, scope)
		groupLimit := perGroup - 1
		if repoWide {
			groupLimit--
		}
		groupCandidates := tagCandidates(patchPathCandidates(group.paths, scope, group.tool, groupLimit, repoWide), group.tool)
		candidates = append(candidates, groupCandidates...)
		lastPattern := ""
		if len(groupCandidates) > 0 {
			lastPattern = groupCandidates[len(groupCandidates)-1].Pattern
		}
		// Every blocked tool group offers its broad rules so the user can allow
		// the whole tool instead of collecting directory rules. They are never
		// pre-selected (the narrow rules are), and patchPathCandidates reserves
		// their slots, so the candidate cap cannot drop them.
		if lastPattern == "*" {
			continue
		}
		if repoWide && lastPattern != "**" {
			candidates = append(candidates, Candidate{
				Tool:    group.tool,
				Pattern: "**",
				Summary: ruleScopeSummary(scope),
				Broad:   true,
			})
		}
		candidates = append(candidates, Candidate{
			Tool:    group.tool,
			Pattern: "*",
			Summary: "any " + group.tool + " call",
			Broad:   true,
		})
	}
	if len(candidates) == 0 {
		candidates = append(candidates, Candidate{Tool: toolname.Normalize(toolName), Pattern: "*", Summary: "any " + toolName + " call", Broad: true, Default: true})
	}
	return normalizePatternCandidates(candidates)
}

func tagCandidates(candidates []Candidate, tool string) []Candidate {
	for i := range candidates {
		candidates[i].Tool = tool
	}
	return candidates
}

// patchPathCandidates groups the given paths into the narrowest reusable
// directory rules, ranked by coverage. limit bounds the group's narrow rules;
// the caller appends the group-wide broad rules ("**" and "*") from its own
// budget. Every returned candidate is default-selected and together they cover
// every requested path: when the narrow rules hit the limit, the group falls
// back to an ancestor, a repository-wide wildcard when every path is in scope,
// or a tool-wide wildcard rather than leaving paths that would ask again on the
// next identical patch. Paths at the directory root fall back to exact-file
// rules.
func patchPathCandidates(paths []string, scope permission.PathScope, tool string, limit int, repoWide bool) []Candidate {
	normalized := dedupePaths(normalizeRulePaths(paths, scope))
	if len(normalized) == 0 {
		return nil
	}

	rules := rankDirectoryRules(normalized, limit)
	candidates := make([]Candidate, 0, limit)
	selected := make([]string, 0, limit)
	for _, rule := range rules {
		candidates = append(candidates, Candidate{
			Pattern: rule.pattern,
			Summary: rule.summary,
			Default: true,
		})
		selected = append(selected, rule.dir)
	}
	if len(candidates) == 0 {
		if len(normalized) <= limit {
			for _, p := range normalized {
				candidates = append(candidates, Candidate{Pattern: p, Summary: "this exact file", Default: true})
			}
			return candidates
		}
		return broadPathCandidate(tool, scope, repoWide)
	}

	var uncovered []string
	for _, p := range normalized {
		covered := false
		for _, dir := range selected {
			// The rule engine's "*" crosses separators, so a parent directory
			// rule already covers every nested directory.
			if strings.HasPrefix(p, dir+"/") {
				covered = true
				break
			}
		}
		if !covered {
			uncovered = append(uncovered, p)
		}
	}
	if len(uncovered) == 0 {
		return candidates
	}
	if len(candidates)+len(uncovered) <= limit {
		for _, p := range uncovered {
			candidates = append(candidates, Candidate{Pattern: p, Summary: "this exact file", Default: true})
		}
		return candidates
	}
	candidates = candidates[:len(candidates)-1]
	if ancestor := commonPathAncestor(normalized); ancestor != "" {
		return append(candidates, Candidate{
			Pattern: pathpkg.Join(ancestor, "*"),
			Summary: "any path under " + ancestor + "/",
			Default: true,
		})
	}
	return append(candidates, broadPathCandidate(tool, scope, repoWide)...)
}

// broadPathCandidate is the widest fallback that still covers every path of a
// group: "**" when all of them are in scope, otherwise the tool-wide "*".
func broadPathCandidate(tool string, scope permission.PathScope, repoWide bool) []Candidate {
	if repoWide {
		return []Candidate{{Pattern: "**", Summary: ruleScopeSummary(scope), Broad: true, Default: true}}
	}
	return []Candidate{{Pattern: "*", Summary: "any " + tool + " path", Broad: true, Default: true}}
}

// commonPathAncestor returns the deepest directory containing every path.
// Root and working-directory ancestors are not reusable rule patterns, so they
// return an empty string and callers fall back to a tool-wide wildcard.
func commonPathAncestor(paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	ancestor := pathpkg.Dir(paths[0])
	for _, p := range paths[1:] {
		for ancestor != "." && ancestor != "/" && ancestor != "" && p != ancestor && !strings.HasPrefix(p, ancestor+"/") {
			ancestor = pathpkg.Dir(ancestor)
		}
		if ancestor == "." || ancestor == "/" || ancestor == "" {
			return ""
		}
	}
	return ancestor
}

func normalizeRulePaths(paths []string, scope permission.PathScope) []string {
	normalized := make([]string, 0, len(paths))
	for _, raw := range paths {
		p := strings.TrimSpace(raw)
		if p == "" {
			continue
		}
		if normalizedPath := permission.NormalizeRulePath(p, scope); normalizedPath != "" && normalizedPath != "." {
			normalized = append(normalized, normalizedPath)
		}
	}
	return normalized
}

func dedupePaths(paths []string) []string {
	if len(paths) < 2 {
		return paths
	}
	seen := make(map[string]struct{}, len(paths))
	result := make([]string, 0, len(paths))
	for _, p := range paths {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if _, ok := seen[p]; ok {
			continue
		}
		seen[p] = struct{}{}
		result = append(result, p)
	}
	return result
}

// suggestWebFetchPatterns generates pattern candidates for WebFetch tool.
func suggestWebFetchPatterns(argsJSON string) []Candidate {
	rawURL := extractURL(argsJSON)
	if rawURL == "" {
		return normalizePatternCandidates([]Candidate{
			{Pattern: "*", Summary: "any WebFetch call", Broad: true, Default: true},
		})
	}

	var candidates []Candidate

	// Literal URL
	candidates = append(candidates, Candidate{
		Pattern: rawURL,
		Summary: "this exact URL",
	})

	parsed, err := url.Parse(rawURL)
	if err == nil && parsed.Scheme != "" && parsed.Host != "" {
		base := parsed.Scheme + "://" + parsed.Host
		cleanPath := pathpkg.Clean("/" + strings.TrimPrefix(parsed.EscapedPath(), "/"))
		if cleanPath != "/" && cleanPath != "." {
			dir := pathpkg.Dir(cleanPath)
			if dir == "." {
				dir = "/"
			}
			pathPrefix := base
			if dir == "/" {
				pathPrefix += "/"
			} else {
				pathPrefix += dir + "/"
			}
			candidates = append(candidates, Candidate{
				Pattern: pathPrefix + "*",
				Summary: "any URL under this path",
				Default: true,
			})
		}
		candidates = append(candidates, Candidate{
			Pattern: base + "/*",
			Summary: "any URL on this host",
			Broad:   true,
		})
	} else {
		// Invalid or relative URLs can still use a simple textual prefix.
		if idx := strings.LastIndex(rawURL, "/"); idx > 0 {
			candidates = append(candidates, Candidate{
				Pattern: rawURL[:idx+1] + "*",
				Summary: "any URL under this path",
				Default: true,
			})
		}
	}

	// Very broad
	candidates = append(candidates, Candidate{
		Pattern: "*",
		Summary: "any WebFetch call",
		Broad:   true,
	})

	return normalizePatternCandidates(candidates)
}

func normalizePatternCandidates(candidates []Candidate) []Candidate {
	out := make([]Candidate, 0, len(candidates))
	seen := make(map[string]struct{}, len(candidates))
	defaultSet := false
	for _, c := range candidates {
		c.Pattern = strings.TrimSpace(c.Pattern)
		if c.Pattern == "" {
			continue
		}
		key := c.Tool + "\x00" + c.Pattern
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		if c.Default {
			defaultSet = true
		}
		out = append(out, c)
		if len(out) >= maxPatternCandidates {
			break
		}
	}
	if len(out) > 0 && !defaultSet {
		out[0].Default = true
	}
	return out
}

// extractShellCommand extracts the command string from Shell tool args JSON.
func extractShellCommand(argsJSON string) string {
	var parsed struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &parsed); err != nil {
		return ""
	}
	return strings.TrimSpace(parsed.Command)
}

// extractFilePath extracts the file path from Edit/Write tool args JSON.
func extractFilePath(argsJSON string) string {
	var parsed struct {
		Path       string `json:"path"`
		TargetFile string `json:"TargetFile"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &parsed); err != nil {
		return ""
	}
	if parsed.Path != "" {
		return parsed.Path
	}
	return parsed.TargetFile
}

// extractURL extracts the URL from WebFetch tool args JSON.
func extractURL(argsJSON string) string {
	var parsed struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal([]byte(argsJSON), &parsed); err != nil {
		return ""
	}
	return strings.TrimSpace(parsed.URL)
}
