package message

import (
	"html"
	"strconv"
	"strings"
)

const FileRefOpenTag = "<file path="

// FileRefPaths returns deduplicated file paths encoded in <file path="..."> blocks.
func FileRefPaths(text string) []string {
	var refs []string
	seen := make(map[string]bool)
	for rest := text; ; {
		path, next, ok := nextFileRefPath(rest)
		if !ok {
			return refs
		}
		rest = next
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		refs = append(refs, path)
	}
}

// FirstFileRefPath returns the first file path encoded in a <file path="..."> block.
func FirstFileRefPath(text string) (string, bool) {
	path, _, ok := nextFileRefPath(strings.TrimSpace(text))
	return path, ok
}

func nextFileRefPath(text string) (string, string, bool) {
	start := strings.Index(text, FileRefOpenTag)
	if start < 0 {
		return "", "", false
	}
	rest := text[start+len(FileRefOpenTag):]
	if len(rest) == 0 {
		return "", "", false
	}
	quote := rest[0]
	if quote != '"' && quote != '\'' {
		return "", "", false
	}

	end := -1
	escaped := false
	for i := 1; i < len(rest); i++ {
		c := rest[i]
		if escaped {
			escaped = false
			continue
		}
		if c == '\\' {
			escaped = true
			continue
		}
		if c == quote {
			end = i
			break
		}
	}
	if end < 0 {
		return "", "", false
	}
	if end+1 >= len(rest) || rest[end+1] != '>' {
		return "", "", false
	}

	value := html.UnescapeString(rest[:end+1])
	path, err := strconv.Unquote(value)
	if err != nil {
		path = strings.Trim(value, "\"'")
	}
	return strings.TrimSpace(path), rest[end+2:], true
}

// IsFileRefContent reports whether text is an @-injected <file path="...">...</file> block.
func IsFileRefContent(text string) bool {
	return strings.HasPrefix(strings.TrimSpace(text), FileRefOpenTag) && strings.Contains(text, "</file>")
}

// UserPromptPlainText returns user-visible text for session titles and usage previews:
// concatenated non–file-ref text parts when Parts is set; otherwise trimmed Content.
func UserPromptPlainText(msg Message) string {
	if len(msg.Parts) > 0 {
		var sb strings.Builder
		for _, p := range msg.Parts {
			if p.Type != ContentPartText || IsFileRefContent(p.Text) {
				continue
			}
			sb.WriteString(p.Text)
		}
		if s := strings.TrimSpace(sb.String()); s != "" {
			return s
		}
	}
	return strings.TrimSpace(msg.Content)
}
