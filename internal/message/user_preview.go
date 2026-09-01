package message

import (
	"html"
	"strconv"
	"strings"
)

const FileRefOpenTag = "<file path="

const fileRefCloseTag = "</file>"

type FileRef struct {
	Path  string
	Lines string
}

// FileRefPaths returns deduplicated file paths encoded in <file path="..."> blocks.
func FileRefPaths(text string) []string {
	var refs []string
	seen := make(map[string]bool)
	for rest := text; ; {
		ref, next, ok := nextFileRef(rest)
		if !ok {
			return refs
		}
		rest = next
		path := ref.Path
		if path == "" || seen[path] {
			continue
		}
		seen[path] = true
		refs = append(refs, path)
	}
}

// FileRefs returns deduplicated file references encoded in <file path="..."> blocks.
func FileRefs(text string) []FileRef {
	var refs []FileRef
	seen := make(map[string]bool)
	for rest := text; ; {
		ref, next, ok := nextFileRef(rest)
		if !ok {
			return refs
		}
		rest = next
		if ref.Path == "" {
			continue
		}
		key := ref.Path + "\x00" + ref.Lines
		if seen[key] {
			continue
		}
		seen[key] = true
		refs = append(refs, ref)
	}
}

// FirstFileRefPath returns the first file path encoded in a <file path="..."> block.
func FirstFileRefPath(text string) (string, bool) {
	ref, _, ok := nextFileRef(strings.TrimSpace(text))
	return ref.Path, ok
}

// ParseSingleFileRefContent parses one complete <file> wrapper and returns the
// exact body bytes represented by its model-visible text. It rejects surrounding
// non-whitespace content so callers cannot mistake one reference embedded in a
// larger text part for a complete file observation.
func ParseSingleFileRefContent(text string) (FileRef, string, bool) {
	trimmed := strings.TrimLeft(text, " \t\r\n")
	if !strings.HasPrefix(trimmed, FileRefOpenTag) {
		return FileRef{}, "", false
	}
	ref, rest, ok := nextFileRef(trimmed)
	if !ok {
		return FileRef{}, "", false
	}
	closeIdx := strings.LastIndex(rest, fileRefCloseTag)
	if closeIdx < 0 || strings.TrimSpace(rest[closeIdx+len(fileRefCloseTag):]) != "" {
		return FileRef{}, "", false
	}
	body := rest[:closeIdx]
	switch {
	case strings.HasPrefix(body, "\r\n"):
		body = body[2:]
	case strings.HasPrefix(body, "\n"):
		body = body[1:]
	default:
		return FileRef{}, "", false
	}
	if !strings.HasSuffix(body, "\n") {
		return FileRef{}, "", false
	}
	return ref, body[:len(body)-1], true
}

func nextFileRef(text string) (FileRef, string, bool) {
	_, after, ok := strings.Cut(text, FileRefOpenTag)
	if !ok {
		return FileRef{}, "", false
	}
	rest := after
	if len(rest) == 0 {
		return FileRef{}, "", false
	}
	quote := rest[0]
	if quote != '"' && quote != '\'' {
		return FileRef{}, "", false
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
		return FileRef{}, "", false
	}
	attrTextEnd := end + 1
	close := strings.IndexByte(rest[attrTextEnd:], '>')
	if close < 0 {
		return FileRef{}, "", false
	}
	attrText := rest[attrTextEnd : attrTextEnd+close]

	value := html.UnescapeString(rest[:end+1])
	path, err := strconv.Unquote(value)
	if err != nil {
		path = strings.Trim(value, "\"'")
	}
	return FileRef{Path: strings.TrimSpace(path), Lines: fileRefAttrValue(attrText, "lines")}, rest[attrTextEnd+close+1:], true
}

func fileRefAttrValue(attrs, name string) string {
	for s := attrs; ; {
		s = strings.TrimLeft(s, " \t\r\n")
		if s == "" {
			return ""
		}
		idx := strings.IndexByte(s, '=')
		if idx <= 0 {
			return ""
		}
		attrName := strings.TrimSpace(s[:idx])
		s = strings.TrimLeft(s[idx+1:], " \t\r\n")
		if s == "" {
			return ""
		}
		quote := s[0]
		if quote != '"' && quote != '\'' {
			return ""
		}
		end := -1
		escaped := false
		for i := 1; i < len(s); i++ {
			c := s[i]
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
			return ""
		}
		value := html.UnescapeString(s[:end+1])
		unquoted, err := strconv.Unquote(value)
		if err != nil {
			unquoted = strings.Trim(value, "\"'")
		}
		if attrName == name {
			return strings.TrimSpace(unquoted)
		}
		s = s[end+1:]
	}
}

// IsFileRefContent reports whether text is an @-injected <file path="...">...</file> block.
func IsFileRefContent(text string) bool {
	return strings.HasPrefix(strings.TrimSpace(text), FileRefOpenTag) && strings.Contains(text, fileRefCloseTag)
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

// UserPromptInstructionText returns the user-authored instruction text for
// classification. Unlike UserPromptPlainText, it also removes complete file
// reference blocks embedded in the legacy Content field. File contents are
// evidence, not user instructions, and must not become durable constraints
// merely because they contain words such as "must" or "do not".
func UserPromptInstructionText(msg Message) string {
	if len(msg.Parts) > 0 {
		var sb strings.Builder
		for _, p := range msg.Parts {
			if p.Type != ContentPartText {
				continue
			}
			text := strings.TrimSpace(p.Text)
			if text == "" || IsFileRefContent(text) {
				continue
			}
			if cleaned := stripFileRefBlocks(text); cleaned != "" {
				sb.WriteString(cleaned)
				sb.WriteByte('\n')
			}
		}
		return strings.TrimSpace(sb.String())
	}
	return stripFileRefBlocks(msg.Content)
}

func stripFileRefBlocks(text string) string {
	var out strings.Builder
	for {
		start := strings.Index(text, FileRefOpenTag)
		if start < 0 {
			out.WriteString(text)
			break
		}
		out.WriteString(text[:start])
		_, afterOpen, ok := nextFileRef(text[start:])
		if !ok {
			out.WriteString(text[start:])
			break
		}
		bodyEnd := strings.Index(afterOpen, fileRefCloseTag)
		if bodyEnd < 0 {
			out.WriteString(text[start:])
			break
		}
		// afterOpen is the tail following the opening tag, so subtracting its
		// length from the whole text yields the offset just past that tag.
		consumed := len(text) - len(afterOpen) + bodyEnd + len(fileRefCloseTag)
		text = text[consumed:]
	}
	return strings.TrimSpace(out.String())
}
