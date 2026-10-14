package main

import (
	"fmt"
	"os"
	"testing"

	"github.com/keakon/golog"

	"github.com/keakon/chord/internal/logtest"
)

func TestMain(m *testing.M) {
	os.Exit(runTestsWithTemporaryRoot(m))
}

// Keep fallback spill files and subprocess scratch inside this test process's
// own directory. Never sweep the shared system temporary directory.
func runTestsWithTemporaryRoot(m *testing.M) (code int) {
	root, err := os.MkdirTemp("", "ch-cmd-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "create test temporary root:", err)
		return 1
	}
	defer func() {
		if err := os.RemoveAll(root); err != nil {
			fmt.Fprintln(os.Stderr, "remove test temporary root:", err)
			code = 1
		}
	}()
	for _, key := range []string{"TMPDIR", "TMP", "TEMP"} {
		if err := os.Setenv(key, root); err != nil {
			fmt.Fprintln(os.Stderr, "set test temporary root:", err)
			return 1
		}
	}
	setDefaultLogger(logtest.NewLogger(nil, golog.InfoLevel))
	code = m.Run()
	setDefaultLogger(logtest.NewLogger(nil, golog.InfoLevel))
	return code
}
