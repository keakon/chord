//go:build windows

package tools

import "testing"

func makeNamedPipeForTest(path string) error {
	panic("makeNamedPipeForTest should not be called on windows tests")
}

func TestNamedPipeHelperWindowsStub(_ *testing.T) {}
