//go:build unix

package externalclaude

import (
	"os"
	"testing"

	"github.com/keakon/chord/internal/externalsupervisor"
)

// TestMain lets this test binary serve as the external lifecycle supervisor.
//
// A supervised session re-executes the running executable, which under
// `go test` is this binary. Intercepting before the testing framework parses
// flags keeps the path identical to production: no test-only entrypoint, no
// override hook in the code under test.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == externalsupervisor.CommandName {
		os.Exit(externalsupervisor.Run(os.Args[2:]))
	}
	os.Exit(m.Run())
}
