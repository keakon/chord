package agent

import (
	"fmt"
	"strings"
)

func (a *MainAgent) FastModeEnabled() bool {
	client, _, _, _ := a.llmSnapshot()
	return client != nil && client.FastMode()
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

	state := "off"
	if enabled {
		state = "on"
	}
	a.emitToTUI(ToastEvent{Message: fmt.Sprintf("Fast mode %s", state), Level: "info"})
	if !busy {
		a.setIdleAndDrainPending()
	}
}
