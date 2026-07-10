package agent

import (
	"fmt"
	"sort"
	"strings"
)

func (a *MainAgent) handleMCPCommand(content string, busy ...bool) {
	isBusy := len(busy) > 0 && busy[0]
	arg := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(content), "/mcp"))
	if arg == "" {
		if len(a.MCPServerList()) == 0 {
			a.emitToTUI(InfoEvent{Message: "No MCP servers available for the active role"})
			if !isBusy {
				a.setIdleAndDrainPending()
			}
			return
		}
		a.emitToTUI(MCPSelectEvent{})
		if !isBusy {
			a.setIdleAndDrainPending()
		}
		return
	}
	fields := strings.Fields(arg)
	if len(fields) == 0 {
		if len(a.MCPServerList()) == 0 {
			a.emitToTUI(InfoEvent{Message: "No MCP servers available for the active role"})
			if !isBusy {
				a.setIdleAndDrainPending()
			}
			return
		}
		a.emitToTUI(MCPSelectEvent{})
		if !isBusy {
			a.setIdleAndDrainPending()
		}
		return
	}

	switch fields[0] {
	case "status":
		a.emitToTUI(InfoEvent{Message: a.mcpStatusText()})
		if !isBusy {
			a.setIdleAndDrainPending()
		}
		return
	case string(MCPControlEnable), string(MCPControlDisable):
		if len(fields) < 2 {
			a.emitToTUI(ErrorEvent{Err: fmt.Errorf("/mcp %s: usage: /mcp %s <server|all|server...>", fields[0], fields[0])})
			if !isBusy {
				a.setIdleAndDrainPending()
			}
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
				if !isBusy {
					a.setIdleAndDrainPending()
				}
				return
			}
		}
		action := MCPControlAction(fields[0])
		a.sendEvent(Event{Type: EventMCPControl, Payload: MCPControlRequest{Action: action, Servers: servers}})
		return
	default:
		a.emitToTUI(ErrorEvent{Err: fmt.Errorf("/mcp: unknown subcommand %q (expected: status, enable, disable)", fields[0])})
		if !isBusy {
			a.setIdleAndDrainPending()
		}
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
