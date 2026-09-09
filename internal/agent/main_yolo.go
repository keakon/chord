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
// evaluated under YOLO mode — YOLO bypasses ordinary tools outright, it never
// bypasses these. YOLO exists to remove the high-frequency confirmation
// friction of ordinary work (file edits, shell commands); it is not a
// redefinition of the role's boundary. These tools change the agent topology
// or the session lifecycle rather than the risk of one operation.
var yoloProtectedTools = []string{tools.NameHandoff, tools.NameDelegate, tools.NameCancel, tools.NameDone, tools.NameCompactContext}

// yoloCapabilityControlTools are the protected tools that hand the agent a
// capability it did not otherwise have — handing the session to another role,
// spawning delegated work, cancelling work it does not own. They are the three
// control tools whose decision can still depend on the ruleset's default, so
// yoloRuleset mirrors the user's wildcard default (or the rule engine's
// no-match default) onto them; see yoloRuleset.
//
// done and compact_context are deliberately absent from that mirroring: they
// only end or shrink the current unit of work and are authorized by the
// runtime modes that mount them (`/loop`, model_driven compaction), so they
// resolve through donePermissionAction and compactContextPermissionAction and
// stay usable regardless of a wildcard default.
var yoloCapabilityControlTools = []string{tools.NameDelegate, tools.NameHandoff, tools.NameCancel}

// yoloProtectedPermissionTool reports whether the tool's permission rules are
// still evaluated under YOLO mode rather than bypassed outright. The name is
// normalized first so an alias spelling of a protected tool cannot slip
// through the bypass.
func yoloProtectedPermissionTool(toolName string) bool {
	return slices.Contains(yoloProtectedTools, toolname.Normalize(toolName))
}

// yoloAskDowngradeTool reports whether an ask decision for toolName relaxes to
// an implicit allow while YOLO is on. All ordinary tools relax (on the main
// agent they are bypassed outright; on a SubAgent inheriting the mode their
// asks stop prompting), and so do delegate, handoff and cancel — the mechanism
// tools keep their user rules, but their ask no longer raises the shared
// confirmation dialog. done never reaches the ask branch (the pipeline lets it
// through before confirming), and compact_context keeps its dedicated action
// semantics, so an explicit ask rule on it still confirms under YOLO exactly
// as without it.
func yoloAskDowngradeTool(toolName string) bool {
	switch tools.NormalizeName(toolName) {
	case tools.NameDone, tools.NameCompactContext:
		return false
	default:
		return true
	}
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

// yoloRuleset returns the ruleset that drives tool-permission decisions while
// YOLO mode is on. It is consumed by callers that present the active ruleset
// to the LLM or UI (system prompt, tool visibility, etc.) and by the execution
// gate, so the visible permission surface matches what bypassPermission
// actually skips at execution time. Unprotected tools never evaluate against
// it (the gate bypasses them), so their rules — including any wildcard default
// — are dropped here; a rule naming a protected tool survives verbatim.
// SubAgent rulesets intentionally do not pass through this filter: their
// inheritance of the main agent's YOLO mode only relaxes ask decisions at
// execution time, so their visible rules stay the user's full set.
//
// YOLO must never turn a previously usable tool into a denied one, which is
// what the old unconditional deny seeds for delegate/handoff/cancel did: a
// role whose default was allow (an explicit `"*": allow`, or no rules at all)
// could delegate with YOLO off and was rejected the moment YOLO switched on.
// To keep the three capability-granting control tools deciding exactly as
// their user ruleset decides them, every wildcard rule is mirrored onto them
// in place (`"*": allow` becomes delegate/handoff/cancel allow; an
// allowlist's `"*": deny` keeps them denied), so a call without a specific
// rule resolves to the same default as with YOLO off. A non-empty ruleset that
// contains no wildcard rule still denies an unmentioned tool through the rule
// engine's no-match default, so those tools get an equivalent deny mirror up
// front, where a rule the user wrote for them can still override it. Only an
// empty ruleset — no permission configuration at all — stays nil, which the
// execution gate treats as unrestricted, exactly as it does without YOLO.
func yoloRuleset(ruleset permission.Ruleset) permission.Ruleset {
	if len(ruleset) == 0 {
		return nil
	}
	filtered := make(permission.Ruleset, 0, len(ruleset)+len(yoloCapabilityControlTools))
	hasWildcard := false
	for _, rule := range ruleset {
		if toolname.Normalize(rule.Permission) == "*" {
			hasWildcard = true
			for _, name := range yoloCapabilityControlTools {
				filtered = append(filtered, permission.Rule{Permission: name, Pattern: rule.Pattern, Action: rule.Action})
			}
			continue
		}
		if yoloProtectedPermissionRule(rule) {
			filtered = append(filtered, rule)
		}
	}
	if !hasWildcard {
		// No wildcard rule: with YOLO off an unmentioned control tool
		// evaluates to the rule engine's no-match default (deny). Mirror that
		// default up front so the YOLO decision stays identical; a rule the
		// user wrote for the tool comes later and still wins.
		mirrored := make(permission.Ruleset, 0, len(yoloCapabilityControlTools)+len(filtered))
		for _, name := range yoloCapabilityControlTools {
			mirrored = append(mirrored, permission.Rule{Permission: name, Pattern: "*", Action: permission.ActionDeny})
		}
		filtered = append(mirrored, filtered...)
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
