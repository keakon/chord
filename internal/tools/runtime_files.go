package tools

import (
	"path/filepath"
	"strings"
)

const sessionJobLogsDirName = "job-logs"

func sessionManagedDir(sessionDir, name string) string {
	sessionDir = strings.TrimSpace(sessionDir)
	if sessionDir == "" || name == "" {
		return ""
	}
	return filepath.Join(sessionDir, name)
}

func sessionJobLogsDir(sessionDir string) string {
	return sessionManagedDir(sessionDir, sessionJobLogsDirName)
}
