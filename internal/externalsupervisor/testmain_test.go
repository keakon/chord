//go:build unix

package externalsupervisor

import (
	"os"
	"testing"
)

// TestMain lets this test binary serve as a supervisor. Chord re-executes the
// running executable to start one, which under `go test` is this binary, so
// the tests exercise the real entrypoint rather than a stand-in.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == CommandName {
		os.Exit(Run(os.Args[2:]))
	}
	os.Exit(m.Run())
}
