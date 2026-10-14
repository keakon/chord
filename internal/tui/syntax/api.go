package syntax

import "io"

// ReadyForPath reports whether a previous render resolved the lexer for this
// path. Callers can skip rebuilding a streaming sample while this remains true.
func (h *Highlighter) ReadyForPath(path string) bool {
	return h != nil && h.lexerResolved && h.filePath == path
}

// ContextKey identifies the path component of an external rendered-prefix cache.
// It does not include language or sample. Compare it together with the
// Highlighter pointer; changing paths can replace
// the lexer in place. Background, width and content remain caller-owned keys.
func (h *Highlighter) ContextKey() string {
	if h == nil {
		return ""
	}
	return h.filePath
}

// Write renders directly to w without retaining the result. It preserves writer
// errors so a bounded preview writer can stop formatting before a full output
// allocation. Missing lexers and tokenization failures write the source verbatim.
// Like other Highlighter operations, Write requires exclusive instance ownership.
func (h *Highlighter) Write(w io.Writer, source string) error {
	lexer := h.getLexer(source)
	if lexer == nil {
		_, err := w.Write([]byte(source))
		return err
	}
	tokens, err := lexer.Tokenise(nil, source)
	if err != nil {
		_, err = w.Write([]byte(source))
		return err
	}
	return (bgFormatter{}).Format(w, h.chromaStyle, tokens)
}
