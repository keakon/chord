package tui

import (
	"encoding/json"
	"net/url"
	"os"
	pathpkg "path"
	"path/filepath"
	"strings"
)

// PatternCandidate represents a suggested rule pattern for a tool invocation.
type PatternCandidate struct {
	Pattern string // the rule pattern (e.g. "git log *")
	Summary string // human-readable description (e.g. "same command, any flags")
	Broad   bool   // true if pattern is very broad (e.g. "*", "git *")
	Default bool   // true if this is the recommended default candidate
}

// suggestRulePatterns generates pattern candidates for a tool invocation.
// toolName: the tool name (e.g. "Bash", "Write")
// argsJSON: the tool arguments as JSON
// needsApproval: explicit paths that need approval (for Delete)
// cwd: current working directory (for relative path generation)
func suggestRulePatterns(toolName, argsJSON string, needsApproval []string, cwd string) []PatternCandidate {
	switch strings.ToLower(toolName) {
	case "bash":
		return suggestBashPatterns(argsJSON, needsApproval)
	case "edit", "write":
		return suggestFilePatterns(toolName, argsJSON, cwd)
	case "webfetch":
		return suggestWebFetchPatterns(argsJSON)
	case "delete":
		// Delete approvals are path-specific; do not suggest reusable rule candidates.
		return nil
	case "read", "grep", "glob", "lsp", "skill":
		return normalizePatternCandidates([]PatternCandidate{
			{Pattern: "*", Summary: "any " + toolName + " call", Broad: true, Default: true},
		})
	default:
		return normalizePatternCandidates([]PatternCandidate{
			{Pattern: "*", Summary: "any tool call", Broad: true, Default: true},
		})
	}
}

// suggestBashPatterns generates pattern candidates for Bash commands.
func suggestBashPatterns(argsJSON string, needsApproval []string) []PatternCandidate {
	command := extractBashCommand(argsJSON)
	if command == "" {
		return normalizePatternCandidates([]PatternCandidate{
			{Pattern: "*", Summary: "any Bash command", Broad: true, Default: true},
		})
	}

	// If needsApproval has a specific subcommand, prefer that
	seed := command
	if len(needsApproval) > 0 && strings.TrimSpace(needsApproval[0]) != "" {
		seed = strings.TrimSpace(needsApproval[0])
	}

	return buildBashCandidates(seed, command)
}

// buildBashCandidates builds pattern candidates from a command string.
func buildBashCandidates(seed, fullCommand string) []PatternCandidate {
	trimmed := strings.TrimSpace(seed)

	// Check for complex commands (pipes, chains, subshells, or multi-line/heredoc)
	isComplex := strings.ContainsAny(trimmed, "|;&") || strings.Contains(trimmed, "$(") || strings.Contains(trimmed, "`") || strings.Contains(trimmed, "\n") || strings.Contains(trimmed, "<<")

	if isComplex {
		// Complex commands: only literal + very broad
		return normalizePatternCandidates([]PatternCandidate{
			{Pattern: trimmed, Summary: "literal (this exact command)", Default: true},
			{Pattern: "*", Summary: "any Bash command", Broad: true},
		})
	}

	// Check for high-risk commands
	isHighRisk := isHighRiskBashCommand(trimmed)

	parts := strings.Fields(trimmed)
	if len(parts) == 0 {
		return normalizePatternCandidates([]PatternCandidate{
			{Pattern: "*", Summary: "any Bash command", Broad: true, Default: true},
		})
	}

	var candidates []PatternCandidate

	// Literal
	candidates = append(candidates, PatternCandidate{
		Pattern: trimmed,
		Summary: "literal (this exact command)",
		Default: isHighRisk, // high risk commands default to literal
	})

	if !isHighRisk && len(parts) >= 2 {
		// head2 *: first two words + wildcard
		head2 := strings.Join(parts[:2], " ") + " *"
		head2Summary := "same command, any flags"
		if len(parts) > 2 {
			head2Summary = "same command prefix, any arguments"
		}
		candidates = append(candidates, PatternCandidate{
			Pattern: head2,
			Summary: head2Summary,
			Default: !isHighRisk && len(parts) >= 2,
		})
	}

	if !isHighRisk && len(parts) >= 1 {
		// head1 *: first word + wildcard
		head1 := parts[0] + " *"
		candidates = append(candidates, PatternCandidate{
			Pattern: head1,
			Summary: "any " + parts[0] + " subcommand",
			Broad:   true,
			Default: len(parts) == 1,
		})
	}

	// Very broad: *
	candidates = append(candidates, PatternCandidate{
		Pattern: "*",
		Summary: "any Bash command",
		Broad:   true,
	})

	return normalizePatternCandidates(candidates)
}

// isHighRiskBashCommand checks if a command contains high-risk patterns.
func isHighRiskBashCommand(command string) bool {
	lower := strings.ToLower(command)
	highRiskPrefixes := []string{
		"rm ", "rm\t",
		"sudo ", "sudo\t",
		"chmod ", "chmod\t",
		"chown ", "chown\t",
		"curl ", "curl\t",
		"wget ", "wget\t",
	}
	for _, prefix := range highRiskPrefixes {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	// curl ... | sh patterns
	if strings.Contains(lower, "| sh") || strings.Contains(lower, "|bash") {
		return true
	}
	return false
}

// suggestFilePatterns generates pattern candidates for Edit/Write tools.
func suggestFilePatterns(toolName, argsJSON, cwd string) []PatternCandidate {
	filePath := extractFilePath(argsJSON)
	if filePath == "" {
		return normalizePatternCandidates([]PatternCandidate{
			{Pattern: "*", Summary: "any " + toolName + " call", Broad: true, Default: true},
		})
	}

	var candidates []PatternCandidate

	// Literal
	candidates = append(candidates, PatternCandidate{
		Pattern: filePath,
		Summary: "this exact file",
	})

	// <dir>/*
	dir := filepath.Dir(filePath)
	if dir != "." && dir != "" {
		dirPattern := filepath.Join(dir, "*")
		candidates = append(candidates, PatternCandidate{
			Pattern: dirPattern,
			Summary: "any file in " + dir + "/",
			Default: isPathWithinCWD(filePath, cwd),
		})
	}

	// <dir>/** - recursive
	if dir != "." && dir != "" {
		dirPattern := filepath.Join(dir, "**")
		candidates = append(candidates, PatternCandidate{
			Pattern: dirPattern,
			Summary: "any file under " + dir + "/ (recursive)",
		})
	}

	// **/*.<ext>
	ext := filepath.Ext(filePath)
	if ext != "" {
		candidates = append(candidates, PatternCandidate{
			Pattern: "**/*" + ext,
			Summary: "any " + ext + " file",
			Broad:   true,
		})
	}

	// cwd/**
	if cwd != "" {
		candidates = append(candidates, PatternCandidate{
			Pattern: filepath.Join(cwd, "**"),
			Summary: "any file under current directory",
			Broad:   true,
		})
	}

	// Very broad
	candidates = append(candidates, PatternCandidate{
		Pattern: "*",
		Summary: "any " + toolName + " call",
		Broad:   true,
	})

	return normalizePatternCandidates(candidates)
}

func isPathWithinCWD(filePath, cwd string) bool {
	path := strings.TrimSpace(filePath)
	if path == "" {
		return false
	}
	path = filepath.Clean(path)
	if !filepath.IsAbs(path) {
		// Relative paths are treated as cwd-relative unless they explicitly escape.
		if path == ".." {
			return false
		}
		parentPrefix := ".." + string(os.PathSeparator)
		return !strings.HasPrefix(path, parentPrefix)
	}
	cwd = strings.TrimSpace(cwd)
	if cwd == "" {
		return false
	}
	cwd = filepath.Clean(cwd)
	if path == cwd {
		return true
	}
	return strings.HasPrefix(path, cwd+string(os.PathSeparator))
}

// suggestWebFetchPatterns generates pattern candidates for WebFetch tool.
func suggestWebFetchPatterns(argsJSON string) []PatternCandidate {
	rawURL := extractURL(argsJSON)
	if rawURL == "" {
		return normalizePatternCandidates([]PatternCandidate{
			{Pattern: "*", Summary: "any WebFetch call", Broad: true, Default: true},
		})
	}

	var candidates []PatternCandidate

	// Literal URL
	candidates = append(candidates, PatternCandidate{
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
			candidates = append(candidates, PatternCandidate{
				Pattern: pathPrefix + "*",
				Summary: "any URL under this path",
				Default: true,
			})
		}
		candidates = append(candidates, PatternCandidate{
			Pattern: base + "/*",
			Summary: "any URL on this host",
			Broad:   true,
		})
	} else {
		// Invalid or relative URLs can still use a simple textual prefix.
		if idx := strings.LastIndex(rawURL, "/"); idx > 0 {
			candidates = append(candidates, PatternCandidate{
				Pattern: rawURL[:idx+1] + "*",
				Summary: "any URL under this path",
				Default: true,
			})
		}
	}

	// Very broad
	candidates = append(candidates, PatternCandidate{
		Pattern: "*",
		Summary: "any WebFetch call",
		Broad:   true,
	})

	return normalizePatternCandidates(candidates)
}

func normalizePatternCandidates(candidates []PatternCandidate) []PatternCandidate {
	const maxPatternCandidates = 6
	out := make([]PatternCandidate, 0, len(candidates))
	seen := make(map[string]struct{}, len(candidates))
	defaultSet := false
	for _, c := range candidates {
		c.Pattern = strings.TrimSpace(c.Pattern)
		if c.Pattern == "" {
			continue
		}
		if _, ok := seen[c.Pattern]; ok {
			continue
		}
		seen[c.Pattern] = struct{}{}
		if c.Default {
			if defaultSet {
				c.Default = false
			} else {
				defaultSet = true
			}
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

// extractBashCommand extracts the command string from Bash tool args JSON.
func extractBashCommand(argsJSON string) string {
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
