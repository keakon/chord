package tools

import (
	"path/filepath"
	"strings"
)

const (
	sessionToolOutputsDirName = "tool-outputs"
	sessionSpawnLogsDirName   = "spawn-logs"
)

func sessionManagedDir(sessionDir, name string) string {
	sessionDir = strings.TrimSpace(sessionDir)
	if sessionDir == "" || name == "" {
		return ""
	}
	return filepath.Join(sessionDir, name)
}

func sessionToolOutputsDir(sessionDir string) string {
	return sessionManagedDir(sessionDir, sessionToolOutputsDirName)
}

func sessionSpawnLogsDir(sessionDir string) string {
	return sessionManagedDir(sessionDir, sessionSpawnLogsDirName)
}
