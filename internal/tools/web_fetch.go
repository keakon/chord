package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/net/proxy"

	"github.com/keakon/chord/internal/config"
	"github.com/keakon/chord/internal/textfile"
	"github.com/keakon/chord/internal/tooloutput"
	"github.com/keakon/chord/internal/webcontent"
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

// WebFetchTool fetches a URL and returns its content as plain text or Markdown.
type WebFetchTool struct {
	cfg           config.WebFetchConfig
	globalProxy   string
	transportPool *webFetchTransportPool
}

type webFetchTransportPool struct {
	mu         sync.Mutex
	transports map[string]*http.Transport
	closed     bool
}

type webFetchArgs struct {
	URL       string `json:"url"`
	Raw       bool   `json:"raw,omitempty"`        // return raw text without HTML->Markdown conversion
	TimeoutMs int    `json:"timeout_ms,omitempty"` // request deadline in milliseconds (default 30000, capped at 120000)
}

// WebFetchDefaultTimeoutMs is the request deadline applied when timeout_ms is
// omitted; WebFetchMaxTimeoutMs caps it so a value off by an order of magnitude
// cannot hold the turn open for minutes. Both are exported because the TUI
// mirrors them when it shows the effective deadline on a card or confirmation.
const (
	WebFetchDefaultTimeoutMs = 30_000
	WebFetchMaxTimeoutMs     = 120_000
)

// WebFetchEffectiveTimeoutMs resolves a timeout_ms argument the way the tool
// does: an omitted or non-positive value keeps the default, and anything above
// the cap is clamped instead of rejected. It is the one place that rule lives,
// so the confirmation dialog and the tool card cannot show a deadline the
// request will not use.
func WebFetchEffectiveTimeoutMs(timeoutMs int) int {
	if timeoutMs <= 0 {
		return WebFetchDefaultTimeoutMs
	}
	return min(timeoutMs, WebFetchMaxTimeoutMs)
}

// webFetchTimeout turns the resolved timeout_ms argument into the request
// deadline.
func webFetchTimeout(timeoutMs int) time.Duration {
	return time.Duration(WebFetchEffectiveTimeoutMs(timeoutMs)) * time.Millisecond
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

func NewWebFetchTool(cfg config.WebFetchConfig, globalProxy string) WebFetchTool {
	return WebFetchTool{cfg: cfg, globalProxy: globalProxy, transportPool: &webFetchTransportPool{}}
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

func newHTTPTransportWithProxy(proxyURL string) (*http.Transport, error) {
	proxyURL = strings.TrimSpace(proxyURL)
	dialer := &net.Dialer{
		Timeout:   60 * time.Second,
		KeepAlive: 30 * time.Second,
	}
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           dialer.DialContext,
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

	return transport, nil
}

func (t WebFetchTool) transportForProxy(proxyURL string) (*http.Transport, error) {
	pool := t.transportPool
	if pool == nil {
		return nil, fmt.Errorf("WebFetch tool is not initialized")
	}
	proxyURL = strings.TrimSpace(proxyURL)
	pool.mu.Lock()
	defer pool.mu.Unlock()
	if pool.closed {
		return nil, fmt.Errorf("WebFetch tool is closed")
	}
	if pool.transports == nil {
		pool.transports = make(map[string]*http.Transport)
	}
	if transport := pool.transports[proxyURL]; transport != nil {
		return transport, nil
	}
	transport, err := newHTTPTransportWithProxy(proxyURL)
	if err != nil {
		return nil, err
	}
	pool.transports[proxyURL] = transport
	return transport, nil
}

// Close releases idle connections and prevents new requests. The application
// owns this pool; workers borrowing the registered tool must not close it.
// Active requests finish through their own cancellation contexts.
func (t WebFetchTool) Close() {
	pool := t.transportPool
	if pool == nil {
		return
	}
	pool.mu.Lock()
	defer pool.mu.Unlock()
	pool.closed = true
	for _, transport := range pool.transports {
		transport.CloseIdleConnections()
	}
	clear(pool.transports)
}

func (WebFetchTool) Name() string { return NameWebFetch }

func (WebFetchTool) ConcurrencyPolicy(args json.RawMessage) ConcurrencyPolicy {
	return normalizeConcurrencyPolicy(NameWebFetch, urlToolConcurrencyPolicy(args))
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
			"timeout_ms": map[string]any{
				"type":        "integer",
				"description": fmt.Sprintf("Optional request timeout in milliseconds (default %d, capped at %d).", WebFetchDefaultTimeoutMs, WebFetchMaxTimeoutMs),
			},
		},
		"required":             []string{"url"},
		"additionalProperties": false,
	}
}

func (WebFetchTool) IsReadOnly() bool { return true }

func (WebFetchTool) ConcurrencySafeReadOnly(json.RawMessage) bool { return true }

func (t WebFetchTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
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

	timeout := webFetchTimeout(a.TimeoutMs)

	execCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	effectiveProxy := t.effectiveProxy()
	transport, err := t.transportForProxy(effectiveProxy)
	if err != nil {
		return "", fmt.Errorf("create HTTP transport: %w", err)
	}
	client := &http.Client{Timeout: timeout, Transport: transport}
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
	isHTML := webcontent.IsHTML(contentType)
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
		rendered := webcontent.RenderHTML(body, contentType, finalURL, "html")
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
		decoded, decErr := webcontent.DecodeText(body, contentType)
		if decErr != nil {
			if errors.Is(decErr, webcontent.ErrBinaryResponse) || errors.Is(decErr, textfile.ErrBinaryFile) {
				return "", fmt.Errorf("response content is not decodable text: %w", decErr)
			}
			extractedBody = toValidUTF8String(body)
			result.Charset = "utf-8"
		} else {
			extractedBody = decoded.Text
			result.Charset = decoded.Charset
		}
		result.ExtractedBytes = len([]byte(extractedBody))
		result.ContentQuality = webcontent.AssessTextQuality(extractedBody)
	}

	formatted, _, outputTruncated := finalizeWebFetchResult(result, extractedBody, readState.InputTruncated)
	if outputTruncated {
		formatted = appendWebFetchArtifactReference(formatted, result, extractedBody, readState.InputTruncated, SessionDirFromContext(ctx))
	}
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
	case webcontent.IsHTML(contentType) && !raw:
		return webFetchLimits{InputBytes: webFetchHTMLInputBytes, OutputBytes: webFetchHTMLOutputBytes}
	case webcontent.IsHTML(contentType) && raw:
		return webFetchLimits{InputBytes: webFetchRawHTMLInputBytes, OutputBytes: webFetchTextOutputBytes}
	case webcontent.IsTextLike(contentType) || strings.TrimSpace(contentType) == "":
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

func finalizeWebFetchResult(meta webFetchResult, extractedBody string, inputTruncated bool) (string, int, bool) {
	body := extractedBody
	outputTruncated := false
	for range 6 {
		meta.Truncated = truncatedStateLabel(inputTruncated, outputTruncated)
		meta.ReturnedBodyBytes = len([]byte(body))
		header := formatWebFetchHeader(meta)
		available := max(meta.OutputLimit-len([]byte(header)), 0)
		nextBody, nextOutputTruncated := fitBodyToBudget(extractedBody, available)
		if nextBody == body && nextOutputTruncated == outputTruncated {
			meta.Truncated = truncatedStateLabel(inputTruncated, nextOutputTruncated)
			meta.ReturnedBodyBytes = len([]byte(nextBody))
			finalHeader := formatWebFetchHeader(meta)
			available = max(meta.OutputLimit-len([]byte(finalHeader)), 0)
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
	available := max(meta.OutputLimit-len([]byte(finalHeader)), 0)
	body, outputTruncated = fitBodyToBudget(extractedBody, available)
	meta.Truncated = truncatedStateLabel(inputTruncated, outputTruncated)
	meta.ReturnedBodyBytes = len([]byte(body))
	finalHeader = formatWebFetchHeader(meta)
	return finalHeader + body, len([]byte(body)), outputTruncated
}

func appendWebFetchArtifactReference(formatted string, meta webFetchResult, extractedBody string, inputTruncated bool, sessionDir string) string {
	if strings.TrimSpace(sessionDir) == "" {
		return formatted
	}
	fullMeta := meta
	fullMeta.Truncated = truncatedStateLabel(inputTruncated, false)
	fullMeta.ReturnedBodyBytes = len([]byte(extractedBody))
	fullOutput := formatWebFetchHeader(fullMeta) + extractedBody
	savedPath := tooloutput.SaveFullOutput(fullOutput, sessionDir, "web-fetch")
	if savedPath == "" {
		return formatted
	}
	return formatted + "\n" + tooloutput.ShortArtifactReference(savedPath)
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
