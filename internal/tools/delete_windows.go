//go:build windows

package tools

import "os"

func removeFileOrSymlink(path string) error {
	return os.Remove(path)
}
