package tui

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
)

type fileViewerRequest struct {
	checkoutGeneration uint64
	links              []localFileLink
	cache              *fileViewerCache
	ctx                context.Context
	generation         uint64
	owner              imageViewerOwner
	file               message.DeliveredFile
	session            string
	width              int
	markdown           bool
	style              glamansi.StyleConfig
	highlighter        *codeHighlighter
}
type fileViewerLoadedMsg struct {
	links        *fileLinksLoadedMsg
	image        *BlockImagePart
	request      fileViewerRequest
	raw          []byte
	lines, plain []string
	note         string
	err          error
}

func safeFileText(s string) string {
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
func loadFileViewer(req fileViewerRequest) fileViewerLoadedMsg {
	result := fileViewerLoadedMsg{request: req}
	if len(req.links) > 0 {
		links := loadLocalFileLinks(req)
		result.links = &links
		return result
	}
	limit := int64(fileViewerReadLimit)
	isImage := strings.HasPrefix(req.file.MIME, "image/")
	if isImage {
		limit = imageutil.MaxImageSourceBytes
	}
	data, current, truncated, err := filedelivery.Load(req.ctx, req.file, req.session, limit)
	if err != nil {
		result.err = err
		return result
	}
	result.note = fileVersionNote(req.file, current)
	if isImage {
		if truncated {
			result.err = errors.New("image exceeds preview byte budget")
			return result
		}
		result.image = &BlockImagePart{FileName: req.file.DisplayName, MimeType: current.MIME, Data: data}
		return result
	}
	if truncated {
		result.note += " · truncated to 1 MiB"
		for trim := 0; trim < utf8.UTFMax && trim <= len(data); trim++ {
			if utf8.Valid(data[:len(data)-trim]) {
				data = data[:len(data)-trim]
				break
			}
		}
	}
	if bytes.IndexByte(data, 0) >= 0 || !utf8.Valid(data) {
		result.err = errors.New("binary or non-UTF-8 file; use Open or Open with")
		return result
	}
	key := filePreviewCacheKey(req, data)
	if cached, ok := req.cache.get(key); ok {
		cached.request = req
		cached.note = strings.TrimSpace(result.note + " " + cached.note)
		return cached
	}
	versionNote := result.note
	result.note = ""
	result.raw = bytes.Clone(data)
	safe := safeFileText(string(data))
	lines := strings.Split(safe, "\n")
	if len(lines) > fileViewerSourceLines {
		lines = lines[:fileViewerSourceLines]
		result.note += " · source lines truncated"
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
	if req.markdown {
		if len(safe) > fileViewerMarkdownLimit {
			result.err = errors.New("preview Markdown exceeds 128 KiB; press m for source")
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
			result.err = err
			return result
		}
		md.SetRenderer(renderer.NewRenderer(renderer.WithNodeRenderers(util.Prioritized(glamansi.NewRenderer(glamansi.Options{WordWrap: min(req.width, fileViewerMaxWidth), Styles: req.style}), 1000))))
		err = md.Renderer().Render(&output, []byte(safe), root)
	} else {
		lexer := req.highlighter.getLexer(safe)
		if lexer == nil {
			_, err = output.Write([]byte(safe))
		} else {
			it, e := lexer.Tokenise(nil, safe)
			if e != nil {
				_, err = output.Write([]byte(safe))
			} else {
				err = (bgFormatter{}).Format(&output, req.highlighter.chromaStyle, it)
			}
		}
	}
	if err != nil && !output.truncated {
		result.err = fmt.Errorf("preview: %w", err)
		return result
	}
	if output.truncated {
		result.note += " · rendering truncated"
	}
	result.lines = strings.Split(stripFileHyperlinks(output.buffer.String()), "\n")
	result.plain = make([]string, len(result.lines))
	for i, line := range result.lines {
		result.plain[i] = ansi.Strip(line)
	}
	req.cache.put(key, result)
	result.note = strings.TrimSpace(versionNote + " " + result.note)
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
