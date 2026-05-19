package agent

import (
	"fmt"
	"strconv"
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

func (a *MainAgent) canUseLoopMode() bool {
	return a.doneToolAvailable()
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

func parseLoopOnCommand(content string) (target string, maxIterations int, maxSet bool, err error) {
	rest := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(content), "/loop on"))
	if rest == "" {
		return "Continue and finish all remaining tasks in the current session.", 0, false, nil
	}
	const flag = "--max-iterations"
	if idx := strings.Index(rest, flag); idx >= 0 {
		before := strings.TrimSpace(rest[:idx])
		after := strings.TrimSpace(rest[idx+len(flag):])
		if after == "" {
			return "", 0, false, fmt.Errorf("missing value for %s", flag)
		}
		fields := strings.Fields(after)
		if len(fields) == 0 {
			return "", 0, false, fmt.Errorf("missing value for %s", flag)
		}
		n, convErr := strconv.Atoi(fields[0])
		if convErr != nil || n < 0 {
			return "", 0, false, fmt.Errorf("invalid %s value %q", flag, fields[0])
		}
		trailing := strings.TrimSpace(strings.TrimPrefix(after, fields[0]))
		if trailing != "" {
			if before != "" {
				before += " "
			}
			before += trailing
		}
		if strings.TrimSpace(before) == "" {
			before = "Continue and finish all remaining tasks in the current session."
		}
		return strings.TrimSpace(before), n, true, nil
	}
	return rest, 0, false, nil
}

func (a *MainAgent) tryHandleLoopSlashCommand(content string, busy bool) bool {
	c := strings.TrimSpace(content)
	switch {
	case c == "/loop off":
		a.DisableLoopMode()
		if !busy {
			a.setIdleAndDrainPending()
		}
		return true
	case c == "/loop":
		if !a.canUseLoopMode() {
			a.emitToTUI(ToastEvent{Message: "Loop mode requires the Done tool to be available for this role.", Level: "error"})
			if !busy {
				a.setIdleAndDrainPending()
			}
			return true
		}
		a.emitToTUI(ToastEvent{Message: "Usage: /loop on [target] [--max-iterations N] | /loop off", Level: "info"})
		if !busy {
			a.setIdleAndDrainPending()
		}
		return true
	case c == "/loop on" || strings.HasPrefix(c, "/loop on "):
		if !a.canUseLoopMode() {
			a.emitToTUI(ToastEvent{Message: "Loop mode requires the Done tool to be available for this role.", Level: "error"})
			if !busy {
				a.setIdleAndDrainPending()
			}
			return true
		}
		target, maxIterations, maxSet, err := parseLoopOnCommand(c)
		if err != nil {
			a.emitToTUI(ToastEvent{Message: "Usage: /loop on [target] [--max-iterations N] | /loop off", Level: "error"})
			if !busy {
				a.setIdleAndDrainPending()
			}
			return true
		}
		a.EnableLoopMode(target)
		if busy {
			a.freezeLoopReductionPrefixForCurrentTurn()
		}
		if maxSet || busy {
			a.loopReductionMu.Lock()
			if maxSet {
				a.loopState.MaxIterations = maxIterations
				a.loopState.MaxIterationsSet = true
			}
			if busy {
				// Busy /loop on should not emit LOOP card or inject continuation
				// prompt immediately; only enforce required tool calls for the ongoing
				// turn, and defer loop continuation prompt until terminal stop_reason=done
				// or a rejected Done exit attempt.
				a.loopState.DeferContinuationPromptUntilDone = true
			}
			a.loopReductionMu.Unlock()
		}
		if busy {
			return true
		}
		// Idle /loop on keeps current behavior: emit LOOP card and inject target.
		a.sendLoopAnchorFromCommand(target)
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
	case c == "/mcp" || strings.HasPrefix(c, "/mcp "):
		a.handleMCPCommand(c)
		return true
	default:
		return false
	}
}
