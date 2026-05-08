package agent

import (
	"fmt"
	"sort"
	"strings"
)

func (a *MainAgent) handleMCPCommand(content string) {
	arg := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(content), "/mcp"))
	if arg == "" {
		a.emitToTUI(MCPSelectEvent{})
		return
	}
	fields := strings.Fields(arg)
	if len(fields) == 0 {
		a.emitToTUI(MCPSelectEvent{})
		return
	}

	switch fields[0] {
	case "status":
		a.emitToTUI(InfoEvent{Message: a.mcpStatusText()})
		a.setIdleAndDrainPending()
		return
	case "enable", "disable", "toggle":
		if len(fields) < 2 {
			a.emitToTUI(ErrorEvent{Err: fmt.Errorf("/mcp %s: usage: /mcp %s <server|all|server...>", fields[0], fields[0])})
			a.setIdleAndDrainPending()
			return
		}
		var servers []string
		if strings.EqualFold(strings.TrimSpace(fields[1]), "all") {
			servers = nil
		} else {
			for _, s := range fields[1:] {
				s = strings.TrimSpace(s)
				if s != "" {
					servers = append(servers, s)
				}
			}
			if len(servers) == 0 {
				a.emitToTUI(ErrorEvent{Err: fmt.Errorf("/mcp %s: usage: /mcp %s <server|all|server...>", fields[0], fields[0])})
				a.setIdleAndDrainPending()
				return
			}
		}
		action := MCPControlAction(fields[0])
		a.sendEvent(Event{Type: EventMCPControl, Payload: MCPControlRequest{Action: action, Servers: servers}})
		return
	default:
		a.emitToTUI(ErrorEvent{Err: fmt.Errorf("/mcp: unknown subcommand %q (expected: status, enable, disable, toggle)", fields[0])})
		a.setIdleAndDrainPending()
		return
	}
}

func (a *MainAgent) mcpStatusText() string {
	rows := a.MCPServerList()
	if len(rows) == 0 {
		return "No MCP servers configured"
	}
	rowsCopy := append([]MCPServerDisplay(nil), rows...)
	sort.Slice(rowsCopy, func(i, j int) bool { return rowsCopy[i].Name < rowsCopy[j].Name })
	var b strings.Builder
	b.WriteString("MCP servers:\n")
	for _, r := range rowsCopy {
		state := "error"
		switch {
		case r.OK:
			state = "enabled"
		case r.Disabled:
			state = "disabled"
		case r.Pending:
			state = "pending"
		}
		if r.Err != "" && state == "error" {
			b.WriteString(fmt.Sprintf("- %s: %s (%s)\n", r.Name, state, r.Err))
			continue
		}
		b.WriteString(fmt.Sprintf("- %s: %s\n", r.Name, state))
	}
	return strings.TrimRight(b.String(), "\n")
}
