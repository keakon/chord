package main

import (
	"io"
	"os"
	"strconv"
	"strings"

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

type logContext struct {
	PWD     string
	PID     int
	SID     string
	AgentID string
}

func (c logContext) fields() string {
	var b strings.Builder
	if c.PWD != "" {
		appendLogField(&b, "pwd", c.PWD)
	}
	if c.PID != 0 {
		appendLogField(&b, "pid", strconv.Itoa(c.PID))
	}
	if c.SID != "" {
		appendLogField(&b, "sid", c.SID)
	}
	if c.AgentID != "" && c.AgentID != "main" {
		appendLogField(&b, "agent", c.AgentID)
	}
	return b.String()
}

func appendLogField(b *strings.Builder, key, value string) {
	if b.Len() > 0 {
		b.WriteByte(' ')
	}
	b.WriteString(key)
	b.WriteByte('=')
	b.WriteString(escapeLogFormatLiteral(formatLogFieldValue(value)))
}

func formatLogFieldValue(value string) string {
	if value == "" || strings.ContainsAny(value, " \t\r\n=\"") {
		return strconv.Quote(value)
	}
	return value
}

func escapeLogFormatLiteral(s string) string {
	return strings.ReplaceAll(s, "%", "%%")
}

func formatterWithContext(base *golog.Formatter, ctx logContext) *golog.Formatter {
	fields := ctx.fields()
	if fields == "" {
		return base
	}
	return golog.ParseFormat("[%l %D %T %s " + fields + "] %m")
}

func newGologLogger(out io.Writer, level golog.Level) *golog.Logger {
	return newGologLoggerWithContext(out, level, logContext{})
}

func newGologLoggerWithContext(out io.Writer, level golog.Level, ctx logContext) *golog.Logger {
	if out == nil {
		out = io.Discard
	}
	logger := golog.NewLogger(level)
	handler := golog.NewHandler(level, formatterWithContext(golog.DefaultFormatter, ctx))
	if wc, ok := out.(io.WriteCloser); ok {
		handler.AddWriter(wc)
	} else {
		handler.AddWriter(nopWriteCloser{Writer: out})
	}
	logger.AddHandler(handler)
	return logger
}

func newStderrGologLoggerWithContext(level golog.Level, ctx logContext) *golog.Logger {
	logger := golog.NewLogger(level)
	handler := golog.NewHandler(level, formatterWithContext(golog.DefaultFormatter, ctx))
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
