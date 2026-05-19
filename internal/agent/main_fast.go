package agent

import (
	"fmt"
	"strings"

	"github.com/keakon/chord/internal/llm"
)

func (a *MainAgent) FastModeEnabled() bool {
	client, _, _, _ := a.llmSnapshot()
	return client != nil && client.FastMode()
}

func (a *MainAgent) applyFastModeToClient(client *llm.Client) {
	if a == nil || client == nil {
		return
	}
	client.SetFastMode(a.FastModeEnabled())
}

func (a *MainAgent) syncSubAgentFastMode(enabled bool) {
	if a == nil {
		return
	}
	a.mu.RLock()
	targets := make([]*SubAgent, 0, len(a.subAgents))
	for _, sub := range a.subAgents {
		if sub != nil {
			targets = append(targets, sub)
		}
	}
	a.mu.RUnlock()
	for _, sub := range targets {
		sub.setFastMode(enabled)
	}
}

func (a *MainAgent) handleFastCommand(content string, busy bool) {
	arg := strings.TrimSpace(strings.TrimPrefix(content, "/fast"))
	client, _, _, _ := a.llmSnapshot()
	if client == nil {
		a.emitToTUI(ToastEvent{Message: "Fast mode unavailable: no LLM client", Level: "error"})
		if !busy {
			a.setIdleAndDrainPending()
		}
		return
	}

	var enabled bool
	switch strings.ToLower(arg) {
	case "", "on", "enable", "enabled", "true":
		client.SetFastMode(true)
		enabled = true
	case "off", "disable", "disabled", "false":
		client.SetFastMode(false)
		enabled = false
	default:
		a.emitToTUI(ToastEvent{Message: "Usage: /fast on | /fast off", Level: "info"})
		if !busy {
			a.setIdleAndDrainPending()
		}
		return
	}
	a.syncSubAgentFastMode(enabled)

	state := "off"
	if enabled {
		state = "on"
	}
	a.emitToTUI(ToastEvent{Message: fmt.Sprintf("Fast mode %s", state), Level: "info"})
	if !busy {
		a.setIdleAndDrainPending()
	}
}
