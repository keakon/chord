package logtest

import (
	"io"

	"github.com/keakon/golog"
	"github.com/keakon/golog/log"
)

type nopWriteCloser struct {
	io.Writer
}

func (w nopWriteCloser) Close() error { return nil }

func NewLogger(out io.Writer, level golog.Level) *golog.Logger {
	if out == nil {
		out = io.Discard
	}
	logger := golog.NewLogger(level)
	handler := golog.NewHandler(level, golog.DefaultFormatter)
	if wc, ok := out.(io.WriteCloser); ok {
		handler.AddWriter(wc)
	} else {
		handler.AddWriter(nopWriteCloser{Writer: out})
	}
	logger.AddHandler(handler)
	return logger
}

func WithDefault(logger *golog.Logger, fn func()) {
	log.SetDefaultLogger(logger)
	defer log.SetDefaultLogger(NewLogger(nil, golog.InfoLevel))
	fn()
}
