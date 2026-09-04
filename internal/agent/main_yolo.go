package agent

import (
	"fmt"
	"slices"
	"strings"

	"github.com/keakon/chord/internal/permission"
	"github.com/keakon/chord/internal/toolname"
	"github.com/keakon/chord/internal/tools"
)

// yoloProtectedTools are the control tools whose permission rules stay
// enforced under YOLO mode.
var yoloProtectedTools = []string{tools.NameHandoff, tools.NameDelegate, tools.NameCancel, tools.NameDone, tools.NameCompactContext}

// yoloProtectedPermissionTool reports whether the tool's permission rules must
// still be enforced under YOLO mode. The name is normalized first so an alias
// spelling of a protected tool cannot slip through the bypass.
func yoloProtectedPermissionTool(toolName string) bool {
	return slices.Contains(yoloProtectedTools, toolname.Normalize(toolName))
}

// yoloProtectedPermissionRule reports whether a rule must survive the YOLO
// filter: its tool pattern names one of the protected tools. Matching goes
// through the shared normalization + glob helper, so a narrow glob such as
// `compact_*` or `handoff*` keeps its rule instead of being dropped by an
// exact-string comparison — the documented contract is that any non-wildcard
// rule matching a protected tool still applies under YOLO.
func yoloProtectedPermissionRule(rule permission.Rule) bool {
	for _, name := range yoloProtectedTools {
		if permissionRuleTargetsTool(rule, name) {
			return true
		}
	}
	return false
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
		if yoloProtectedPermissionRule(rule) {
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
