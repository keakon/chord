package agent

import (
	"fmt"
	"strings"

	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/tools"
)

// yoloProtectedPermissionTool reports whether the tool's permission rules must
// still be enforced under YOLO mode. Tool names come from the registry without
// surrounding whitespace, so no trimming is needed here.
func yoloProtectedPermissionTool(toolName string) bool {
	switch toolName {
	case tools.NameHandoff, tools.NameDelegate, tools.NameCancel, tools.NameDone:
		return true
	default:
		return false
	}
}

// yoloRuleset returns a ruleset containing only the protected-tool rules.
// It is consumed by callers that present the active ruleset to the LLM or UI
// (system prompt, tool visibility, etc.) so the visible permission surface
// matches what bypassPermission actually skips at execution time. SubAgent
// inheritance intentionally does NOT pass through this filter; YOLO only
// relaxes the main agent's own permission checks.
func yoloRuleset(ruleset permission.Ruleset) permission.Ruleset {
	if len(ruleset) == 0 {
		return nil
	}
	filtered := make(permission.Ruleset, 0, len(ruleset))
	for _, rule := range ruleset {
		if yoloProtectedPermissionTool(rule.Permission) {
			filtered = append(filtered, rule)
		}
	}
	return filtered
}

func (a *MainAgent) YoloEnabled() bool {
	return a != nil && a.yoloEnabled.Load()
}

func (a *MainAgent) SetInitialYoloMode(enabled bool) {
	if a == nil {
		return
	}
	a.yoloEnabled.Store(enabled)
	if enabled {
		a.markRuntimeSurfaceDirty()
	}
}

func (a *MainAgent) setYoloMode(enabled bool) {
	if a == nil || a.yoloEnabled.Load() == enabled {
		return
	}
	a.yoloEnabled.Store(enabled)
	a.markRuntimeSurfaceDirty()
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
