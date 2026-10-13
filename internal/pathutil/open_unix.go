//go:build unix

package pathutil

import (
	"os"

	"golang.org/x/sys/unix"
)

// A replacement FIFO must not block before its identity can be checked.
const canonicalReadFlags = os.O_RDONLY | unix.O_NONBLOCK
