package filepreview

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	glamansi "github.com/keakon/glamour/v2/ansi"
	"github.com/keakon/x/ansi"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	east "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"

	"github.com/keakon/chord/internal/filedelivery"
	"github.com/keakon/chord/internal/imageutil"
	"github.com/keakon/chord/internal/message"
	"github.com/keakon/chord/internal/tui/syntax"
)

// Request captures the immutable inputs used to load and render a preview.
type Request struct {
	File        message.DeliveredFile
	Session     string
	Width       int
	Markdown    bool
	Style       glamansi.StyleConfig
	Highlighter *syntax.Highlighter
}

// Result owns no viewer or worker state. Its buffers and slices are shared with
// the cache and must be treated as read-only by callers.
type Result struct {
	Raw          []byte
	Lines, Plain []string
	Note         string
	Err          error
	Image        *Image
}

// Image is the source image returned for presentation by the viewer.
type Image struct {
	FileName, MimeType string
	Data               []byte
}

// SafeText strips terminal escapes and controls while retaining tabs and newlines.
func SafeText(s string) string {
	s = ansi.Strip(s)
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, s)
}

type fileRenderWriter struct {
	buffer    bytes.Buffer
	lines     int
	truncated bool
}

func (w *fileRenderWriter) Write(p []byte) (int, error) {
	n := len(p)
	remaining := fileViewerRenderBytes - w.buffer.Len()
	if len(p) > remaining {
		p = p[:max(0, remaining)]
		w.truncated = true
	}
	for i, c := range p {
		if c == '\n' {
			w.lines++
			if w.lines >= fileViewerRenderLines {
				p = p[:i]
				w.truncated = true
				break
			}
		}
	}
	w.buffer.Write(p)
	if w.truncated {
		return len(p), errors.New("preview render budget reached")
	}
	return n, nil
}

// Load validates the current file and renders a bounded preview. A nil cache
// disables caching without changing loading or rendering behavior.
func (c *Cache) Load(ctx context.Context, req Request) Result {
	result := Result{}
	limit := int64(fileViewerReadLimit)
	isImage := strings.HasPrefix(req.File.MIME, "image/")
	if isImage {
		limit = imageutil.MaxImageSourceBytes
	}
	data, current, truncated, err := filedelivery.Load(ctx, req.File, req.Session, limit)
	if err != nil {
		result.Err = err
		return result
	}
	result.Note = VersionNote(req.File, current)
	if isImage {
		if truncated {
			result.Err = errors.New("image exceeds preview byte budget")
			return result
		}
		result.Image = &Image{FileName: req.File.DisplayName, MimeType: current.MIME, Data: data}
		return result
	}
	if truncated {
		result.Note += " · truncated to 1 MiB"
		for trim := 0; trim < utf8.UTFMax && trim <= len(data); trim++ {
			if utf8.Valid(data[:len(data)-trim]) {
				data = data[:len(data)-trim]
				break
			}
		}
	}
	if bytes.IndexByte(data, 0) >= 0 || !utf8.Valid(data) {
		result.Err = errors.New("binary or non-UTF-8 file; use Open or Open with")
		return result
	}
	key := filePreviewCacheKey(req, data)
	if cached, ok := c.get(key); ok {
		cached.Note = strings.TrimSpace(result.Note + " " + cached.Note)
		return cached
	}
	versionNote := result.Note
	result.Note = ""
	result.Raw = bytes.Clone(data)
	safe := SafeText(string(data))
	lines := strings.Split(safe, "\n")
	if len(lines) > fileViewerSourceLines {
		lines = lines[:fileViewerSourceLines]
		result.Note += " · source lines truncated"
	}
	for i, line := range lines {
		if len(line) > fileViewerLineLimit {
			cut := fileViewerLineLimit
			for !utf8.RuneStart(line[cut]) {
				cut--
			}
			lines[i] = line[:cut] + " …"
		}
	}
	safe = strings.Join(lines, "\n")
	var output fileRenderWriter
	if req.Markdown {
		if len(safe) > MarkdownLimit {
			result.Err = errors.New("preview Markdown exceeds 128 KiB; press m for source")
			return result
		}
		md := goldmark.New(goldmark.WithExtensions(extension.GFM, extension.DefinitionList))
		root := md.Parser().Parse(text.NewReader([]byte(safe)))
		nodes, depth, cells := 0, 0, 0
		err = ast.Walk(root, func(n ast.Node, enter bool) (ast.WalkStatus, error) {
			if !enter {
				depth--
				return ast.WalkContinue, nil
			}
			nodes++
			depth++
			if _, ok := n.(*east.TableCell); ok {
				cells++
			}
			if table, ok := n.(*east.Table); ok && len(table.Alignments) > 32 {
				return ast.WalkStop, errors.New("table exceeds 32 columns; press m for source")
			}
			if nodes > 4096 || depth > 32 || cells > 4096 {
				return ast.WalkStop, errors.New("preview Markdown structure exceeds budget; press m for source")
			}
			return ast.WalkContinue, nil
		})
		if err != nil {
			result.Err = err
			return result
		}
		md.SetRenderer(renderer.NewRenderer(renderer.WithNodeRenderers(util.Prioritized(glamansi.NewRenderer(glamansi.Options{WordWrap: min(req.Width, fileViewerMaxWidth), Styles: req.Style}), 1000))))
		err = md.Renderer().Render(&output, []byte(safe), root)
	} else {
		err = req.Highlighter.Write(&output, safe)
	}
	if err != nil && !output.truncated {
		result.Err = fmt.Errorf("preview: %w", err)
		return result
	}
	if output.truncated {
		result.Note += " · rendering truncated"
	}
	result.Lines = strings.Split(stripFileHyperlinks(output.buffer.String()), "\n")
	result.Plain = make([]string, len(result.Lines))
	for i, line := range result.Lines {
		result.Plain[i] = ansi.Strip(line)
	}
	c.put(key, result)
	result.Note = strings.TrimSpace(versionNote + " " + result.Note)
	return result
}

// Keep only renderer-generated SGR styling. Incomplete sequences at the output
// budget boundary, hyperlinks, and cursor/clipboard controls are discarded.
func stripFileHyperlinks(s string) string {
	var out strings.Builder
	for i := 0; i < len(s); {
		if s[i] != 27 {
			out.WriteByte(s[i])
			i++
			continue
		}
		start := i
		i++
		if i >= len(s) {
			break
		}
		switch s[i] {
		case ']':
			i++
			for i < len(s) {
				if s[i] == 7 {
					i++
					break
				}
				if i+1 < len(s) && s[i] == 27 && s[i+1] == '\\' {
					i += 2
					break
				}
				i++
			}
		case '[':
			i++
			valid := true
			for i < len(s) && !(s[i] >= 0x40 && s[i] <= 0x7e) {
				if !(s[i] >= '0' && s[i] <= '9') && s[i] != ';' && s[i] != ':' {
					valid = false
				}
				i++
			}
			if i < len(s) {
				if s[i] == 'm' && valid {
					out.WriteString(s[start : i+1])
				}
				i++
			}
		default:
			i++
		}
	}
	return out.String()
}
