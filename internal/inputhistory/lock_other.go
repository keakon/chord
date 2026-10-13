//go:build !unix && !windows

package inputhistory

import (
	"context"
	"errors"
	"os"
)

func lockFile(context.Context, *os.File) error {
	return errors.New("input history file locking is unsupported on this platform")
}
func unlockFile(*os.File) error { return nil }
