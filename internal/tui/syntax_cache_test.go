package tui

import (
	"testing"

	"github.com/alecthomas/chroma/v2"
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

// TestLexerResolutionKeepsFilenameMatch pins extensions whose name and filename
// tables disagree inside chroma. Resolving the bare name first would highlight
// ".gql" as GraphQL and switch ".sql" from MySQL to the SQL lexer, so the
// filename glob match stays the only resolution order.
func TestLexerResolutionKeepsFilenameMatch(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"demo.go", "Go"},
		{"demo.sql", "MySQL"},
		{"demo.gql", ""},
		{"demo.md", "markdown"},
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
	warm := newCodeHighlighterWithLanguage("demo.go", "package main", "")
	_ = warm.highlightSnippet("package main\n", "")
	for b.Loop() {
		h := newCodeHighlighterWithLanguage("demo.go", "package main", "")
		_ = h.highlightSnippet("package main\n", "")
	}
}
