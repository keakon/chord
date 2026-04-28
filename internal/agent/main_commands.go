package agent

import (
	"strings"

	"github.com/keakon/chord/internal/command"
	"github.com/keakon/chord/internal/message"
)

func (a *MainAgent) expandSlashCommandForModel(content string, parts []message.ContentPart) (string, []message.ContentPart) {
	if len(parts) > 0 {
		for _, part := range parts {
			if part.Type == "image" {
				return content, parts
			}
		}
		if len(parts) == 1 && parts[0].Type == "text" {
			t := strings.TrimSpace(parts[0].Text)
			if strings.HasPrefix(t, "/") {
				if exp, ok := a.customSlashExpansion(t); ok {
					return exp, nil
				}
			}
		}
		return content, parts
	}
	t := strings.TrimSpace(content)
	if !strings.HasPrefix(t, "/") {
		return content, parts
	}
	if exp, ok := a.customSlashExpansion(t); ok {
		return exp, nil
	}
	return content, parts
}

func (a *MainAgent) SetCustomCommands(defs []*command.Definition) {
	a.customCommandsMu.Lock()
	a.customCommands = defs
	a.customCommandsMu.Unlock()
}

func (a *MainAgent) customSlashExpansion(trimmedUserLine string) (prompt string, ok bool) {
	if !strings.HasPrefix(trimmedUserLine, "/") {
		return "", false
	}
	rest := trimmedUserLine[1:]
	var name, args string
	if idx := strings.IndexAny(rest, " \t"); idx >= 0 {
		name = rest[:idx]
		args = strings.TrimSpace(rest[idx+1:])
	} else {
		name = rest
	}
	name = strings.ToLower(name)
	a.customCommandsMu.RLock()
	cmds := a.customCommands
	a.customCommandsMu.RUnlock()
	for _, def := range cmds {
		if strings.ToLower(def.Name) == name {
			return expandCommandTemplate(def.Template, args), true
		}
	}
	return "", false
}

func expandCommandTemplate(tmpl, args string) string {
	if strings.Contains(tmpl, "$ARGUMENTS") {
		return strings.ReplaceAll(tmpl, "$ARGUMENTS", args)
	}
	if args != "" {
		return tmpl + "\n\n" + args
	}
	return tmpl
}

func isLoopSlashCommand(content string) bool {
	c := strings.TrimSpace(content)
	switch {
	case c == "/loop":
		return true
	case c == "/loop on" || strings.HasPrefix(c, "/loop on "):
		return true
	case c == "/loop off":
		return true
	default:
		return false
	}
}

func (a *MainAgent) tryHandleLoopSlashCommand(content string, busy bool) bool {
	c := strings.TrimSpace(content)
	switch {
	case c == "/loop":
		a.emitToTUI(ToastEvent{Message: "Usage: /loop on [target] | /loop off", Level: "info"})
		if !busy {
			a.setIdleAndDrainPending()
		}
		return true
	case c == "/loop on" || strings.HasPrefix(c, "/loop on "):
		target := strings.TrimSpace(strings.TrimPrefix(c, "/loop on"))
		if target == "" {
			target = "Continue and finish all remaining tasks in the current session."
		}
		a.EnableLoopMode(target)
		// busy=true: turn is active, so this queues the loop anchor target for the
		// next round without treating the slash command as transcript content.
		a.sendLoopAnchorFromCommand(target)
		return true
	case c == "/loop off":
		a.DisableLoopMode()
		if !busy {
			a.setIdleAndDrainPending()
		}
		return true
	default:
		return false
	}
}

func (a *MainAgent) tryHandleBusySlashCommand(content string) bool {
	return a.tryHandleLoopSlashCommand(content, true)
}

func (a *MainAgent) tryHandleSlashCommand(content string) bool {
	if a.tryHandleLoopSlashCommand(content, false) {
		return true
	}
	c := strings.TrimSpace(content)
	switch {
	case c == "/resume":
		list, _ := a.ListSessionSummaries()
		a.emitToTUI(SessionSelectEvent{Sessions: list})
		a.setIdleAndDrainPending()
		return true
	case strings.HasPrefix(c, "/resume "):
		sessionID := strings.TrimSpace(strings.TrimPrefix(c, "/resume "))
		a.handleResumeCommand(sessionID)
		return true
	case c == "/new":
		a.handleNewSessionCommand()
		return true
	case c == "/compact":
		a.handleCompactCommand()
		return true
	default:
		return false
	}
}
