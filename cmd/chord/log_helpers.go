package main

import (
	"io"
	"os"

	"github.com/keakon/golog"
	glog "github.com/keakon/golog/log"
)

type nopWriteCloser struct {
	io.Writer
}

func (w nopWriteCloser) Close() error { return nil }

var defaultLogger = newGologLogger(os.Stderr, golog.InfoLevel)

func init() {
	glog.SetDefaultLogger(defaultLogger)
}

func newGologLogger(out io.Writer, level golog.Level) *golog.Logger {
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

func newStderrGologLogger(level golog.Level) *golog.Logger {
	logger := golog.NewLogger(level)
	handler := golog.NewHandler(level, golog.DefaultFormatter)
	handler.AddWriter(golog.NewStderrWriter())
	logger.AddHandler(handler)
	return logger
}

func setDefaultLogger(logger *golog.Logger) {
	if logger == nil {
		return
	}
	defaultLogger = logger
	glog.SetDefaultLogger(logger)
}

func getDefaultLogger() *golog.Logger {
	return defaultLogger
}
