package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	readability "github.com/mackee/go-readability"
	"golang.org/x/net/html"
	"golang.org/x/net/html/charset"
	"golang.org/x/net/proxy"
	"golang.org/x/text/transform"

	"github.com/keakon/chord/internal/config"
)

const (
	webFetchHTMLInputBytes        = 5 * 1024 * 1024
	webFetchRawHTMLInputBytes     = 1 * 1024 * 1024
	webFetchTextInputBytes        = 1 * 1024 * 1024
	webFetchOtherInputBytes       = 512 * 1024
	webFetchDefaultUserAgent      = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/136.0.0.0 Safari/537.36"
	webFetchAcceptLanguage        = "en-US,en;q=0.9"
	webFetchHTMLOutputBytes       = 200 * 1024
	webFetchTextOutputBytes       = 120 * 1024
	webFetchOutputTruncatedSuffix = "\n...(output truncated)"
)

var (
	errWebFetchBinaryResponse = errors.New("response appears to be binary")

	webFetchReadabilityExtract = extractReadableHTML
)

func extractReadableHTML(htmlText string, options readability.ReadabilityOptions, baseURL string) (readability.ReadabilityArticle, error) {
	doc, err := readability.ParseHTML(htmlText, baseURL)
	if err != nil {
		return readability.ReadabilityArticle{}, err
	}
	readability.PreprocessDocument(doc)
	return readability.ExtractContent(doc, options), nil
}

// WebFetchTool fetches a URL and returns its content as plain text or Markdown.
type WebFetchTool struct {
	cfg         config.WebFetchConfig
	globalProxy string
}

type webFetchArgs struct {
	URL     string `json:"url"`
	Raw     bool   `json:"raw,omitempty"`     // return raw text without HTML->Markdown conversion
	Timeout int    `json:"timeout,omitempty"` // timeout in seconds (default 30, max 120)
}

type webFetchResult struct {
	URL               string
	FinalURL          string
	ContentType       string
	Charset           string
	Title             string
	Byline            string
	SiteName          string
	PublishedTime     string
	PageType          string
	ExtractionMode    string
	ContentQuality    string
	Truncated         string
	ReadBytes         int
	InputLimit        int
	ExtractedBytes    int
	ReturnedBodyBytes int
	OutputLimit       int
}

type webFetchReadState struct {
	BytesRead      int
	InputTruncated bool
	DeclaredLength int64
	InputLimit     int
}

type webFetchLimits struct {
	InputBytes  int
	OutputBytes int
}

type webFetchDecoded struct {
	Text    string
	UTF8    []byte
	Charset string
}

type webFetchHTMLRender struct {
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

func NewWebFetchTool(cfg config.WebFetchConfig, globalProxy string) WebFetchTool {
	return WebFetchTool{cfg: cfg, globalProxy: globalProxy}
}

func (t WebFetchTool) effectiveProxy() string {
	if t.cfg.Proxy != nil {
		if *t.cfg.Proxy == "" {
			return "direct"
		}
		return *t.cfg.Proxy
	}
	return t.globalProxy
}

func newHTTPClientWithProxy(proxyURL string, timeout time.Duration) (*http.Client, error) {
	proxyURL = strings.TrimSpace(proxyURL)
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   60 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ResponseHeaderTimeout: 60 * time.Second,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		MaxIdleConns:          32,
		MaxIdleConnsPerHost:   16,
	}

	if proxyURL != "" && proxyURL != "direct" {
		parsed, err := url.Parse(proxyURL)
		if err != nil {
			return nil, fmt.Errorf("parse proxy URL %q: %w", proxyURL, err)
		}
		scheme := strings.ToLower(parsed.Scheme)
		switch scheme {
		case "http", "https":
			transport.Proxy = http.ProxyURL(parsed)
		case "socks5":
			dialer, err := proxy.FromURL(parsed, proxy.Direct)
			if err != nil {
				return nil, fmt.Errorf("create SOCKS5 dialer from %q: %w", proxyURL, err)
			}
			transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
				if cd, ok := dialer.(proxy.ContextDialer); ok {
					return cd.DialContext(ctx, network, addr)
				}
				conn, err := dialer.Dial(network, addr)
				return conn, err
			}
		default:
			return nil, fmt.Errorf("unsupported proxy scheme %q", scheme)
		}
	} else if proxyURL == "direct" {
		transport.Proxy = nil
	}

	return &http.Client{Timeout: timeout, Transport: transport}, nil
}

func (WebFetchTool) Name() string { return "WebFetch" }

func (WebFetchTool) ConcurrencyPolicy(args json.RawMessage) ConcurrencyPolicy {
	return normalizeConcurrencyPolicy("WebFetch", urlToolConcurrencyPolicy(args))
}

func (WebFetchTool) Description() string {
	return "Fetch a URL and return its content. HTML pages are automatically converted to " +
		"Markdown (main content extracted). Set raw=true to get plain text without conversion. " +
		"Useful for reading documentation, GitHub issues/PRs, and other web resources."
}

func (WebFetchTool) Parameters() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"url": map[string]any{
				"type":        "string",
				"description": "The URL to fetch (http:// or https://).",
			},
			"raw": map[string]any{
				"type":        "boolean",
				"description": "Return raw text without HTML->Markdown conversion. Default false.",
			},
			"timeout": map[string]any{
				"type":        "integer",
				"description": "Request timeout in seconds. Default 30, max 120.",
			},
		},
		"required":             []string{"url"},
		"additionalProperties": false,
	}
}

func (WebFetchTool) IsReadOnly() bool { return true }

func (t WebFetchTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	var a webFetchArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %w", err)
	}
	if a.URL == "" {
		return "", fmt.Errorf("url is required")
	}
	if !strings.HasPrefix(a.URL, "http://") && !strings.HasPrefix(a.URL, "https://") {
		return "", fmt.Errorf("url must start with http:// or https://")
	}

	timeoutSec := 30
	if a.Timeout > 0 {
		timeoutSec = a.Timeout
		if timeoutSec > 120 {
			timeoutSec = 120
		}
	}

	execCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
	defer cancel()

	effectiveProxy := t.effectiveProxy()
	client, err := newHTTPClientWithProxy(effectiveProxy, time.Duration(timeoutSec)*time.Second)
	if err != nil {
		return "", fmt.Errorf("create HTTP client: %w", err)
	}
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return fmt.Errorf("stopped after 5 redirects")
		}
		scheme := req.URL.Scheme
		if scheme != "http" && scheme != "https" {
			return fmt.Errorf("redirect to non-http scheme %q not allowed", scheme)
		}
		return nil
	}
	req, err := http.NewRequestWithContext(execCtx, http.MethodGet, a.URL, nil)
	if err != nil {
		return "", fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("User-Agent", t.userAgent())
	req.Header.Set("Accept", "text/html,application/xhtml+xml,text/plain;q=0.8,*/*;q=0.7")
	req.Header.Set("Accept-Language", webFetchAcceptLanguage)

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetching URL: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, resp.Status)
	}

	contentType := resp.Header.Get("Content-Type")
	limits := webFetchLimitsFor(contentType, a.Raw)
	body, readState, err := readWebFetchBody(resp, limits.InputBytes)
	if err != nil {
		return "", fmt.Errorf("reading response: %w", err)
	}

	finalURL := resp.Request.URL.String()
	isHTML := isHTMLContentType(contentType)
	result := webFetchResult{
		URL:            a.URL,
		FinalURL:       finalURL,
		ContentType:    contentType,
		ExtractionMode: "raw",
		ReadBytes:      readState.BytesRead,
		InputLimit:     readState.InputLimit,
		OutputLimit:    limits.OutputBytes,
	}

	var extractedBody string
	if isHTML && !a.Raw {
		rendered := renderHTMLResponse(body, contentType, finalURL, "html")
		result.Charset = rendered.Charset
		result.Title = rendered.Title
		result.Byline = rendered.Byline
		result.SiteName = rendered.SiteName
		result.PublishedTime = rendered.PublishedTime
		result.PageType = rendered.PageType
		result.ExtractionMode = rendered.ExtractionMode
		result.ContentQuality = rendered.ContentQuality
		result.ExtractedBytes = rendered.ExtractedBytes
		extractedBody = rendered.Body
	} else {
		if isHTML {
			result.ExtractionMode = "html-raw"
		} else {
			result.ExtractionMode = "raw"
		}
		decoded, decErr := decodeWebFetchText(body, contentType)
		if decErr != nil {
			if errors.Is(decErr, errWebFetchBinaryResponse) || errors.Is(decErr, ErrBinaryFile) {
				return "", fmt.Errorf("response content is not decodable text: %w", decErr)
			}
			extractedBody = toValidUTF8String(body)
			result.Charset = "utf-8"
		} else {
			extractedBody = decoded.Text
			result.Charset = decoded.Charset
		}
		result.ExtractedBytes = len([]byte(extractedBody))
		result.ContentQuality = assessPlainTextQuality(extractedBody)
	}

	formatted, _, _ := finalizeWebFetchResult(result, extractedBody, readState.InputTruncated)
	return formatted, nil
}

func (t WebFetchTool) userAgent() string {
	if t.cfg.UserAgent != nil {
		if ua := strings.TrimSpace(*t.cfg.UserAgent); ua != "" {
			return ua
		}
	}
	return webFetchDefaultUserAgent
}

func webFetchLimitsFor(contentType string, raw bool) webFetchLimits {
	switch {
	case isHTMLContentType(contentType) && !raw:
		return webFetchLimits{InputBytes: webFetchHTMLInputBytes, OutputBytes: webFetchHTMLOutputBytes}
	case isHTMLContentType(contentType) && raw:
		return webFetchLimits{InputBytes: webFetchRawHTMLInputBytes, OutputBytes: webFetchTextOutputBytes}
	case isTextLikeContentType(contentType) || strings.TrimSpace(contentType) == "":
		return webFetchLimits{InputBytes: webFetchTextInputBytes, OutputBytes: webFetchTextOutputBytes}
	default:
		return webFetchLimits{InputBytes: webFetchOtherInputBytes, OutputBytes: webFetchTextOutputBytes}
	}
}

func readWebFetchBody(resp *http.Response, limit int) ([]byte, webFetchReadState, error) {
	if limit <= 0 {
		limit = webFetchTextInputBytes
	}
	reader := io.LimitReader(resp.Body, int64(limit)+1)
	body, err := io.ReadAll(reader)
	if err != nil {
		return nil, webFetchReadState{}, err
	}
	state := webFetchReadState{
		BytesRead:      len(body),
		DeclaredLength: resp.ContentLength,
		InputLimit:     limit,
	}
	if len(body) > limit {
		state.InputTruncated = true
		body = body[:limit]
		state.BytesRead = len(body)
	}
	return body, state, nil
}

func isHTMLContentType(contentType string) bool {
	mediaType := normalizedMediaType(contentType)
	return mediaType == "text/html" || mediaType == "application/xhtml+xml"
}

func isTextLikeContentType(contentType string) bool {
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

func renderHTMLResponse(body []byte, contentType, baseURL, modePrefix string) webFetchHTMLRender {
	decoded, err := decodeWebFetchHTML(body, contentType)
	utf8Body := bytes.ToValidUTF8(body, []byte("�"))
	if err == nil {
		utf8Body = decoded.UTF8
	}
	htmlText := string(utf8Body)
	result := webFetchHTMLRender{Charset: decoded.Charset}

	var doc *html.Node
	if parsed, parseErr := html.Parse(bytes.NewReader(utf8Body)); parseErr == nil {
		doc = parsed
	}
	fillHTMLMetadata(&result, htmlText, doc, baseURL)

	article, extractErr := webFetchReadabilityExtract(htmlText, readability.DefaultOptions(), baseURL)
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

func fillHTMLMetadata(result *webFetchHTMLRender, htmlText string, doc *html.Node, baseURL string) {
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
			sb.WriteString(strings.Repeat("#", level) + " ")
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
				sb.WriteString("](" + href + ")")
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
			sb.WriteString("\n" + listIndent(depth) + "- ")
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

func decodeWebFetchText(data []byte, contentType string) (webFetchDecoded, error) {
	if decoded, ok := decodeUsingDeclaredCharset(data, contentType); ok {
		return decoded, nil
	}
	decoded, err := decodeTextBytes(data, "")
	if err == nil {
		return webFetchDecoded{Text: decoded.Text, UTF8: []byte(decoded.Text), Charset: decoded.Encoding.Name}, nil
	}
	if isHTMLContentType(contentType) {
		if decoded, ok := decodeHTMLByDocumentEncoding(data, contentType); ok {
			return decoded, nil
		}
	}
	if errors.Is(err, ErrBinaryFile) {
		return webFetchDecoded{}, errWebFetchBinaryResponse
	}
	return webFetchDecoded{}, err
}

func decodeWebFetchHTML(data []byte, contentType string) (webFetchDecoded, error) {
	if decoded, ok := decodeUsingDeclaredCharset(data, contentType); ok {
		return decoded, nil
	}
	if utf8.Valid(data) {
		utf8Body := append([]byte(nil), data...)
		return webFetchDecoded{Text: string(utf8Body), UTF8: utf8Body, Charset: "utf-8"}, nil
	}
	if decoded, ok := decodeHTMLByDocumentEncoding(data, contentType); ok {
		return decoded, nil
	}
	decoded, err := decodeTextBytes(data, "")
	if err == nil {
		return webFetchDecoded{Text: decoded.Text, UTF8: []byte(decoded.Text), Charset: decoded.Encoding.Name}, nil
	}
	if errors.Is(err, ErrBinaryFile) {
		return webFetchDecoded{}, errWebFetchBinaryResponse
	}
	return webFetchDecoded{}, err
}

func decodeUsingDeclaredCharset(data []byte, contentType string) (webFetchDecoded, bool) {
	label := charsetLabelFromContentType(contentType)
	if label == "" {
		return webFetchDecoded{}, false
	}
	reader, err := charset.NewReaderLabel(label, bytes.NewReader(data))
	if err != nil {
		return webFetchDecoded{}, false
	}
	utf8Body, readErr := io.ReadAll(reader)
	if readErr != nil {
		return webFetchDecoded{}, false
	}
	_, canonical := charset.Lookup(label)
	if canonical == "" {
		canonical = strings.ToLower(strings.TrimSpace(label))
	}
	canonical = normalizeCharsetName(canonical)
	return webFetchDecoded{Text: string(utf8Body), UTF8: utf8Body, Charset: canonical}, true
}

func decodeHTMLByDocumentEncoding(data []byte, contentType string) (webFetchDecoded, bool) {
	enc, name, _ := charset.DetermineEncoding(samplePrefix(data, 1024), contentType)
	if enc == nil {
		return webFetchDecoded{}, false
	}
	reader := transform.NewReader(bytes.NewReader(data), enc.NewDecoder())
	utf8Body, err := io.ReadAll(reader)
	if err != nil {
		return webFetchDecoded{}, false
	}
	if name == "" {
		name = "utf-8"
	}
	return webFetchDecoded{Text: string(utf8Body), UTF8: utf8Body, Charset: normalizeCharsetName(strings.ToLower(name))}, true
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

func assessPlainTextQuality(text string) string {
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

func finalizeWebFetchResult(meta webFetchResult, extractedBody string, inputTruncated bool) (string, int, bool) {
	body := extractedBody
	outputTruncated := false
	for i := 0; i < 6; i++ {
		meta.Truncated = truncatedStateLabel(inputTruncated, outputTruncated)
		meta.ReturnedBodyBytes = len([]byte(body))
		header := formatWebFetchHeader(meta)
		available := meta.OutputLimit - len([]byte(header))
		if available < 0 {
			available = 0
		}
		nextBody, nextOutputTruncated := fitBodyToBudget(extractedBody, available)
		if nextBody == body && nextOutputTruncated == outputTruncated {
			meta.Truncated = truncatedStateLabel(inputTruncated, nextOutputTruncated)
			meta.ReturnedBodyBytes = len([]byte(nextBody))
			finalHeader := formatWebFetchHeader(meta)
			available = meta.OutputLimit - len([]byte(finalHeader))
			if available < 0 {
				available = 0
			}
			nextBody, nextOutputTruncated = fitBodyToBudget(extractedBody, available)
			meta.Truncated = truncatedStateLabel(inputTruncated, nextOutputTruncated)
			meta.ReturnedBodyBytes = len([]byte(nextBody))
			finalHeader = formatWebFetchHeader(meta)
			return finalHeader + nextBody, len([]byte(nextBody)), nextOutputTruncated
		}
		body = nextBody
		outputTruncated = nextOutputTruncated
	}
	meta.Truncated = truncatedStateLabel(inputTruncated, outputTruncated)
	meta.ReturnedBodyBytes = len([]byte(body))
	finalHeader := formatWebFetchHeader(meta)
	available := meta.OutputLimit - len([]byte(finalHeader))
	if available < 0 {
		available = 0
	}
	body, outputTruncated = fitBodyToBudget(extractedBody, available)
	meta.Truncated = truncatedStateLabel(inputTruncated, outputTruncated)
	meta.ReturnedBodyBytes = len([]byte(body))
	finalHeader = formatWebFetchHeader(meta)
	return finalHeader + body, len([]byte(body)), outputTruncated
}

func fitBodyToBudget(body string, maxBytes int) (string, bool) {
	if maxBytes <= 0 {
		return "", body != ""
	}
	if len([]byte(body)) <= maxBytes {
		return body, false
	}
	suffixBytes := len([]byte(webFetchOutputTruncatedSuffix))
	if maxBytes <= suffixBytes {
		truncated, _ := truncateValidUTF8(webFetchOutputTruncatedSuffix, maxBytes)
		return truncated, true
	}
	truncatedBody, _ := truncateValidUTF8(body, maxBytes-suffixBytes)
	return truncatedBody + webFetchOutputTruncatedSuffix, true
}

func truncateValidUTF8(s string, maxBytes int) (string, bool) {
	if maxBytes <= 0 {
		return "", s != ""
	}
	b := []byte(s)
	if len(b) <= maxBytes {
		return s, false
	}
	cut := maxBytes
	for cut > 0 && !utf8.Valid(b[:cut]) {
		cut--
	}
	if cut == 0 {
		return "", true
	}
	return string(b[:cut]), true
}

func truncatedStateLabel(inputTruncated, outputTruncated bool) string {
	switch {
	case inputTruncated && outputTruncated:
		return "input,output"
	case inputTruncated:
		return "input"
	case outputTruncated:
		return "output"
	default:
		return "none"
	}
}

func formatWebFetchHeader(r webFetchResult) string {
	var sb strings.Builder
	sb.WriteString("URL: ")
	sb.WriteString(r.URL)
	sb.WriteString("\n")
	if strings.TrimSpace(r.FinalURL) != "" && strings.TrimSpace(r.FinalURL) != strings.TrimSpace(r.URL) {
		sb.WriteString("Final-URL: ")
		sb.WriteString(r.FinalURL)
		sb.WriteString("\n")
	}
	sb.WriteString("Content-Type: ")
	sb.WriteString(r.ContentType)
	sb.WriteString("\n")
	if strings.TrimSpace(r.Charset) != "" {
		sb.WriteString("Charset: ")
		sb.WriteString(r.Charset)
		sb.WriteString("\n")
	}
	if strings.TrimSpace(r.Title) != "" {
		sb.WriteString("Title: ")
		sb.WriteString(r.Title)
		sb.WriteString("\n")
	}
	if strings.TrimSpace(r.Byline) != "" {
		sb.WriteString("Byline: ")
		sb.WriteString(r.Byline)
		sb.WriteString("\n")
	}
	if strings.TrimSpace(r.SiteName) != "" {
		sb.WriteString("Site-Name: ")
		sb.WriteString(r.SiteName)
		sb.WriteString("\n")
	}
	if strings.TrimSpace(r.PublishedTime) != "" {
		sb.WriteString("Published-Time: ")
		sb.WriteString(r.PublishedTime)
		sb.WriteString("\n")
	}
	if strings.TrimSpace(r.PageType) != "" {
		sb.WriteString("Page-Type: ")
		sb.WriteString(r.PageType)
		sb.WriteString("\n")
	}
	if strings.TrimSpace(r.ExtractionMode) != "" {
		sb.WriteString("Extraction-Mode: ")
		sb.WriteString(r.ExtractionMode)
		sb.WriteString("\n")
	}
	if strings.TrimSpace(r.ContentQuality) != "" {
		sb.WriteString("Content-Quality: ")
		sb.WriteString(r.ContentQuality)
		sb.WriteString("\n")
	}
	sb.WriteString("Read-Bytes: ")
	sb.WriteString(fmt.Sprintf("%d/%d", r.ReadBytes, r.InputLimit))
	sb.WriteString("\n")
	sb.WriteString("Extracted-Bytes: ")
	sb.WriteString(fmt.Sprintf("%d", r.ExtractedBytes))
	sb.WriteString("\n")
	sb.WriteString("Returned-Body-Bytes: ")
	sb.WriteString(fmt.Sprintf("%d", r.ReturnedBodyBytes))
	sb.WriteString("\n")
	sb.WriteString("Output-Limit: ")
	sb.WriteString(fmt.Sprintf("%d", r.OutputLimit))
	sb.WriteString("\n")
	if strings.TrimSpace(r.Truncated) != "" {
		sb.WriteString("Truncated: ")
		sb.WriteString(r.Truncated)
		sb.WriteString("\n")
	}
	sb.WriteString("\n")
	return sb.String()
}

func toValidUTF8String(data []byte) string {
	return string(bytes.ToValidUTF8(data, []byte("�")))
}
