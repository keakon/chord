package tools

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

var errHomeDirUnavailable = errors.New("home directory is unavailable")

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
