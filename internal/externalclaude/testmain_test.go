//go:build unix

package externalclaude

import (
	"os"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/keakon/chord/internal/externalsupervisor"
)

// escapedDescendantArg selects the helper below when this test binary is
// re-executed as a descendant of a fixture. The managed transport rejects
// environment overrides, so the helper gets its mode and paths as arguments.
const escapedDescendantArg = "escaped-descendant-test-helper"

// TestMain lets this test binary serve as the external lifecycle supervisor,
// and as the escaped descendant below.
//
// A supervised session re-executes the running executable, which under
// `go test` is this binary. Intercepting before the testing framework parses
// flags keeps the path identical to production: no test-only entrypoint, no
// override hook in the code under test.
func TestMain(m *testing.M) {
	if len(os.Args) > 2 && os.Args[1] == escapedDescendantArg {
		runEscapedDescendant(os.Args[2])
	}
	if len(os.Args) > 1 && os.Args[1] == externalsupervisor.CommandName {
		os.Exit(externalsupervisor.Run(os.Args[2:]))
	}
	os.Exit(m.Run())
}

// runEscapedDescendant models a process the managed CLI left behind: it leaves
// the managed session so a group kill cannot reach it, keeps the inherited
// stderr write end open, and dies once Chord stops reading.
func runEscapedDescendant(readyPath string) {
	if _, err := unix.Setsid(); err != nil {
		os.Exit(1)
	}
	_ = os.WriteFile(readyPath, []byte("ready"), 0o644)
	for {
		if _, err := os.Stderr.Write([]byte("escaped descendant alive\n")); err != nil {
			os.Exit(0)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
