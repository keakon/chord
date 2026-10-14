package tooloutput

import (
	"path/filepath"
	"strings"
)

const sessionToolOutputsDirName = "tool-outputs"

func sessionToolOutputsDir(sessionDir string) string {
	sessionDir = strings.TrimSpace(sessionDir)
	if sessionDir == "" {
		return ""
	}
	return filepath.Join(sessionDir, sessionToolOutputsDirName)
}
