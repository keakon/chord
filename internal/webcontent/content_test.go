package webcontent

import (
	"bytes"
	"errors"
	"strings"
	"testing"

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

func TestAssessHTMLContentQualityMarksThinAndFallback(t *testing.T) {
	if got := assessHTMLContentQuality("short text", "html-readability", "<html><body>short text</body></html>"); got != "thin" {
		t.Fatalf("quality = %q, want thin", got)
	}
	if got := assessHTMLContentQuality(strings.Repeat("content ", 40), "html-legacy", "<html><body>content</body></html>"); got != "fallback" {
		t.Fatalf("quality = %q, want fallback", got)
	}
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

func mustParseHTMLDoc(t *testing.T, body string) *html.Node {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(body))
	if err != nil {
		t.Fatalf("html.Parse: %v", err)
	}
	return doc
}

// htmlToMarkdown converts an HTML document to a simplified Markdown
// representation; used by tests that exercise extraction paths without a
// surrounding HTTP fetch.
func htmlToMarkdown(data []byte) string {
	decoded, decErr := decodeHTML(data, "text/html")
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

func TestRenderHTMLMalformedFallback(t *testing.T) {
	original := readabilityExtract
	t.Cleanup(func() { readabilityExtract = original })
	readabilityExtract = func(string, readability.ReadabilityOptions, string) (readability.ReadabilityArticle, error) {
		return readability.ReadabilityArticle{}, errors.New("force fallback")
	}
	rendered := RenderHTML([]byte(`<!doctype html><html><head><title>Broken</title></head><body><main><h1>Broken page<p>still readable`), "text/html; charset=utf-8", "https://example.invalid/", "html")
	if rendered.Title != "Broken" || !strings.Contains(rendered.Body, "Broken page") || !strings.Contains(rendered.Body, "still readable") {
		t.Fatalf("fallback omitted readable content: %+v", rendered)
	}
	if rendered.ExtractionMode != "html-legacy" {
		t.Fatalf("unexpected fallback mode: %s", rendered.ExtractionMode)
	}
}

func TestRenderHTMLSuspectShellFallback(t *testing.T) {
	original := readabilityExtract
	t.Cleanup(func() { readabilityExtract = original })
	readabilityExtract = func(string, readability.ReadabilityOptions, string) (readability.ReadabilityArticle, error) {
		return readability.ReadabilityArticle{}, errors.New("force fallback")
	}
	rendered := RenderHTML([]byte(`<!doctype html><html><body><div id="app">Loading...</div><script src="/a.js"></script><script src="/b.js"></script><script src="/c.js"></script></body></html>`), "text/html; charset=utf-8", "https://example.invalid/", "html")
	if rendered.ContentQuality != "suspect-shell" {
		t.Fatalf("quality = %q, want suspect-shell", rendered.ContentQuality)
	}
}
