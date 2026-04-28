package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/keakon/chord/internal/message"
)

func renderEvidenceArtifactContent(items []evidenceItem) string {
	if len(items) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString(message.CompactionEvidenceTag)
	sb.WriteString("Verbatim excerpts preserved for the immediate continuation.\n")
	for i, item := range items {
		fmt.Fprintf(&sb, "\n%d. %s\n", i+1, item.Title)
		if item.Source != "" {
			fmt.Fprintf(&sb, "Source: %s\n", item.Source)
		}
		if item.WhyNeeded != "" {
			fmt.Fprintf(&sb, "Why it matters: %s\n", item.WhyNeeded)
		}
		if item.Excerpt != "" {
			sb.WriteString("Excerpt:\n")
			sb.WriteString(item.Excerpt)
			sb.WriteByte('\n')
		}
	}
	return strings.TrimRight(sb.String(), "\n")
}

func buildCompactionCheckpointMessage(summary string, historyRefs []string, mode string, evidenceItems []evidenceItem) string {
	var sb strings.Builder
	sb.WriteString(message.CompactionSummaryHeader)
	sb.WriteString(strings.TrimSpace(summary))
	sb.WriteString(message.CompactionCompressedTag)
	sb.WriteString("\n")
	switch mode {
	case "truncate_only":
		sb.WriteString("Earlier conversation was compacted without a model-generated summary.\n")
	case "structured_fallback":
		sb.WriteString("Earlier conversation was compacted using a structured fallback summary after model summarization was weak or unavailable.\n")
	default:
		sb.WriteString("Earlier conversation was compacted into the summary above.\n")
	}
	sb.WriteString("Archived history files:\n")
	for _, ref := range historyRefs {
		sb.WriteString("- ")
		sb.WriteString(ref)
		sb.WriteByte('\n')
	}
	if evidence := renderEvidenceArtifactContent(evidenceItems); evidence != "" {
		sb.WriteString("\n")
		sb.WriteString(evidence)
		sb.WriteByte('\n')
	}
	sb.WriteString(message.CompactionDisplayHint)
	sb.WriteString("Press toggle-collapse to expand and inspect the full preserved context message.\n")
	return strings.TrimRight(sb.String(), "\n")
}

func formatKeyFileCandidatesForPrompt(paths []string) string {
	if len(paths) == 0 {
		return "- (none confidently extracted)"
	}
	var sb strings.Builder
	for _, path := range paths {
		sb.WriteString("- ")
		sb.WriteString(path)
		sb.WriteByte('\n')
	}
	return strings.TrimRight(sb.String(), "\n")
}

func ensureCompactionSummaryKeyFiles(summary string, keyFiles []string) string {
	summary = strings.TrimSpace(summary)
	if summary == "" || len(keyFiles) == 0 {
		return summary
	}
	const heading = "## Files and Evidence"
	start := strings.Index(summary, heading)
	if start < 0 {
		return summary
	}
	searchStart := start + len(heading)
	relEnd := strings.Index(summary[searchStart:], "\n## ")
	end := len(summary)
	if relEnd >= 0 {
		end = searchStart + relEnd
	}
	section := summary[start:end]
	existing := make(map[string]bool)
	for _, line := range strings.Split(section, "\n") {
		if path := normalizeSummaryBulletCandidate(line); path != "" {
			existing[path] = true
		}
	}
	var lines []string
	for _, keyFile := range keyFiles {
		if existing[keyFile] {
			continue
		}
		lines = append(lines, "- "+keyFile)
	}
	if len(lines) == 0 {
		return summary
	}
	var b strings.Builder
	b.WriteString(strings.TrimRight(summary[:end], "\n"))
	b.WriteByte('\n')
	for _, line := range lines {
		b.WriteString(line)
		b.WriteByte('\n')
	}
	if end < len(summary) {
		b.WriteString(strings.TrimLeft(summary[end:], "\n"))
	}
	return strings.TrimRight(b.String(), "\n")
}

func normalizeSummaryBulletCandidate(line string) string {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "- ") {
		return ""
	}
	line = strings.TrimSpace(strings.TrimPrefix(line, "- "))
	line = strings.Trim(line, "`")
	line = strings.TrimRight(line, ".,;:!?)]}>\"'，。；：！？）】》」』’”")
	line = strings.TrimPrefix(line, "@")
	return strings.TrimSpace(line)
}

func compactionSummaryBody(content string) string {
	content = strings.TrimSpace(content)
	if content == "" {
		return ""
	}
	start := strings.Index(content, message.CompactionSummaryHeader)
	if start < 0 {
		return content
	}
	start += len(message.CompactionSummaryHeader)
	end := strings.Index(content[start:], message.CompactionCompressedTag)
	if end < 0 {
		return strings.TrimSpace(content[start:])
	}
	return strings.TrimSpace(content[start : start+end])
}

func compactionFilesAndEvidenceSection(summaryContent string) string {
	body := compactionSummaryBody(summaryContent)
	if body == "" {
		return ""
	}
	const heading = "## Files and Evidence"
	start := strings.Index(body, heading)
	if start < 0 {
		return ""
	}
	searchStart := start + len(heading)
	relEnd := strings.Index(body[searchStart:], "\n## ")
	if relEnd < 0 {
		return strings.TrimSpace(body[searchStart:])
	}
	return strings.TrimSpace(body[searchStart : searchStart+relEnd])
}

func extractCompactionKeyFiles(summaryContent, projectRoot string) []string {
	section := compactionFilesAndEvidenceSection(summaryContent)
	if strings.TrimSpace(section) == "" {
		return nil
	}
	seen := make(map[string]bool)
	var out []string
	for _, rawLine := range strings.Split(section, "\n") {
		line := strings.TrimSpace(rawLine)
		if !strings.HasPrefix(line, "- ") {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "- "))
		line = strings.Trim(line, "`")
		line = strings.TrimRight(line, ".,;:!?)]}>\"'，。；：！？）】》」』’”")
		line = strings.TrimPrefix(line, "@")
		path := normalizeCheckpointFilePath(line, projectRoot)
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		out = append(out, path)
	}
	return out
}

func normalizeCheckpointFilePath(path, projectRoot string) string {
	path = strings.TrimSpace(path)
	if path == "" || projectRoot == "" {
		return ""
	}
	if strings.Contains(path, ": ") || strings.HasPrefix(strings.ToLower(path), "archived history") {
		return ""
	}
	candidate := filepath.FromSlash(path)
	if filepath.IsAbs(candidate) {
		rel, err := filepath.Rel(projectRoot, candidate)
		if err != nil {
			return ""
		}
		candidate = rel
	}
	candidate = filepath.Clean(candidate)
	if candidate == "." || candidate == "" || strings.HasPrefix(candidate, ".."+string(filepath.Separator)) || candidate == ".." {
		return ""
	}
	rel := filepath.ToSlash(candidate)
	if strings.HasPrefix(rel, ".chord/") {
		return ""
	}
	info, err := os.Stat(filepath.Join(projectRoot, candidate))
	if err != nil || info.IsDir() {
		return ""
	}
	return rel
}

func extractCompactionKeyFileCandidates(messages []message.Message, projectRoot string, limit int) []string {
	if limit <= 0 || projectRoot == "" {
		return nil
	}
	seen := make(map[string]bool, limit)
	out := make([]string, 0, limit)
	add := func(candidate string) {
		if len(out) >= limit {
			return
		}
		normalized := normalizeCheckpointFilePath(candidate, projectRoot)
		if normalized == "" || seen[normalized] {
			return
		}
		seen[normalized] = true
		out = append(out, normalized)
	}

	for i := len(messages) - 1; i >= 0 && len(out) < limit; i-- {
		msg := messages[i]
		for _, tc := range msg.ToolCalls {
			for _, path := range extractToolArgFilePaths(tc.Args) {
				add(path)
			}
		}
		for _, path := range extractUserFileRefPaths(msg) {
			add(path)
		}
	}
	return out
}

func extractToolArgFilePaths(args json.RawMessage) []string {
	if len(args) == 0 || !json.Valid(args) {
		return nil
	}
	var payload any
	if err := json.Unmarshal(args, &payload); err != nil {
		return nil
	}
	seen := make(map[string]bool)
	var out []string
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			for key, child := range x {
				switch key {
				case "path":
					if s, ok := child.(string); ok && strings.TrimSpace(s) != "" && !seen[s] {
						seen[s] = true
						out = append(out, s)
					}
				case "paths":
					if arr, ok := child.([]any); ok {
						for _, item := range arr {
							if s, ok := item.(string); ok && strings.TrimSpace(s) != "" && !seen[s] {
								seen[s] = true
								out = append(out, s)
							}
						}
					}
				default:
					walk(child)
				}
			}
		case []any:
			for _, item := range x {
				walk(item)
			}
		}
	}
	walk(payload)
	return out
}

func extractUserFileRefPaths(msg message.Message) []string {
	var refs []string
	seen := make(map[string]bool)
	addFromText := func(text string) {
		rest := text
		for {
			start := strings.Index(rest, "<file path=")
			if start < 0 {
				return
			}
			rest = rest[start+len("<file path="):]
			if len(rest) == 0 {
				return
			}
			quote := rest[0]
			if quote != '"' && quote != '\'' {
				return
			}
			end := strings.IndexByte(rest[1:], quote)
			if end < 0 {
				return
			}
			path := rest[1 : end+1]
			rest = rest[end+2:]
			if path != "" && !seen[path] {
				seen[path] = true
				refs = append(refs, path)
			}
		}
	}
	for _, part := range msg.Parts {
		if part.Type != "text" {
			continue
		}
		addFromText(part.Text)
	}
	if len(msg.Parts) == 0 {
		addFromText(msg.Content)
	}
	return refs
}
