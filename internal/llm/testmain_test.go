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
	code := runTests(m)
	log.SetDefaultLogger(nil)
	os.Exit(code)
}

// runTests pins the config home and HOME to a temporary directory for the
// whole package: a request resolves the machine-level installation id once per
// process and persists it under the config home when no file carries it, which
// must not read or write into the developer's own ~/.codex or config home.
// os.Exit skips deferred cleanup, so the directory is removed inside this
// function before TestMain exits.
func runTests(m *testing.M) int {
	if configHome, err := os.MkdirTemp("", "chord-llm-config-home"); err == nil {
		defer os.RemoveAll(configHome)
		for _, key := range []string{"CHORD_CONFIG_HOME", "HOME"} {
			if err := os.Setenv(key, configHome); err == nil {
				defer os.Unsetenv(key)
			}
		}
	}
	return m.Run()
}
