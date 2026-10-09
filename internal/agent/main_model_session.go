package agent

import (
	"path/filepath"
	"strings"

	"github.com/keakon/chord/internal/llm"
)

// ensureLLMSessionID binds client to the current session before installation or
// request preparation. Hold the directory read lock through the client update:
// a session switch must not be followed by a stale snapshot restoring its key.
func (a *MainAgent) ensureLLMSessionID(client *llm.Client) {
	if client == nil {
		return
	}
	a.stateMu.RLock()
	defer a.stateMu.RUnlock()
	sid := strings.TrimSpace(filepath.Base(a.sessionDir))
	if sid != "" && sid != "." {
		client.SetSessionID(sid)
	}
}
