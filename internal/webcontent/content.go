// Package webcontent decodes and extracts readable content from web response bodies.
package webcontent

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/url"
	"strings"
	"unicode/utf8"

	readability "github.com/mackee/go-readability"
	"golang.org/x/net/html"
	"golang.org/x/net/html/charset"
	"golang.org/x/text/transform"

	"github.com/keakon/chord/internal/textfile"
)

var (
	// ErrBinaryResponse indicates a response body that appears to contain binary data.
	ErrBinaryResponse = errors.New("response appears to be binary")

	readabilityExtract = extractReadableHTML
)

func extractReadableHTML(htmlText string, options readability.ReadabilityOptions, baseURL string) (readability.ReadabilityArticle, error) {
	doc, err := readability.ParseHTML(htmlText, baseURL)
	if err != nil {
		return readability.ReadabilityArticle{}, err
	}
	readability.PreprocessDocument(doc)
	return readability.ExtractContent(doc, options), nil
}

// Decoded is text decoded to UTF-8 with its detected charset.
type Decoded struct {
	Text    string
	UTF8    []byte
	Charset string
}

// HTMLRender contains extracted Markdown, document metadata, and quality evidence.
type HTMLRender struct {
	Body           string
	Charset        string
	Title          string
	Byline         string
	SiteName       string
	PublishedTime  string
	PageType       string
	ExtractionMode string
	ContentQuality string
	ExtractedBytes int
}

// IsHTML reports whether the content type denotes an HTML document.
func IsHTML(contentType string) bool {
	mediaType := normalizedMediaType(contentType)
	return mediaType == "text/html" || mediaType == "application/xhtml+xml"
}

// IsTextLike reports whether the content type denotes a supported textual format.
func IsTextLike(contentType string) bool {
	mediaType := normalizedMediaType(contentType)
	if mediaType == "" {
		return false
	}
	if strings.HasPrefix(mediaType, "text/") {
		return true
	}
	switch mediaType {
	case "application/json", "application/ld+json", "application/xml", "application/rss+xml",
		"application/atom+xml", "application/javascript", "application/x-javascript",
		"application/ecmascript", "application/x-www-form-urlencoded", "application/graphql":
		return true
	default:
		return false
	}
}

func normalizedMediaType(contentType string) string {
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return strings.ToLower(strings.TrimSpace(contentType))
	}
	return strings.ToLower(strings.TrimSpace(mediaType))
}

// RenderHTML extracts readable Markdown and metadata from an HTML response body.
// modePrefix labels the extraction strategy in the returned evidence.
func RenderHTML(body []byte, contentType, baseURL, modePrefix string) HTMLRender {
	decoded, err := decodeHTML(body, contentType)
	utf8Body := bytes.ToValidUTF8(body, []byte("�"))
	if err == nil {
		utf8Body = decoded.UTF8
	}
	htmlText := string(utf8Body)
	result := HTMLRender{Charset: decoded.Charset}

	var doc *html.Node
	if parsed, parseErr := html.Parse(bytes.NewReader(utf8Body)); parseErr == nil {
		doc = parsed
	}
	fillHTMLMetadata(&result, htmlText, doc, baseURL)

	article, extractErr := readabilityExtract(htmlText, readability.DefaultOptions(), baseURL)
	if extractErr == nil {
		if strings.TrimSpace(article.Title) != "" && strings.TrimSpace(result.Title) == "" {
			result.Title = strings.TrimSpace(article.Title)
		}
		if strings.TrimSpace(article.Byline) != "" && strings.TrimSpace(result.Byline) == "" {
			result.Byline = strings.TrimSpace(article.Byline)
		}
		if article.PageType != "" {
			result.PageType = string(article.PageType)
		}
		if article.Root != nil {
			markdown := normalizeMarkdownOutput(resolveMarkdownURLs(readability.ToMarkdown(article.Root), baseURL))
			if markdown != "" {
				result.Body = markdown
				result.ExtractionMode = modePrefix + "-readability"
				result.ExtractedBytes = len([]byte(markdown))
				result.ContentQuality = assessHTMLContentQuality(markdown, result.ExtractionMode, htmlText)
				return result
			}
		}
	}

	if doc == nil {
		result.Body = htmlText
		result.ExtractionMode = modePrefix + "-fallback"
		result.ExtractedBytes = len([]byte(result.Body))
		result.ContentQuality = assessHTMLContentQuality(result.Body, result.ExtractionMode, htmlText)
		return result
	}

	markdown := htmlDocumentToMarkdownBase(doc, baseURL)
	if strings.TrimSpace(markdown) == "" {
		result.Body = htmlText
		result.ExtractionMode = modePrefix + "-fallback"
		result.ExtractedBytes = len([]byte(result.Body))
		result.ContentQuality = assessHTMLContentQuality(result.Body, result.ExtractionMode, htmlText)
		return result
	}
	result.Body = markdown
	result.ExtractionMode = modePrefix + "-legacy"
	result.ExtractedBytes = len([]byte(markdown))
	result.ContentQuality = assessHTMLContentQuality(markdown, result.ExtractionMode, htmlText)
	return result
}

func fillHTMLMetadata(result *HTMLRender, htmlText string, doc *html.Node, baseURL string) {
	if strings.TrimSpace(htmlText) == "" {
		return
	}
	if vdoc, err := readability.ParseHTML(htmlText, baseURL); err == nil {
		meta := readability.GetJSONLD(vdoc)
		result.Title = firstNonEmpty(meta.Title, result.Title)
		result.Byline = firstNonEmpty(meta.Byline, result.Byline)
		result.SiteName = firstNonEmpty(meta.SiteName, result.SiteName)
		result.PublishedTime = firstNonEmpty(meta.PublishedTime, result.PublishedTime)
	}
	if doc == nil {
		return
	}
	result.Title = firstNonEmpty(
		result.Title,
		extractHTMLMetaContent(doc, "property", "og:title"),
		extractHTMLMetaContent(doc, "name", "twitter:title"),
		extractHTMLTitle(doc),
	)
	result.Byline = firstNonEmpty(
		result.Byline,
		extractHTMLMetaContent(doc, "name", "author"),
		extractHTMLMetaContent(doc, "property", "article:author"),
	)
	result.SiteName = firstNonEmpty(
		result.SiteName,
		extractHTMLMetaContent(doc, "property", "og:site_name"),
		extractHTMLMetaContent(doc, "name", "application-name"),
	)
	result.PublishedTime = firstNonEmpty(
		result.PublishedTime,
		extractHTMLMetaContent(doc, "property", "article:published_time"),
		extractHTMLMetaContent(doc, "property", "og:published_time"),
		extractHTMLMetaContent(doc, "name", "pubdate"),
		extractHTMLMetaContent(doc, "name", "date"),
	)
}

func htmlDocumentToMarkdownBase(doc *html.Node, baseURL string) string {
	root := findContentRoot(doc)
	if root == nil {
		return ""
	}
	var sb strings.Builder
	nodeToMarkdown(root, &sb, 0, baseURL)
	return normalizeMarkdownOutput(sb.String())
}

func normalizeMarkdownOutput(s string) string {
	replacer := strings.NewReplacer("\u00a0", " ")
	s = replacer.Replace(s)
	s = collapseBlankLines(s)
	return strings.TrimSpace(s)
}

func collapseBlankLines(s string) string {
	var out strings.Builder
	newlineRun := 0
	for _, r := range s {
		if r == '\n' {
			newlineRun++
			if newlineRun <= 2 {
				out.WriteRune(r)
			}
			continue
		}
		newlineRun = 0
		out.WriteRune(r)
	}
	return out.String()
}

// findContentRoot returns the best node for content extraction:
// prefers <main> or <article>, falls back to <body>.
func findContentRoot(n *html.Node) *html.Node {
	if n.Type == html.ElementNode {
		switch n.Data {
		case "main", "article":
			return n
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if found := findContentRoot(c); found != nil {
			return found
		}
	}
	// Fall back to body.
	if n.Type == html.ElementNode && n.Data == "body" {
		return n
	}
	return nil
}

// skipNode returns true for elements whose content should be omitted.
func skipNode(tag string) bool {
	switch tag {
	case "script", "style", "noscript", "nav", "footer", "aside",
		"iframe", "svg", "form", "button", "input", "select", "textarea":
		return true
	}
	return false
}

// nodeToMarkdown recursively converts an HTML node tree to Markdown text.
func nodeToMarkdown(n *html.Node, sb *strings.Builder, depth int, baseURL string) {
	if n == nil {
		return
	}
	switch n.Type {
	case html.TextNode:
		text := strings.TrimSpace(n.Data)
		if text != "" {
			sb.WriteString(text)
			sb.WriteString(" ")
		}
		return
	case html.ElementNode:
		if skipNode(n.Data) {
			return
		}
		switch n.Data {
		case "h1", "h2", "h3", "h4", "h5", "h6":
			level := int(n.Data[1] - '0')
			sb.WriteString("\n")
			sb.WriteString(strings.Repeat("#", level))
			sb.WriteString(" ")
			childrenToMarkdown(n, sb, depth, baseURL)
			sb.WriteString("\n")
			return
		case "p", "div", "section":
			sb.WriteString("\n")
			childrenToMarkdown(n, sb, depth, baseURL)
			sb.WriteString("\n")
			return
		case "br":
			sb.WriteString("\n")
			return
		case "hr":
			sb.WriteString("\n---\n")
			return
		case "strong", "b":
			sb.WriteString("**")
			childrenToMarkdown(n, sb, depth, baseURL)
			sb.WriteString("**")
			return
		case "em", "i":
			sb.WriteString("_")
			childrenToMarkdown(n, sb, depth, baseURL)
			sb.WriteString("_")
			return
		case "code":
			sb.WriteString("`")
			childrenToMarkdown(n, sb, depth, baseURL)
			sb.WriteString("`")
			return
		case "pre":
			sb.WriteString("\n```\n")
			childrenToMarkdown(n, sb, depth, baseURL)
			sb.WriteString("\n```\n")
			return
		case "a":
			href := resolveURL(baseURL, attrVal(n, "href"))
			sb.WriteString("[")
			childrenToMarkdown(n, sb, depth, baseURL)
			if href != "" {
				sb.WriteString("](")
				sb.WriteString(href)
				sb.WriteString(")")
			} else {
				sb.WriteString("]")
			}
			return
		case "img":
			alt := attrVal(n, "alt")
			src := resolveURL(baseURL, attrVal(n, "src"))
			if alt != "" || src != "" {
				sb.WriteString(fmt.Sprintf("![%s](%s)", alt, src))
			}
			return
		case "ul", "ol":
			sb.WriteString("\n")
			childrenToMarkdown(n, sb, depth+1, baseURL)
			sb.WriteString("\n")
			return
		case "li":
			sb.WriteString("\n")
			sb.WriteString(listIndent(depth))
			sb.WriteString("- ")
			childrenToMarkdown(n, sb, depth, baseURL)
			return
		case "blockquote":
			sb.WriteString("\n> ")
			childrenToMarkdown(n, sb, depth, baseURL)
			sb.WriteString("\n")
			return
		case "table":
			sb.WriteString("\n")
			childrenToMarkdown(n, sb, depth, baseURL)
			sb.WriteString("\n")
			return
		case "tr":
			sb.WriteString("\n| ")
			childrenToMarkdown(n, sb, depth, baseURL)
			sb.WriteString("|")
			return
		case "th", "td":
			childrenToMarkdown(n, sb, depth, baseURL)
			sb.WriteString(" | ")
			return
		}
	}
	childrenToMarkdown(n, sb, depth, baseURL)
}

func listIndent(depth int) string {
	return strings.Repeat("  ", max(depth-1, 0))
}

func childrenToMarkdown(n *html.Node, sb *strings.Builder, depth int, baseURL string) {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		nodeToMarkdown(c, sb, depth, baseURL)
	}
}

func attrVal(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func resolveMarkdownURLs(markdown, baseURL string) string {
	if strings.TrimSpace(baseURL) == "" || strings.TrimSpace(markdown) == "" {
		return markdown
	}
	var sb strings.Builder
	for i := 0; i < len(markdown); {
		if markdown[i] == ']' && i+1 < len(markdown) && markdown[i+1] == '(' {
			close := strings.IndexByte(markdown[i+2:], ')')
			if close >= 0 {
				urlStart := i + 2
				urlEnd := urlStart + close
				rawURL := markdown[urlStart:urlEnd]
				sb.WriteString("](")
				sb.WriteString(resolveURL(baseURL, rawURL))
				sb.WriteByte(')')
				i = urlEnd + 1
				continue
			}
		}
		sb.WriteByte(markdown[i])
		i++
	}
	return sb.String()
}

func resolveURL(baseURL, raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || baseURL == "" {
		return raw
	}
	ref, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	if ref.IsAbs() {
		return ref.String()
	}
	base, err := url.Parse(baseURL)
	if err != nil {
		return raw
	}
	return base.ResolveReference(ref).String()
}

// DecodeText decodes a response body using declared charset and content evidence.
func DecodeText(data []byte, contentType string) (Decoded, error) {
	if decoded, ok := decodeUsingDeclaredCharset(data, contentType); ok {
		return decoded, nil
	}
	decoded, err := textfile.Decode(data, "")
	if err == nil {
		return Decoded{Text: decoded.Text, UTF8: []byte(decoded.Text), Charset: decoded.Encoding.Name}, nil
	}
	if IsHTML(contentType) {
		if decoded, ok := decodeHTMLByDocumentEncoding(data, contentType); ok {
			return decoded, nil
		}
	}
	if errors.Is(err, textfile.ErrBinaryFile) {
		return Decoded{}, ErrBinaryResponse
	}
	return Decoded{}, err
}

func decodeHTML(data []byte, contentType string) (Decoded, error) {
	if decoded, ok := decodeUsingDeclaredCharset(data, contentType); ok {
		return decoded, nil
	}
	if utf8.Valid(data) {
		utf8Body := append([]byte(nil), data...)
		return Decoded{Text: string(utf8Body), UTF8: utf8Body, Charset: "utf-8"}, nil
	}
	if decoded, ok := decodeHTMLByDocumentEncoding(data, contentType); ok {
		return decoded, nil
	}
	decoded, err := textfile.Decode(data, "")
	if err == nil {
		return Decoded{Text: decoded.Text, UTF8: []byte(decoded.Text), Charset: decoded.Encoding.Name}, nil
	}
	if errors.Is(err, textfile.ErrBinaryFile) {
		return Decoded{}, ErrBinaryResponse
	}
	return Decoded{}, err
}

func decodeUsingDeclaredCharset(data []byte, contentType string) (Decoded, bool) {
	label := charsetLabelFromContentType(contentType)
	if label == "" {
		return Decoded{}, false
	}
	reader, err := charset.NewReaderLabel(label, bytes.NewReader(data))
	if err != nil {
		return Decoded{}, false
	}
	utf8Body, readErr := io.ReadAll(reader)
	if readErr != nil {
		return Decoded{}, false
	}
	_, canonical := charset.Lookup(label)
	if canonical == "" {
		canonical = strings.ToLower(strings.TrimSpace(label))
	}
	canonical = normalizeCharsetName(canonical)
	return Decoded{Text: string(utf8Body), UTF8: utf8Body, Charset: canonical}, true
}

func decodeHTMLByDocumentEncoding(data []byte, contentType string) (Decoded, bool) {
	enc, name, _ := charset.DetermineEncoding(samplePrefix(data, 1024), contentType)
	if enc == nil {
		return Decoded{}, false
	}
	reader := transform.NewReader(bytes.NewReader(data), enc.NewDecoder())
	utf8Body, err := io.ReadAll(reader)
	if err != nil {
		return Decoded{}, false
	}
	if name == "" {
		name = "utf-8"
	}
	return Decoded{Text: string(utf8Body), UTF8: utf8Body, Charset: normalizeCharsetName(strings.ToLower(name))}, true
}

func normalizeCharsetName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	switch name {
	case "shift_jis":
		return "shift-jis"
	default:
		return name
	}
}

func charsetLabelFromContentType(contentType string) string {
	_, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(params["charset"])
}

func samplePrefix(data []byte, maxBytes int) []byte {
	if len(data) <= maxBytes {
		return data
	}
	return data[:maxBytes]
}

func extractHTMLTitle(n *html.Node) string {
	if n == nil {
		return ""
	}
	if n.Type == html.ElementNode && n.Data == "title" {
		return strings.TrimSpace(textContent(n))
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if title := extractHTMLTitle(c); title != "" {
			return title
		}
	}
	return ""
}

func extractHTMLMetaContent(n *html.Node, attrKey, attrValWant string) string {
	if n == nil {
		return ""
	}
	attrKey = strings.ToLower(strings.TrimSpace(attrKey))
	attrValWant = strings.ToLower(strings.TrimSpace(attrValWant))
	if n.Type == html.ElementNode && n.Data == "meta" {
		matched := false
		content := ""
		for _, a := range n.Attr {
			key := strings.ToLower(strings.TrimSpace(a.Key))
			val := strings.TrimSpace(a.Val)
			lowerVal := strings.ToLower(val)
			if key == attrKey && lowerVal == attrValWant {
				matched = true
			}
			if key == "content" {
				content = val
			}
		}
		if matched {
			return content
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if v := extractHTMLMetaContent(c, attrKey, attrValWant); v != "" {
			return v
		}
	}
	return ""
}

func textContent(n *html.Node) string {
	if n == nil {
		return ""
	}
	if n.Type == html.TextNode {
		return n.Data
	}
	var sb strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		sb.WriteString(textContent(c))
	}
	return sb.String()
}

// AssessTextQuality classifies whether extracted plain text is thin or substantial.
func AssessTextQuality(text string) string {
	if isThinText(text) {
		return "thin"
	}
	return "ok"
}

func assessHTMLContentQuality(body, mode, htmlSource string) string {
	trimmed := strings.TrimSpace(body)
	if looksLikeJSShellHTML(htmlSource, trimmed) {
		return "suspect-shell"
	}
	if isThinText(trimmed) {
		return "thin"
	}
	if strings.Contains(mode, "legacy") || strings.Contains(mode, "fallback") {
		return "fallback"
	}
	return "ok"
}

func isThinText(text string) bool {
	text = strings.TrimSpace(text)
	if text == "" {
		return true
	}
	runeCount := 0
	for _, r := range text {
		if r == '\n' || r == '\r' || r == '\t' || r == ' ' {
			continue
		}
		runeCount++
	}
	if runeCount < 40 {
		return true
	}
	return len(strings.Fields(text)) < 10 && runeCount < 120
}

func looksLikeJSShellHTML(htmlSource, extracted string) bool {
	lowerHTML := strings.ToLower(htmlSource)
	lowerExtracted := strings.ToLower(strings.TrimSpace(extracted))
	markers := []string{
		"enable javascript",
		"javascript is required",
		"requires javascript",
		"turn on javascript",
		"please enable cookies",
		"app shell",
		"loading...",
		"loading…",
		"loading application",
		"please wait while the application loads",
	}
	for _, marker := range markers {
		if strings.Contains(lowerHTML, marker) || strings.Contains(lowerExtracted, marker) {
			if len([]rune(extracted)) < 1200 {
				return true
			}
		}
	}
	scriptCount := strings.Count(lowerHTML, "<script")
	if scriptCount >= 6 && len(lowerHTML) >= 6000 && len([]rune(extracted)) < 300 {
		return true
	}
	if scriptCount >= 3 && len(strings.Fields(extracted)) <= 25 && len(lowerHTML) > 3000 {
		return true
	}
	return false
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
