package agent

import (
	"fmt"
	"strings"

	"github.com/keakon/chord/internal/permission"
)

// YOLO keeps the active role's capability set exactly as configured and only
// removes the confirmation and call-level friction inside that granted scope.
// The model-visible surface — system prompt, tool declarations, tool
// visibility, delegate and skill catalogs — never changes with the toggle
// (surface consumers read the configured ruleset via effectiveRuleset), so a
// toggle costs no request-prefix rebuild and the prompt describes the same
// capabilities in both states.
//
// Execution decisions go through executionRuleset. The permission engine
// resolves the concrete tool, including glob rules and edit/patch inheritance,
// before relaxing a scoped deny. Only a tool-wide allow/ask grants the scope
// needed to lift that limit; a default deny or a narrow grant does not.
// Catalog membership and control-tool disables stay binding. Asks become
// allows, while the independent loop completion confirmation stays unchanged.
//
// The rewrite preserves every rule's tool and argument pattern, so the
// engine's last-match-wins selection, the edit/patch counterpart fallback,
// path classification, and the shell compound-command review all see the same
// rule shapes as with YOLO off. A narrow shell allow rewritten from an ask or
// a lifted scoped deny still declines to authorize a compound command the
// analyzer cannot split into subcommands: the engine's conservative review
// wins there, matching the unattended-execution contract for shell.
//
// SubAgents never see this rewrite. They evaluate the configured ruleset and
// inherit only the ask→allow downgrade while the parent's YOLO mode is on
// (sub_tool_exec_pipeline.go), so a delegation contract survives the toggle.

func (a *MainAgent) YoloEnabled() bool {
	return a != nil && a.yoloEnabled.Load()
}

// executionRuleset returns the ruleset that decides main-agent tool execution:
// the configured ruleset, with the YOLO transformation applied while the mode
// is on. Every main-agent permission evaluation — the execution gate, the
// speculative-reuse prefilter, the streaming policy, the WebFetch redirect
// re-check, internal read gates, and the composite dependency checks inside
// the evaluator — must go through this one entry so a main call and its
// dependency checks always decide with the same semantics. Surface consumers
// use effectiveRuleset instead.
func (a *MainAgent) executionRuleset() permission.Ruleset {
	ruleset := a.effectiveRuleset()
	if a.yoloEnabled.Load() {
		return ruleset.RelaxCalls()
	}
	return ruleset
}

func (a *MainAgent) SetInitialYoloMode(enabled bool) {
	if a == nil {
		return
	}
	a.yoloEnabled.Store(enabled)
}

func (a *MainAgent) setYoloMode(enabled bool) {
	if a == nil || a.yoloEnabled.Load() == enabled {
		return
	}
	a.yoloEnabled.Store(enabled)
	a.NotifyEnvStatusUpdated()
	a.emitToTUI(YoloModeChangedEvent{Enabled: enabled})
	state := "off"
	if enabled {
		state = "on"
	}
	a.emitToTUI(ToastEvent{Message: fmt.Sprintf("YOLO mode %s", state), Level: "info"})
}

func (a *MainAgent) handleYoloCommand(command string, _ bool) {
	fields := strings.Fields(command)
	if len(fields) == 1 {
		a.setYoloMode(!a.YoloEnabled())
		return
	}
	if len(fields) != 2 {
		a.emitToTUI(ToastEvent{Message: "Usage: /yolo on|off", Level: "warn"})
		return
	}
	switch strings.ToLower(fields[1]) {
	case "on", "true", "1":
		a.setYoloMode(true)
	case "off", "false", "0":
		a.setYoloMode(false)
	default:
		a.emitToTUI(ToastEvent{Message: "Usage: /yolo on|off", Level: "warn"})
	}
}
