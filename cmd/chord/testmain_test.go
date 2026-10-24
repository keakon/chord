package main

import (
	"fmt"
	"os"
	"testing"

	"github.com/keakon/golog"

	"github.com/keakon/chord/internal/externalsupervisor"
	"github.com/keakon/chord/internal/logtest"
)

func TestMain(m *testing.M) {
	// Host-level external-agent tests launch a real supervised worker. The
	// supervisor re-executes the running executable, which under `go test` is
	// this binary, so it must serve the hidden supervisor command before the
	// testing framework sees the argument. Same interception as the agent and
	// externalclaude test mains.
	if len(os.Args) > 1 && os.Args[1] == externalsupervisor.CommandName {
		os.Exit(externalsupervisor.Run(os.Args[2:]))
	}
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
