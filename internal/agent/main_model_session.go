package agent

import (
	"path/filepath"
	"strings"

	"github.com/keakon/chord/internal/identity"
	"github.com/keakon/chord/internal/llm"
)

// mainSessionIdentity uses the committed checkpoint in ctxmgr; exported drafts
// and best-effort archive status files cannot advance the wire window.
func (a *MainAgent) mainSessionIdentity(sessionDir string) llm.SessionIdentity {
	base := strings.TrimSpace(filepath.Base(sessionDir))
	if base == "" || base == "." || base == string(filepath.Separator) {
		return llm.SessionIdentity{}
	}
	var window uint64
	if a.ctxMgr != nil {
		window = a.ctxMgr.CompactionWindowNumber()
	}
	return llm.SessionIdentity{Key: base, WindowNumber: window}
}

func (a *MainAgent) ensureLLMSessionIdentity(client *llm.Client) {
	a.bindLLMClientSession(client, identity.MainAgentID)
}

// Read ctxmgr without stateMu: compaction persistence takes those locks in the
// opposite order. Recheck the directory under stateMu and hold it through the
// client update so a session switch cannot be followed by a stale binding.
func (a *MainAgent) bindLLMClientSession(client *llm.Client, owner string) {
	if client == nil {
		return
	}
	for {
		sessionDir := a.SessionDir()
		id := a.mainSessionIdentity(sessionDir)
		a.stateMu.RLock()
		if a.sessionDir != sessionDir {
			a.stateMu.RUnlock()
			continue
		}
		client.SetExecutionGate(a.executionGate, owner)
		if id.Key != "" {
			client.SetSessionIdentity(id)
		}
		a.stateMu.RUnlock()
		return
	}
}
