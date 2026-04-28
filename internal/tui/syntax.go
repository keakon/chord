package tui

import (
	"bytes"
	"fmt"
	"hash/fnv"
	"image/color"
	"io"
	"path/filepath"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
	"github.com/charmbracelet/x/ansi"
)

const darkThemeSyntaxCommentColour = "#9aa06b"

// syntaxHighlightExtWhitelist is the conservative set of file extensions we
// trust for file-based syntax highlighting. For files with a path, we only
// highlight when the extension is explicitly allowed; otherwise we fall back to
// a small allowlist of well-known special basenames such as Dockerfile.*.
//
// This avoids false positives from Chroma's glob-based filename matching (for
// example, bash_jobs.go matching Bash via the "bash_*" glob instead of Go via
// ".go").
var syntaxHighlightExtWhitelist = map[string]struct{}{
	".astro":   {},
	".bash":    {},
	".c":       {},
	".cc":      {},
	".cjs":     {},
	".cpp":     {},
	".css":     {},
	".cxx":     {},
	".dart":    {},
	".fish":    {},
	".go":      {},
	".gql":     {},
	".graphql": {},
	".h":       {},
	".hh":      {},
	".hpp":     {},
	".htm":     {},
	".html":    {},
	".hcl":     {},
	".ini":     {},
	".java":    {},
	".js":      {},
	".json":    {},
	".jsx":     {},
	".kt":      {},
	".kts":     {},
	".less":    {},
	".lua":     {},
	".m":       {},
	".md":      {},
	".mjs":     {},
	".mm":      {},
	".nim":     {},
	".php":     {},
	".proto":   {},
	".ps1":     {},
	".py":      {},
	".rb":      {},
	".rs":      {},
	".rst":     {},
	".sass":    {},
	".scala":   {},
	".scss":    {},
	".sh":      {},
	".sql":     {},
	".svelte":  {},
	".swift":   {},
	".tf":      {},
	".tfvars":  {},
	".toml":    {},
	".ts":      {},
	".tsx":     {},
	".vue":     {},
	".xml":     {},
	".yaml":    {},
	".yml":     {},
	".zig":     {},
	".zsh":     {},
}

type specialFilenameLexerRule struct {
	name        string
	lexerName   string
	allowSuffix bool
}

var specialFilenameLexerRules = []specialFilenameLexerRule{
	{name: "dockerfile", lexerName: "dockerfile", allowSuffix: true},
	{name: "makefile", lexerName: "makefile", allowSuffix: true},
	{name: "caddyfile", lexerName: "caddyfile", allowSuffix: true},
	{name: "justfile", lexerName: "justfile", allowSuffix: true},
}

func lexerForFilePath(filePath string) chroma.Lexer {
	base := filepath.Base(filePath)
	if base == "" || base == "." {
		return nil
	}
	if l := lexerForWhitelistedExtension(base); l != nil {
		return l
	}
	return lexerForSpecialFilename(base)
}

func lexerForWhitelistedExtension(base string) chroma.Lexer {
	ext := strings.ToLower(filepath.Ext(base))
	if ext == "" {
		return nil
	}
	if _, ok := syntaxHighlightExtWhitelist[ext]; !ok {
		return nil
	}
	return lexers.Get(ext)
}

func lexerForSpecialFilename(base string) chroma.Lexer {
	lowerBase := strings.ToLower(base)
	for _, rule := range specialFilenameLexerRules {
		if lowerBase == rule.name {
			return lexers.Get(rule.lexerName)
		}
		if !rule.allowSuffix {
			continue
		}
		for _, sep := range []string{".", "-", "_"} {
			if strings.HasPrefix(lowerBase, rule.name+sep) {
				return lexers.Get(rule.lexerName)
			}
		}
	}
	return nil
}

func lexerForExplicitLanguage(language string) chroma.Lexer {
	language = normalizeCodeFenceLanguage(language)
	if language == "" || language == "text" {
		return nil
	}
	candidates := []string{language}
	switch language {
	case "javascript":
		candidates = append(candidates, "js")
	case "typescript":
		candidates = append(candidates, "ts")
	case "bash":
		candidates = append(candidates, "shell", "sh", "zsh")
	case "yaml":
		candidates = append(candidates, "yml")
	case "plaintext":
		candidates = append(candidates, "text")
	}
	for _, name := range candidates {
		if l := lexers.Get(name); l != nil {
			return l
		}
	}
	return nil
}

// codeHighlighter is a stateful syntax highlighter for code rendering.
// It caches the chroma lexer (expensive file-path lookup / optional content
// analysis when no file path exists) and highlighted render results across all
// lines/snippets of a single block.
type codeHighlighter struct {
	filePath      string
	sample        string
	language      string
	chromaStyle   *chroma.Style
	cachedLexer   chroma.Lexer
	lexerResolved bool
	renderCache   map[uint64]string // key: FNV-1a 64-bit hash of (bgTerm + "\x00" + source)
}

// newCodeHighlighter creates a codeHighlighter for the given file path.
func newCodeHighlighter(filePath, sample string) *codeHighlighter {
	return newCodeHighlighterWithLanguage(filePath, sample, "")
}

func newCodeHighlighterWithLanguage(filePath, sample, language string) *codeHighlighter {
	return &codeHighlighter{
		filePath:    filePath,
		sample:      sample,
		language:    normalizeCodeFenceLanguage(language),
		chromaStyle: toolCodeChromaStyle(),
		renderCache: make(map[uint64]string),
	}
}

func toolCodeChromaStyle() *chroma.Style {
	baseStyle := styles.Get("monokai")
	if baseStyle == nil {
		baseStyle = styles.Fallback
	}

	commentColour := darkThemeSyntaxCommentColour

	builder := baseStyle.Builder()
	for _, tokenType := range []chroma.TokenType{
		chroma.Comment,
		chroma.CommentHashbang,
		chroma.CommentMultiline,
		chroma.CommentSingle,
		chroma.CommentSpecial,
		chroma.CommentPreproc,
		chroma.CommentPreprocFile,
	} {
		entry := builder.Get(tokenType)
		entry.Colour = chroma.ParseColour(commentColour)
		builder.AddEntry(tokenType, entry)
	}
	styled, err := builder.Build()
	if err != nil {
		return baseStyle
	}
	return styled
}

func normalizeCodeFenceLanguage(language string) string {
	language = strings.TrimSpace(strings.ToLower(language))
	switch language {
	case "":
		return ""
	case "text", "plain", "plaintext", "txt":
		return "text"
	case "js":
		return "javascript"
	case "ts":
		return "typescript"
	case "sh", "shell", "zsh":
		return "bash"
	case "yml":
		return "yaml"
	default:
		return language
	}
}

// updateContext refreshes file/sample detection inputs and clears cached lexer
// and render results when either input changes.
func (h *codeHighlighter) updateContext(filePath, sample string) {
	reset := false
	if h.filePath != filePath {
		h.filePath = filePath
		reset = true
	}
	if sample != "" && h.sample != sample {
		h.sample = sample
		reset = true
	}
	if reset {
		h.cachedLexer = nil
		h.lexerResolved = false
		h.renderCache = make(map[uint64]string)
	}
}

func (h *codeHighlighter) updateLanguage(language string) {
	language = normalizeCodeFenceLanguage(language)
	if h.language == language {
		return
	}
	h.language = language
	h.cachedLexer = nil
	h.lexerResolved = false
	h.renderCache = make(map[uint64]string)
}

// getLexer returns the cached lexer, initialising it on first call.
// Detection order:
//   - explicit fence language (assistant code blocks)
//   - file path with an allowlisted extension
//   - file path matching an allowlisted special basename (eg Dockerfile.prod)
//   - content analysis, but only when there is no file path and no explicit language
//   - plaintext fallback for content-only snippets
func (h *codeHighlighter) getLexer(source string) chroma.Lexer {
	if h.lexerResolved {
		return h.cachedLexer
	}

	var l chroma.Lexer
	if h.language != "" {
		l = lexerForExplicitLanguage(h.language)
	} else if h.filePath != "" {
		l = lexerForFilePath(h.filePath)
	} else {
		sample := h.sample
		if sample == "" {
			sample = source
		}
		if sample != "" {
			l = lexers.Analyse(sample)
		}
		if l == nil {
			l = lexers.Fallback
		}
	}
	if l != nil {
		l = chroma.Coalesce(l)
	}
	h.cachedLexer = l
	h.lexerResolved = true
	return h.cachedLexer
}

// highlightLine syntax-highlights a single source line with the given terminal
// 256-colour background (e.g. "22" for dark green, "52" for dark red).
func (h *codeHighlighter) highlightLine(source, bgTerm string) string {
	return strings.TrimRight(h.highlightRendered(source, bgTerm), "\n")
}

// highlightSnippet syntax-highlights a full source snippet while preserving
// its original newline structure.
func (h *codeHighlighter) highlightSnippet(source, bgTerm string) string {
	return h.highlightRendered(source, bgTerm)
}

// highlightRendered syntax-highlights the given source and preserves its
// original newline structure.
func (h *codeHighlighter) highlightRendered(source, bgTerm string) string {
	if strings.TrimSpace(source) == "" {
		return source
	}

	// Compute FNV-1a 64-bit cache key over bgTerm + NUL + source.
	hv := fnv.New64a()
	_, _ = hv.Write([]byte(bgTerm))
	_, _ = hv.Write([]byte{'\x00'})
	_, _ = hv.Write([]byte(source))
	key := hv.Sum64()
	if cached, ok := h.renderCache[key]; ok {
		return cached
	}

	var bg color.Color
	if bgTerm != "" {
		bg = lipgloss.Color(bgTerm)
	}

	l := h.getLexer(source)
	if l == nil {
		return source
	}
	it, err := l.Tokenise(nil, source)
	if err != nil {
		return source
	}

	f := bgFormatter{bg: bg}
	var buf bytes.Buffer
	if err = f.Format(&buf, h.chromaStyle, it); err != nil {
		return source
	}

	result := buf.String()
	// Evict the entire cache when it grows too large to bound memory usage.
	// A simple full-clear is sufficient: the cache is per-Block and a single
	// diff rarely exceeds 1024 unique lines in practice.
	const maxRenderCacheSize = 1024
	if len(h.renderCache) > maxRenderCacheSize {
		h.renderCache = make(map[uint64]string, maxRenderCacheSize)
	}
	h.renderCache[key] = result
	return result
}

// bgFormatter is a chroma formatter that renders tokens with syntax colors
// and a fixed terminal background color on every token.
// bg is an arbitrary color.Color (including lipgloss.Color, which accepts both
// ANSI 256-colour index strings like "22" and hex RGB strings like "#005f00").
// ansiSeqForColor always emits a 24-bit RGB sequence (\x1b[48;2;R;G;Bm) so
// that fg and bg are never merged into a single SGR param — some layers in the
// bubbletea/ultraviolet render pipeline do not handle merged params correctly,
// causing garbled output.
type bgFormatter struct {
	bg color.Color
}

// escapeControlChars sanitises ASCII control characters for terminal display:
// TAB is expanded to 4 spaces; CR and LF are dropped; all other C0 control
// characters (0x00–0x1f) are replaced with their Unicode Control Picture
// representations (U+2400–U+241F); DEL (0x7f) becomes U+2421 (␡).
func escapeControlChars(s string) string {
	var sb strings.Builder
	sb.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '\t':
			sb.WriteString("    ") // expand tab to 4 spaces
		case r == '\n' || r == '\r':
			// skip inline newlines in token values
		case r >= 0 && r <= 0x1f:
			sb.WriteRune('\u2400' + r)
		case r == ansi.DEL:
			sb.WriteRune('\u2421')
		default:
			sb.WriteRune(r)
		}
	}
	return sb.String()
}

func writeStyledSegment(w io.Writer, prefix, value string) error {
	if value == "" {
		return nil
	}
	if prefix == "" {
		_, err := io.WriteString(w, value)
		return err
	}
	_, err := fmt.Fprintf(w, "%s%s\x1b[m", prefix, value)
	return err
}

func writeStyledTokenValue(w io.Writer, prefix, value string) error {
	var segment strings.Builder
	flush := func() error {
		escaped := escapeControlChars(segment.String())
		segment.Reset()
		return writeStyledSegment(w, prefix, escaped)
	}

	for _, r := range value {
		switch r {
		case '\r':
			continue
		case '\n':
			if err := flush(); err != nil {
				return err
			}
			if _, err := io.WriteString(w, "\n"); err != nil {
				return err
			}
		default:
			segment.WriteRune(r)
		}
	}
	return flush()
}

func (f bgFormatter) Format(w io.Writer, style *chroma.Style, it chroma.Iterator) error {
	// Build the background sequence once. We emit fg and bg as separate
	// \x1b[...m sequences so that no parser needs to handle combined
	// "38;2;R;G;B;48;5;N" params in a single SGR — some layers in the
	// bubbletea/ultraviolet render pipeline do not handle merged params
	// correctly, causing garbled output.
	bgSeq := ansiSeqForColor(f.bg, false)
	for token := it(); token != chroma.EOF; token = it() {
		if token.Value == "" {
			continue
		}
		entry := style.Get(token.Type)

		// Build a text-decoration sequence only when needed (Bold/Italic/Underline).
		// fg and bg colours are handled separately via ansiSeqForColor below.
		var seq strings.Builder
		if entry.Bold == chroma.Yes || entry.Italic == chroma.Yes || entry.Underline == chroma.Yes {
			seq.WriteString("\x1b[")
			first := true
			writeParam := func(p string) {
				if !first {
					seq.WriteByte(';')
				}
				seq.WriteString(p)
				first = false
			}
			if entry.Bold == chroma.Yes {
				writeParam("1")
			}
			if entry.Italic == chroma.Yes {
				writeParam("3")
			}
			if entry.Underline == chroma.Yes {
				writeParam("4")
			}
			seq.WriteString("m")
		}

		// Emit fg and bg as separate sequences to avoid combined SGR params.
		prefix := ""
		if entry.Colour.IsSet() {
			prefix += ansiSeqForColor(lipgloss.Color(entry.Colour.String()), true)
		}
		if bgSeq != "" {
			prefix += bgSeq
		}
		if seq.Len() > 0 {
			prefix += seq.String()
		}
		if err := writeStyledTokenValue(w, prefix, token.Value); err != nil {
			return err
		}
	}
	return nil
}

// ansiSeqForColor converts a color.Color to an ANSI SGR sequence.
// If fg is true, generates a foreground sequence; otherwise background.
// Returns an empty string if c is nil.
func ansiSeqForColor(c color.Color, fg bool) string {
	if c == nil {
		return ""
	}
	r, g, b, _ := c.RGBA()
	// RGBA returns 16-bit values; shift to 8-bit.
	r8, g8, b8 := uint8(r>>8), uint8(g>>8), uint8(b>>8)
	if fg {
		return fmt.Sprintf("\x1b[38;2;%d;%d;%dm", r8, g8, b8)
	}
	return fmt.Sprintf("\x1b[48;2;%d;%d;%dm", r8, g8, b8)
}
