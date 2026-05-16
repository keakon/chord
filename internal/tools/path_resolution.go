package tools

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

var errHomeDirUnavailable = errors.New("home directory is unavailable")

var blockedDevicePaths = map[string]struct{}{
	"/dev/console": {},
	"/dev/fd/0":    {},
	"/dev/fd/1":    {},
	"/dev/fd/2":    {},
	"/dev/full":    {},
	"/dev/random":  {},
	"/dev/stderr":  {},
	"/dev/stdin":   {},
	"/dev/stdout":  {},
	"/dev/tty":     {},
	"/dev/urandom": {},
	"/dev/zero":    {},
}

func expandTildePath(path string) (string, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return trimmed, nil
	}
	if trimmed == "~" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", errHomeDirUnavailable
		}
		return home, nil
	}
	if runtime.GOOS == "windows" {
		if strings.HasPrefix(trimmed, `~/`) || strings.HasPrefix(trimmed, `~\`) {
			home, err := os.UserHomeDir()
			if err != nil {
				return "", errHomeDirUnavailable
			}
			rel := trimmed[2:]
			return filepath.Join(home, rel), nil
		}
		return trimmed, nil
	}
	if strings.HasPrefix(trimmed, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", errHomeDirUnavailable
		}
		return filepath.Join(home, trimmed[2:]), nil
	}
	return trimmed, nil
}

func resolveToolPath(path string) (string, error) {
	expanded, err := expandTildePath(path)
	if err != nil {
		return "", err
	}
	return filepath.Clean(expanded), nil
}

func resolveToolPathAbs(path string) (string, error) {
	resolved, err := resolveToolPath(path)
	if err != nil {
		return "", err
	}
	return filepath.Abs(resolved)
}

func isBlockedDevicePath(path string) bool {
	if path == "" || runtime.GOOS == "windows" {
		return false
	}
	cleaned := filepath.Clean(path)
	if !filepath.IsAbs(cleaned) {
		return false
	}
	if _, ok := blockedDevicePaths[cleaned]; ok {
		return true
	}
	if strings.HasPrefix(cleaned, "/dev/fd/") {
		fd, err := strconv.Atoi(strings.TrimPrefix(cleaned, "/dev/fd/"))
		return err == nil && fd >= 0 && fd <= 2
	}
	if strings.HasPrefix(cleaned, "/proc/") {
		rel := strings.TrimPrefix(cleaned, "/proc/")
		parts := strings.Split(rel, "/")
		if len(parts) == 3 && parts[1] == "fd" {
			fd, fdErr := strconv.Atoi(parts[2])
			if fdErr != nil || fd < 0 || fd > 2 {
				return false
			}
			if parts[0] == "self" || parts[0] == "thread-self" {
				return true
			}
			_, pidErr := strconv.Atoi(parts[0])
			return pidErr == nil
		}
	}
	return false
}

func ensureRegularFilePath(path string, info os.FileInfo) error {
	if info == nil {
		return fmt.Errorf("path is not a regular file: %s", path)
	}
	if info.IsDir() {
		return fmt.Errorf("path is a directory, not a regular file: %s", path)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("path is not a regular file: %s", path)
	}
	return nil
}
