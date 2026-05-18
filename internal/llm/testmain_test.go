package llm

import (
	"os"
	"testing"

	"github.com/keakon/golog"
	"github.com/keakon/golog/log"

	"github.com/keakon/chord/internal/logtest"
)

func TestMain(m *testing.M) {
	log.SetDefaultLogger(logtest.NewLogger(nil, golog.InfoLevel))
	code := m.Run()
	log.SetDefaultLogger(nil)
	os.Exit(code)
}
