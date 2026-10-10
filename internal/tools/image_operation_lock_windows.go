package tools

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"

	"github.com/keakon/chord/internal/privatefs"
)

func lockImageOperation(dir, path string) (*os.File, error) {
	f, err := privatefs.OpenFile(dir, path+".lock", os.O_CREATE|os.O_RDWR)
	if err != nil {
		return nil, fmt.Errorf("open image operation lock: %w", err)
	}
	if err := windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &windows.Overlapped{}); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("image operation is active: %w", err)
	}
	return f, nil
}
