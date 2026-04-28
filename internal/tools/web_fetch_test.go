package tools

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/keakon/chord/internal/config"
	readability "github.com/mackee/go-readability"
	"golang.org/x/net/html"
)

func TestHTMLToMarkdownHandlesTopLevelListItems(t *testing.T) {
	got := normalizeMarkdownForTest(htmlToMarkdown([]byte(`
		<!doctype html>
		<html>
		<body>
			<li>Alpha</li>
			<li>Beta</li>
		</body>
		</html>
	`)))

	const want = "- Alpha\n- Beta"
	if got != want {
		t.Fatalf("htmlToMarkdown(top-level li) = %q, want %q", got, want)
	}
}

func TestHTMLToMarkdownKeepsNestedListIndentation(t *testing.T) {
	got := normalizeMarkdownForTest(htmlToMarkdown([]byte(`
		<!doctype html>
		<html>
		<body>
			<ul>
				<li>
					Parent
					<ul>
						<li>Child</li>
					</ul>
				</li>
			</ul>
		</body>
		</html>
	`)))

	const want = "- Parent\n  - Child"
	if got != want {
		t.Fatalf("htmlToMarkdown(nested li) = %q, want %q", got, want)
	}
}

func TestHTMLToMarkdownPrefersMainContentOverNavNoise(t *testing.T) {
	got := normalizeMarkdownForTest(htmlToMarkdown([]byte(`
		<!doctype html>
		<html>
		<head><title>Example</title></head>
		<body>
			<nav>
				<ul><li>Home</li><li>Pricing</li></ul>
			</nav>
			<main>
				<h1>Article Title</h1>
				<p>Useful content paragraph.</p>
			</main>
			<footer>Footer links</footer>
		</body>
		</html>
	`)))
	if strings.Contains(got, "Home") || strings.Contains(got, "Pricing") || strings.Contains(got, "Footer links") {
		t.Fatalf("htmlToMarkdown kept obvious chrome noise: %q", got)
	}
	if !strings.Contains(got, "Article Title") || !strings.Contains(got, "Useful content paragraph.") {
		t.Fatalf("htmlToMarkdown missing main content: %q", got)
	}
}

func TestHTMLToMarkdownResolvesRelativeLinks(t *testing.T) {
	doc := `<!doctype html><html><body><main><a href="/docs/start">Start</a><img alt="logo" src="/img/logo.png"></main></body></html>`
	parsed := mustParseHTMLDoc(t, doc)
	got := normalizeMarkdownForTest(htmlDocumentToMarkdownBase(parsed, "https://example.com/base/"))
	if !strings.Contains(got, "[Start ](https://example.com/docs/start)") {
		t.Fatalf("expected resolved relative link, got %q", got)
	}
	if !strings.Contains(got, "![logo](https://example.com/img/logo.png)") {
		t.Fatalf("expected resolved relative image src, got %q", got)
	}
}

func TestWebFetchUsesDefaultHeadersAndMetadata(t *testing.T) {
	var gotUA, gotAL, gotAE string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		gotAL = r.Header.Get("Accept-Language")
		gotAE = r.Header.Get("Accept-Encoding")
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, "hello")
	}))
	defer server.Close()

	tool := NewWebFetchTool(config.WebFetchConfig{})
	out := executeWebFetchForTest(t, tool, map[string]any{"url": server.URL})

	if gotUA != webFetchDefaultUserAgent {
		t.Fatalf("User-Agent = %q, want %q", gotUA, webFetchDefaultUserAgent)
	}
	if gotAL != webFetchAcceptLanguage {
		t.Fatalf("Accept-Language = %q, want %q", gotAL, webFetchAcceptLanguage)
	}
	if gotAE != "gzip" {
		t.Fatalf("Accept-Encoding = %q, want Go transport-managed gzip", gotAE)
	}
	mustContain(t, out, "URL: "+server.URL+"\n")
	mustContain(t, out, "Content-Type: text/plain; charset=utf-8\n")
	mustContain(t, out, "Charset: utf-8\n")
	mustContain(t, out, "Extraction-Mode: raw\n")
	mustContain(t, out, "Truncated: none\n")
	if strings.Contains(out, "darwin") || strings.Contains(out, "arm64") {
		t.Fatalf("output should not expose local platform details: %q", out)
	}
}

func TestWebFetchUserAgentOverride(t *testing.T) {
	const customUA = "CustomUA/1.0"
	var gotUA string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, "ok")
	}))
	defer server.Close()

	tool := NewWebFetchTool(config.WebFetchConfig{UserAgent: stringPtr(customUA)})
	_ = executeWebFetchForTest(t, tool, map[string]any{"url": server.URL})
	if gotUA != customUA {
		t.Fatalf("User-Agent = %q, want %q", gotUA, customUA)
	}
}

func TestWebFetchEmptyUserAgentFallsBackToDefault(t *testing.T) {
	empty := "   "
	var gotUA string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, "ok")
	}))
	defer server.Close()

	tool := NewWebFetchTool(config.WebFetchConfig{UserAgent: &empty})
	_ = executeWebFetchForTest(t, tool, map[string]any{"url": server.URL})
	if gotUA != webFetchDefaultUserAgent {
		t.Fatalf("User-Agent = %q, want %q", gotUA, webFetchDefaultUserAgent)
	}
}

func TestWebFetchReportsFinalURLAfterRedirect(t *testing.T) {
	mux := http.NewServeMux()
	server := httptest.NewServer(mux)
	defer server.Close()

	mux.HandleFunc("/start", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/final", http.StatusFound)
	})
	mux.HandleFunc("/final", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, "done")
	})

	tool := NewWebFetchTool(config.WebFetchConfig{})
	out := executeWebFetchForTest(t, tool, map[string]any{"url": server.URL + "/start"})
	mustContain(t, out, "Final-URL: "+server.URL+"/final\n")
}

func TestWebFetchDecodesCharsetDeclaredText(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=shift_jis")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte{0x82, 0xb1, 0x82, 0xf1, 0x82, 0xc9, 0x82, 0xbf, 0x82, 0xcd}) // こんにちは in Shift_JIS
	}))
	defer server.Close()

	tool := NewWebFetchTool(config.WebFetchConfig{})
	out := executeWebFetchForTest(t, tool, map[string]any{"url": server.URL})
	mustContain(t, out, "Charset: shift-jis\n")
	mustContain(t, out, "こんにちは")
}

func TestWebFetchHTMLPrefersDeclaredCharset(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=shift_jis")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(
			"<html><head><title>" + string([]byte{0x83, 0x65, 0x83, 0x58, 0x83, 0x67}) + "</title></head><body><main><p>" +
				string([]byte{0x82, 0xb1, 0x82, 0xf1, 0x82, 0xc9, 0x82, 0xbf, 0x82, 0xcd}) + "</p></main></body></html>",
		))
	}))
	defer server.Close()

	tool := NewWebFetchTool(config.WebFetchConfig{})
	out := executeWebFetchForTest(t, tool, map[string]any{"url": server.URL})
	mustContain(t, out, "Charset: shift-jis\n")
	mustContain(t, out, "こんにちは")
}

func TestWebFetchHTMLUsesMetaCharsetFallback(t *testing.T) {
	data := mustEncodeForTest("<!doctype html><html><head><meta charset=\"shift_jis\"><title>テスト</title></head><body><main><p>こんにちは</p></main></body></html>", "shift-jis")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(data)
	}))
	defer server.Close()

	tool := NewWebFetchTool(config.WebFetchConfig{})
	out := executeWebFetchForTest(t, tool, map[string]any{"url": server.URL})
	mustContain(t, out, "Charset: shift-jis\n")
	mustContain(t, out, "こんにちは")
}

func TestWebFetchHTMLWithoutCharsetKeepsUTF8(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = io.WriteString(w, `<!doctype html><html><head><title>中文标题</title></head><body><main><h1>中文标题</h1><p>这是一段没有声明 charset 但实际使用 UTF-8 编码的中文正文。</p></main></body></html>`)
	}))
	defer server.Close()

	tool := NewWebFetchTool(config.WebFetchConfig{})
	out := executeWebFetchForTest(t, tool, map[string]any{"url": server.URL})
	mustContain(t, out, "Charset: utf-8\n")
	mustContain(t, out, "中文标题")
	mustContain(t, out, "这是一段没有声明 charset 但实际使用 UTF-8 编码的中文正文。")
	if strings.Contains(out, "ä¸") || strings.Contains(out, "æ–") {
		t.Fatalf("output appears mojibake-decoded as windows-1252: %q", out)
	}
}

func TestWebFetchHTMLIncludesMetadataHeaders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, `<!doctype html>
		<html>
		<head>
			<title>Ignored title</title>
			<meta property="og:title" content="Structured Title">
			<meta name="author" content="Jane Doe">
			<meta property="og:site_name" content="Doc Site">
			<meta property="article:published_time" content="2026-04-28">
			<script type="application/ld+json">{"author":{"@type":"Person","name":"Jane Doe"},"headline":"Structured Title","datePublished":"2026-04-28","publisher":{"name":"Doc Site"}}</script>
		</head>
		<body><main><h1>Structured Title</h1><p>This page contains enough article content to avoid being marked thin. It also exercises metadata extraction.</p></main></body>
		</html>`)
	}))
	defer server.Close()

	tool := NewWebFetchTool(config.WebFetchConfig{})
	out := executeWebFetchForTest(t, tool, map[string]any{"url": server.URL})
	mustContain(t, out, "Title: Structured Title\n")
	mustContain(t, out, "Byline: Jane Doe\n")
	mustContain(t, out, "Site-Name: Doc Site\n")
	mustContain(t, out, "Published-Time: 2026-04-28\n")
}

func TestWebFetchResultIsHardCappedAtOutputLimit(t *testing.T) {
	big := strings.Repeat("你好", webFetchTextOutputBytes)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, big)
	}))
	defer server.Close()

	tool := NewWebFetchTool(config.WebFetchConfig{})
	out := executeWebFetchForTest(t, tool, map[string]any{"url": server.URL})
	if len([]byte(out)) > webFetchTextOutputBytes {
		t.Fatalf("final output size = %d, want <= %d", len([]byte(out)), webFetchTextOutputBytes)
	}
	mustContain(t, out, "Truncated: output\n")
	mustContain(t, out, "Output-Limit: "+strconv.Itoa(webFetchTextOutputBytes)+"\n")
	mustContain(t, out, "...(output truncated)")
}

func TestWebFetchInputTruncationForTextResources(t *testing.T) {
	big := strings.Repeat("a", webFetchTextInputBytes+2048)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, big)
	}))
	defer server.Close()

	tool := NewWebFetchTool(config.WebFetchConfig{})
	out := executeWebFetchForTest(t, tool, map[string]any{"url": server.URL})
	mustContain(t, out, "Truncated: input,output\n")
	mustContain(t, out, "Read-Bytes: "+strconv.Itoa(webFetchTextInputBytes)+"/"+strconv.Itoa(webFetchTextInputBytes)+"\n")
}

func TestWebFetchHTMLUsesLargerInputCapThanRawHTML(t *testing.T) {
	if got := webFetchLimitsFor("text/html; charset=utf-8", false); got.InputBytes != webFetchHTMLInputBytes || got.OutputBytes != webFetchHTMLOutputBytes {
		t.Fatalf("html limits = %+v", got)
	}
	if got := webFetchLimitsFor("text/html; charset=utf-8", true); got.InputBytes != webFetchRawHTMLInputBytes || got.OutputBytes != webFetchTextOutputBytes {
		t.Fatalf("raw html limits = %+v", got)
	}
	if got := webFetchLimitsFor("text/plain; charset=utf-8", false); got.InputBytes != webFetchTextInputBytes || got.OutputBytes != webFetchTextOutputBytes {
		t.Fatalf("text limits = %+v", got)
	}
}

func TestWebFetchSuspectShellIsReportedWithoutBrowserFallback(t *testing.T) {
	origExtract := webFetchReadabilityExtract
	defer func() { webFetchReadabilityExtract = origExtract }()

	webFetchReadabilityExtract = func(string, readability.ReadabilityOptions, string) (readability.ReadabilityArticle, error) {
		return readability.ReadabilityArticle{}, errors.New("force fallback")
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, `<!doctype html><html><body><div id="app">Loading...</div><script src="/a.js"></script><script src="/b.js"></script><script src="/c.js"></script></body></html>`)
	}))
	defer server.Close()

	tool := NewWebFetchTool(config.WebFetchConfig{})
	out := executeWebFetchForTest(t, tool, map[string]any{"url": server.URL})
	mustContain(t, out, "Content-Quality: suspect-shell\n")
	if strings.Contains(out, "Browser-Fallback:") {
		t.Fatalf("WebFetch should not expose browser fallback metadata: %q", out)
	}
}

func TestWebFetchReadabilityResolvesRelativeLinks(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, `<!doctype html>
		<html><body><article>
		<h1>Readable links</h1>
		<p>This article contains enough text for readability extraction to choose the article body as main content. It includes links and images that should be resolved against the final response URL. The additional prose here intentionally pushes the sample above the default readability character threshold so this test exercises the readability path instead of the legacy fallback. Readers can use the linked documentation to continue learning about the feature, understand configuration tradeoffs, and verify that extracted Markdown remains useful for language models. This final sentence adds still more ordinary article content for a stable extraction candidate.</p>
		<p><a href="/docs/start">Start</a><img alt="logo" src="/img/logo.png"></p>
		</article></body></html>`)
	}))
	defer server.Close()

	tool := NewWebFetchTool(config.WebFetchConfig{})
	out := executeWebFetchForTest(t, tool, map[string]any{"url": server.URL + "/articles/readable"})
	mustContain(t, out, "Extraction-Mode: html-readability\n")
	mustContain(t, out, "[Start]("+server.URL+"/docs/start)")
	mustContain(t, out, "![logo]("+server.URL+"/img/logo.png)")
}

func TestWebFetchRawHTMLKeepsOriginalHTML(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, `<!doctype html><html><body><main><h1>Raw Title</h1><p>Raw paragraph.</p></main></body></html>`)
	}))
	defer server.Close()

	tool := NewWebFetchTool(config.WebFetchConfig{})
	out := executeWebFetchForTest(t, tool, map[string]any{"url": server.URL, "raw": true})
	mustContain(t, out, "Extraction-Mode: html-raw\n")
	mustContain(t, out, "<h1>Raw Title</h1>")
}

func TestWebFetchNonHTMLContentStaysRaw(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_, _ = io.WriteString(w, `{"ok":true,"message":"hello"}`)
	}))
	defer server.Close()

	tool := NewWebFetchTool(config.WebFetchConfig{})
	out := executeWebFetchForTest(t, tool, map[string]any{"url": server.URL})
	mustContain(t, out, "Extraction-Mode: raw\n")
	mustContain(t, out, `{"ok":true,"message":"hello"}`)
}

func TestWebFetchNon2xxReturnsError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "missing", http.StatusNotFound)
	}))
	defer server.Close()

	tool := NewWebFetchTool(config.WebFetchConfig{})
	_, err := executeWebFetchForTestAllowError(t, tool, map[string]any{"url": server.URL})
	if err == nil || !strings.Contains(err.Error(), "HTTP 404") {
		t.Fatalf("expected HTTP 404 error, got %v", err)
	}
}

func TestAssessHTMLContentQualityMarksThinAndFallback(t *testing.T) {
	if got := assessHTMLContentQuality("short text", "html-readability", "<html><body>short text</body></html>"); got != "thin" {
		t.Fatalf("quality = %q, want thin", got)
	}
	if got := assessHTMLContentQuality(strings.Repeat("content ", 40), "html-legacy", "<html><body>content</body></html>"); got != "fallback" {
		t.Fatalf("quality = %q, want fallback", got)
	}
}

func TestTruncateValidUTF8DoesNotBreakRune(t *testing.T) {
	got, truncated := truncateValidUTF8("你a", 1)
	if !truncated {
		t.Fatal("expected truncation")
	}
	if got != "" {
		t.Fatalf("truncateValidUTF8 returned %q, want empty string", got)
	}

	got, truncated = truncateValidUTF8("你好a", 4)
	if !truncated {
		t.Fatal("expected truncation")
	}
	if got != "你" {
		t.Fatalf("truncateValidUTF8 returned %q, want %q", got, "你")
	}
}

func executeWebFetchForTest(t *testing.T, tool WebFetchTool, args map[string]any) string {
	t.Helper()
	out, err := executeWebFetchForTestAllowError(t, tool, args)
	if err != nil {
		t.Fatalf("WebFetch.Execute: %v", err)
	}
	return out
}

func executeWebFetchForTestAllowError(t *testing.T, tool WebFetchTool, args map[string]any) (string, error) {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	return tool.Execute(context.Background(), raw)
}

func normalizeMarkdownForTest(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		out = append(out, strings.TrimRight(line, " "))
	}
	return strings.Join(out, "\n")
}

func mustContain(t *testing.T, s, want string) {
	t.Helper()
	if !strings.Contains(s, want) {
		t.Fatalf("expected output to contain %q, got %q", want, s)
	}
}

func mustParseHTMLDoc(t *testing.T, body string) *html.Node {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(body))
	if err != nil {
		t.Fatalf("html.Parse: %v", err)
	}
	return doc
}

func stringPtr(v string) *string {
	return &v
}
