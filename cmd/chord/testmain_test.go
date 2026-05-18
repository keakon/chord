package main

import (
	"os"
	"testing"

	"github.com/keakon/golog"

	"github.com/keakon/chord/internal/logtest"
)

func TestMain(m *testing.M) {
	setDefaultLogger(logtest.NewLogger(nil, golog.InfoLevel))
	code := m.Run()
	setDefaultLogger(logtest.NewLogger(nil, golog.InfoLevel))
	os.Exit(code)
}
