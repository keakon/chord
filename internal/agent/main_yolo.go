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
// enforced under YOLO mode. YOLO exists to remove the high-frequency
// confirmation friction of ordinary work (file edits, shell commands); it is
// not a redefinition of the role's boundary. These tools change the agent
// topology or the session lifecycle rather than the risk of one operation, and
// they are called rarely enough that confirming them is not the friction YOLO
// is meant to remove.
var yoloProtectedTools = []string{tools.NameHandoff, tools.NameDelegate, tools.NameCancel, tools.NameDone, tools.NameCompactContext}

// yoloDeniedByDefaultTools are the protected control tools YOLO must never
// authorize implicitly: each grants the role a capability it did not have
// (handing the session to another role, spawning delegated work, cancelling
// work it does not own), so a wildcard rule is not enough and the role has to
// name the tool directly.
//
// done and compact_context are deliberately absent. They only end or shrink
// the current unit of work, carry no external side effect, and are required by
// the very runtime modes that make them reachable (`/loop`, model_driven
// compaction), so they resolve through donePermissionAction and
// compactContextPermissionAction and default to allow. Seeding a deny for them
// here would make YOLO + `/loop` unable to finish and would silently disable
// model-driven compaction.
var yoloDeniedByDefaultTools = []string{tools.NameHandoff, tools.NameDelegate, tools.NameCancel}

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
// (system prompt, tool visibility, etc.) and by the execution gate, so the
// visible permission surface matches what bypassPermission actually skips at
// execution time. SubAgent rulesets intentionally do not pass through this
// filter: their inheritance of the main agent's YOLO mode only downgrades ask
// decisions to allow at execution time, so their visible rules stay the
// user's full set.
//
// The capability-granting control tools are seeded with an explicit deny ahead
// of the user's own rules. Two things depend on that seed. First, wildcard
// rules are dropped below, so last-match-wins leaves the seeded deny standing
// unless the user named the tool directly — which is what makes "a broad
// `*: allow` does not grant these tools by itself" actually true. Second, the
// filtered result must never be empty while the user does have rules: an empty
// ruleset means "no permission configuration at all" to the execution gate,
// which returns early and allows everything. Without the seed, an allowlist
// role (`"*": deny` plus a few tools) named no protected tool, filtered down
// to nothing, and was handed every protected control tool the moment YOLO was
// switched on — the exact opposite of the denial it had configured.
func yoloRuleset(ruleset permission.Ruleset) permission.Ruleset {
	if len(ruleset) == 0 {
		// No rules at all means the user configured no permissions; YOLO must
		// not invent restrictions that do not exist without it.
		return nil
	}
	filtered := make(permission.Ruleset, 0, len(yoloDeniedByDefaultTools)+len(ruleset))
	for _, name := range yoloDeniedByDefaultTools {
		filtered = append(filtered, permission.Rule{Permission: name, Pattern: "*", Action: permission.ActionDeny})
	}
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
