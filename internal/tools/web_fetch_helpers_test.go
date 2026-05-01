package tools

import (
	"bytes"

	"golang.org/x/net/html"
)

// htmlToMarkdown converts an HTML document to a simplified Markdown
// representation; used by tests that exercise extraction paths without a
// surrounding HTTP fetch.
func htmlToMarkdown(data []byte) string {
	decoded, decErr := decodeWebFetchHTML(data, "text/html")
	utf8Body := bytes.ToValidUTF8(data, []byte("�"))
	if decErr == nil {
		utf8Body = decoded.UTF8
	}
	doc, parseErr := html.Parse(bytes.NewReader(utf8Body))
	if parseErr != nil {
		if decErr == nil && decoded.Text != "" {
			return decoded.Text
		}
		return string(utf8Body)
	}
	return htmlDocumentToMarkdownBase(doc, "")
}
