package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/keakon/chord/internal/config"
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
	if toolName != "Skill" {
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
	case "Skill":
		if path := skillToolPathFromResult(result); path != "" {
			return shortenSkillDisplayPath(path)
		}
		return result
	case "Delegate":
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
	case "Skill":
		if body := skillToolBodyFromResult(result); body != "" {
			return body
		}
		if path := skillToolPathFromResult(result); path != "" {
			return path
		}
		return result
	case "Delegate":
		lines := taskToolExpandedHandleLines(result)
		if len(lines) == 0 {
			return result
		}
		return strings.Join(lines, "\n")
	default:
		return result
	}
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
	if idx := strings.Index(inner, "\n\n"); idx >= 0 {
		body := strings.TrimSpace(inner[idx+2:])
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
