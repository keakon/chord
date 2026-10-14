package syntax

import (
	"fmt"
	"image/color"
	"math"
	"strings"
	"testing"

	"github.com/keakon/lipgloss/v2"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
)

func TestToolCodeChromaStyleShared(t *testing.T) {
	first := toolCodeChromaStyle()
	if first == nil {
		t.Fatal("toolCodeChromaStyle returned nil")
	}
	if second := toolCodeChromaStyle(); second != first {
		t.Fatalf("toolCodeChromaStyle rebuilt the style: %p != %p", first, second)
	}
	if got, want := first.Get(chroma.Error).Colour.String(), darkThemeSyntaxTextColour; got != want {
		t.Fatalf("Error colour = %q, want %q", got, want)
	}
	// The shared instance must still resolve tokens it does not override
	// through the Monokai parent.
	if got := first.Get(chroma.Keyword); !got.Colour.IsSet() {
		t.Fatal("Keyword colour is unset; the parent style chain is broken")
	}
}

func TestToolCodeChromaStyleAllocsGuard(t *testing.T) {
	if allocs := testing.AllocsPerRun(50, func() { _ = toolCodeChromaStyle() }); allocs != 0 {
		t.Fatalf("toolCodeChromaStyle allocated %v times per call, want 0", allocs)
	}
}

func TestLexerLookupMemoized(t *testing.T) {
	goLexer := chromaLexerForName("go")
	if goLexer == nil {
		t.Fatal("no lexer resolved for \"go\"")
	}
	if again := chromaLexerForName("go"); again != goLexer {
		t.Fatalf("chromaLexerForName rebuilt the lexer: %p != %p", goLexer, again)
	}
	if missing := chromaLexerForName("chord-no-such-lexer"); missing != nil {
		t.Fatalf("unknown lexer name resolved to %v", missing.Config().Name)
	}
	if repeated := chromaLexerForName("chord-no-such-lexer"); repeated != nil {
		t.Fatalf("unknown lexer name did not stay cached as nil: %v", repeated.Config().Name)
	}
	if first, second := lexerForFilePath("demo.go"), lexerForFilePath("demo.go"); first == nil || first != second {
		t.Fatalf("extension lookup did not reuse the memoized lexer: %v vs %v", first, second)
	}
}

func TestLexerLookupAllocsGuard(t *testing.T) {
	if allocs := testing.AllocsPerRun(50, func() { _ = lexerForFilePath("demo.go") }); allocs != 0 {
		t.Fatalf("lexerForFilePath allocated %v times per call, want 0", allocs)
	}
}

// TestLexerLookupBounded pins the memo's upper bound: its keys include code
// fence languages taken from model output, so a long session must not be able to
// grow the map without limit.
func TestLexerLookupBounded(t *testing.T) {
	lexerLookupMu.Lock()
	lexerLookup = make(map[string]chroma.Lexer, lexerLookupMaxEntries)
	for i := range lexerLookupMaxEntries {
		lexerLookup[fmt.Sprintf("chord-filler-%d", i)] = nil
	}
	lexerLookupMu.Unlock()

	if got := chromaLexerForName("go"); got == nil {
		t.Fatal("no lexer resolved for \"go\"")
	}

	lexerLookupMu.Lock()
	size := len(lexerLookup)
	lexerLookupMu.Unlock()
	if size > 1 {
		t.Fatalf("lexer memo kept %d entries past the cap of %d, want it emptied", size, lexerLookupMaxEntries)
	}
}

// TestLexerFastNamesMatchGlobMatch pins every fast-path entry to the lookup it
// replaces: the name must still be a registered lexer name, and the filename
// glob match must reach that same lexer instance. An entry that stopped
// holding would otherwise silently change which lexer highlights the
// extension.
func TestLexerFastNamesMatchGlobMatch(t *testing.T) {
	for ext, name := range syntaxHighlightExtFastNames {
		byName := lexers.Get(name)
		if byName == nil {
			t.Fatalf("%s: fast-path name %q is not a registered lexer", ext, name)
		}
		if got := lexerName(byName); got != name {
			t.Fatalf("%s: lexers.Get(%q) resolved to %q, want the registered name", ext, name, got)
		}
		if byGlob := lexers.Get(ext); byGlob != byName {
			t.Fatalf("%s: fast path resolves %q but the glob match resolves %q", ext, name, lexerName(byGlob))
		}
	}
}

// TestLexerFastPathFilesResolveToTheirLexer walks the fast table through the
// real entry point, so a key the whitelist or alias step shadows still fails
// here.
func TestLexerFastPathFilesResolveToTheirLexer(t *testing.T) {
	for ext, name := range syntaxHighlightExtFastNames {
		if got := lexerName(lexerForFilePath("sample" + ext)); got != name {
			t.Fatalf("lexerForFilePath(sample%s) resolved to %q, want %q", ext, got, name)
		}
	}
}

// TestLexerResolutionKeepsFilenameMatch pins the extensions whose name and
// filename tables disagree inside chroma: ".gql" resolves as a name although
// no lexer claims the glob, and the ".sql" name resolves to a lexer the glob
// match outranks. The whitelist keeps those filename answers, apart from the
// fast-table entries pinned above and the alias for ".mdx", whose glob is
// unclaimed.
func TestLexerResolutionKeepsFilenameMatch(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"demo.go", "Go"},
		{"demo.sql", "MySQL"},
		{"demo.gql", ""},
		{"demo.md", "markdown"},
		{"demo.mdx", "markdown"},
		{"Dockerfile", "Docker"},
	}
	for _, tc := range tests {
		lexer := lexerForFilePath(tc.path)
		if tc.want == "" {
			if lexer != nil {
				t.Fatalf("%s resolved to %q, want no lexer", tc.path, lexerName(lexer))
			}
			continue
		}
		if got := lexerName(lexer); got != tc.want {
			t.Fatalf("%s resolved to %q, want %q", tc.path, got, tc.want)
		}
	}
}

func lexerName(lexer chroma.Lexer) string {
	if lexer == nil {
		return "<nil>"
	}
	return lexer.Config().Name
}

// BenchmarkCodeHighlighterPerFileSection mirrors renderFileDiffCall: a fresh
// highlighter per diff file section followed by its first highlighted line.
// Sharing the style and memoizing the lexer lookup is what keeps this out of
// the millisecond range that resolving both per section used to cost.
func BenchmarkCodeHighlighterPerFileSection(b *testing.B) {
	b.ReportAllocs()
	// Warm what a session has already paid for before it renders a diff: the
	// shared style, the lexer memo, and chroma's lazily built lexer rules.
	warm := New("demo.go", "package main", "")
	_ = warm.Snippet("package main\n", "")
	for b.Loop() {
		h := New("demo.go", "package main", "")
		_ = h.Snippet("package main\n", "")
	}
}

func TestNormalizeCodeFenceLanguage(t *testing.T) {
	cases := map[string]string{
		"":          "",
		"text":      "text",
		"plaintext": "text",
		"txt":       "text",
		"js":        "javascript",
		"ts":        "typescript",
		"sh":        "bash",
		"yml":       "yaml",
	}
	for in, want := range cases {
		if got := NormalizeLanguage(in); got != want {
			t.Fatalf("NormalizeLanguage(%q)=%q want %q", in, got, want)
		}
	}
}

func TestNewCodeHighlighterUsesToolStyle(t *testing.T) {
	h := newCodeHighlighter("sample.go", "package main\n// comment\n")
	if got := h.chromaStyle.Get(chroma.CommentSingle).Colour.String(); got != darkThemeSyntaxCommentColour {
		t.Fatalf("highlighter comment colour = %q, want %q", got, darkThemeSyntaxCommentColour)
	}
}
func TestLexerForFilePathPrefersWhitelistedExtensionOverConflictingBasename(t *testing.T) {
	hl := newCodeHighlighter("bash_jobs.go", "package tools\n")
	lexer := hl.getLexer("package tools\n")
	if lexer == nil {
		t.Fatal("expected lexer for whitelisted .go extension")
	}
	if got := lexer.Config().Name; got != "Go" {
		t.Fatalf("expected Go lexer for bash_jobs.go, got %q", got)
	}
}

func TestLexerForFilePathUsesSpecialFilenameWithSuffix(t *testing.T) {
	hl := newCodeHighlighter("Dockerfile.prod", "FROM alpine:3.20\nRUN echo hi\n")
	lexer := hl.getLexer("FROM alpine:3.20\nRUN echo hi\n")
	if lexer == nil {
		t.Fatal("expected lexer for Dockerfile.prod")
	}
	if got := lexer.Config().Name; got != "Docker" {
		t.Fatalf("expected Docker lexer for Dockerfile.prod, got %q", got)
	}
}

func TestLexerForFilePathUsesMDXAsMarkdown(t *testing.T) {
	hl := newCodeHighlighter("website/src/content/docs/index.mdx", "# Hello\n\n<Component />\n")
	lexer := hl.getLexer("# Hello\n")
	if lexer == nil {
		t.Fatal("expected lexer for .mdx extension")
	}
	if got := lexer.Config().Name; got != "markdown" {
		t.Fatalf("expected Markdown lexer for .mdx, got %q", got)
	}
}

func TestLexerForFilePathUsesMarkdownExtensionAsMarkdown(t *testing.T) {
	hl := newCodeHighlighter("README.markdown", "# Hello\n\nContent\n")
	lexer := hl.getLexer("# Hello\n")
	if lexer == nil {
		t.Fatal("expected lexer for .markdown extension")
	}
	if got := lexer.Config().Name; got != "markdown" {
		t.Fatalf("expected Markdown lexer for .markdown, got %q", got)
	}
}

func TestLexerForExplicitMDXLanguageUsesMarkdown(t *testing.T) {
	hl := New("", "# Hello\n\n<Component />\n", "mdx")
	lexer := hl.getLexer("# Hello\n")
	if lexer == nil {
		t.Fatal("expected lexer for mdx language hint")
	}
	if got := lexer.Config().Name; got != "markdown" {
		t.Fatalf("expected Markdown lexer for mdx language hint, got %q", got)
	}
}

func TestLexerForFilePathDisablesHighlightForUnknownExtension(t *testing.T) {
	hl := newCodeHighlighter("notes.unknownext", "package tools\n")
	rendered := hl.Line("package tools", "")
	if rendered != "package tools" {
		t.Fatalf("expected unknown extension to remain plain text, got %q", rendered)
	}
}

func TestLexerForFilePathDisablesHighlightForUnknownBasenameWithoutExtension(t *testing.T) {
	hl := newCodeHighlighter("Jenkinsfile", "pipeline { agent any }\n")
	rendered := hl.Line("pipeline { agent any }", "")
	if rendered != "pipeline { agent any }" {
		t.Fatalf("expected unsupported basename to remain plain text, got %q", rendered)
	}
}

func TestLexerForContentOnlyStillUsesAnalysis(t *testing.T) {
	hl := newCodeHighlighter("", "package tools\n")
	lexer := hl.getLexer("package tools\n")
	if lexer == nil {
		t.Fatal("expected lexer when no file path is available")
	}
	if got := lexer.Config().Name; got == "fallback" {
		t.Fatalf("expected analysis-based lexer for content-only highlighting, got %q", got)
	}
}

func TestHighlightCodeLinesKeepsMarkdownEOFBlockMarkersStyled(t *testing.T) {
	hl := newCodeHighlighter("plan.md", "")
	lines := []string{
		"1. first item",
		"2. second item",
	}

	rendered := Lines(hl, lines, "")
	if len(rendered) != len(lines) {
		t.Fatalf("expected %d highlighted lines, got %d: %#v", len(lines), len(rendered), rendered)
	}
	keywordSeq := ColorSequence(lipgloss.Color(toolCodeChromaStyle().Get(chroma.Keyword).Colour.String()), true)
	if keywordSeq == "" {
		t.Fatal("expected markdown keyword colour to produce an ANSI sequence")
	}
	for i, line := range rendered {
		marker := fmt.Sprintf("%d.", i+1)
		if !strings.Contains(line, keywordSeq+marker) {
			t.Fatalf("expected marker %q to be highlighted with keyword style; got %q", marker, line)
		}
	}
}

func TestHighlightCodeLinesKeepsMarkdownEOFHeadingStyled(t *testing.T) {
	hl := newCodeHighlighter("notes.md", "")
	lines := []string{
		"# first heading",
		"## second heading",
	}

	rendered := Lines(hl, lines, "")
	if len(rendered) != len(lines) {
		t.Fatalf("expected %d highlighted lines, got %d: %#v", len(lines), len(rendered), rendered)
	}
	subheadingSeq := ColorSequence(lipgloss.Color(toolCodeChromaStyle().Get(chroma.GenericSubheading).Colour.String()), true)
	if subheadingSeq == "" {
		t.Fatal("expected markdown subheading colour to produce an ANSI sequence")
	}
	if !strings.Contains(rendered[1], subheadingSeq+"## second heading") {
		t.Fatalf("expected EOF subheading to be highlighted with subheading style; got %q", rendered[1])
	}
}

func TestCodeHighlighterReevaluatesUnanchoredStreamingSample(t *testing.T) {
	h := New("", "plain text", "")
	first := h.getLexer(h.sample)
	if first == nil {
		t.Fatal("expected an initial lexer")
	}
	h.updateContext("", "plain text\nfunc main() {}")
	second := h.getLexer(h.sample)
	if second == nil {
		t.Fatal("expected a lexer after the sample grew")
	}
	if second == first {
		t.Fatal("unanchored content lexer should be reevaluated after streaming sample growth")
	}
}

func TestCodeHighlighterSkipsOversizedRenderCacheEntry(t *testing.T) {
	h := New("demo.go", "", "")
	oversized := strings.Repeat("x", maxRenderCacheBytes)
	got := h.cacheRendered(1, oversized)
	if got != oversized {
		t.Fatal("cacheRendered changed the oversized result")
	}
	if len(h.renderCache) != 0 || h.renderCacheBytes != 0 {
		t.Fatalf("oversized result entered cache: entries=%d bytes=%d", len(h.renderCache), h.renderCacheBytes)
	}
}

// newCodeHighlighter is the language-inferring constructor used by syntax
// tests; production callers go through New to
// honor an explicit language hint.
func newCodeHighlighter(filePath, sample string) *Highlighter {
	return New(filePath, sample, "")
}
func TestToolCodeChromaStyleKeepsTokenContrastOnToolBackgrounds(t *testing.T) {
	style := toolCodeChromaStyle()

	if style == nil {
		t.Fatal("tool code style should not be nil")
	}
	backgrounds := map[string]color.Color{
		"tool":        lipgloss.Color("237"),
		"diff add":    lipgloss.Color("#1e3d2e"),
		"diff delete": lipgloss.Color("#3d2525"),
	}
	checkToken := func(tokenType chroma.TokenType) {
		t.Helper()
		entry := style.Get(tokenType)
		if !entry.Colour.IsSet() {
			return
		}
		foreground := color.RGBA{R: entry.Colour.Red(), G: entry.Colour.Green(), B: entry.Colour.Blue(), A: 0xff}
		for name, background := range backgrounds {
			if got := contrastRatio(foreground, background); got < 4.5 {
				t.Errorf("%s foreground %s contrast on %s = %.2f, want >= 4.5", tokenType, entry.Colour.String(), name, got)
			}
		}
	}
	checkToken(chroma.Error)
	checked := map[chroma.TokenType]bool{chroma.Error: true}
	for tokenType := range chroma.StandardTypes {
		if tokenType <= 0 {
			continue
		}
		checkToken(tokenType)
		checked[tokenType] = true
	}
	for _, tokenType := range style.Types() {
		if tokenType <= 0 || checked[tokenType] {
			continue
		}
		checkToken(tokenType)
	}
}

func contrastRatio(a, b color.Color) float64 {
	al := relativeLuminance(a)
	bl := relativeLuminance(b)
	if al < bl {
		al, bl = bl, al
	}
	return (al + 0.05) / (bl + 0.05)
}
func relativeLuminance(c color.Color) float64 {
	r, g, b, _ := c.RGBA()
	linear := func(v uint32) float64 {
		n := float64(v) / 0xffff
		if n <= 0.04045 {
			return n / 12.92
		}
		return math.Pow((n+0.055)/1.055, 2.4)
	}
	return 0.2126*linear(r) + 0.7152*linear(g) + 0.0722*linear(b)
}
