//go:build unix

package filedelivery

import (
	"os"

	"golang.org/x/sys/unix"
)

func openRegularCandidate(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|unix.O_NONBLOCK, 0)
}
func openRootCandidate(root *os.Root, path string) (*os.File, error) {
	return root.OpenFile(path, os.O_RDONLY|unix.O_NONBLOCK, 0)
}
