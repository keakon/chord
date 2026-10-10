//go:build unix

package tools

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"

	"github.com/keakon/chord/internal/privatefs"
)

// The kernel releases this lock on process exit; the separate permanent claim
// prevents generation replay after a crash while allowing receipt recovery.
func lockImageOperation(dir, path string) (*os.File, error) {
	f, err := privatefs.OpenFile(dir, path+".lock", os.O_CREATE|os.O_RDWR)
	if err != nil {
		return nil, fmt.Errorf("open image operation lock: %w", err)
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("image operation is active: %w", err)
	}
	return f, nil
}
