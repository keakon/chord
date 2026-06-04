package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/tools"
)

type skillToolArgs struct {
	Name         string `json:"name"`
	SourcePrefix string `json:"source_prefix"`
}

func skillToolNameFromArgs(argsJSON string) string {
	if strings.TrimSpace(argsJSON) == "" {
		return ""
	}
	var parsed skillToolArgs
	if err := json.Unmarshal([]byte(argsJSON), &parsed); err != nil {
		return ""
	}
	return strings.TrimSpace(parsed.Name)
}

type skillToolDisplayParts struct {
	Prefix string
	Name   string
}

func skillToolHeaderDisplayParts(argsJSON, result string) (prefix, name string) {
	name = skillToolNameFromArgs(argsJSON)
	if name == "" {
		name = filepath.Base(strings.TrimSpace(shortenSkillDisplayPath(skillToolPathFromResult(result))))
	}
	prefix = skillSourcePrefixDisplay(skillToolPathFromResult(result))
	return strings.TrimSpace(prefix), strings.TrimSpace(name)
}

func skillToolDisplayPartsFromArgsAndResult(argsJSON, result string) skillToolDisplayParts {
	prefix, name := skillToolHeaderDisplayParts(argsJSON, result)
	return skillToolDisplayParts{
		Prefix: prefix,
		Name:   name,
	}
}

func buildSkillDisplayArgsJSON(name, result string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	display := map[string]string{"name": name}
	if prefix := skillSourcePrefixDisplay(skillToolPathFromResult(result)); prefix != "" {
		display["source_prefix"] = prefix
	}
	if path := skillToolPathFromResult(result); path != "" {
		display["result"] = "<path>" + path + "</path>"
	}
	b, err := json.Marshal(display)
	if err != nil {
		return ""
	}
	return string(b)
}

func eventToolDisplayArgs(toolName, argsJSON, result string) string {
	if tools.NormalizeName(toolName) != tools.NameSkill {
		return argsJSON
	}
	parts := skillToolDisplayPartsFromArgsAndResult(argsJSON, result)
	if parts.Name == "" {
		return argsJSON
	}
	if built := buildSkillDisplayArgsJSON(parts.Name, result); built != "" {
		return built
	}
	return argsJSON
}

func skillToolDisplayPath(result string) string {
	if path := skillToolPathFromResult(result); path != "" {
		return shortenSkillDisplayPath(path)
	}
	return ""
}

func skillToolSourceLabel(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	path = filepath.Clean(path)
	if strings.EqualFold(filepath.Base(path), "SKILL.md") {
		path = filepath.Dir(path)
	}
	if configHome, err := config.ConfigHomeDir(); err == nil && configHome != "" {
		configHome = filepath.Clean(configHome)
		if strings.HasPrefix(path, configHome+string(os.PathSeparator)) || path == configHome {
			return "config-home"
		}
	}
	for dir := path; ; dir = filepath.Dir(dir) {
		base := filepath.Base(dir)
		if base == ".agents" {
			return ".agents"
		}
		if base == ".chord" {
			return ".chord"
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
	}
	return ""
}

func skillSourcePrefixDisplay(path string) string {
	if label := skillToolSourceLabel(path); label != "" {
		return label + "/skills/"
	}
	return ""
}

func toolCollapsedResultContent(toolName, result string) string {
	switch toolName {
	case tools.NameSkill:
		if path := skillToolPathFromResult(result); path != "" {
			return shortenSkillDisplayPath(path)
		}
		return result
	case tools.NameDelegate:
		if summary := taskToolCollapsedHandleSummary(result); summary != "" {
			return summary
		}
		return result
	default:
		return result
	}
}

func toolExpandedResultContent(toolName, result string) string {
	switch toolName {
	case tools.NameSkill:
		if body := skillToolBodyFromResult(result); body != "" {
			return body
		}
		if path := skillToolPathFromResult(result); path != "" {
			return path
		}
		return result
	case tools.NameDelegate:
		lines := taskToolExpandedHandleLines(result)
		if len(lines) == 0 {
			return result
		}
		return strings.Join(lines, "\n")
	default:
		return result
	}
}

func toolDisplayResultContent(b *Block) string {
	if b == nil {
		return ""
	}
	result := toolExpandedResultContent(b.ToolName, b.ResultContent)
	if b.toolResultIsError() || b.toolResultIsCancelled() {
		return result
	}
	if toolShouldHideSuccessfulFileOpResult(b) {
		return ""
	}
	return result
}

func toolShouldHideSuccessfulFileOpResult(b *Block) bool {
	if b == nil || b.toolResultIsError() || b.toolResultIsCancelled() {
		return false
	}
	switch tools.NormalizeName(b.ToolName) {
	case tools.NameEdit:
		return !strings.Contains(b.ResultContent, "Diagnostics summary:")
	default:
		return false
	}
}

func toolSuccessfulFileOpSummary(b *Block) string {
	if b == nil || strings.TrimSpace(b.ResultContent) == "" || b.toolResultIsError() || b.toolResultIsCancelled() {
		return ""
	}
	switch tools.NormalizeName(b.ToolName) {
	case tools.NameEdit:
		path := b.displayToolPath(tools.ExtractEditPathFromArgs([]byte(b.Content)))
		if path == "" {
			path = b.displayToolPath(firstPathFromToolResult(b.ResultContent))
		}
		if path == "" {
			return ""
		}
		return fmt.Sprintf("Applied patch to %s", path)
	case tools.NameWrite:
		var args struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal([]byte(b.Content), &args); err != nil || strings.TrimSpace(args.Path) == "" {
			return ""
		}
		path := b.displayToolPath(args.Path)
		first := firstDisplayLine(strings.TrimSpace(b.ResultContent))
		if first == "" {
			return ""
		}
		return fmt.Sprintf("Wrote %s — %s", path, first)
	case tools.NameDelete:
		groups := tools.ParseDeleteResult(b.ResultContent)
		if len(groups.Deleted) == 1 && len(groups.AlreadyAbsent) == 0 && len(groups.Blocked) == 0 && len(groups.Failed) == 0 && len(groups.NotAttempted) == 0 {
			return fmt.Sprintf("Deleted %s", b.displayToolPath(groups.Deleted[0]))
		}
		return ""
	default:
		return ""
	}
}

func firstPathFromToolResult(result string) string {
	for _, prefix := range []string{"Applied patch to ", "Updated file ", "Wrote file ", "Deleted file "} {
		idx := strings.Index(result, prefix)
		if idx < 0 {
			continue
		}
		rest := strings.TrimSpace(result[idx+len(prefix):])
		if rest == "" {
			continue
		}
		cut := len(rest)
		for _, marker := range []string{" (", "\n"} {
			if i := strings.Index(rest, marker); i >= 0 && i < cut {
				cut = i
			}
		}
		cand := strings.TrimSpace(rest[:cut])
		if cand != "" {
			return filepath.Clean(cand)
		}
	}
	return ""
}

func skillToolBodyFromResult(result string) string {
	trimmed := strings.TrimSpace(result)
	if trimmed == "" || !strings.HasPrefix(trimmed, "<skill>") {
		return ""
	}
	inner := strings.TrimPrefix(trimmed, "<skill>")
	inner = strings.TrimSuffix(inner, "</skill>")
	inner = strings.TrimSpace(inner)
	if inner == "" {
		return ""
	}
	if _, after, ok := strings.Cut(inner, "\n\n"); ok {
		body := strings.TrimSpace(after)
		if body != "" {
			return body
		}
	}
	return ""
}

func skillToolCopyContent(displayArgs, result string) string {
	name := skillToolNameFromArgs(displayArgs)
	path := skillToolPathFromResult(result)
	body := skillToolBodyFromResult(result)

	var parts []string
	if name != "" {
		parts = append(parts, "Name: "+name)
	}
	if path != "" {
		parts = append(parts, "Path: "+path)
	}
	if body != "" {
		parts = append(parts, body)
	} else if trimmed := strings.TrimSpace(result); trimmed != "" {
		parts = append(parts, trimmed)
	}
	return strings.TrimSpace(strings.Join(parts, "\n\n"))
}

func shortenSkillDisplayPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	path = filepath.Clean(path)
	if strings.EqualFold(filepath.Base(path), "SKILL.md") {
		path = filepath.Dir(path)
	}
	if prefix := skillSourcePrefixDisplay(path); prefix != "" {
		return prefix + filepath.Base(path)
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		home = filepath.Clean(home)
		if path == home {
			return "~"
		}
		if strings.HasPrefix(path, home+string(os.PathSeparator)) {
			return "~"
		}
	}
	return path
}

func skillToolPathFromResult(result string) string {
	if tagged := extractSkillTaggedValue(result, "path"); tagged != "" {
		return tagged
	}
	trimmed := strings.TrimSpace(result)
	if trimmed == "" || strings.Contains(trimmed, "\n") || strings.Contains(trimmed, "<") || strings.Contains(trimmed, ">") {
		return ""
	}
	if strings.Contains(trimmed, "/") || strings.Contains(trimmed, `\\`) || strings.HasSuffix(strings.ToLower(trimmed), ".md") {
		return trimmed
	}
	return ""
}

func extractSkillTaggedValue(result, tag string) string {
	result = strings.TrimSpace(result)
	tag = strings.TrimSpace(tag)
	if result == "" || tag == "" {
		return ""
	}
	open := "<" + tag + ">"
	close := "</" + tag + ">"
	start := strings.Index(result, open)
	if start < 0 {
		return ""
	}
	start += len(open)
	end := strings.Index(result[start:], close)
	if end < 0 {
		return ""
	}
	return strings.TrimSpace(result[start : start+end])
}
