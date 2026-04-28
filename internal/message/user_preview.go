package message

import "strings"

// IsFileRefContent reports whether text is an @-injected <file path="...">...</file> block.
func IsFileRefContent(text string) bool {
	return strings.HasPrefix(text, "<file path=") && strings.Contains(text, "</file>")
}

// UserPromptPlainText returns user-visible text for session titles and usage previews:
// concatenated non–file-ref text parts when Parts is set; otherwise trimmed Content.
func UserPromptPlainText(msg Message) string {
	if len(msg.Parts) > 0 {
		var sb strings.Builder
		for _, p := range msg.Parts {
			if p.Type != "text" || IsFileRefContent(p.Text) {
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
