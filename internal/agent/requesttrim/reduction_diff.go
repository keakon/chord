package requesttrim

import (
	"fmt"
	"strings"
)

func looksLikeDiffOrPatch(content string) bool {
	seenHeader := false
	start := 0
	for range 80 {
		if start > len(content) {
			break
		}
		line := content[start:]
		if newline := strings.IndexByte(line, '\n'); newline >= 0 {
			line = line[:newline]
			start += newline + 1
		} else {
			start = len(content) + 1
		}
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "diff --git "), strings.HasPrefix(trimmed, "diff --combined "), strings.HasPrefix(trimmed, "diff --cc "), strings.HasPrefix(trimmed, "--- "), strings.HasPrefix(trimmed, "+++ "):
			seenHeader = true
		case seenHeader && diffHunkHeaderLineRe.MatchString(trimmed):
			return true
		case seenHeader && (strings.HasPrefix(trimmed, "GIT binary patch") ||
			strings.HasPrefix(trimmed, "Binary files ") ||
			strings.HasPrefix(trimmed, "old mode ") ||
			strings.HasPrefix(trimmed, "new mode ") ||
			strings.HasPrefix(trimmed, "new file mode ") ||
			strings.HasPrefix(trimmed, "deleted file mode ") ||
			strings.HasPrefix(trimmed, "similarity index ") ||
			strings.HasPrefix(trimmed, "rename from ") ||
			strings.HasPrefix(trimmed, "rename to ") ||
			strings.HasPrefix(trimmed, "copy from ") ||
			strings.HasPrefix(trimmed, "copy to ")):
			return true
		case strings.HasPrefix(trimmed, "*** Begin Patch"), strings.HasPrefix(trimmed, "*** Update File:"), strings.HasPrefix(trimmed, "*** Add File:"), strings.HasPrefix(trimmed, "*** Delete File:"):
			return true
		}
	}
	return false
}

// reduceDiffOutputSummary keeps the structural parts of a patch instead of
// routing it through log heuristics. A review can recover the exact patch with
// the original git command, while the summary still identifies changed files,
// hunks, and representative additions/removals.
func reduceDiffOutputSummary(content string) string {
	const maxFiles = 12
	const maxChangesPerHunk = 3
	const maxChangesTotal = 36

	type diffFile struct {
		name           string
		hunks          int
		changes        int
		kind           string
		shownChanges   []string
		omittedChanges int
	}
	files := make([]diffFile, 0)
	current := -1
	currentKind := ""
	hunkChanges := 0
	shownChanges := 0
	pendingUnifiedPath := ""
	forEachLine(content, func(line string) bool {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "*** Update File:") || strings.HasPrefix(trimmed, "*** Add File:") || strings.HasPrefix(trimmed, "*** Delete File:"):
			name := strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(strings.TrimPrefix(trimmed, "*** Update File:"), "*** Add File:"), "*** Delete File:"))
			name = TextSnippet(name, SummaryLineSnippetChars)
			if len(files) < maxFiles {
				files = append(files, diffFile{name: name})
				current = len(files) - 1
			} else {
				current = -1
			}
			currentKind = "patch"
			hunkChanges = 0
		case strings.HasPrefix(trimmed, "diff --git ") || strings.HasPrefix(trimmed, "diff --combined ") || strings.HasPrefix(trimmed, "diff --cc "):
			name := TextSnippet(diffSummaryGitHeaderPath(trimmed), SummaryLineSnippetChars)
			if len(files) < maxFiles {
				files = append(files, diffFile{name: name})
				current = len(files) - 1
			} else {
				current = -1
			}
			currentKind = "git"
			hunkChanges = 0
		case currentKind != "patch" && strings.HasPrefix(trimmed, "--- "):
			pendingUnifiedPath = strings.TrimPrefix(trimmed, "--- ")
		case currentKind != "patch" && strings.HasPrefix(trimmed, "+++ ") && pendingUnifiedPath != "":
			name := diffSummaryPath(strings.TrimPrefix(trimmed, "+++ "))
			if name == "/dev/null" {
				name = diffSummaryPath(pendingUnifiedPath)
			}
			if currentKind == "git" && current >= 0 {
				files[current].name = TextSnippet(name, SummaryLineSnippetChars)
			} else {
				name = TextSnippet(name, SummaryLineSnippetChars)
				if len(files) < maxFiles {
					files = append(files, diffFile{name: name})
					current = len(files) - 1
				} else {
					current = -1
				}
			}
			currentKind = "unified"
			pendingUnifiedPath = ""
		case current >= 0 && (strings.HasPrefix(trimmed, "GIT binary patch") || strings.HasPrefix(trimmed, "Binary files ")):
			files[current].kind = "binary"
		case current >= 0 && (strings.HasPrefix(trimmed, "old mode ") || strings.HasPrefix(trimmed, "new mode ") || strings.HasPrefix(trimmed, "new file mode ") || strings.HasPrefix(trimmed, "deleted file mode ")):
			files[current].kind = "mode"
		case current >= 0 && (strings.HasPrefix(trimmed, "similarity index ") || strings.HasPrefix(trimmed, "rename from ") || strings.HasPrefix(trimmed, "rename to ")):
			files[current].kind = "rename"
		case current >= 0 && (strings.HasPrefix(trimmed, "copy from ") || strings.HasPrefix(trimmed, "copy to ")):
			files[current].kind = "copy"
		case strings.HasPrefix(trimmed, "@@"):
			if current >= 0 {
				files[current].hunks++
			}
			hunkChanges = 0
		case current >= 0 && (strings.HasPrefix(line, "+") || strings.HasPrefix(line, "-")) &&
			!strings.HasPrefix(line, "+++") && !strings.HasPrefix(line, "---"):
			files[current].changes++
			if hunkChanges < maxChangesPerHunk && shownChanges < maxChangesTotal {
				files[current].shownChanges = append(files[current].shownChanges, TextSnippet(trimmed, SummaryLineSnippetChars))
				shownChanges++
			} else {
				files[current].omittedChanges++
			}
			hunkChanges++
		}
		return true
	})

	lines := make([]string, 0, len(files)*2+2)
	for _, file := range files {
		line := fmt.Sprintf("- %s: hunks=%d changes=%d", file.name, file.hunks, file.changes)
		if file.kind != "" {
			line += "; kind=" + file.kind
		}
		if len(file.shownChanges) > 0 {
			line += "; shown: " + strings.Join(file.shownChanges, " | ")
		}
		if file.omittedChanges > 0 {
			line += fmt.Sprintf("; %d changes omitted", file.omittedChanges)
		}
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		lines = append(lines, "- (no diff file headers preserved)")
	}
	if omitted := countDiffFileHeaders(content) - len(files); omitted > 0 {
		lines = append(lines, fmt.Sprintf("- ... (%d additional files omitted) ...", omitted))
	}
	return fmt.Sprintf("[Older git diff summarized for this request; bytes=%d lines=%d]\n%s", len(content), countMeaningfulLines(content), strings.Join(lines, "\n"))
}

func diffSummaryPath(path string) string {
	path = strings.TrimSpace(path)
	if fields := strings.SplitN(path, "\t", 2); len(fields) > 0 {
		path = fields[0]
	}
	path = strings.Trim(path, "\"")
	if path == "/dev/null" {
		return path
	}
	path = strings.TrimPrefix(path, "a/")
	path = strings.TrimPrefix(path, "b/")
	return path
}

func diffSummaryGitHeaderPath(header string) string {
	header = strings.TrimSpace(header)
	for _, prefix := range []string{"diff --git ", "diff --combined ", "diff --cc "} {
		if !strings.HasPrefix(header, prefix) {
			continue
		}
		paths := strings.TrimSpace(strings.TrimPrefix(header, prefix))
		if idx := strings.LastIndex(paths, " b/"); idx >= 0 {
			return diffSummaryPath(paths[idx+1:])
		}
		if idx := strings.LastIndex(paths, `"b/`); idx >= 0 {
			return diffSummaryPath(paths[idx+1:])
		}
		fields := strings.Fields(paths)
		if len(fields) > 0 {
			return diffSummaryPath(fields[len(fields)-1])
		}
	}
	return header
}

func countDiffFileHeaders(content string) int {
	count := 0
	pendingUnified := false
	explicitKind := ""
	forEachLine(content, func(line string) bool {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "diff --git ") || strings.HasPrefix(trimmed, "diff --combined ") || strings.HasPrefix(trimmed, "diff --cc "):
			count++
			pendingUnified = false
			explicitKind = "git"
		case strings.HasPrefix(trimmed, "*** Update File:") || strings.HasPrefix(trimmed, "*** Add File:") || strings.HasPrefix(trimmed, "*** Delete File:"):
			count++
			pendingUnified = false
			explicitKind = "patch"
		case explicitKind != "patch" && strings.HasPrefix(trimmed, "--- "):
			pendingUnified = true
		case explicitKind != "patch" && strings.HasPrefix(trimmed, "+++ ") && pendingUnified:
			if explicitKind != "git" {
				count++
			}
			explicitKind = "unified"
			pendingUnified = false
		}
		return true
	})
	return count
}
